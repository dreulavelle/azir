package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

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
		"EnableHotdesking,Require2FA,HideInPhonebook,Language")
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
		"extension":              u.Number,
		"name":                   firstNonBlank(u.DisplayName, strings.TrimSpace(u.FirstName+" "+u.LastName)),
		"email":                  u.Email,
		"enabled":                u.Enabled,
		"registered":             u.Registered,
		"status_profile":         u.Profile,
		"queue_status":           u.QueueStatus,
		"outbound_caller_id":     u.CallerID,
		"voicemail_on":           u.VMEnabled,
		"voicemail_email":        u.VMEmail,
		"voicemail_caller_id":    u.VMCallerID,
		"missed_call_emails":     u.MissedEmails,
		"hotdesking":             u.Hotdesking,
		"two_factor_required":    u.Require2FA,
		"hidden_in_phonebook":    u.Hidden,
		"language":               u.Language,
		"groups":                 groups,
		"forwarding_profiles":    profiles,
		"why_calls_may_not_land": why,
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
