package supportinfo

import (
	"strings"
	"time"
)

/*
A capture pulled off a live phone system, rather than uploaded as a file.

3CX has no API that generates a support bundle — that is a button in its own
console, and what comes out lands on the machine as a zip somebody then has to
send. What it does have is the event log over the API, and that is the richest
thing in the bundle anyway: the same rows, the same event ids, the same
per-call quality reports buried in the same column.

So a pulled capture is deliberately the same shape as an uploaded one, read by
the same catalogue and turned into the same findings. What it does not have is
everything the API cannot serve — the metrics, the packet capture, the audit
log, the phone inventory — and rather than leaving those sections mysteriously
empty it says so, in Missing, in the same place a bundle would say a file was
absent.

That honesty is the point. Somebody looking at a pulled capture should be able
to tell at a glance that the reason there is no chart is that nobody uploaded a
bundle, not that the phone system is fine.
*/

// LiveEvent is one row of a phone system's event log, as a plugin returned it.
type LiveEvent struct {
	At       string `json:"at"`
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// FromEvents builds a report out of a live capture.
func FromEvents(host string, events []LiveEvent) Snapshot {
	snap := Snapshot{
		Health:   []Check{},
		Findings: []Finding{},
		Read:     []string{"the phone system's event log, over its API"},
		System:   System{FQDN: strings.ToLower(strings.TrimSpace(host))},
		Missing: []string{
			"machine and network metrics",
			"packet capture",
			"configuration history",
			"phone inventory",
		},
	}

	totals := newEventTotals()
	totals.source = "the phone system's event log"
	quality := newQualityTotals()

	for _, e := range events {
		at, _ := parseLiveTime(e.At)
		id := strings.TrimSpace(e.ID)
		totals.readEvent(strings.TrimSpace(e.Source), id, strings.TrimSpace(e.Severity), e.Message, at)
		if id == "10034" {
			quality.readQuality(e.Message)
		}
	}

	snap.Events = totals.result()
	snap.Quality = quality.result()
	snap.System.CapturedAt = time.Now().UTC()

	snap.Findings = append(snap.Findings, totals.findings()...)
	snap.Findings = append(snap.Findings, quality.findings(totals.source)...)
	sortFindings(snap.Findings)
	return snap
}

// parseLiveTime reads the timestamps an API returns, which are ISO rather than
// the Postgres dump format the bundle's CSVs use.
func parseLiveTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return parseTime(s)
}
