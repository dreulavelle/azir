package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
When a phone system is closed, and what it does then.

3CX calls all of it a holiday, which undersells it: the same record covers
Christmas Day, the afternoon of Christmas Eve, and the Tuesday somebody is
shutting at three for a funeral. A closure is a span of dates, optionally
narrowed to a span of hours within them, optionally repeating every year. While
it is on, calls go wherever the group's break route sends them.

That is why "schedule an office hours change" and "add a holiday" are one tool.
They are the same act on the same object, and a technician asked to do either
is looking for the same screen.

Every property name here was read from xapi/openapi.yaml rather than
remembered, and schema_test.go checks them.
*/

// closure is one holiday as the phone system keeps it. The names are 3CX's.
type closure struct {
	ID   int64  `json:"Id"`
	Name string `json:"Name"`
	// The span of days. Day and Month always mean something; Year is zero on
	// one that repeats every year.
	Day      int `json:"Day"`
	Month    int `json:"Month"`
	Year     int `json:"Year"`
	DayEnd   int `json:"DayEnd"`
	MonthEnd int `json:"MonthEnd"`
	YearEnd  int `json:"YearEnd"`
	// The hours within those days, as ISO 8601 durations from midnight —
	// "PT13H30M" is half past one. Empty covers the whole day.
	TimeOfStartDate string `json:"TimeOfStartDate"`
	TimeOfEndDate   string `json:"TimeOfEndDate"`
	IsRecurrent     *bool  `json:"IsRecurrent"`
	// Group is the department the closure applies to, empty for all of them.
	Group         string `json:"Group"`
	HolidayPrompt string `json:"HolidayPrompt"`
}

/*
listSchedule reads one department's schedule, and names the rest.

Per department, because that is how the phone system keeps it: a group carries
its own office hours, its own holidays and its own time zone, and "the company"
is simply the group everybody is in. Reading the system-wide /OfficeHours and
/Holidays gave one answer for an organisation that has as many as it has
departments — right for Cooli, which has one, and wrong for anybody with a
warehouse that shuts at four.

The departments come back with it so a screen can offer them without a second
request, and because the one being shown means nothing without the others
beside it.
*/
func listSchedule(ctx context.Context, req plugin.Request) (any, error) {
	var args struct {
		Department string `json:"department"`
	}
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	all, err := readGroups(ctx, conn)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, plugin.Errorf("404", "this phone system has no departments")
	}

	departments := make([]map[string]any, 0, len(all))
	for _, g := range all {
		departments = append(departments, map[string]any{"name": g.Name, "number": g.Number})
	}
	sort.SliceStable(departments, func(a, b int) bool {
		return asText(departments[a]["name"]) < asText(departments[b]["name"])
	})

	chosen, err := pickGroup(all, args.Department)
	if err != nil {
		return nil, err
	}

	detail, err := groupSchedule(ctx, conn, chosen.ID)
	if err != nil {
		return nil, err
	}
	detail["departments"] = departments
	detail["department"] = chosen.Name
	detail["department_number"] = chosen.Number
	return detail, nil
}

/*
pickGroup resolves which department was asked for.

By name or by number, because a screen holds the name and a closure carries the
number. Nothing named means the default group, which on 3CX is everybody and is
therefore what "the company" means.
*/
func pickGroup(all []group, asked string) (group, error) {
	asked = strings.TrimSpace(asked)
	for _, g := range all {
		if asked != "" && (g.Name == asked || g.Number == asked) {
			return g, nil
		}
	}
	if asked != "" {
		return group{}, plugin.Errorf("404", "this phone system has no department called %q", asked)
	}
	for _, g := range all {
		if g.Name == "DEFAULT" {
			return g, nil
		}
	}
	return all[0], nil
}

