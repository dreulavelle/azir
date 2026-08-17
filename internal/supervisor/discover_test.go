package supervisor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dreulavelle/azir/internal/supervisor"
)

// drop writes an executable with the given name into dir.
func drop(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The whole contract: an executable called plugin-<name> is a plugin, and
// nothing has to be registered anywhere for that to be true.
func TestAnExecutableNamedPluginIsFound(t *testing.T) {
	dir := t.TempDir()
	drop(t, dir, "plugin-syncro")
	drop(t, dir, "plugin-3cx")

	// Things that are not plugins, and must not be launched as one.
	drop(t, dir, "helper")  // wrong prefix
	drop(t, dir, "plugin-") // no name
	if err := os.WriteFile(filepath.Join(dir, "plugin-notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err) // not executable
	}
	if err := os.Mkdir(filepath.Join(dir, "plugin-folder"), 0o755); err != nil {
		t.Fatal(err) // a directory
	}

	found, err := supervisor.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range found {
		names[c.Name] = true
	}
	if len(found) != 2 || !names["syncro"] || !names["3cx"] {
		t.Errorf("found %d plugins %v, want exactly syncro and 3cx", len(found), names)
	}
}

// A site drops a binary into a mounted directory; the bundled ones keep working.
func TestPluginsAreFoundAcrossDirectories(t *testing.T) {
	bundled, local := t.TempDir(), t.TempDir()
	drop(t, bundled, "plugin-syncro")
	drop(t, local, "plugin-ringlogix")

	found, err := supervisor.Discover(bundled, local)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d, want both the bundled and the dropped-in one: %+v", len(found), found)
	}
}

// The mounted directory is usually absent, and that is not a failure.
func TestAMissingDirectoryIsFine(t *testing.T) {
	bundled := t.TempDir()
	drop(t, bundled, "plugin-syncro")

	found, err := supervisor.Discover(bundled, filepath.Join(t.TempDir(), "nothing-here"))
	if err != nil {
		t.Fatalf("a missing plugin directory failed the whole startup: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("found %d, want the one that does exist", len(found))
	}
}

// Two binaries claiming one name is a packaging mistake, and the quiet version
// of it is somebody editing a plugin that is not the one running.
func TestDuplicateNamesAreRefused(t *testing.T) {
	bundled, local := t.TempDir(), t.TempDir()
	drop(t, bundled, "plugin-syncro")
	drop(t, local, "plugin-syncro")

	_, err := supervisor.Discover(bundled, local)
	if err == nil {
		t.Fatal("two plugins called syncro were accepted; one silently shadows the other")
	}
	if !strings.Contains(err.Error(), "syncro") {
		t.Errorf("the error does not name the clash: %v", err)
	}
}

func TestSplitDirsReadsAPathList(t *testing.T) {
	got := supervisor.SplitDirs(strings.Join([]string{"/one", "", "/two"}, string(os.PathListSeparator)))
	if len(got) != 2 || got[0] != "/one" || got[1] != "/two" {
		t.Errorf("SplitDirs = %v, want the two non-empty entries", got)
	}
}
