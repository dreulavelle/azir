package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/dreulavelle/azir/internal/blf"
	"github.com/dreulavelle/azir/internal/bulk"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
Reading and changing what an extension is set to.

Everything a technician sees on an extension's page in the phone system's own
console: who it is, how it forwards, what its voicemail does, the options, and
the buttons on the desk phone. Kept apart from diagnostics next door, which
asks what a phone system is doing rather than what it is set to.

The fields are published rather than hardcoded anywhere else. Azir does not
know what a 3CX extension can be set to and should not — this is the list, and
the columns of a spreadsheet, the controls in a form and the allowlist on the
way back out are all built from it.
*/

/*
editable is every extension option this plugin will set in bulk, and the type
each one takes.

An allowlist rather than a pass-through, and the reason is the same one that
governs every read here: 3CX's user object carries AuthID, AuthPassword and the
phone's web password alongside these. A bulk editor that forwarded whatever it
was handed would be a way to set a SIP password on forty extensions at once,
from a tool whose whole point is convenience — and the assistant can propose
calls to it.

The names are 3CX's own, so what an administrator reads in the console is what
they write here. The comments are the console's wording, which is not always
the same thing.
*/
// option is one extension setting this plugin will change, and how it reads.
//
// Published to Azir with every settings read, so the console can offer these
// as columns and as a form without keeping its own copy of the list. Keeping
// a second copy is how the sheet ended up carrying two of the thirty-two.
type option struct {
	// Field is 3CX's own name for the setting, so what an administrator reads
	// in the console is what they write here. Published as "field" because
	// that is what it is to whatever reads this: the thing a column or a form
	// control is identified by.
	Field string `json:"field"`
	// Label is the console's wording, which is not always the same thing.
	Label string `json:"label"`
	// Kind is "bool", "text", "choice" or "secret". A choice carries its
	// Choices. A secret can be written and is never read back — a voicemail
	// PIN is a credential, and the rule here is that Azir sets them and never
	// shows them.
	Kind    string   `json:"kind"`
	Group   string   `json:"group"`
	Choices []string `json:"choices,omitempty"`
}

var editable = []option{
	// General — who the extension is.
	{"FirstName", "First name", "text", "General", nil},
	{"LastName", "Last name", "text", "General", nil},
	{"EmailAddress", "Email", "text", "General", nil},
	{"Mobile", "Mobile number", "text", "General", nil},
	{"OutboundCallerID", "Outbound caller ID", "text", "General", nil},
	{"Enable2FA", "Two-factor authentication", "bool", "General", nil},
	{"Enabled", "Enabled", "bool", "General", nil},
	{"Internal", "May only call internally", "bool", "General", nil},
	{"HideInPhonebook", "Hide from the company phonebook", "bool", "General", nil},
	{"EnableHotdesking", "Hot desking", "bool", "General", nil},
	{"SendEmailMissedCalls", "Email on a missed call", "bool", "General", nil},

	// Voicemail.
	{"VMEnabled", "Voicemail", "bool", "Voicemail", nil},
	{"VMPIN", "Voicemail PIN", "secret", "Voicemail", nil},
	{"VMDisablePinAuth", "No PIN needed for voicemail", "bool", "Voicemail", nil},
	{"VMEmailOptions", "What to email about voicemail", "choice", "Voicemail",
		[]string{"None", "Notification", "Attachment", "AttachmentAndDelete", "VmailToMembers", "EmailToExtrasOnly"}},
	{"VMPlayMsgDateTime", "Read out the time of the message", "choice", "Voicemail",
		[]string{"None", "Play12Hr", "Play24Hr"}},
	{"VMPlayCallerID", "Read out the caller's number", "bool", "Voicemail", nil},
	{"PinProtected", "PIN protected", "bool", "Voicemail", nil},

	// Options — the three every MSP checks, and the recording set.
	{"PbxDeliversAudio", "PBX delivers audio", "bool", "Options", nil},
	// One setting, because that is what it is in the console. 3CX keeps two
	// booleans — BlockTunnel and AllowLanOnly — behind a single checkbox, so
	// offering both meant a technician could set the one the console does not
	// show and watch nothing happen. Written together, always; see
	// setExtensionOptions.
	{"BlockTunnel", "Block remote non-tunnel connections", "bool", "Options", nil},
	{"RecordCalls", "Record calls", "bool", "Options", nil},
	{"RecordExternalCallsOnly", "Record external calls only", "bool", "Options", nil},
	{"RecordEmailNotify", "Email when a call is recorded", "bool", "Options", nil},
	{"AllowOwnRecordings", "Let them hear their own recordings", "bool", "Options", nil},
	{"SRTPMode", "Encrypt the audio (SRTP)", "choice", "Options",
		[]string{"SRTPDisabled", "SRTPEnabled", "SRTPEnforced"}},
	{"CallScreening", "Ask callers to say who they are", "bool", "Options", nil},
	{"TranscriptionMode", "Transcribe", "choice", "Options",
		[]string{"Nothing", "Voicemail", "Recordings", "Both", "Inherit"}},
	{"PromptSet", "Prompt set", "text", "Options", nil},

	// Apps and sign-in. Not on the list of what has to be editable, but it was
	// already here and taking it away would be a regression somebody notices.
	{"MyPhoneShowRecordings", "Show recordings in the app", "bool", "Apps and sign-in", nil},
	{"MyPhoneHideForwardings", "Hide forwarding rules in the app", "bool", "Apps and sign-in", nil},
	{"MyPhoneAllowDeleteRecordings", "Let them delete recordings", "bool", "Apps and sign-in", nil},
	{"GoogleSignInEnabled", "Sign in with Google", "bool", "Apps and sign-in", nil},
	{"GoogleCalendarEnabled", "Google Calendar", "bool", "Apps and sign-in", nil},
	{"GoogleContactsEnabled", "Google Contacts", "bool", "Apps and sign-in", nil},
	{"MS365SignInEnabled", "Sign in with Microsoft 365", "bool", "Apps and sign-in", nil},
	{"MS365CalendarEnabled", "Microsoft 365 Calendar", "bool", "Apps and sign-in", nil},
	{"MS365ContactsEnabled", "Microsoft 365 Contacts", "bool", "Apps and sign-in", nil},
	{"MS365TeamsEnabled", "Microsoft Teams", "bool", "Apps and sign-in", nil},
}

