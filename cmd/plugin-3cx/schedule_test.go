package main

import (
	"strings"
	"testing"
)

func on(name, starts, ends string, repeating bool) closure {
	y, m, d, err := dateParts(starts)
	if err != nil {
		panic(err)
	}
	ey, em, ed := y, m, d
	if ends != "" {
		if ey, em, ed, err = dateParts(ends); err != nil {
			panic(err)
		}
	}
	return closure{Name: name, Day: d, Month: m, Year: y,
		DayEnd: ed, MonthEnd: em, YearEnd: ey, IsRecurrent: &repeating}
}

/*
Two rules the phone system does not usefully enforce.

A repeated name is refused with HTTP 500 and an empty body, which reads as the
phone system having fallen over. Overlapping dates are not refused at all: 3CX
accepted Christmas Day and a Boxing week spanning it without complaint, leaving
two closures over one day and no saying which one's hours apply.
*/
func TestANameIsUsedOnce(t *testing.T) {
	have := []closure{on("Christmas Day", "--12-25", "", true)}

	why := conflicts(on("christmas day", "--06-01", "", true), have)
	if !strings.Contains(why, "already a closure called") {
		t.Errorf("a repeated name was allowed through: %q", why)
	}
	if why := conflicts(on("Summer shutdown", "--06-01", "", true), have); why != "" {
		t.Errorf("a new name was refused: %q", why)
	}
}

func TestOverlappingClosuresAreRefused(t *testing.T) {
	// The pair the phone system actually accepted.
	have := []closure{on("Christmas Day", "--12-25", "", true)}

	why := conflicts(on("Boxing week", "--12-24", "--12-27", true), have)
	if !strings.Contains(why, "overlaps") {
		t.Fatalf("a closure spanning Christmas Day was allowed: %q", why)
	}
	if !strings.Contains(why, "Christmas Day") {
		t.Errorf("%q does not say which one it clashes with", why)
	}
}

// Touching is not overlapping. A closure ending on the 24th and one starting on
// the 25th are two ordinary consecutive days.
func TestAdjacentClosuresAreFine(t *testing.T) {
	have := []closure{on("Christmas Eve", "--12-23", "--12-24", true)}
	if why := conflicts(on("Christmas Day", "--12-25", "", true), have); why != "" {
		t.Errorf("consecutive closures were refused: %q", why)
	}
}

/*
A span that crosses new year is two intervals, not one.

Compared as a single pair of numbers, 12-28 to 01-02 reads as running backwards
and would overlap nothing at all — including the new year's day sitting inside
it.
*/
func TestAClosureAcrossNewYearStillOverlaps(t *testing.T) {
	have := []closure{on("Shutdown", "--12-28", "--01-02", true)}

	for _, inside := range []string{"--12-31", "--01-01"} {
		if why := conflicts(on("Something", inside, "", true), have); why == "" {
			t.Errorf("%s sits inside the shutdown and was allowed", inside)
		}
	}
	if why := conflicts(on("Something", "--06-01", "", true), have); why != "" {
		t.Errorf("a June closure clashed with a new year shutdown: %q", why)
	}
}

/*
A closure that repeats happens in every year, including the one a dated closure
is in.

Comparing only the dates would let "Christmas Day, every year" and "Christmas
Day 2026, closing early" both exist, which is the case somebody would most
easily create by accident.
*/
func TestARepeatingClosureClashesWithADatedOne(t *testing.T) {
	have := []closure{on("Christmas Day", "--12-25", "", true)}
	if why := conflicts(on("Christmas 2026", "2026-12-25", "", false), have); why == "" {
		t.Error("a dated closure was allowed on a day that is closed every year")
	}
}

// Two dated closures in different years share a date and not a day.
func TestDatedClosuresInDifferentYearsDoNotClash(t *testing.T) {
	have := []closure{on("Christmas 2026", "2026-12-25", "", false)}
	if why := conflicts(on("Christmas 2027", "2027-12-25", "", false), have); why != "" {
		t.Errorf("closures a year apart were treated as overlapping: %q", why)
	}
}