/*
groupSchedule reads one department's hours, closures and current state.

Expanded in one request rather than fetched in three. The holidays hang off the
group as a navigation property, which is the phone system saying plainly that
they belong to it.
*/
func groupSchedule(ctx context.Context, conn pbx, id int64) (map[string]any, error) {
	var g struct {
		Hours struct {
			Type           string `json:"Type"`
			IgnoreHolidays *bool  `json:"IgnoreHolidays"`
			Periods        []struct {
				DayOfWeek string `json:"DayOfWeek"`
				Start     string `json:"Start"`
				Stop      string `json:"Stop"`
			} `json:"Periods"`
		} `json:"Hours"`
		OfficeHolidays    []closure `json:"OfficeHolidays"`
		CurrentGroupHours string    `json:"CurrentGroupHours"`
		TimeZoneID        string    `json:"TimeZoneId"`
	}
	query := url.Values{"$expand": {"OfficeHolidays"}}
	if err := conn.get(ctx, fmt.Sprintf("Groups(%d)", id), query, &g); err != nil {
		return nil, err
	}

	closures := make([]map[string]any, 0, len(g.OfficeHolidays))
	for _, c := range g.OfficeHolidays {
		closures = append(closures, c.plain())
	}
	// By when they happen, so the list reads as a year rather than as whatever
	// order they were added in. A repeating closure sorts as "--12-25", which
	// puts it among the months where it belongs.
	sort.SliceStable(closures, func(a, b int) bool {
		return asText(closures[a]["starts"]) < asText(closures[b]["starts"])
	})

	open := make([]map[string]any, 0, len(g.Hours.Periods))
	for _, p := range g.Hours.Periods {
		open = append(open, map[string]any{
			"day": p.DayOfWeek,
			// The phone system keeps seconds; nobody opens at 09:00:30.
			"from": hhmm(p.Start),
			"to":   hhmm(p.Stop),
		})
	}

	return map[string]any{
		"closures": closures,
		"count":    len(closures),
		"office_hours": map[string]any{
			"kind":            g.Hours.Type,
			"days":            open,
			"ignore_closures": g.Hours.IgnoreHolidays != nil && *g.Hours.IgnoreHolidays,
		},
		// Whether somebody has forced this department open or closed right
		// now, which no amount of reading the schedule would tell you.
		"forced":    forcedAs(g.CurrentGroupHours),
		"time_zone": timeZoneName(ctx, conn, g.TimeZoneID),
	}, nil
}

/*
forcedAs reads the phone system's override mode as something worth showing.

"Default" is the schedule doing its job and is reported as nothing at all — an
indicator that is always lit says nothing. Anything else is somebody having
overridden the schedule by hand, which is the single most useful thing to know
when a customer says their phones are behaving oddly.
*/
func forcedAs(mode string) string {
	switch mode {
	case "", "Default":
		return ""
	case "ForceOpened":
		return "forced open"
	case "ForceClosed":
		return "forced closed"
	case "ForceBreak":
		return "forced onto break"
	case "ForceHoliday":
		return "forced onto holiday hours"
	case "ForceCustomOperator":
		return "forced to a custom operator"
	}
	return mode
}

/*
plain writes a closure the way Azir reads one.

Dates as text rather than as the phone system's six separate numbers, because
everything downstream — a list, a calendar, a confirmation — wants to compare
and sort them, and none of it should have to know that a repeating closure
carries a zero year.
*/
func (c closure) plain() map[string]any {
	return map[string]any{
		"id":         c.ID,
		"name":       c.Name,
		"starts":     dateText(c.Year, c.Month, c.Day),
		"ends":       dateText(c.YearEnd, c.MonthEnd, c.DayEnd),
		"from_time":  clock(c.TimeOfStartDate),
		"to_time":    clock(c.TimeOfEndDate),
		"repeats":    c.IsRecurrent != nil && *c.IsRecurrent,
		"department": c.Group,
		"prompt":     c.HolidayPrompt,
	}
}