/*
settable is everything a form or a sheet can change: the fields on the
extension itself, and the forwarding rules that live on a profile beside it.

One list, because everything reading this is asking the same question — what
can be changed — and the answer should not depend on where the phone system
happens to keep it.
*/
func settable(lists fieldLists) []published {
	all := make([]option, 0, len(editable)+len(forwardingFields)+2)
	all = append(all, editable...)
	all = append(all, forwardingFields...)
	// The department and the role both live on the membership joining an
	// extension to a group, and both are offered as what the phone system
	// actually has rather than as free text. The department used to be
	// read-only on the reasoning that moving somebody between groups is a
	// wider act than renaming them — which is true, and left the extensions
	// that belong to no group with no way to be given one, and therefore no
	// way to be given a role either.
	all = append(all,
		option{fieldDepartment, "Department", "choice", "General", lists.Departments.Values},
		option{fieldRole, "Role", "choice", "General", lists.Roles.Values},
	)
	all = append(all, handsetFields...)

	labelled := map[string]map[string]string{
		fieldRole:       lists.Roles.Labels,
		fieldDepartment: lists.Departments.Labels,
		fieldInterface:  lists.Routing.Labels,
	}
	out := make([]published, 0, len(all))
	for _, o := range all {
		// The routing device is the one thing on the IP phone tab worth
		// setting in bulk, so it alone stops being read-only — and only when
		// the phone system named somewhere for it to point.
		if o.Field == fieldInterface && len(lists.Routing.Values) > 0 {
			o.Kind, o.Choices = "choice", lists.Routing.Values
		}
		entry := published{option: o, Unique: uniquePerExtension[o.Field]}
		entry.Labels = labelled[o.Field]
		out = append(out, entry)
	}
	return out
}

// fieldLists is what the phone system will accept, for the fields whose
// answers are a list rather than anything somebody types.
type fieldLists struct {
	Roles       choices
	Departments choices
	Routing     choices
}

/*
published is an option on its way out to Azir: the table entry above, plus the
two things that are true of a field's use rather than of the field.

Embedded rather than added as columns to the table, which is fifty lines long
and stays readable precisely because every row says the same five things.
*/
type published struct {
	option
	// Labels is what to show for each choice where the stored value is not
	// something to put in front of a person — "system_owners" is a role, but
	// it is not what anybody calls it.
	Labels map[string]string `json:"labels,omitempty"`
	// Unique marks a field no two extensions may share.
	Unique bool `json:"unique,omitempty"`
}

/*
uniquePerExtension is the fields 3CX will not let two extensions share.

Published so that Azir can refuse the edit rather than discover it: setting one
address across five extensions is refused four times by the phone system, once
per extension, and only after the first has already been written. There is no
undoing that from the error.

An email address is the one so far. A mobile number is not — 3CX takes the same
one on every extension, which is what a shared on-call phone is.
*/
var uniquePerExtension = map[string]bool{
	"EmailAddress": true,
}

// editableByName is the same list as an allowlist to check against.
var editableByName = func() map[string]option {
	byName := make(map[string]option, len(editable)+len(forwardingFields))
	for _, o := range editable {
		byName[o.Field] = o
	}
	for _, o := range forwardingFields {
		byName[o.Field] = o
	}
	// The three that are not fields on the extension. Their choices are left
	// empty here on purpose: what a phone system will accept for them is
	// fetched per request, and a stale copy in a package-level map is the
	// thing that made the role dropdown wrong in the first place. The value is
	// checked by the writer that knows — setDepartment against the groups that
	// exist, setRoutingDevice against the phone it is changing.
	byName[fieldRole] = option{fieldRole, "Role", "choice", "General", nil}
	byName[fieldDepartment] = option{fieldDepartment, "Department", "choice", "General", nil}
	byName[fieldInterface] = option{fieldInterface, "Routing device", "choice", "IP phone", nil}
	return byName
}()

