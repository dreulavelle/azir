package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dreulavelle/azir/internal/blf"
	"io"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dreulavelle/azir/internal/supportinfo"
	"github.com/dreulavelle/azir/pkg/plugin"
)

/*
The tools a technician reaches for when a ticket says the phones are wrong.

Almost none of those tickets are answered by "is the system up". They are
answered by one of four questions, and each of these tools exists to answer
exactly one of them:

  - Did the call happen, and what became of it?        calls.history
  - Why is this extension behaving like that?          extension.detail
  - What is that handset, and is it talking to us?     devices.list
  - Which part of the system is unwell?                system.services

Every one of them is a read, and every one names its fields explicitly. That is
not tidiness: 3CX returns SIP passwords, phone web passwords and provisioning
links from these same endpoints, and the field list is the only thing standing
between those and whoever asked — including the assistant.
*/

// safeCallFields is what a call record may contain. Deliberately no RecId: the
// recording identifiers lead somewhere a summary has no business going.
const safeCallFields = "SegmentStartTime,SegmentEndTime,CallTime,CallAnswered," +
	"SrcDn,SrcDisplayName,SrcCallerNumber,SrcExternal," +
	"DstDn,DstDisplayName,DstCallerNumber,DstExternal"

type historyArgs struct {
	Extension string `json:"extension"`
	Number    string `json:"number"`
	Since     string `json:"since"`
	Until     string `json:"until"`
	Missed    bool   `json:"missed_only"`
	Limit     int    `json:"limit"`
}