/*
dateText writes a date the way a closure means it.

"2026-12-25" for one that happens once, "--12-25" for one that repeats every
year — the shape RFC 3339 already has for a recurring date, and one that sorts
within a year the way a person expects.
*/
func dateText(year, month, day int) string {
	if month == 0 || day == 0 {
		return ""
	}
	if year == 0 {
		return fmt.Sprintf("--%02d-%02d", month, day)
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

/*
clock reads the phone system's duration-from-midnight as a time of day.

Empty comes back empty rather than as "00:00": a closure covering whole days
has no time, and midnight is a real answer that would be indistinguishable
from the absence of one.
*/
func clock(duration string) string {
	rest, found := strings.CutPrefix(strings.TrimSpace(duration), "PT")
	if !found {
		return ""
	}
	var hours, minutes int
	if before, after, ok := strings.Cut(rest, "H"); ok {
		fmt.Sscanf(before, "%d", &hours)
		rest = after
	}
	if before, _, ok := strings.Cut(rest, "M"); ok {
		fmt.Sscanf(before, "%d", &minutes)
	}
	if hours == 0 && minutes == 0 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", hours, minutes)
}

// asDuration is clock backwards: "13:30" becomes "PT13H30M".
func asDuration(hhmm string) (string, error) {
	hhmm = strings.TrimSpace(hhmm)
	if hhmm == "" {
		return "", nil
	}
	var hours, minutes int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &hours, &minutes); err != nil {
		return "", plugin.Errorf("400", "%q is not a time of day, which reads as 13:30", hhmm)
	}
	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return "", plugin.Errorf("400", "%q is not a time of day", hhmm)
	}
	return fmt.Sprintf("PT%dH%dM", hours, minutes), nil
}