type bulkOptionsArgs struct {
	Extensions []string       `json:"extensions"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Options    map[string]any `json:"options"`
}

/*
setExtensionOptions applies one set of options to many extensions at once.

3CX has its own bulk endpoint, so this is one call rather than forty — which
matters both for the PBX and for how long somebody waits. Extensions are named
explicitly or given as an inclusive range, because "every extension" is not
something this should be able to express by accident.

It reports which numbers it matched before changing anything, so a range that
quietly covered fewer extensions than expected is visible in the answer rather
than discovered later.
*/
func setExtensionOptions(ctx context.Context, req plugin.Request) (any, error) {
	var args bulkOptionsArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	if len(args.Options) == 0 {
		return nil, plugin.Errorf("400", "nothing to change: give at least one option")
	}

	// Refused by name, so a caller learns which option is not available rather
	// than watching the change silently do nothing.
	settings := map[string]any{}
	rules := map[string]any{}
	var refused []string
	for key, value := range args.Options {
		spec, ok := editableByName[key]
		if !ok {
			refused = append(refused, key)
			continue
		}
		// Forwarding lives on a profile beside the extension, and the role
		// on the group membership. Both are collected here and written
		// separately below.
		if forwarding(key) || key == fieldRole {
			rules[key] = value
			continue
		}
		// The department is the extension's group membership rather than a
		// value on the extension, and the routing device belongs to the phone
		// beside it. Both are written separately below.
		if key == fieldDepartment || key == fieldInterface {
			rules[key] = value
			continue
		}
		switch spec.Kind {
		case "bool":
			b, ok := value.(bool)
			if !ok {
				return nil, plugin.Errorf("400", "%s is either true or false", key)
			}
			settings[key] = b
			// One checkbox in the console, two booleans underneath. Setting
			// only the one the console does not draw is a change a technician
			// makes, is told succeeded, and cannot see anywhere.
			if key == fieldTunnel {
				settings[fieldLanOnly] = b
			}
		default:
			// A choice is checked against what 3CX accepts, so a typo is
			// refused by name here rather than as an opaque 400 from the PBX
			// halfway through a batch.
			//
			// A secret goes through unchecked and unlogged. It is write-only
			// by construction: nothing reads it back, so there is nothing to
			// compare it against.
			if len(spec.Choices) > 0 {
				text, _ := value.(string)
				if !slices.Contains(spec.Choices, text) {
					return nil, plugin.Errorf("400", "%s is one of %s",
						key, strings.Join(spec.Choices, ", "))
				}
			}
			settings[key] = value
		}
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return nil, plugin.Errorf("400",
			"this cannot set %s. It changes extension options only, never credentials or numbering",
			strings.Join(refused, ", "))
	}

	wanted, err := args.numbers()
	if err != nil {
		return nil, err
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	known, err := extensionIDs(ctx, conn)
	if err != nil {
		return nil, err
	}

	// One PATCH per extension rather than 3CX's own MultiUserUpdate.
	//
	// That endpoint exists and takes exactly this shape, and it answers a bare
	// HTTP 500 with no body for a partial object — which is precisely what a
	// caller changing two settings out of forty-seven has to send. Guessing
	// which of the other forty-five it wants populated is not a foundation.
	//
	// Fifty sequential changes is a few seconds and stays well inside the rate
	// limit, and it buys what the bulk call could not: a result per extension.
	// Eight changed and two skipped is the case that actually happens, and one
	// success or one failure cannot express it.
	results := make([]map[string]any, 0, len(wanted))
	changed := 0
	for _, number := range wanted {
		id, ok := known[number]
		if !ok {
			results = append(results, map[string]any{
				"extension": number, "changed": false, "reason": "no extension here has that number",
			})
			continue
		}
		// Every part is attempted, and what failed is named.
		//
		// This used to stop at the first failure and report the extension
		// unchanged, which was a lie in the case that actually happens: the
		// settings patch succeeds, the role write is refused because the
		// extension is in no group, and a technician is told nothing happened
		// to an extension that has just been renamed.
		var refusals []string
		var did []string
		attempt := func(what string, err error) {
			if err != nil {
				refusals = append(refusals, plainReason(err))
				return
			}
			did = append(did, what)
		}

		if len(settings) > 0 {
			attempt("its settings", conn.patch(ctx, fmt.Sprintf("Users(%d)", id), settings))
		}
		if group, changing := rules[fieldDepartment]; changing {
			attempt("its department", setDepartment(ctx, conn, id, asText(group)))
		}
		if role, changing := rules[fieldRole]; changing {
			attempt("its role", setRole(ctx, conn, id, asText(role)))
		}
		if iface, changing := rules[fieldInterface]; changing {
			attempt("its routing device", setRoutingDevice(ctx, conn, id, asText(iface)))
		}
		if fwd := forwardingOnly(rules); len(fwd) > 0 {
			attempt("its call forwarding", setForwarding(ctx, conn, id, fwd))
		}

		if len(refusals) > 0 {
			reason := strings.Join(refusals, "; ")
			if len(did) > 0 {
				// Saying what did land matters more than saying what did not:
				// somebody deciding whether to run this again needs to know
				// the extension is now half-changed.
				reason += " (" + strings.Join(did, " and ") + " did change)"
			}
			results = append(results, map[string]any{
				"extension": number, "changed": false, "reason": reason,
			})
			continue
		}
		changed++
		results = append(results, map[string]any{"extension": number, "changed": true})
	}

	return map[string]any{
		"changed":  changed,
		"asked":    len(wanted),
		"options":  settings,
		"results":  results,
		"complete": changed == len(wanted),
	}, nil
}

// numbers resolves either way of naming extensions into one list.
func (a bulkOptionsArgs) numbers() ([]string, error) {
	if len(a.Extensions) > 0 && (a.From != "" || a.To != "") {
		return nil, plugin.Errorf("400", "name the extensions or give a range, not both")
	}

	if len(a.Extensions) > 0 {
		if len(a.Extensions) > howManyAtOnce {
			return nil, plugin.Errorf("400", "that is more than %d at once", howManyAtOnce)
		}
		return a.Extensions, nil
	}

	from, err1 := strconv.Atoi(strings.TrimSpace(a.From))
	to, err2 := strconv.Atoi(strings.TrimSpace(a.To))
	if err1 != nil || err2 != nil {
		return nil, plugin.Errorf("400", "give a list of extensions, or a numeric range from and to")
	}
	if to < from {
		return nil, plugin.Errorf("400", "%d is before %d", to, from)
	}
	if to-from+1 > howManyAtOnce {
		return nil, plugin.Errorf("400", "that range covers more than %d extensions", howManyAtOnce)
	}

	// The whole range is asked for, and whatever does not exist comes back as
	// not_found rather than being skipped in silence — a range is usually typed
	// from memory, and the interesting case is the one that is not there.
	out := make([]string, 0, to-from+1)
	for n := from; n <= to; n++ {
		out = append(out, strconv.Itoa(n))
	}
	return out, nil
}

/*
extensionSettings reads what the editable options are set to right now.

The before half of a before-and-after. Comparing a sheet of forty extensions
against the system cannot mean forty requests to somebody's PBX, so this asks
once and pages through, the way the extension list does.

The field list is the same allowlist that governs writing them, which is the
property worth keeping: this can never return a name that could not have been
set, and it can never return AuthID, AuthPassword or the phone's web password,
which sit on the same 3CX user object. Reading is strictly less than the
writing already allowed here — the assistant can already propose setting these
— and a proposal made against what is actually configured is a better proposal
than one made blind.
*/
func extensionSettings(ctx context.Context, req plugin.Request) (any, error) {
	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	// Sorted so the request is identical between calls, which keeps it
	// cacheable and keeps a diff of two runs readable.
	// Secrets are never asked for. Leaving them out of the $select is the
	// boundary itself: a value that is never fetched cannot be logged, cached,
	// returned or exported by some later mistake.
	fields := make([]string, 0, len(editable))
	for _, o := range editable {
		if o.Kind == "secret" {
			continue
		}
		fields = append(fields, o.Field)
	}
	sort.Strings(fields)
	// Blfs comes along but is not a field: it is one blob of XML holding the
	// whole key layout, so it is returned as its own thing rather than as a
	// setting somebody could put in a spreadsheet column.
	selected := append([]string{"Number", "DisplayName", "Blfs"}, fields...)

	// The same page as everything else here. A larger one would mean fewer
	// round trips on a big system, and 3CX answers 400 to $top=500 — the
	// schema documents no maximum, so the ceiling is whatever the runtime
	// decides and not something to guess at from here.
	//
	// It costs less than it looks. The loop stops on the first short page, so
	// a thousand extensions is eleven requests, and the result is cached for
	// two minutes rather than fetched on every screen that reads it.
	const settingsPage = pageSize

	out := make([]map[string]any, 0, settingsPage)
	everyRow := make([]map[string]any, 0, settingsPage)
	complete := true
	for skip := 0; skip < maxExtensions; skip += settingsPage {
		q := url.Values{}
		q.Set("$select", strings.Join(selected, ","))
		q.Set("$expand", "ForwardingProfiles,Groups($expand=GroupRights),Phones")
		q.Set("$top", fmt.Sprint(settingsPage))
		q.Set("$skip", fmt.Sprint(skip))
		q.Set("$orderby", "Number")

		var page struct {
			Value []map[string]any `json:"value"`
		}
		if err := conn.get(ctx, "Users", q, &page); err != nil {
			return nil, err
		}
		everyRow = append(everyRow, page.Value...)
		for _, row := range page.Value {
			settings := map[string]any{}
			for _, name := range fields {
				if value, ok := row[name]; ok && value != nil {
					settings[name] = value
				}
			}
			// The forwarding rules live on a profile rather than on the
			// extension, so they are flattened onto it — one field each,
			// looking like every other field, which is what lets them be
			// changed in bulk and shown in a diff without anything else
			// learning what a forwarding profile is.
			for name, value := range forwardingOf(row["ForwardingProfiles"]) {
				settings[name] = value
			}
			// Which department somebody is in and what they are trusted with
			// are held on the membership rather than on the extension.
			for name, value := range membershipOf(row["Groups"]) {
				settings[name] = value
			}
			// The handset, which is a record beside the extension rather than
			// anything on it.
			for name, value := range handsetOf(row["Phones"]) {
				settings[name] = value
			}
			out = append(out, map[string]any{
				"extension": asText(row["Number"]),
				"name":      asText(row["DisplayName"]),
				"settings":  settings,
				"keys":      parsedKeys(asText(row["Blfs"])),
			})
		}
		if len(page.Value) < settingsPage {
			break
		}
		if skip+settingsPage >= maxExtensions {
			// Stopped at the ceiling with more to come. Saying so is the whole
			// point: a list that quietly ends is read as the whole list, and
			// somebody changing "all of them" would miss whatever was past the
			// cut without ever knowing there was a cut.
			complete = false
		}
	}

	// The field list travels with the values, so whatever reads this can offer
	// them as columns or as a form without keeping its own copy of what 3CX
	// will accept.
	return map[string]any{
		"extensions": out, "count": len(out),
		"fields": settable(whatItAccepts(ctx, conn, everyRow)), "complete": complete,
	}, nil
}

// asText reads a JSON value as a string without caring which shape it arrived
// in. 3CX returns extension numbers as strings; being strict about that here
// would trade correctness for nothing.
func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

/*
parsedKeys reads a layout, and answers an unreadable one with no keys.

A layout this cannot parse is a screen that cannot draw, and one extension with
something unexpected on it should not take the list down with it. What it does
mean is that Azir will not offer to change that extension's keys, which is the
right way for this to fail.
*/
func parsedKeys(raw string) []blf.Key {
	keys, err := blf.Parse(raw)
	if err != nil {
		return nil
	}
	if keys == nil {
		return []blf.Key{}
	}
	return keys
}

// blfArgs sets the key layout on one or more extensions.
type blfArgs struct {
	Extensions []string  `json:"extensions"`
	Keys       []blf.Key `json:"keys"`
	// From copies the layout off another extension instead of giving one.
	From string `json:"from"`
}

/*
setExtensionKeys writes a phone's key layout.

Many extensions at once, because the job this exists for is usually "give these
twelve phones the layout that one has". A key that watches extension 101 points
at 101 by the phone system's own id, so the same layout means the same thing on
every phone it lands on — copying is a copy, not a translation.

The whole layout goes every time. The phone system keeps it as one string, so
there is no such thing as changing one button, and pretending otherwise would
mean reading, editing and writing on every keystroke.

Numbering is taken from what is already there. A layout that starts at button
two stays starting at button two: the phone system does not write out a key
left at its default, and renumbering from one would take that button over.
*/
func setExtensionKeys(ctx context.Context, req plugin.Request) (any, error) {
	var args blfArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	if len(args.Extensions) == 0 {
		return nil, plugin.Errorf("400", "say which extensions to set the keys on")
	}
	if len(args.Extensions) > howManyAtOnce {
		return nil, plugin.Errorf("400", "that is more than %d at once", howManyAtOnce)
	}
	if args.From != "" && len(args.Keys) > 0 {
		return nil, plugin.Errorf("400", "give the keys, or an extension to copy them from, not both")
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	known, err := extensionIDs(ctx, conn)
	if err != nil {
		return nil, err
	}

	// Where the layout sits on the physical phone.
	//
	// Copying keeps the source's first button. The phone system does not write
	// out a key left at its default, so a layout that starts at button two is
	// one where button one was left alone — and landing it on button one of
	// another phone would shift every key up by one and take over a button
	// nobody mentioned. Two phones that were copied should have the same
	// buttons in the same places, which is the entire point.
	//
	// Setting keys outright keeps whatever the target already started at.
	keys := args.Keys
	from := 0
	if args.From != "" {
		keys, err = keysOf(ctx, conn, known, args.From)
		if err != nil {
			return nil, err
		}
		from = blf.StartsAt(keys)
	}

	// A key that watches or dials an extension points at it by the phone
	// system's own id. Resolved here, from the number somebody wrote, because
	// nothing outside this plugin should have to know those ids exist.
	for i, key := range keys {
		if !blf.Known(key.Kind) {
			return nil, plugin.Errorf("400", "this does not know how to write a %q key", key.Kind)
		}
		switch key.Kind {
		case blf.KindBLF, blf.KindSpeedDial:
			number := strings.TrimSpace(key.Value)
			id, there := known[number]
			if !there {
				return nil, plugin.Errorf("400",
					"key %d watches extension %s, and there is no such extension here", i+1, number)
			}
			keys[i].ID = fmt.Sprint(id)
			keys[i].Value = number
		case blf.KindQueueLogin:
			if key.ID != blf.LoggedIn && key.ID != blf.LoggedOut {
				return nil, plugin.Errorf("400",
					"a queue login key is either %s or %s", blf.LoggedIn, blf.LoggedOut)
			}
			keys[i].Value = blf.ByID
		default:
			if key.ID == "" {
				keys[i].ID = blf.Nothing
			}
		}
	}

	results := make([]map[string]any, 0, len(args.Extensions))
	changed := 0
	for _, number := range args.Extensions {
		id, there := known[number]
		if !there {
			results = append(results, map[string]any{
				"extension": number, "changed": false, "reason": "no extension here has that number",
			})
			continue
		}
		// Read what is there to keep the layout on the same buttons.
		was, err := keysOf(ctx, conn, known, number)
		if err != nil {
			results = append(results, map[string]any{
				"extension": number, "changed": false, "reason": plainReason(err),
			})
			continue
		}
		first := from
		if first == 0 {
			first = blf.StartsAt(was)
		}
		laid := blf.Renumber(keys, first)
		if err := conn.patch(ctx, fmt.Sprintf("Users(%d)", id),
			map[string]any{"Blfs": blf.Render(laid)}); err != nil {
			results = append(results, map[string]any{
				"extension": number, "changed": false, "reason": plainReason(err),
			})
			continue
		}
		changed++
		results = append(results, map[string]any{
			"extension": number, "changed": true, "keys": len(laid),
		})
	}

	return map[string]any{
		"changed": changed, "asked": len(args.Extensions),
		"keys": len(keys), "results": results, "complete": changed == len(args.Extensions),
	}, nil
}

// keysOf reads one extension's layout.
func keysOf(ctx context.Context, conn pbx, known map[string]int64, number string) ([]blf.Key, error) {
	id, there := known[number]
	if !there {
		return nil, plugin.Errorf("400", "no extension here has the number %s", number)
	}
	var user struct {
		Blfs string `json:"Blfs"`
	}
	q := url.Values{}
	q.Set("$select", "Blfs")
	if err := conn.get(ctx, fmt.Sprintf("Users(%d)", id), q, &user); err != nil {
		return nil, err
	}
	return blf.Parse(user.Blfs)
}

/*
Call forwarding, as fields.

The phone system keeps forwarding on a set of named profiles — Available, Away,
Out of office and two custom ones — each holding its own rules for busy and
unanswered calls. The one that matters for the question people actually ask is
Available: it is what an extension does when nobody has told it otherwise.

Flattened onto the extension as ordinary fields so a rule can be read in a
list, compared in a diff and changed across forty extensions at once. Writing
one back means reading the whole profile set, changing the one, and writing all
of them, because that is the shape the phone system takes.
*/

// theProfile is the forwarding profile these fields read and write.
const theProfile = "Available"

// forwardingFields are the rules, in the order they read on the phone system's
// own page.
var forwardingFields = []option{
	{"AcceptMultipleCalls", "Accept multiple calls", "bool", "Call forwarding", nil},
	{"NoAnswerTimeout", "Ring for, in seconds", "text", "Call forwarding", nil},
	{"NoAnswerExternal", "No answer, external calls", "destination", "Call forwarding", destinations},
	{"NoAnswerInternal", "No answer, internal calls", "destination", "Call forwarding", destinations},
	{"BusyExternal", "Busy, external calls", "destination", "Call forwarding", destinations},
	{"BusyInternal", "Busy, internal calls", "destination", "Call forwarding", destinations},
}

// destinations is where the phone system will send a call. Its own list rather
// than every value the API defines, because the rest are internal routing
// states nobody sets from a forwarding rule.
var destinations = []string{
	"None", "VoiceMail", "Extension", "External", "Queue", "RingGroup", "IVR", "Fax",
}

/*
forwardingOf reads the Available profile's rules as flat fields.

An extension with no such profile reads as no fields rather than as empty ones,
so nothing claims a rule is unset when it was never looked at.
*/
func forwardingOf(raw any) map[string]any {
	profiles, ok := raw.([]any)
	if !ok {
		return nil
	}
	for _, entry := range profiles {
		profile, ok := entry.(map[string]any)
		if !ok || asText(profile["Name"]) != theProfile {
			continue
		}
		out := map[string]any{
			"AcceptMultipleCalls": profile["AcceptMultipleCalls"],
			"NoAnswerTimeout":     asText(profile["NoAnswerTimeout"]),
		}
		route, _ := profile["AvailableRoute"].(map[string]any)
		for field, key := range map[string]string{
			"NoAnswerExternal": "NoAnswerExternal",
			"NoAnswerInternal": "NoAnswerInternal",
			"BusyExternal":     "BusyExternal",
			"BusyInternal":     "BusyInternal",
		} {
			out[field] = whereTo(route[key])
		}
		return out
	}
	return nil
}

// whereTo writes a destination as the one string the field carries.
func whereTo(raw any) string {
	dest, ok := raw.(map[string]any)
	if !ok {
		return "None"
	}
	where := asText(dest["To"])
	if where == "" {
		return "None"
	}
	// Only where the number means something. Voicemail carries the extension's
	// own number in this field, which is not somewhere a rule points — it is
	// whose voicemail it is. Including it would read as "VoiceMail:100" in
	// every diff, and would never match what a form sends back.
	//
	// The same answer internal/bulk gives when it reads one somebody typed,
	// from the same function, because two copies of this drifted apart once
	// already.
	if !bulk.NeedsNumber(where) {
		return where
	}
	number := asText(dest["Number"])
	if where == "External" {
		// An outside number is kept in its own field rather than in Number.
		if outside := asText(dest["External"]); outside != "" {
			number = outside
		}
	}
	if number == "" {
		return where
	}
	return where + ":" + number
}

// asDestination turns the field's string back into what the phone system takes.
func asDestination(value string) map[string]any {
	where, number := bulk.Where(value)
	if where == "" || where == "None" {
		return map[string]any{"To": "None", "Number": "", "External": ""}
	}
	dest := map[string]any{"To": where, "Number": number, "External": ""}
	if where == "External" {
		dest["Number"] = ""
		dest["External"] = number
	}
	return dest
}

/*
setForwarding writes the changed rules back onto the Available profile.

Read, change, write all — the profiles are a collection on the extension and
the phone system takes them together. Everything not named here is written back
exactly as it was found.
*/
func setForwarding(ctx context.Context, conn pbx, id int64, changes map[string]any) error {
	var user struct {
		Profiles []map[string]any `json:"ForwardingProfiles"`
	}
	q := url.Values{}
	q.Set("$select", "Id")
	q.Set("$expand", "ForwardingProfiles")
	if err := conn.get(ctx, fmt.Sprintf("Users(%d)", id), q, &user); err != nil {
		return err
	}

	found := false
	for _, profile := range user.Profiles {
		if asText(profile["Name"]) != theProfile {
			continue
		}
		found = true
		route, ok := profile["AvailableRoute"].(map[string]any)
		if !ok {
			route = map[string]any{}
			profile["AvailableRoute"] = route
		}
		for field, value := range changes {
			switch field {
			case "AcceptMultipleCalls":
				profile[field] = value
			case "NoAnswerTimeout":
				seconds, err := strconv.Atoi(asText(value))
				if err != nil || seconds < 1 || seconds > 600 {
					return plugin.Errorf("400", "ring for is a number of seconds between 1 and 600")
				}
				profile[field] = seconds
			default:
				route[field] = asDestination(asText(value))
			}
		}
	}
	if !found {
		return plugin.Errorf("400", "this extension has no %s forwarding profile", theProfile)
	}

	return conn.patch(ctx, fmt.Sprintf("Users(%d)", id),
		map[string]any{"ForwardingProfiles": user.Profiles})
}

// without copies a map minus one key, so the forwarding writer is handed only
// forwarding.
func without(all map[string]any, key string) map[string]any {
	out := make(map[string]any, len(all))
	for k, v := range all {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// forwardingOnly keeps the rules that belong to the forwarding profile, which
// is everything collected aside from the handful written through an endpoint
// of their own. Named for what it keeps rather than built by removing each of
// the others in turn: the removing version was one edit away from quietly
// sending a department to the forwarding writer.
func forwardingOnly(rules map[string]any) map[string]any {
	out := make(map[string]any, len(rules))
	for k, v := range rules {
		if forwarding(k) {
			out[k] = v
		}
	}
	return out
}

// forwarding reports whether a field belongs to the forwarding profile rather
// than to the extension itself.
func forwarding(field string) bool {
	for _, f := range forwardingFields {
		if f.Field == field {
			return true
		}
	}
	return false
}

/*
Department and role.

Neither is a field on the extension. The phone system holds both on the
membership joining an extension to a group: the group's name is the department,
and the rights attached to that membership carry the role.

An extension can be in several groups. The first is the one its page shows and
the one these read, because that is the answer to the question somebody is
asking when they look at a list of extensions.
*/
const (
	fieldDepartment = "Department"
	fieldRole       = "Role"
	// fieldInterface is the phone's routing device, which lives on the handset
	// record beside the extension rather than on the extension.
	fieldInterface = "PhoneInterface"
	// The two booleans behind the console's one "Block remote non-tunnel
	// connections" checkbox. Azir publishes the first and writes both.
	fieldTunnel  = "BlockTunnel"
	fieldLanOnly = "AllowLanOnly"
)

// membershipOf reads the department and role off the first group an extension
// belongs to.
func membershipOf(raw any) map[string]any {
	groups, ok := raw.([]any)
	if !ok || len(groups) == 0 {
		return nil
	}
	first, ok := groups[0].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{fieldDepartment: asText(first["Name"])}
	if rights, ok := first["GroupRights"].(map[string]any); ok {
		out[fieldRole] = asText(rights["RoleName"])
	}
	return out
}

/*
rolesInUse is every role this phone system has somebody in.

The API documents RoleName as a plain string with no list of what it accepts,
and there is no endpoint that will name them — so the roles that exist are
found by looking at who has one. A role has to exist for somebody to hold it,
which makes this sound; what it misses is a role defined and given to nobody,
and offering a name nobody uses is a worse mistake than not offering it.
*/
func rolesInUse(rows []map[string]any) []string {
	seen := map[string]bool{}
	for _, row := range rows {
		groups, ok := row["Groups"].([]any)
		if !ok {
			continue
		}
		for _, entry := range groups {
			group, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			rights, ok := group["GroupRights"].(map[string]any)
			if !ok {
				continue
			}
			if name := asText(rights["RoleName"]); name != "" {
				seen[name] = true
			}
		}
	}
	roles := make([]string, 0, len(seen))
	for name := range seen {
		roles = append(roles, name)
	}
	sort.Strings(roles)
	return roles
}

/*
setRole changes what an extension is trusted with.

Read the memberships, change the role on the first, write them all back — the
same shape as everything else the phone system keeps as a collection on the
extension. The department is not changed here: moving somebody between groups
is a different act with wider effects than a name, and it is not something to
do as a side effect of setting a role.
*/
func setRole(ctx context.Context, conn pbx, id int64, role string) error {
	membership, err := membershipRow(ctx, conn, id)
	if err != nil {
		return err
	}

	// Both, because the phone system carries two and only one of them is the
	// membership's own. Writing GroupRights alone was accepted with a 200,
	// validated the name, and changed nothing — a role set five times over
	// that read back unchanged every time, which is the worst way for a write
	// to fail.
	for _, property := range []string{"Rights", "GroupRights"} {
		rights, ok := membership[property].(map[string]any)
		if !ok {
			rights = map[string]any{}
		}
		rights["RoleName"] = role
		membership[property] = rights
	}

	if err := conn.patch(ctx, fmt.Sprintf("Users(%d)", id),
		map[string]any{"Groups": []any{membership}}); err != nil {
		return err
	}

	// Read it back. The phone system answered the old shape with a success it
	// did not mean, so this asks rather than assumes — and a role is what
	// somebody is trusted with, which is not a thing to report on faith.
	after, err := membershipRow(ctx, conn, id)
	if err != nil {
		return err
	}
	if got := roleOf(after); got != role {
		return plugin.Errorf("502",
			"the phone system accepted the change and left the role as %q", got)
	}
	return nil
}

// membershipRow reads the first group membership on an extension, which is the
// one its page shows and the one a department and a role are read from.
func membershipRow(ctx context.Context, conn pbx, id int64) (map[string]any, error) {
	var user struct {
		Groups []map[string]any `json:"Groups"`
	}
	q := url.Values{}
	q.Set("$select", "Id")
	q.Set("$expand", "Groups($expand=Rights,GroupRights)")
	if err := conn.get(ctx, fmt.Sprintf("Users(%d)", id), q, &user); err != nil {
		return nil, err
	}
	if len(user.Groups) == 0 {
		return nil, plugin.Errorf("400",
			"this extension is not in any group, so it has no role to change. Set its department first")
	}
	return user.Groups[0], nil
}

// roleOf reads whichever of the two rights properties carries a name.
func roleOf(membership map[string]any) string {
	for _, property := range []string{"Rights", "GroupRights"} {
		if rights, ok := membership[property].(map[string]any); ok {
			if name := asText(rights["RoleName"]); name != "" {
				return name
			}
		}
	}
	return ""
}

/*
The handset.

A phone is its own record beside the extension, not a value on it, and one
extension can have several. The first is the one its page shows.

Read and not written. Assigning a MAC address is what provisions a phone — the
phone system builds a configuration, the handset fetches it, and getting it
wrong is a desk phone that does not come back. No handset on the system this
was built against has ever been provisioned, so there is no shape to check a
write against and nothing to try one on. Reading it is still worth having:
"what phone is on this extension" is a question asked constantly.
*/
var handsetFields = []option{
	{"PhoneModel", "Phone model", "readonly", "IP phone", nil},
	{"PhoneMac", "MAC address", "readonly", "IP phone", nil},
	{"PhoneName", "Phone name", "readonly", "IP phone", nil},
	{"PhoneInterface", "Routing device", "readonly", "IP phone", nil},
}

// handsetOf reads the first phone on an extension.
func handsetOf(raw any) map[string]any {
	phones, ok := raw.([]any)
	if !ok || len(phones) == 0 {
		return nil
	}
	phone, ok := phones[0].(map[string]any)
	if !ok {
		return nil
	}
	return map[string]any{
		"PhoneModel":     asText(phone["TemplateName"]),
		"PhoneMac":       asText(phone["MacAddress"]),
		"PhoneName":      asText(phone["Name"]),
		"PhoneInterface": asText(phone["Interface"]),
	}
}

/*
setDepartment moves an extension into a group.

Read the memberships, point the first at the new group, write them all back —
the same shape as setRole beside it. An extension in no group at all gets one,
which is the case that matters: the extensions on this phone system that hold
no role hold none because they are in no group, so "give these five a role"
begins here.

Written by id and read by name, because that is how 3CX keeps it.
*/
func setDepartment(ctx context.Context, conn pbx, id int64, department string) error {
	_, groups := departmentsAvailable(ctx, conn)
	target, known := groups[department]
	if !known {
		return plugin.Errorf("400", "this phone system has no department called %q", department)
	}

	var user struct {
		Groups []map[string]any `json:"Groups"`
	}
	q := url.Values{}
	q.Set("$select", "Id")
	q.Set("$expand", "Groups($expand=GroupRights)")
	if err := conn.get(ctx, fmt.Sprintf("Users(%d)", id), q, &user); err != nil {
		return err
	}

	if len(user.Groups) == 0 {
		// No membership to move, so one is made. Rights are left unset and the
		// phone system gives the group's default, which is the right answer:
		// inventing a role here would be deciding what somebody is trusted
		// with as a side effect of filing them under a department.
		user.Groups = []map[string]any{{"GroupId": target}}
	} else {
		first := user.Groups[0]
		if current, ok := first["GroupId"]; ok && asText(current) == fmt.Sprint(target) {
			return nil
		}
		first["GroupId"] = target
		// Name and Number describe the old group and would contradict the id.
		// The phone system fills them in from the id it is given.
		delete(first, "Name")
		delete(first, "Number")
		delete(first, "MemberName")
	}
	return conn.patch(ctx, fmt.Sprintf("Users(%d)", id),
		map[string]any{"Groups": user.Groups})
}

/*
setRoutingDevice changes where a desk phone fetches its configuration from.

The one thing on the IP phone tab worth setting across many extensions at once:
moving a floor of handsets from the phone system to a session border controller
is a real job, and doing it one extension at a time in the console is the kind
of task this tool exists for.

Deliberately the only writable field on that tab. A MAC address is what
provisions a phone — the phone system builds a configuration and the handset
fetches it — so writing one is how a desk phone stops coming back, and it is
not something to do to fifty at once from a form.
*/
func setRoutingDevice(ctx context.Context, conn pbx, id int64, iface string) error {
	var user struct {
		Phones []map[string]any `json:"Phones"`
	}
	q := url.Values{}
	q.Set("$select", "Id")
	q.Set("$expand", "Phones")
	if err := conn.get(ctx, fmt.Sprintf("Users(%d)", id), q, &user); err != nil {
		return err
	}
	if len(user.Phones) == 0 {
		return plugin.Errorf("400", "this extension has no phone, so there is no routing device to set")
	}
	if asText(user.Phones[0]["Interface"]) == iface {
		return nil
	}
	user.Phones[0]["Interface"] = iface
	return conn.patch(ctx, fmt.Sprintf("Users(%d)", id),
		map[string]any{"Phones": user.Phones})
}

/*
whatItAccepts asks the phone system for every list a field is chosen from.

Three requests beside the one that read the extensions, and they answer the
question a form has to answer before it can be drawn: not "what is this set to"
but "what may it be set to". Each falls back to nothing on its own, so a phone
system that will not describe one of them still yields a usable form for the
rest.
*/
func whatItAccepts(ctx context.Context, conn pbx, rows []map[string]any) fieldLists {
	departments, groups := departmentsAvailable(ctx, conn)
	roles, roleLabels := rolesAvailable(ctx, conn, groups, rolesInUse(rows))
	return fieldLists{
		Roles:       choices{Values: roles, Labels: roleLabels},
		Departments: departments,
		Routing:     routingDevices(ctx, conn, conn.fqdn()),
	}
}
