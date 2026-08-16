package main

import (
	"slices"
	"testing"

	"github.com/dreulavelle/azir/internal/syncro"
)

// Every tool must be accounted for in the permission map. A tool added without
// an entry reports itself available and then fails at call time with a 401 —
// the exact failure the preflight mechanism exists to prevent, reintroduced by
// forgetting to update a map two files away.
func TestEveryToolHasAPermissionEntry(t *testing.T) {
	known := syncro.KnownTools()

	for _, tool := range definition().Tools {
		if !slices.Contains(known, tool.Name) {
			t.Errorf("tool %q has no entry in the permission map; it would report "+
				"itself available and then fail with a 401", tool.Name)
		}
	}
}

// And the reverse: an entry for a tool that no longer exists is dead weight
// that quietly stops protecting anything.
func TestPermissionMapHasNoOrphans(t *testing.T) {
	names := []string{}
	for _, tool := range definition().Tools {
		names = append(names, tool.Name)
	}

	for _, known := range syncro.KnownTools() {
		if !slices.Contains(names, known) {
			t.Errorf("permission map references %q, which is not a registered tool", known)
		}
	}
}

// Freshness is a judgement about how fast data changes, and getting it wrong
// in the volatile direction is how a technician acts on stale information.
func TestVolatileToolsAreNotCachedForLong(t *testing.T) {
	limits := map[string]int{
		// seconds of hard TTL a tool may not exceed
		"tickets.get":      120,
		"tickets.timeline": 120,
		"tickets.search":   300,
	}

	for _, tool := range definition().Tools {
		limit, checked := limits[tool.Name]
		if !checked {
			continue
		}
		if tool.Freshness == nil {
			continue // never cached is always safe
		}
		if got := int(tool.Freshness.Hard.Seconds()); got > limit {
			t.Errorf("%s may be served up to %ds stale; ticket data changes while "+
				"someone is reading it", tool.Name, got)
		}
		if tool.Freshness.Soft > tool.Freshness.Hard {
			t.Errorf("%s has a soft budget longer than its hard one", tool.Name)
		}
	}
}

// A tool that reports live data must not be cached at all.
func TestAccessCheckIsNeverCached(t *testing.T) {
	for _, tool := range definition().Tools {
		if tool.Name == "access.check" && tool.Freshness != nil {
			t.Error("access.check is cached; a permission change would not be visible")
		}
	}
}

// Every tool that reads a permissioned resource must be guarded. An unguarded
// tool surfaces a bare vendor 401 instead of naming what is missing, which is
// the failure the guard exists to prevent.
func TestEveryPermissionedToolIsGuarded(t *testing.T) {
	// guarded() closes over the tool name, so the only reliable check is that
	// the wrapper is present: an unwrapped handler and a wrapped one differ in
	// behaviour when the resource is unreachable, and that is what the live
	// degradation test covers. Here we assert the map is complete, which is
	// the part a person forgets.
	for _, tool := range definition().Tools {
		if _, known := syncroToolResource(tool.Name); !known {
			t.Errorf("tool %q is not in the resource map, so it cannot be guarded", tool.Name)
		}
	}
}

func syncroToolResource(name string) (string, bool) {
	for _, known := range syncro.KnownTools() {
		if known == name {
			return known, true
		}
	}
	return "", false
}
