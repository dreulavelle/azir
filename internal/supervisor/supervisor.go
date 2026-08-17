// Package supervisor runs bundled plugins as child processes.
//
// Why a Go supervisor rather than s6 or supervisord: a child's stdout is piped
// through the same redacting slog handler core uses, so a plugin that logs
// carelessly still cannot put a credential on the container's stdout. An
// external init system would write those bytes straight out, silently undoing
// the guarantee the rest of the system is built around. It also needs no root,
// no second init, and it shuts down with the same context everything else uses.
//
// Plugins remain separate processes, so crash isolation is unchanged. Plugins
// that are not bundled still run as their own containers against the same NATS.
package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Child describes one supervised process.
type Child struct {
	Name string
	Path string
	Args []string
	Env  []string
}

// Supervisor restarts children until the context is cancelled.
type Supervisor struct {
	log      *slog.Logger
	children []Child

	// backoff bounds. A plugin that crashes instantly must not become a hot
	// loop that fills the disk with logs.
	minBackoff time.Duration
	maxBackoff time.Duration
}

// New builds a Supervisor.
func New(log *slog.Logger, children ...Child) *Supervisor {
	return &Supervisor{
		log:        log,
		children:   children,
		minBackoff: 500 * time.Millisecond,
		maxBackoff: 30 * time.Second,
	}
}

/*
Discover finds plugin binaries across one or more directories.

An executable named "plugin-<name>" is a plugin. That is the whole contract:
nothing has to be registered, imported or listed, so adding one to the image is
a matter of dropping a file in — and so is adding one to a running deployment,
which is why this takes a list. The bundled directory inside the image comes
first, and a site can mount its own alongside it without rebuilding anything.

Directories are searched in order and the first definition of a name wins, so a
locally dropped plugin cannot quietly displace a bundled one of the same name.
A directory that does not exist is not an error: the mounted one is usually
absent, and a deployment with no local plugins is the normal case.

Anything found here still has to introduce itself over NATS and still has to be
approved by an administrator before a single tool it offers can be used. Being
in the folder buys a process, not trust.
*/
func Discover(dirs ...string) ([]Child, error) {
	var out []Child
	seen := map[string]string{}

	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}

		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || len(name) < 8 || !strings.HasPrefix(name, "plugin-") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Mode()&0o111 == 0 {
				continue
			}
			short := name[7:]
			if first, taken := seen[short]; taken {
				// Named rather than silently skipped: two binaries claiming one
				// name is a packaging mistake, and the quiet version of it is
				// somebody editing a plugin that is not the one running.
				return nil, fmt.Errorf(
					"supervisor: two plugins are called %q (%s and %s); rename one",
					short, first, filepath.Join(dir, name))
			}
			seen[short] = filepath.Join(dir, name)
			out = append(out, Child{Name: short, Path: filepath.Join(dir, name)})
		}
	}
	return out, nil
}

// SplitDirs reads a search path the way PATH is read, so one environment
// variable can name several directories.
func SplitDirs(value string) []string {
	var out []string
	for _, dir := range filepath.SplitList(value) {
		if dir = strings.TrimSpace(dir); dir != "" {
			out = append(out, dir)
		}
	}
	return out
}

// Run supervises every child until ctx is cancelled, then waits for them to
// exit. It returns once all children have stopped.
func (s *Supervisor) Run(ctx context.Context) {
	if len(s.children) == 0 {
		s.log.Info("no bundled plugins to supervise")
		<-ctx.Done()
		return
	}

	var wg sync.WaitGroup
	for _, c := range s.children {
		wg.Add(1)
		go func(c Child) {
			defer wg.Done()
			s.supervise(ctx, c)
		}(c)
	}
	wg.Wait()
}

func (s *Supervisor) supervise(ctx context.Context, c Child) {
	log := s.log.With("plugin", c.Name)
	backoff := s.minBackoff

	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx, c, log)

		if ctx.Err() != nil {
			log.Info("plugin stopped for shutdown")
			return
		}

		// A process that stayed up is not in a crash loop; reset the delay so
		// an unrelated later failure is not punished by earlier history.
		if time.Since(started) > 30*time.Second {
			backoff = s.minBackoff
		}

		if err != nil {
			log.Error("plugin exited", "error", err, "restart_in", backoff)
		} else {
			log.Warn("plugin exited cleanly, restarting", "restart_in", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, s.maxBackoff)
	}
}

func (s *Supervisor) runOnce(ctx context.Context, c Child, log *slog.Logger) error {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Env = append(os.Environ(), c.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	log.Info("plugin started", "pid", cmd.Process.Pid)

	// Both streams go through the redacting logger. This is the whole reason
	// this package exists rather than an off-the-shelf init system.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pipe(stdout, log, slog.LevelInfo) }()
	go func() { defer wg.Done(); pipe(stderr, log, slog.LevelWarn) }()

	// Terminate on shutdown, escalating only if the child ignores SIGTERM.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				log.Warn("plugin ignored shutdown signal, killing")
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()

	wg.Wait()
	err = cmd.Wait()
	close(done)
	return err
}

// pipe forwards a child's output line by line through the parent's redacting
// logger. A plugin emitting slog JSON is flattened into a real record rather
// than nested as a quoted string, so one container produces one readable log
// stream. Anything else is passed through as a message.
func pipe(r io.Reader, log *slog.Logger, level slog.Level) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if msg, attrs, ok := flatten(line); ok {
			log.Log(context.Background(), childLevel(attrs, level), msg, attrsToArgs(attrs)...)
			continue
		}
		log.Log(context.Background(), level, line)
	}
}

// flatten decodes a child's slog JSON line into its message and remaining
// fields. Returns ok=false for anything that is not such a line.
func flatten(line string) (msg string, attrs map[string]any, ok bool) {
	if len(line) == 0 || line[0] != '{' {
		return "", nil, false
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		return "", nil, false
	}
	m, ok := decoded["msg"].(string)
	if !ok {
		return "", nil, false
	}
	// The parent stamps time, level and plugin name; keeping the child's
	// copies would emit duplicate keys in the same record.
	delete(decoded, "msg")
	delete(decoded, "time")
	delete(decoded, "plugin")
	return m, decoded, true
}

func childLevel(attrs map[string]any, fallback slog.Level) slog.Level {
	raw, _ := attrs["level"].(string)
	delete(attrs, "level")
	switch strings.ToUpper(raw) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return fallback
	}
}

func attrsToArgs(attrs map[string]any) []any {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, k, attrs[k])
	}
	return args
}