/*
timeZoneName turns the phone system's own id into somewhere a person has heard
of.

It keeps time zones by number — "14" — which is meaningless on a screen whose
whole subject is what time things happen. The name is looked up rather than
mapped from a table here, because the numbering is the phone system's and it is
under no obligation to keep it.

The id comes back unchanged when the lookup fails, which is worse than a name
and better than nothing.
*/
func timeZoneName(ctx context.Context, conn pbx, id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	var answer struct {
		Value []struct {
			ID       string `json:"Id"`
			Name     string `json:"Name"`
			IanaName string `json:"IanaName"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "Defs/TimeZones", nil, &answer); err != nil {
		return id
	}
	for _, zone := range answer.Value {
		if zone.ID != id {
			continue
		}
		// The IANA name is the one anybody has seen before, and the only one
		// another system would recognise.
		if zone.IanaName != "" {
			return zone.IanaName
		}
		return zone.Name
	}
	return id
}

// hhmm drops the seconds the phone system keeps on an office-hours time.
func hhmm(t string) string {
	if len(t) >= 5 {
		return t[:5]
	}
	return t
}

// scheduleArgs is one closure on its way in.
type scheduleArgs struct {
	Name       string `json:"name"`
	Starts     string `json:"starts"`
	Ends       string `json:"ends"`
	FromTime   string `json:"from_time"`
	ToTime     string `json:"to_time"`
	Repeats    bool   `json:"repeats"`
	Department string `json:"department"`
}

/*
addSchedule puts a closure on the phone system.

Dates arrive as text and are taken apart here, because the phone system keeps
six numbers and nothing above this should have to know that.
*/
func addSchedule(ctx context.Context, req plugin.Request) (any, error) {
	var args scheduleArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, plugin.Errorf("400",
			"a closure needs a name, so somebody reading the list knows what it is")
	}

	year, month, day, err := dateParts(args.Starts)
	if err != nil {
		return nil, err
	}
	// One day unless a second is given. A closure with no end is not something
	// the phone system can hold.
	endYear, endMonth, endDay := year, month, day
	if strings.TrimSpace(args.Ends) != "" {
		if endYear, endMonth, endDay, err = dateParts(args.Ends); err != nil {
			return nil, err
		}
	}

	from, err := asDuration(args.FromTime)
	if err != nil {
		return nil, err
	}
	to, err := asDuration(args.ToTime)
	if err != nil {
		return nil, err
	}
	if (from == "") != (to == "") {
		return nil, plugin.Errorf("400",
			"give both a start and an end time, or neither for a closure that covers whole days")
	}

	// A repeating closure carries no year: it is the twenty-fifth of December,
	// not the twenty-fifth of December 2026.
	if args.Repeats {
		year, endYear = 0, 0
	}

	body := map[string]any{
		"Name":        strings.TrimSpace(args.Name),
		"Day":         day,
		"Month":       month,
		"Year":        year,
		"DayEnd":      endDay,
		"MonthEnd":    endMonth,
		"YearEnd":     endYear,
		"IsRecurrent": args.Repeats,
	}
	if from != "" {
		body["TimeOfStartDate"] = from
		body["TimeOfEndDate"] = to
	}
	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	// Checked against what is already scheduled, before anything is written.
	// See conflicts.
	var existing struct {
		Value []closure `json:"value"`
	}
	if err := conn.get(ctx, "Holidays", nil, &existing); err != nil {
		return nil, err
	}
	wanted := closure{
		Name: strings.TrimSpace(args.Name),
		Day:  day, Month: month, Year: year,
		DayEnd: endDay, MonthEnd: endMonth, YearEnd: endYear,
		IsRecurrent: &args.Repeats,
	}
	if why := conflicts(wanted, existing.Value); why != "" {
		return nil, plugin.Errorf("409", "%s", why)
	}

	/*
		Which department the closure belongs to.

		Required, whatever the schema says: it is documented nullable and the
		phone system refuses a create without it. So one is chosen when nobody
		names one, and it is the default group — which on 3CX is the whole
		company, and therefore what "a company holiday" means.
	*/
	department, err := groupFor(ctx, conn, args.Department)
	if err != nil {
		return nil, err
	}
	body["Group"] = department
	var made closure
	if err := conn.post(ctx, "Holidays", body, &made); err != nil {
		return nil, err
	}
	return map[string]any{"added": true, "closure": made.plain()}, nil
}

/*
removeSchedule takes a closure off the phone system.

By id and one at a time. A closure is small enough to re-add and large enough
to matter — deleting the wrong one means a business open on a day it meant to
be shut — so there is no way here to say "all" and no pattern to get wrong.
*/
func removeSchedule(ctx context.Context, req plugin.Request) (any, error) {
	var args struct {
		ID int64 `json:"id"`
	}
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	if args.ID == 0 {
		return nil, plugin.Errorf("400", "say which closure to remove, by its id")
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := conn.remove(ctx, fmt.Sprintf("Holidays(%d)", args.ID)); err != nil {
		return nil, err
	}
	return map[string]any{"removed": true, "id": args.ID}, nil
}

/*
dateParts takes a date back apart into what the phone system stores.

"2026-12-25" and "--12-25" both arrive here; the second repeats every year and
has no year to give.
*/
func dateParts(date string) (year, month, day int, err error) {
	date = strings.TrimSpace(date)
	if rest, repeating := strings.CutPrefix(date, "--"); repeating {
		if _, e := fmt.Sscanf(rest, "%d-%d", &month, &day); e != nil {
			return 0, 0, 0, badDate(date)
		}
	} else if _, e := fmt.Sscanf(date, "%d-%d-%d", &year, &month, &day); e != nil {
		return 0, 0, 0, badDate(date)
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return 0, 0, 0, badDate(date)
	}
	return year, month, day, nil
}

/*
groupFor resolves which department a closure belongs to, as the phone system
names it.

By number rather than by name. A holiday's Group is documented as a plain
string and refuses a display name with NOT_FOUND — it wants the group's number,
"GRP0000", which is the same identifier a group carries everywhere else and not
the one anybody types.

Required, whatever the schema says: it is documented nullable and a create
without it is refused. So a closure nobody assigned goes to the default group,
which on 3CX is the whole company and therefore what "a company holiday" means.
*/
func groupFor(ctx context.Context, conn pbx, asked string) (string, error) {
	groups, err := readGroups(ctx, conn)
	if err != nil {
		return "", err
	}
	asked = strings.TrimSpace(asked)

	var fallback string
	for _, g := range groups {
		if g.Number == "" {
			continue
		}
		if asked != "" && (g.Name == asked || g.Number == asked) {
			return g.Number, nil
		}
		if g.Name == "DEFAULT" || fallback == "" {
			if g.Name == "DEFAULT" || fallback == "" {
				fallback = g.Number
			}
		}
	}
	if asked != "" {
		return "", plugin.Errorf("400", "this phone system has no department called %q", asked)
	}
	if fallback == "" {
		return "", plugin.Errorf("400",
			"say which department this closure is for; the phone system will not take one without")
	}
	return fallback, nil
}

/*
conflicts reports why a closure cannot be added, or "" if it can.

Two rules, and the phone system enforces neither usefully.

A name has to be unique: 3CX refuses a repeat with HTTP 500 and an empty body,
which is indistinguishable from the phone system having fallen over. Catching
it here turns an outage-shaped error into a sentence.

Dates must not overlap, and 3CX does not check at all — it accepted Christmas
Day and a Boxing week spanning it without complaint, leaving two closures
covering the same day and no way to say which one's hours apply. That is a
business closed when it meant to be open, found out on the day.
*/
func conflicts(wanted closure, existing []closure) string {
	for _, have := range existing {
		if strings.EqualFold(strings.TrimSpace(have.Name), wanted.Name) {
			return fmt.Sprintf("there is already a closure called %q", have.Name)
		}
	}
	for _, have := range existing {
		if !overlap(wanted, have) {
			continue
		}
		return fmt.Sprintf("that overlaps %q, which runs %s to %s. Two closures over one day leave "+
			"no saying which one's hours apply",
			have.Name, dateText(have.Year, have.Month, have.Day), dateText(have.YearEnd, have.MonthEnd, have.DayEnd))
	}
	return ""
}

/*
overlap reports whether two closures cover any of the same day.

Compared as days of the year, because that is the part every closure has — a
repeating one carries no year at all. The years are then checked separately,
and only when both closures have them: a closure that repeats happens in every
year, including whichever year the other one is in.

A span that crosses new year runs from December to January and is two intervals
rather than one, which is why this splits rather than comparing a pair of
numbers.
*/
func overlap(a, b closure) bool {
	if !sameYears(a, b) {
		return false
	}
	for _, one := range daysOf(a) {
		for _, two := range daysOf(b) {
			if one[0] <= two[1] && two[0] <= one[1] {
				return true
			}
		}
	}
	return false
}

// daysOf writes a closure as day-of-year intervals, splitting one that crosses
// new year into the two it really is.
func daysOf(c closure) [][2]int {
	from, to := c.Month*100+c.Day, c.MonthEnd*100+c.DayEnd
	if to == 0 {
		to = from
	}
	if from <= to {
		return [][2]int{{from, to}}
	}
	return [][2]int{{from, 1231}, {101, to}}
}

// sameYears reports whether two closures can fall in the same year. One that
// repeats falls in all of them.
func sameYears(a, b closure) bool {
	if repeats(a) || repeats(b) || a.Year == 0 || b.Year == 0 {
		return true
	}
	aEnd, bEnd := a.YearEnd, b.YearEnd
	if aEnd == 0 {
		aEnd = a.Year
	}
	if bEnd == 0 {
		bEnd = b.Year
	}
	return a.Year <= bEnd && b.Year <= aEnd
}

func repeats(c closure) bool { return c.IsRecurrent != nil && *c.IsRecurrent }

func badDate(date string) error {
	return plugin.Errorf("400",
		"%q is not a date. Write one as 2026-12-25, or as --12-25 for a day that repeats every year",
		date)
}

/*
Office hours and office holidays are two different things.

The hours are the week a department keeps — open nine to six, Monday to Friday.
The holidays are the dated exceptions to it. The console keeps them on separate
pages and so does this: setting the hours is changing what normally happens,
and adding a holiday is saying that one day is not normal.

Which also settles a question that looks harder than it is. "Close at four this
Friday, back to six the Friday after" needs no scheduled reversion and no
second action — it is one holiday, on one date, from 16:00 to 18:00. It expires
because it is dated. Changing the office hours and changing them back is the
thing to avoid: it leaves the department wrong if the second half is forgotten,
and there is nothing to forget in a dated exception.
*/

// hoursArgs sets one department's weekly pattern.
type hoursArgs struct {
	Department string `json:"department"`
	// Days is the week, as it will be. A day left out is a day the department
	// is closed — the whole pattern is written at once, because that is how
	// the phone system keeps it and a partial write would be a week nobody
	// described.
	Days []struct {
		Day  string `json:"day"`
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"days"`
}

// The days a week has, in the order the phone system names them.
var weekdays = []string{
	"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday",
}

/*
setHours writes a department's office hours.

The whole week every time. 3CX keeps the pattern as a list of periods and
replaces it wholesale, so sending one day would be sending a week with one day
in it — and a department open only on Thursdays is a change nobody asked for.
*/
func setHours(ctx context.Context, req plugin.Request) (any, error) {
	var args hoursArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}

	periods := make([]map[string]any, 0, len(args.Days))
	for _, day := range args.Days {
		name := strings.TrimSpace(day.Day)
		if !slices.Contains(weekdays, name) {
			return nil, plugin.Errorf("400", "%q is not a day of the week", day.Day)
		}
		from, err := hoursOfDay(day.From)
		if err != nil {
			return nil, err
		}
		to, err := hoursOfDay(day.To)
		if err != nil {
			return nil, err
		}
		if from == to {
			// A day that opens and closes at the same moment is a day the
			// department is shut, and the phone system says that by the day
			// not being there at all.
			continue
		}
		periods = append(periods, map[string]any{"DayOfWeek": name, "Start": from, "Stop": to})
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	all, err := readGroups(ctx, conn)
	if err != nil {
		return nil, err
	}
	chosen, err := pickGroup(all, args.Department)
	if err != nil {
		return nil, err
	}

	/*
		SpecificHoursExcludingHolidays: the week below, with the holidays still
		overriding it. The alternative treats every holiday as an ordinary day,
		which is the setting somebody notices on Christmas morning.

		Written whatever the week says, including when it says nothing. The
		phone system has a "Never" for a department that is never open, and
		reaching for it because somebody cleared the days would be answering a
		question nobody asked — a week with no open days and a department shut
		permanently are different statements, and only one of them was made.
	*/
	body := map[string]any{"Hours": map[string]any{
		"Type":    "SpecificHoursExcludingHolidays",
		"Periods": periods,
	}}
	if err := conn.patch(ctx, fmt.Sprintf("Groups(%d)", chosen.ID), body); err != nil {
		return nil, err
	}
	return map[string]any{"changed": true, "department": chosen.Name, "open_days": len(periods)}, nil
}

/*
hoursOfDay writes a time of day the way office hours keep one.

Seconds included, because the phone system's own format has them — its pattern
insists on HH:MM:SS and refuses HH:MM. Nobody opens at half past nine and
thirty seconds, so they are always zero.
*/
func hoursOfDay(hhmm string) (string, error) {
	hhmm = strings.TrimSpace(hhmm)
	if hhmm == "" {
		return "00:00:00", nil
	}
	var hours, minutes int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &hours, &minutes); err != nil {
		return "", plugin.Errorf("400", "%q is not a time of day, which reads as 09:00", hhmm)
	}
	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return "", plugin.Errorf("400", "%q is not a time of day", hhmm)
	}
	return fmt.Sprintf("%02d:%02d:00", hours, minutes), nil
}