/*
callHistory answers "what actually happened to that call".

This is the first thing to reach for on most phone tickets, because almost all
of them are a disagreement about reality: the customer says they rang and nobody
picked up, the client says no call ever arrived, somebody says the number goes
to the wrong person. A call record settles it — whether the call reached the
system at all, which extension it was offered to, whether it was answered, and
how long it lasted.
*/
func callHistory(ctx context.Context, req plugin.Request) (any, error) {
	var args historyArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}

	limit := args.Limit
	if limit <= 0 || limit > pageSize {
		limit = 50
	}

	// Built as OData filters so the PBX does the narrowing. Pulling everything
	// and filtering here would be slower, larger, and would put call records
	// this caller never asked for through the process.
	var filters []string
	if ext := strings.TrimSpace(args.Extension); ext != "" {
		filters = append(filters, fmt.Sprintf("(SrcDn eq '%s' or DstDn eq '%s')", odata(ext), odata(ext)))
	}
	if num := strings.TrimSpace(args.Number); num != "" {
		filters = append(filters, fmt.Sprintf(
			"(contains(SrcCallerNumber,'%s') or contains(DstCallerNumber,'%s'))", odata(num), odata(num)))
	}
	if since := strings.TrimSpace(args.Since); since != "" {
		filters = append(filters, "SegmentStartTime ge "+since)
	}
	if until := strings.TrimSpace(args.Until); until != "" {
		filters = append(filters, "SegmentStartTime le "+until)
	}
	if args.Missed {
		filters = append(filters, "CallAnswered eq false")
	}

	q := url.Values{}
	q.Set("$select", safeCallFields)
	q.Set("$orderby", "SegmentStartTime desc")
	q.Set("$top", fmt.Sprint(limit))
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	var page struct {
		Value []struct {
			Start     string `json:"SegmentStartTime"`
			End       string `json:"SegmentEndTime"`
			CallTime  string `json:"CallTime"`
			Answered  bool   `json:"CallAnswered"`
			SrcDn     string `json:"SrcDn"`
			SrcName   string `json:"SrcDisplayName"`
			SrcNumber string `json:"SrcCallerNumber"`
			SrcExt    bool   `json:"SrcExternal"`
			DstDn     string `json:"DstDn"`
			DstName   string `json:"DstDisplayName"`
			DstNumber string `json:"DstCallerNumber"`
			DstExt    bool   `json:"DstExternal"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "CallHistoryView", q, &page); err != nil {
		return nil, err
	}

	calls := make([]map[string]any, 0, len(page.Value))
	var answered int
	for _, c := range page.Value {
		if c.Answered {
			answered++
		}
		calls = append(calls, map[string]any{
			"at":        c.Start,
			"ended":     c.End,
			"talk_time": readableDuration(c.CallTime),
			"answered":  c.Answered,
			// "who rang" and "who it reached", rather than src and dst, because
			// the direction is what a person is actually reading for.
			"from":         firstNonBlank(c.SrcNumber, c.SrcDn),
			"from_name":    c.SrcName,
			"from_outside": c.SrcExt,
			"to":           firstNonBlank(c.DstNumber, c.DstDn),
			"to_name":      c.DstName,
			"to_outside":   c.DstExt,
			"was_inbound":  c.SrcExt && !c.DstExt,
			"was_outbound": !c.SrcExt && c.DstExt,
		})
	}

	return map[string]any{
		"calls":    calls,
		"returned": len(calls),
		"answered": answered,
		"missed":   len(calls) - answered,
		// Said plainly, because a short list can mean "that is all there was"
		// or "that is all we asked for", and those lead somewhere different.
		"limit": limit,
	}, nil
}

/*
extensionDetail answers "why is this extension behaving like that".

The complaints this settles are the ones a status page never will: calls going
straight to voicemail, a phone that never rings, someone missing from a queue.
Every one of those has a cause sitting on the extension record — a profile left
on Do Not Disturb, a forwarding rule somebody set months ago, a queue they are
logged out of — and none of it is visible from a list of numbers.
*/
func extensionDetail(ctx context.Context, req plugin.Request) (any, error) {
	var args struct {
		Extension string `json:"extension"`
	}
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}
	number := strings.TrimSpace(args.Extension)
	if number == "" {
		return nil, plugin.Errorf("400", "which extension?")
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	// Named one by one. This endpoint will return AuthID and AuthPassword — the
	// SIP credentials for the handset — and asking for everything would put
	// them in front of whoever called, model included.
	q := url.Values{}
	q.Set("$select", "Number,DisplayName,FirstName,LastName,EmailAddress,Enabled,"+
		"IsRegistered,CurrentProfileName,OutboundCallerID,QueueStatus,"+
		"VMEnabled,VMEmailOptions,VMPlayCallerID,SendEmailMissedCalls,"+
		"EnableHotdesking,Require2FA,HideInPhonebook,Language,"+
		"PbxDeliversAudio,BlockTunnel,AllowLanOnly,RecordCalls,Internal")
	q.Set("$expand", "Groups($select=Name),ForwardingProfiles($select=Name,CustomName,OfficeHoursAutoQueueLogOut)")
	q.Set("$filter", fmt.Sprintf("Number eq '%s'", odata(number)))
	q.Set("$top", "1")

	var page struct {
		Value []struct {
			Number       string `json:"Number"`
			DisplayName  string `json:"DisplayName"`
			FirstName    string `json:"FirstName"`
			LastName     string `json:"LastName"`
			Email        string `json:"EmailAddress"`
			Enabled      bool   `json:"Enabled"`
			Registered   bool   `json:"IsRegistered"`
			Profile      string `json:"CurrentProfileName"`
			CallerID     string `json:"OutboundCallerID"`
			QueueStatus  string `json:"QueueStatus"`
			VMEnabled    bool   `json:"VMEnabled"`
			VMEmail      string `json:"VMEmailOptions"`
			VMCallerID   bool   `json:"VMPlayCallerID"`
			MissedEmails bool   `json:"SendEmailMissedCalls"`
			Hotdesking   bool   `json:"EnableHotdesking"`
			Require2FA   bool   `json:"Require2FA"`
			Hidden       bool   `json:"HideInPhonebook"`
			Language     string `json:"Language"`
			PbxAudio     bool   `json:"PbxDeliversAudio"`
			BlockTunnel  bool   `json:"BlockTunnel"`
			LanOnly      bool   `json:"AllowLanOnly"`
			RecordCalls  bool   `json:"RecordCalls"`
			InternalOnly bool   `json:"Internal"`
			Groups       []struct {
				Name string `json:"Name"`
			} `json:"Groups"`
			Forwarding []struct {
				Name       string `json:"Name"`
				CustomName string `json:"CustomName"`
			} `json:"ForwardingProfiles"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "Users", q, &page); err != nil {
		return nil, err
	}
	if len(page.Value) == 0 {
		return nil, plugin.Errorf("404", "no extension here is numbered %s", number)
	}
	u := page.Value[0]

	groups := make([]string, 0, len(u.Groups))
	for _, g := range u.Groups {
		groups = append(groups, g.Name)
	}
	profiles := make([]string, 0, len(u.Forwarding))
	for _, f := range u.Forwarding {
		profiles = append(profiles, firstNonBlank(f.CustomName, f.Name))
	}

	// The reasons this extension might not be ringing, worked out rather than
	// left for a reader to infer from eighteen fields.
	var why []string
	if !u.Enabled {
		why = append(why, "the extension is disabled, so it cannot be called at all")
	}
	if !u.Registered {
		why = append(why, "no handset or app is registered, so there is nothing for a call to ring")
	}
	if p := strings.ToLower(u.Profile); strings.Contains(p, "away") || strings.Contains(p, "dnd") ||
		strings.Contains(p, "do not disturb") || strings.Contains(p, "out of office") {
		why = append(why, fmt.Sprintf("the status profile is %q, which usually diverts calls", u.Profile))
	}
	if strings.EqualFold(u.QueueStatus, "LoggedOut") {
		why = append(why, "they are logged out of their queues, so queue calls will skip them")
	}

	return map[string]any{
		"extension":               u.Number,
		"name":                    firstNonBlank(u.DisplayName, strings.TrimSpace(u.FirstName+" "+u.LastName)),
		"email":                   u.Email,
		"enabled":                 u.Enabled,
		"registered":              u.Registered,
		"status_profile":          u.Profile,
		"queue_status":            u.QueueStatus,
		"outbound_caller_id":      u.CallerID,
		"voicemail_on":            u.VMEnabled,
		"voicemail_email":         u.VMEmail,
		"voicemail_caller_id":     u.VMCallerID,
		"missed_call_emails":      u.MissedEmails,
		"hotdesking":              u.Hotdesking,
		"two_factor_required":     u.Require2FA,
		"hidden_in_phonebook":     u.Hidden,
		"language":                u.Language,
		"pbx_delivers_audio":      u.PbxAudio,
		"block_remote_non_tunnel": u.BlockTunnel,
		"lan_only":                u.LanOnly,
		"records_calls":           u.RecordCalls,
		"internal_calls_only":     u.InternalOnly,
		"worth_checking":          differsFrom(map[string]bool{"PbxDeliversAudio": u.PbxAudio, "AllowLanOnly": u.LanOnly}),
		"groups":                  groups,
		"forwarding_profiles":     profiles,
		"why_calls_may_not_land":  why,
	}, nil
}

/*
listDevices answers "what is that handset, and is it talking to us".

A phone that will not register is one of the most common tickets there is, and
answering it means knowing what the thing is: its make and model, the firmware
on it, the address it is calling from, and whether the system has seen it at
all. Without that the conversation is somebody reading a label off the back of
a desk phone over the telephone.
*/
func listDevices(ctx context.Context, req plugin.Request) (any, error) {
	var args struct {
		Unassigned bool `json:"unassigned_only"`
	}
	if len(req.Args) > 0 {
		_ = json.Unmarshal(req.Args, &args)
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	// No InterfaceLink, no NetworkPath, no Parameters: those carry provisioning
	// URLs, and a provisioning URL is a credential wearing a different hat.
	q := url.Values{}
	q.Set("$select", "MAC,Model,Vendor,FirmwareVersion,NetworkAddress,Assigned,AssignedUser,DetectedAt,UserAgent,TemplateName")
	q.Set("$top", fmt.Sprint(pageSize))

	var page struct {
		Value []struct {
			MAC        string `json:"MAC"`
			Model      string `json:"Model"`
			Vendor     string `json:"Vendor"`
			Firmware   string `json:"FirmwareVersion"`
			Address    string `json:"NetworkAddress"`
			Assigned   bool   `json:"Assigned"`
			User       string `json:"AssignedUser"`
			DetectedAt string `json:"DetectedAt"`
			UserAgent  string `json:"UserAgent"`
			Template   string `json:"TemplateName"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "DeviceInfos", q, &page); err != nil {
		return nil, err
	}

	devices := make([]map[string]any, 0, len(page.Value))
	var unassigned int
	for _, d := range page.Value {
		if !d.Assigned {
			unassigned++
		}
		if args.Unassigned && d.Assigned {
			continue
		}
		devices = append(devices, map[string]any{
			"mac":         d.MAC,
			"make":        d.Vendor,
			"model":       firstNonBlank(d.Model, d.UserAgent),
			"firmware":    d.Firmware,
			"address":     d.Address,
			"assigned_to": d.User,
			"assigned":    d.Assigned,
			"last_seen":   d.DetectedAt,
			"template":    d.Template,
		})
	}

	return map[string]any{
		"devices":    devices,
		"total":      len(page.Value),
		"unassigned": unassigned,
	}, nil
}

// listServices answers "which part of the system is unwell", which is the
// question behind every ticket that says everything is broken.
func listServices(ctx context.Context, req plugin.Request) (any, error) {
	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("$top", fmt.Sprint(pageSize))

	var page struct {
		Value []struct {
			Name      string `json:"Name"`
			Status    string `json:"Status"`
			Memory    int64  `json:"MemoryUsed"`
			CPU       int    `json:"CpuUsage"`
			Restarts  int    `json:"RestartCount"`
			StartedAt string `json:"StartStopTime"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "Services", q, &page); err != nil {
		return nil, err
	}

	services := make([]map[string]any, 0, len(page.Value))
	var stopped []string
	for _, s := range page.Value {
		running := strings.EqualFold(s.Status, "Running")
		if !running && s.Name != "" {
			stopped = append(stopped, s.Name)
		}
		services = append(services, map[string]any{
			"name":       s.Name,
			"status":     s.Status,
			"running":    running,
			"restarts":   s.Restarts,
			"memory":     s.Memory,
			"cpu":        s.CPU,
			"started_at": s.StartedAt,
		})
	}

	return map[string]any{
		"services":    services,
		"all_running": len(stopped) == 0,
		"not_running": stopped,
	}, nil
}

// odata escapes a value for an OData string literal, where the quote is
// doubled. Without it a number containing an apostrophe would end the literal
// and the rest would be read as filter syntax.
func odata(v string) string {
	return strings.ReplaceAll(v, "'", "''")
}

// readableDuration turns an ISO 8601 duration into something a person reads.
// 3CX answers PT2M13S; nobody wants to see that on a call record.
func readableDuration(iso string) string {
	if iso == "" || iso == "PT0S" {
		return "0s"
	}
	out := strings.TrimPrefix(iso, "P")
	out = strings.ReplaceAll(out, "T", "")
	out = strings.ToLower(out)
	// Trailing fractional seconds are noise on a call that lasted two minutes.
	if i := strings.Index(out, "."); i >= 0 {
		out = out[:i] + "s"
	}
	return out
}

// firstNonBlank returns the first value with something in it.
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

/*
searchLogs is the tool for "when did this start, and what does the system say
about it".

Two things make it worth having over a list of the last twenty events.

It searches on the PBX rather than here. A technician chasing a registration
problem wants the lines mentioning that extension, not the most recent lines of
everything — and pulling a thousand entries back to filter them in the process
would be slower, larger, and would spend a great deal of a model's context
arriving at the same four lines.

And it fills in the message. 3CX stores log lines as templates with the values
alongside them, so the raw record reads "Trunk %1$s has changed status to %2$s"
and tells you nothing about which trunk or which status. Every log line this
system has shown until now has been that template.
*/
func searchLogs(ctx context.Context, req plugin.Request) (any, error) {
	var args struct {
		Query  string `json:"query"`
		Source string `json:"source"`
		Since  string `json:"since"`
		Until  string `json:"until"`
		Limit  int    `json:"limit"`
	}
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, plugin.Errorf("400", "those arguments could not be read")
		}
	}

	limit := args.Limit
	if limit <= 0 || limit > pageSize {
		limit = 50
	}

	q := url.Values{}
	q.Set("$select", "TimeGenerated,Type,Source,Message,Params,GroupName")
	q.Set("$orderby", "TimeGenerated desc")
	q.Set("$top", fmt.Sprint(limit))
	if s := strings.TrimSpace(args.Query); s != "" {
		q.Set("$search", s)
	}

	var filters []string
	if src := strings.TrimSpace(args.Source); src != "" {
		filters = append(filters, fmt.Sprintf("contains(Source,'%s')", odata(src)))
	}
	if since := strings.TrimSpace(args.Since); since != "" {
		filters = append(filters, "TimeGenerated ge "+since)
	}
	if until := strings.TrimSpace(args.Until); until != "" {
		filters = append(filters, "TimeGenerated le "+until)
	}
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	var page struct {
		Value []struct {
			At      string   `json:"TimeGenerated"`
			Type    string   `json:"Type"`
			Source  string   `json:"Source"`
			Message string   `json:"Message"`
			Params  []string `json:"Params"`
			Group   string   `json:"GroupName"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "EventLogs", q, &page); err != nil {
		return nil, err
	}

	lines := make([]map[string]any, 0, len(page.Value))
	for _, e := range page.Value {
		lines = append(lines, map[string]any{
			"at":      e.At,
			"kind":    e.Type,
			"source":  e.Source,
			"message": fillTemplate(e.Message, e.Params),
			"group":   e.Group,
		})
	}

	return map[string]any{
		"log":      lines,
		"returned": len(lines),
		"searched": strings.TrimSpace(args.Query),
		"limit":    limit,
	}, nil
}

// placeholder matches 3CX's message templates: %1$s, %2$s and so on, one-based.
var placeholder = regexp.MustCompile(`%(\d+)\$s`)

// fillTemplate puts the parameters back into the message they came out of.
//
// A placeholder with nothing to fill it is left exactly as it was rather than
// blanked, because a line reading "Trunk  has changed status to " looks like a
// system with an empty trunk name instead of a record we could not complete.
func fillTemplate(message string, params []string) string {
	if message == "" || len(params) == 0 {
		return message
	}
	return placeholder.ReplaceAllStringFunc(message, func(match string) string {
		n, err := strconv.Atoi(placeholder.FindStringSubmatch(match)[1])
		if err != nil || n < 1 || n > len(params) {
			return match
		}
		return params[n-1]
	})
}

// --- mass editing -------------------------------------------------------------

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
	{"BlockTunnel", "Block remote non-tunnel connections", "bool", "Options", nil},
	{"AllowLanOnly", "Allow only from the local network", "bool", "Options", nil},
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

// editableByName is the same list as an allowlist to check against.
var editableByName = func() map[string]option {
	byName := make(map[string]option, len(editable))
	for _, o := range editable {
		byName[o.Field] = o
	}
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
	var refused []string
	for key, value := range args.Options {
		spec, ok := editableByName[key]
		if !ok {
			refused = append(refused, key)
			continue
		}
		switch spec.Kind {
		case "bool":
			b, ok := value.(bool)
			if !ok {
				return nil, plugin.Errorf("400", "%s is either true or false", key)
			}
			settings[key] = b
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
		if err := conn.patch(ctx, fmt.Sprintf("Users(%d)", id), settings); err != nil {
			results = append(results, map[string]any{
				"extension": number, "changed": false, "reason": plainReason(err),
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
phoneAction reboots or reprovisions a handset.

The two things a technician does to a phone that will not behave, and until now
both meant somebody physically at the desk or a remote session onto a machine on
that network. A reprovision in particular is the standard fix for a handset that
has drifted from its configuration, and it is one call.
*/
func phoneAction(what string) func(context.Context, plugin.Request) (any, error) {
	return func(ctx context.Context, req plugin.Request) (any, error) {
		var args struct {
			MAC string `json:"mac"`
		}
		if len(req.Args) > 0 {
			if err := json.Unmarshal(req.Args, &args); err != nil {
				return nil, plugin.Errorf("400", "those arguments could not be read")
			}
		}
		mac := strings.TrimSpace(args.MAC)
		if mac == "" {
			return nil, plugin.Errorf("400", "which handset? Give its MAC address — devices.list has them")
		}

		conn, err := connect(ctx, req)
		if err != nil {
			return nil, err
		}
		if err := conn.action(ctx, "Users/Pbx."+what, map[string]any{"mac": mac}, nil); err != nil {
			return nil, err
		}
		return map[string]any{"mac": mac, "action": what, "sent": true}, nil
	}
}

// --- conventions --------------------------------------------------------------

/*
The settings most deployments end up wanting, and what the other value means.

These are conventions, not correctness. Every one of them has a site where the
opposite is right, so nothing here changes anything on its own and nothing here
says "wrong" — it says what this extension is set to, what most are set to, and
what the difference does. A technician decides.

Both of the current entries are about the same thing: whether somebody working
away from the office can use their phone at all.
*/
type convention struct {
	Property string
	Want     bool
	// Why the usual value is usual, in one sentence.
	Because string
	// What the other value actually does, so the choice can be made on
	// consequences rather than on a checkbox's name.
	Otherwise string
}

var conventions = []convention{
	{
		Property:  "PbxDeliversAudio",
		Want:      true,
		Because:   "the PBX relays the audio itself, which is what makes calls work through home routers and firewalls it does not control",
		Otherwise: "endpoints are left to send audio directly to each other, and remote callers get one-way or silent calls when that path does not exist",
	},
	{
		Property:  "AllowLanOnly",
		Want:      false,
		Because:   "an extension can register from outside the office, which is what anybody working from home needs",
		Otherwise: "the extension only works on the office network, so a remote handset or app never registers and the person appears permanently offline",
	},
}

// conventionFields is what a review has to read to judge them.
var conventionFields = "Number,DisplayName,PbxDeliversAudio,AllowLanOnly"

// differsFrom returns the conventions this extension does not follow.
func differsFrom(set map[string]bool) []map[string]any {
	var out []map[string]any
	for _, c := range conventions {
		got, known := set[c.Property]
		if !known || got == c.Want {
			continue
		}
		out = append(out, map[string]any{
			"setting":   c.Property,
			"currently": got,
			"usually":   c.Want,
			"because":   c.Because,
			"as_it_is":  c.Otherwise,
		})
	}
	return out
}

/*
reviewExtensions checks every extension against the conventions above.

The point is not the list of differences; it is that the answer can be acted on
without transcribing anything. Each convention comes back with the extensions
that differ from it, in the shape extensions.options takes, so noticing the
problem and fixing it are the same two steps rather than a spreadsheet in
between.
*/
func reviewExtensions(ctx context.Context, req plugin.Request) (any, error) {
	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	type row struct {
		Number   string `json:"Number"`
		Name     string `json:"DisplayName"`
		PbxAudio bool   `json:"PbxDeliversAudio"`
		LanOnly  bool   `json:"AllowLanOnly"`
	}

	var all []row
	for skip := 0; skip < maxExtensions; skip += pageSize {
		q := url.Values{}
		q.Set("$select", conventionFields)
		q.Set("$top", fmt.Sprint(pageSize))
		q.Set("$skip", fmt.Sprint(skip))
		q.Set("$orderby", "Number")

		var page struct {
			Value []row `json:"value"`
		}
		if err := conn.get(ctx, "Users", q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		if len(page.Value) < pageSize {
			break
		}
	}

	// Grouped by setting rather than by extension, because the fix is applied
	// per setting: one call to extensions.options per group, with the list of
	// numbers already assembled.
	type group struct {
		convention
		numbers []string
	}
	groups := make([]*group, 0, len(conventions))
	for _, c := range conventions {
		groups = append(groups, &group{convention: c})
	}

	following := 0
	for _, u := range all {
		set := map[string]bool{"PbxDeliversAudio": u.PbxAudio, "AllowLanOnly": u.LanOnly}
		clean := true
		for _, g := range groups {
			if set[g.Property] != g.Want {
				g.numbers = append(g.numbers, u.Number)
				clean = false
			}
		}
		if clean {
			following++
		}
	}

	suggestions := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		if len(g.numbers) == 0 {
			continue
		}
		suggestions = append(suggestions, map[string]any{
			"setting":    g.Property,
			"set_it_to":  g.Want,
			"extensions": g.numbers,
			"count":      len(g.numbers),
			"because":    g.Because,
			"as_it_is":   g.Otherwise,
			// Ready to hand to extensions.options unchanged.
			"fix_with": map[string]any{
				"extensions": g.numbers,
				"options":    map[string]any{g.Property: g.Want},
			},
		})
	}

	return map[string]any{
		"extensions":       len(all),
		"already_as_usual": following,
		"suggestions":      suggestions,
		// Said plainly. An empty list means everything matches, not that
		// nothing was checked.
		"nothing_to_suggest": len(suggestions) == 0,
	}, nil
}

/*
capture collects a support bundle from a live phone system.

3CX will build one on demand: /xapi/v1/SupportInfo answers with the same zip
that the "collect support info" button in its own console produces. It is not
in the published OData spec — everything else here is an entity and this is a
file — which is why it takes a raw fetch rather than the JSON helper.

The bundle is read here rather than sent onward. It is tens of megabytes and
the reply travels over NATS, so forwarding it would mean either raising the
message limit to something absurd or inventing a side channel to move it; and
the credentials that fetched it live in this process by design, so this is
already the only place that can. What goes back is the report — a few hundred
kilobytes of findings — which is exactly what an uploaded bundle turns into, by
the same parser. A pulled capture and an uploaded one are the same thing.

Older systems that do not offer the endpoint fall back to their event log,
which is the richest single table in a bundle and is available over the API on
every version. That capture is thinner and says so.
*/
func capture(ctx context.Context, req plugin.Request) (any, error) {
	var args struct {
		Days int `json:"days"`
	}
	if len(req.Args) > 0 {
		_ = json.Unmarshal(req.Args, &args)
	}
	if args.Days <= 0 {
		args.Days = 7
	}
	if args.Days > 30 {
		args.Days = 30
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	host := strings.TrimPrefix(conn.base, "https://")

	// Generous, because the phone system has to walk its own logs and zip them
	// before a single byte comes back. On a large site that is minutes.
	res, err := conn.fetch(ctx, "SupportInfo", nil, 10*time.Minute)
	if err != nil {
		// Only a system that does not offer it falls back. A refusal or a
		// timeout is a real failure and quietly returning a thinner capture
		// would hide it.
		var refused *plugin.Error
		if errors.As(err, &refused) && refused.Code == "404" {
			return fromEventLog(ctx, conn, host, args.Days)
		}
		return nil, err
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	// Held in memory rather than spooled to disk: a zip needs random access to
	// be read at all, and a temporary file of somebody's logs is a thing to
	// clean up and eventually fail to.
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBundle+1))
	if err != nil {
		return nil, plugin.Errorf("502", "the support bundle could not be read")
	}
	if len(raw) > maxBundle {
		return nil, plugin.Errorf("507",
			"that phone system produced a support bundle larger than %d MB", maxBundle>>20)
	}

	snapshot, err := supportinfo.Read(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, plugin.Errorf("502", "that support bundle could not be read: %s", err)
	}
	raw = nil
	if snapshot.System.FQDN == "" {
		snapshot.System.FQDN = host
	}

	return map[string]any{"report": snapshot, "host": host, "source": "bundle"}, nil
}

/*
fromEventLog is the thinner capture, for a system with no SupportInfo endpoint.

Paged rather than topped: the event that explains the fault is rarely in the
last twenty.
*/
func fromEventLog(ctx context.Context, conn pbx, host string, days int) (any, error) {
	since := time.Now().AddDate(0, 0, -days).UTC()
	events := make([]supportinfo.LiveEvent, 0, captureEvents)

	for skip := 0; skip < captureEvents; skip += pageSize {
		q := url.Values{}
		q.Set("$top", fmt.Sprint(pageSize))
		q.Set("$skip", fmt.Sprint(skip))
		q.Set("$orderby", "TimeGenerated desc")
		q.Set("$select", "EventId,Type,Message,Source,TimeGenerated")
		q.Set("$filter", fmt.Sprintf("TimeGenerated ge %s", since.Format(time.RFC3339)))

		var page struct {
			Value []struct {
				EventID int    `json:"EventId"`
				Type    string `json:"Type"`
				Message string `json:"Message"`
				Source  string `json:"Source"`
				At      string `json:"TimeGenerated"`
			} `json:"value"`
		}
		if err := conn.get(ctx, "EventLogs", q, &page); err != nil {
			// A page that fails after others succeeded is still a capture, just
			// a shorter one. Losing the whole thing to one bad page would be a
			// worse answer than a partial log.
			if len(events) == 0 {
				return nil, err
			}
			break
		}
		for _, e := range page.Value {
			events = append(events, supportinfo.LiveEvent{
				At: e.At, ID: fmt.Sprint(e.EventID),
				Severity: e.Type, Source: e.Source, Message: e.Message,
			})
		}
		if len(page.Value) < pageSize {
			break
		}
	}

	if len(events) == 0 {
		return map[string]any{"report": nil, "host": host, "source": "events"}, nil
	}
	return map[string]any{
		"report": supportinfo.FromEvents(host, events),
		"host":   host,
		"source": "events",
	}, nil
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
	complete := true
	for skip := 0; skip < maxExtensions; skip += settingsPage {
		q := url.Values{}
		q.Set("$select", strings.Join(selected, ","))
		q.Set("$top", fmt.Sprint(settingsPage))
		q.Set("$skip", fmt.Sprint(skip))
		q.Set("$orderby", "Number")

		var page struct {
			Value []map[string]any `json:"value"`
		}
		if err := conn.get(ctx, "Users", q, &page); err != nil {
			return nil, err
		}
		for _, row := range page.Value {
			settings := map[string]any{}
			for _, name := range fields {
				if value, ok := row[name]; ok && value != nil {
					settings[name] = value
				}
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
		"extensions": out, "count": len(out), "fields": editable, "complete": complete,
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
