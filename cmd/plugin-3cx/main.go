// Command plugin-3cx reads a customer's 3CX phone system.
//
// An MSP holds a system-owner extension on each customer's PBX and uses it to
// answer the questions a phone ticket actually turns on: is anything
// registered, is the trunk up, did the call come through.
//
// # Reading and changing
//
// Reading is the default and is what almost every ticket needs. Changing is
// possible and is marked as such, which in Azir means four separate things have
// to be true before it happens: an administrator has allowed changes for this
// connection, the person asking has a role that permits it, they pressed
// something that says what it will do, and it is recorded against their name.
//
// The assistant is never offered a tool that changes anything — not gated
// behind a confirmation, not at all. Ticket text is written by customers, so a
// model that could reach a write tool would be a model a customer could aim at
// one. Containment is that the action is not in the list it is given.
//
// # Credentials
//
// Each customer's PBX has its own address and its own system-owner extension,
// so the settings are per customer: the same plugin serves every customer and
// resolves whichever credential belongs to the one being asked about. The
// password goes to the vault like any other secret and is exchanged for a
// short-lived token at request time.
//
// # What is deliberately not returned
//
// 3CX's extension list includes AuthID, AuthPassword, DeskphonePassword, VMPIN
// and SIPID — live SIP credentials and voicemail PINs for every extension in
// the business. Those are never requested: every call names the fields it
// wants with $select, so the credentials do not cross the network, do not enter
// this process, and cannot reach a model even by accident. The SDK's redactor
// would catch most of them on the way out; not fetching them is the gate that
// does not depend on remembering.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dreulavelle/azir/pkg/plugin"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := plugin.Plugin{
		Name:        "3cx",
		Version:     "0.1.0",
		Description: "A customer's 3CX phone system. Reads freely; creating, changing and removing extensions or ring groups needs your permission.",
		Category:    plugin.CategoryTelephony,
		// Every customer has their own PBX. One global setting could only ever
		// reach one of them.
		ConfigScope: plugin.ScopeCustomer,
		ConfigSchema: json.RawMessage(`{
			"type": "object",
			"required": ["fqdn", "extension", "password"],
			"properties": {
				"fqdn": {
					"type": "string",
					"title": "3CX address",
					"description": "The customer's 3CX web address, such as acme.ny.3cx.us. Set this per customer — each of your customers has their own phone system."
				},
				"extension": {
					"type": "string",
					"title": "System owner extension",
					"description": "The extension number you manage this PBX with. It needs the system owner role; a normal extension cannot read system status."
				},
				"password": {
					"type": "string",
					"title": "Extension password",
					"description": "The web client password for that extension. Stored encrypted, exchanged for a short-lived token on each request, and never shown again or given to the assistant.",
					"x-azir-secret": true
				}
			}
		}`),
		Tools: []plugin.Tool{
			{
				Name: "system.status",
				Description: "Reports whether a customer's phone system is healthy: how many extensions and trunks are registered " +
					"against how many exist, how many calls are running, whether any service has stopped, and free disk space. " +
					"Use this first for any phone problem — most of them are a trunk or a handset that is not registered.",
				Summary:  "Checks whether a customer's phone system is healthy.",
				Provides: []plugin.Capability{plugin.CapPhoneStatus},
				Freshness: &plugin.Freshness{
					Soft: 2 * time.Minute,
					Hard: 15 * time.Minute,
				},
				Schema:  json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler: systemStatus,
			},
			{
				Name: "extensions.list",
				Description: "Lists the extensions on a customer's phone system with the name on each and whether the handset " +
					"is currently registered. An extension that exists but is not registered is a phone that is unplugged, " +
					"on a dead network port, or misconfigured — which is what a 'my phone does not ring' ticket usually is.",
				Summary:  "Lists a customer's extensions and which handsets are registered.",
				Provides: []plugin.Capability{plugin.CapPhoneExtensions},
				Freshness: &plugin.Freshness{
					Soft: 2 * time.Minute,
					Hard: 30 * time.Minute,
				},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"only_unregistered": {
							"type": "boolean",
							"description": "Return only extensions whose handset is not registered."
						}
					}
				}`),
				Handler: extensions,
			},
			{
				Name: "calls.recent",
				Description: "Recent calls on a customer's phone system, newest first, with who called whom, when, " +
					"how long it lasted and whether it was answered. Use it to confirm whether a call actually arrived.",
				Summary:  "Reads a customer's recent call history.",
				Provides: []plugin.Capability{plugin.CapCallsList},
				Freshness: &plugin.Freshness{
					Soft: 5 * time.Minute,
					Hard: time.Hour,
				},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"limit": {
							"type": "integer",
							"description": "How many calls to return. Defaults to 25, at most 100.",
							"minimum": 1,
							"maximum": 100
						}
					}
				}`),
				Handler: recentCalls,
			},

			{
				Name: "events.recent",
				Description: "Recent events the phone system logged — failed registrations, licence warnings, service problems. " +
					"Use it when the status looks wrong and you need to know since when, or why.",
				Summary:  "Reads what a customer's phone system has been complaining about.",
				Provides: []plugin.Capability{plugin.CapPhoneEvents},
				Freshness: &plugin.Freshness{
					Soft: 2 * time.Minute,
					Hard: 30 * time.Minute,
				},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"limit": {"type": "integer", "minimum": 1, "maximum": 100, "description": "How many events. Defaults to 20."}
					}
				}`),
				Handler: recentEvents,
			},

			// --- changes ---------------------------------------------------
			//
			// Marked Mutates, which keeps them out of the assistant's hands
			// entirely and behind the write switch for everybody else.
			{
				Name: "calls.history",
				Description: "Call records: who rang, who it reached, whether it was answered and for how long. " +
					"Filter by extension, by phone number, by date, or to missed calls only. This is the tool for " +
					"any question about whether a call happened or what became of it.",
				Summary:   "Looks up call records — who rang whom, and what became of it.",
				Provides:  []plugin.Capability{plugin.CapPhoneCallHistory},
				Freshness: &plugin.Freshness{Soft: 60 * time.Second, Hard: 10 * time.Minute},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"extension": {"type": "string", "description": "Only calls involving this extension, either end."},
						"number": {"type": "string", "description": "Only calls involving this phone number, either end. Partial matches count."},
						"since": {"type": "string", "description": "ISO 8601 timestamp; calls at or after it."},
						"until": {"type": "string", "description": "ISO 8601 timestamp; calls at or before it."},
						"missed_only": {"type": "boolean", "description": "Only calls that were never answered."},
						"limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 50}
					}
				}`),
				Handler: callHistory,
			},
			{
				Name: "extension.detail",
				Description: "Everything about one extension that explains how it behaves: whether it is registered, " +
					"its status profile, forwarding, queue login state, voicemail, groups, and outbound caller ID. " +
					"Reach for this when somebody is not receiving calls. Never returns SIP credentials.",
				Summary:   "Explains why one extension is behaving the way it is.",
				Provides:  []plugin.Capability{plugin.CapPhoneExtensionDetail},
				Freshness: &plugin.Freshness{Soft: 60 * time.Second, Hard: 10 * time.Minute},
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["extension"],
					"properties": {
						"extension": {"type": "string", "description": "The extension number."}
					}
				}`),
				Handler: extensionDetail,
			},
			{
				Name: "devices.list",
				Description: "The handsets this phone system has seen: make, model, firmware, network address, " +
					"which extension each belongs to and when it was last detected. Use it for a phone that will " +
					"not register, or to find out what hardware is on a site.",
				Summary:   "Lists handsets, their firmware and who they belong to.",
				Provides:  []plugin.Capability{plugin.CapPhoneDevices},
				Freshness: &plugin.Freshness{Soft: 5 * time.Minute, Hard: time.Hour},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"unassigned_only": {"type": "boolean", "description": "Only handsets not yet attached to an extension."}
					}
				}`),
				Handler: listDevices,
			},
			{
				Name: "logs.search",
				Description: "Searches the phone system's log for lines matching a phrase — an extension number, " +
					"a trunk name, a phrase from an error — optionally narrowed by source or a time window. " +
					"Reach for this to find out when something started, or what the system said about it. " +
					"Prefer it over reading recent events: searching happens on the PBX, so the answer is the " +
					"handful of lines that matter rather than the last hundred of everything.",
				Summary:   "Searches the phone system's log for lines that mention something.",
				Provides:  []plugin.Capability{plugin.CapPhoneLogSearch},
				Freshness: &plugin.Freshness{Soft: 60 * time.Second, Hard: 10 * time.Minute},
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"query": {"type": "string", "description": "Text to look for — an extension, a trunk name, part of an error."},
						"source": {"type": "string", "description": "Only lines from this part of the system, e.g. SIP Server."},
						"since": {"type": "string", "description": "ISO 8601 timestamp; lines at or after it."},
						"until": {"type": "string", "description": "ISO 8601 timestamp; lines at or before it."},
						"limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 50}
					}
				}`),
				Handler: searchLogs,
			},
			{
				Name:        "system.services",
				Description: "Which of the phone system's own services are running, and which are not.",
				Summary:     "Reports which parts of the phone system are running.",
				Provides:    []plugin.Capability{plugin.CapPhoneServices},
				Freshness:   &plugin.Freshness{Soft: 60 * time.Second, Hard: 10 * time.Minute},
				Schema:      json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:     listServices,
			},
			{
				Name: "extension.update",
				Description: "Changes settings on one extension: the name shown to colleagues, the email address, " +
					"and whether the extension is enabled.",
				Summary:            "Changes one extension's name, email or enabled state.",
				Provides:           []plugin.Capability{plugin.CapPhoneExtensionWrite},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["extension"],
					"properties": {
						"extension": {"type": "string", "description": "The extension number to change."},
						"first_name": {"type": "string"},
						"last_name": {"type": "string"},
						"email": {"type": "string"},
						"enabled": {"type": "boolean", "description": "Whether the extension may be used at all."}
					}
				}`),
				Handler: updateExtension,
			},
			{
				Name: "extensions.bulk_update",
				Description: "Applies the same change to several extensions at once — enabling or disabling a set of them, " +
					"or moving them to a different outbound caller ID.",
				Summary:            "Applies one change to several extensions at once.",
				Provides:           []plugin.Capability{plugin.CapPhoneExtensionWrite},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["extensions"],
					"properties": {
						"extensions": {
							"type": "array",
							"items": {"type": "string"},
							"description": "The extension numbers to change. Named one by one on purpose: there is no way to say all."
						},
						"enabled": {"type": "boolean"},
						"outbound_caller_id": {"type": "string"}
					}
				}`),
				Handler: bulkUpdateExtensions,
			},
			{
				Name: "extensions.review",
				Description: "Checks every extension against the settings most deployments end up wanting, and " +
					"reports which ones differ. Currently: whether the PBX delivers audio, and whether an " +
					"extension is restricted to the office network — both of which decide whether somebody " +
					"working from home can use their phone. Each suggestion comes back with the extension " +
					"numbers and the exact arguments extensions.options takes, so finding it and fixing it are " +
					"the same two steps. These are conventions, not rules: say what differs and let the " +
					"technician decide.",
				Summary:   "Checks extensions against the settings most deployments want.",
				Provides:  []plugin.Capability{plugin.CapPhoneReview},
				Freshness: &plugin.Freshness{Soft: 5 * time.Minute, Hard: time.Hour},
				Schema:    json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:   reviewExtensions,
			},
			{
				Name: "extensions.options",
				Description: "Applies the same extension options to many extensions at once — the settings on an " +
					"extension's page, such as whether the PBX delivers audio, whether remote non-tunnel " +
					"connections are blocked, voicemail behaviour, recording, and the app and integration " +
					"toggles. Name the extensions, or give a numeric range. Changes options only: it can never " +
					"set a password, an extension number, or anything else about who an extension is.",
				Summary:            "Applies the same options to many extensions at once.",
				Provides:           []plugin.Capability{plugin.CapPhoneExtensionOptions},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["options"],
					"properties": {
						"extensions": {
							"type": "array",
							"items": {"type": "string"},
							"description": "The extensions to change. Use this, or from with to."
						},
						"from": {"type": "string", "description": "First extension of an inclusive range."},
						"to": {"type": "string", "description": "Last extension of an inclusive range."},
						"options": {
							"type": "object",
							"description": "3CX's own option names, e.g. PbxDeliversAudio, BlockTunnel, AllowLanOnly, VMEnabled, RecordCalls, Enabled, HideInPhonebook, EnableHotdesking, SendEmailMissedCalls, MS365SignInEnabled. Anything not an extension option is refused by name."
						}
					}
				}`),
				Handler: setExtensionOptions,
			},
			{
				Name: "phones.reprovision",
				Description: "Tells one handset to fetch its configuration again. The standard fix for a phone " +
					"that has drifted from its settings or will not register. Identify it by MAC address, which " +
					"devices.list reports.",
				Summary:            "Makes a handset reload its configuration.",
				Provides:           []plugin.Capability{plugin.CapPhoneHandsetAction},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["mac"],
					"properties": {"mac": {"type": "string", "description": "The handset's MAC address."}}
				}`),
				Handler: phoneAction("ReprovisionPhone"),
			},
			{
				Name:               "phones.reboot",
				Description:        "Restarts one handset, by MAC address. Any call on it at the time is dropped.",
				Summary:            "Restarts a handset.",
				Provides:           []plugin.Capability{plugin.CapPhoneHandsetAction},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["mac"],
					"properties": {"mac": {"type": "string", "description": "The handset's MAC address."}}
				}`),
				Handler: phoneAction("RebootPhone"),
			},
			{
				Name: "extensions.create",
				Description: "Creates one or more new extensions. Numbers are named explicitly, or a starting " +
					"number and a count are given and the extensions are made in sequence. Reports what was " +
					"created and what was skipped, one line each.",
				Summary:            "Creates new extensions, one or several at a time.",
				Provides:           []plugin.Capability{plugin.CapPhoneExtensionCreate},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"properties": {
						"extensions": {
							"type": "array",
							"description": "The extensions to create. Use this, or start_at with count.",
							"items": {
								"type": "object",
								"required": ["number"],
								"properties": {
									"number": {"type": "string"},
									"first_name": {"type": "string"},
									"last_name": {"type": "string"},
									"email": {"type": "string"}
								}
							}
						},
						"start_at": {"type": "string", "description": "First extension number when creating a run of them."},
						"count": {"type": "integer", "minimum": 1, "maximum": 50, "description": "How many to create from start_at."},
						"name_prefix": {"type": "string", "description": "Display name for a created run; the number is appended."}
					}
				}`),
				Handler: createExtensions,
			},
			{
				Name:        "ringgroups.list",
				Description: "The ring groups on this phone system: what each is called, its number, how it rings, and who is in it.",
				Summary:     "Lists ring groups and their members.",
				Provides:    []plugin.Capability{plugin.CapPhoneRingGroups},
				Schema:      json.RawMessage(`{"type": "object", "properties": {}}`),
				Handler:     listRingGroups,
			},
			{
				Name: "ringgroups.create",
				Description: "Creates a ring group: a number that rings a set of extensions together, or one after " +
					"another, until somebody answers.",
				Summary:            "Creates a ring group from a set of extensions.",
				Provides:           []plugin.Capability{plugin.CapPhoneRingGroupWrite},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["number", "name", "members"],
					"properties": {
						"number": {"type": "string", "description": "The number that will ring the group."},
						"name": {"type": "string", "description": "What the group is called."},
						"members": {
							"type": "array",
							"items": {"type": "string"},
							"description": "Extension numbers in the group."
						},
						"strategy": {
							"type": "string",
							"enum": ["RingAll", "Hunt", "PairedRinging", "Paging"],
							"default": "RingAll",
							"description": "RingAll rings everyone at once; Hunt tries them in turn."
						},
						"ring_seconds": {"type": "integer", "minimum": 5, "maximum": 300, "default": 30}
					}
				}`),
				Handler: createRingGroup,
			},
			{
				Name: "extensions.delete",
				Description: "Removes extensions, named one at a time. There is no way to say all, and no " +
					"pattern or range — every extension to be removed has to be written out.",
				Summary:            "Removes extensions you name explicitly.",
				Provides:           []plugin.Capability{plugin.CapPhoneExtensionDelete},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["extensions"],
					"properties": {
						"extensions": {
							"type": "array",
							"items": {"type": "string"},
							"description": "The extension numbers to remove, written out one by one."
						}
					}
				}`),
				Handler: deleteExtensions,
			},
			{
				Name:               "ringgroups.delete",
				Description:        "Removes one ring group, named by its number. The extensions in it are left alone.",
				Summary:            "Removes a ring group.",
				Provides:           []plugin.Capability{plugin.CapPhoneRingGroupDelete},
				Mutates:            true,
				RequiresPermission: "phone.manage",
				Schema: json.RawMessage(`{
					"type": "object",
					"required": ["number"],
					"properties": {
						"number": {"type": "string", "description": "The ring group's number."}
					}
				}`),
				Handler: deleteRingGroup,
			},
		},
	}

	if err := plugin.Serve(ctx, p, plugin.WithLogger(log)); err != nil {
		log.Error("plugin failed to start", "error", err)
		os.Exit(1)
	}
}

// 3CX rejects any $top above 100, so anything that can return more than that
// has to be paged. maxExtensions is a ceiling on how far paging will go, so a
// misconfigured filter cannot walk a very large PBX forever.
const (
	pageSize      = 100
	maxExtensions = 2000
)

// --- connection --------------------------------------------------------------

// pbx is one customer's phone system, and a token that is still valid for it.
type pbx struct {
	base  string
	token string
	until time.Time
}

// tokens caches an access token per customer.
//
// 3CX issues a short-lived token in exchange for the extension password. Asking
// for a new one on every call would triple the request count and would mean the
// password crossing the network far more often than it needs to — the token is
// the thing that should be in flight, not the credential behind it.
var tokens struct {
	sync.Mutex
	byCustomer map[string]pbx
}

func connect(ctx context.Context, req plugin.Request) (pbx, error) {
	tokens.Lock()
	defer tokens.Unlock()
	if tokens.byCustomer == nil {
		tokens.byCustomer = map[string]pbx{}
	}

	// A per-customer plugin asked without a customer is a caller bug, and a
	// dangerous one: it would answer with whichever phone system happened to
	// be configured deployment-wide and give no sign whose it was. A
	// technician reading "no extensions are registered" needs to know which
	// business that is about.
	if strings.TrimSpace(req.CustomerID) == "" {
		return pbx{}, plugin.Errorf("400",
			"say which customer's phone system to look at — every customer has their own")
	}

	cfg, ok := plugin.ConfigFrom(ctx)
	if !ok {
		return pbx{}, plugin.Errorf("500", "settings unavailable")
	}
	values, err := cfg.All(ctx, req.CustomerID)
	if err != nil {
		return pbx{}, plugin.Errorf("500", "settings could not be read")
	}

	host, _ := values["fqdn"].(string)
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	if host == "" {
		return pbx{}, plugin.Errorf("400", "no phone system address is set for this customer")
	}
	base := "https://" + host

	extension, _ := values["extension"].(string)
	extension = strings.TrimSpace(extension)
	if extension == "" {
		return pbx{}, plugin.Errorf("400", "no system owner extension is set for this customer")
	}

	// A token still good for a minute is still good. The margin is there
	// because a token that expires mid-request fails in a way that reads like
	// the PBX being down.
	if have, ok := tokens.byCustomer[req.CustomerID]; ok &&
		have.base == base && time.Now().Before(have.until.Add(-time.Minute)) {
		return have, nil
	}

	vault, ok := plugin.VaultFrom(ctx)
	if !ok {
		return pbx{}, plugin.Errorf("500", "vault unavailable")
	}
	password, err := vault.For(ctx, req.CustomerID, "password")
	if err != nil {
		return pbx{}, plugin.Errorf("400", "no password is stored for this customer's phone system")
	}

	body, err := json.Marshal(map[string]string{
		"Username": extension, "Password": password, "SecurityCode": "",
	})
	if err != nil {
		return pbx{}, plugin.Errorf("500", "the sign-in could not be prepared")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/webclient/api/Login/GetAccessToken", bytes.NewReader(body))
	if err != nil {
		return pbx{}, plugin.Errorf("400", "that phone system address is not usable")
	}
	httpReq.Header.Set("content-type", "application/json")

	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(httpReq)
	if err != nil {
		return pbx{}, plugin.Errorf("502", "the phone system could not be reached")
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return pbx{}, plugin.Errorf("401", "the phone system refused that extension and password")
	}
	if res.StatusCode != http.StatusOK {
		return pbx{}, plugin.Errorf("502", "the phone system answered %d when signing in", res.StatusCode)
	}

	var answer struct {
		Status string `json:"Status"`
		Token  struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		} `json:"Token"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return pbx{}, plugin.Errorf("502", "the phone system returned something unreadable")
	}
	if answer.Status != "AuthSuccess" || answer.Token.AccessToken == "" {
		return pbx{}, plugin.Errorf("401", "the phone system refused that extension and password")
	}

	// The token is registered for redaction as well. It is a bearer credential
	// for the whole PBX, so it must not survive into an error message or a log
	// line by way of some future change here.
	vault.Learn(answer.Token.AccessToken)

	life := time.Duration(answer.Token.ExpiresIn) * time.Second
	if life <= 0 {
		life = 30 * time.Minute
	}
	conn := pbx{base: base, token: answer.Token.AccessToken, until: time.Now().Add(life)}
	tokens.byCustomer[req.CustomerID] = conn
	return conn, nil
}

// get performs one read against the PBX.
//
// Every caller names the fields it wants. That is not tidiness: 3CX returns
// SIP passwords and voicemail PINs in its default projections, and the only
// reliable way not to leak a field is not to ask for it.
func (c pbx) get(ctx context.Context, path string, query url.Values, into any) error {
	ask := c.base + "/xapi/v1/" + path
	if len(query) > 0 {
		ask += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ask, nil)
	if err != nil {
		return plugin.Errorf("500", "the request could not be prepared")
	}
	req.Header.Set("authorization", "Bearer "+c.token)
	req.Header.Set("accept", "application/json")

	res, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return plugin.Errorf("502", "the phone system could not be reached")
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return plugin.Errorf("401", "the phone system rejected our sign-in")
	case res.StatusCode == http.StatusForbidden:
		return plugin.Errorf("403", "that extension does not have the system owner role")
	case res.StatusCode == http.StatusNotFound:
		return plugin.Errorf("404", "this phone system does not offer that")
	case res.StatusCode != http.StatusOK:
		return plugin.Errorf("502", "the phone system answered %d", res.StatusCode)
	}

	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(into); err != nil {
		return plugin.Errorf("502", "the phone system returned something unreadable")
	}
	return nil
}

// post creates something on the PBX and returns what it made.
//
// Separate from patch because creating and changing fail differently: a create
// that collides with an existing extension comes back as a 400 whose body is
// the only thing that says which number was taken, and that detail is the
// difference between a usable error and "the phone system would not accept
// that".
func (c pbx) post(ctx context.Context, path string, body any, into any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return plugin.Errorf("500", "the request could not be prepared")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/xapi/v1/"+path, bytes.NewReader(payload))
	if err != nil {
		return plugin.Errorf("500", "the request could not be prepared")
	}
	req.Header.Set("authorization", "Bearer "+c.token)
	req.Header.Set("content-type", "application/json")
	// Without these 3CX refuses every create with "The delta field is
	// required", which is not about the body at all: the delta it means is
	// OData's change-tracking, and it only engages once the request declares
	// which OData version it speaks and asks for the created object back.
	// Hours are lost to this error because it names a field that never existed.
	// Assigned to the map directly rather than through Set, which canonicalises
	// to "Odata-Version". 3CX's OData stack looks for the exact spelling, and
	// the difference between the two is a create that always fails.
	req.Header["OData-Version"] = []string{"4.0"}
	req.Header["OData-MaxVersion"] = []string{"4.0"}
	req.Header["Prefer"] = []string{"return=representation"}

	res, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return plugin.Errorf("502", "the phone system could not be reached")
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	switch {
	case res.StatusCode == http.StatusForbidden:
		return plugin.Errorf("403", "that extension does not have permission to create this")
	case res.StatusCode >= 400:
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 300 {
			detail = detail[:300]
		}
		if detail == "" {
			// The status alone, rather than a sentence that says nothing. A 4xx
			// with an empty body is a real answer and hiding it wastes an hour.
			return plugin.Errorf("400", "the phone system refused that with HTTP %d and said nothing", res.StatusCode)
		}
		return plugin.Errorf("400", "the phone system would not accept that: %s", detail)
	}

	if into == nil {
		return nil
	}
	// A create can legitimately answer 204 with no body.
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(into); err != nil {
		return nil
	}
	return nil
}

// remove deletes something from the PBX.
func (c pbx) remove(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/xapi/v1/"+path, nil)
	if err != nil {
		return plugin.Errorf("500", "the request could not be prepared")
	}
	req.Header.Set("authorization", "Bearer "+c.token)

	res, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return plugin.Errorf("502", "the phone system could not be reached")
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	switch {
	case res.StatusCode == http.StatusForbidden:
		return plugin.Errorf("403", "that extension does not have permission to remove this")
	case res.StatusCode == http.StatusNotFound:
		return plugin.Errorf("404", "that no longer exists on this phone system")
	case res.StatusCode >= 400:
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return plugin.Errorf("400", "the phone system would not remove that: %s", detail)
	}
	return nil
}

/*
action invokes one of 3CX's named operations, such as Pbx.MultiUserUpdate.

Separate from post because an action is not a create, and the headers a create
needs are wrong here: an action answers 204 with no entity, so asking for the
representation back makes the server build one that does not exist. That is a
500 with an empty body, which says nothing about the cause and costs an hour.
*/
func (c pbx) action(ctx context.Context, name string, body any, into any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return plugin.Errorf("500", "the request could not be prepared")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/xapi/v1/"+name, bytes.NewReader(payload))
	if err != nil {
		return plugin.Errorf("500", "the request could not be prepared")
	}
	req.Header.Set("authorization", "Bearer "+c.token)
	req.Header.Set("content-type", "application/json")

	res, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return plugin.Errorf("502", "the phone system could not be reached")
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	switch {
	case res.StatusCode == http.StatusForbidden:
		return plugin.Errorf("403", "that extension does not have permission to do this")
	case res.StatusCode >= 400:
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 300 {
			detail = detail[:300]
		}
		if detail == "" {
			return plugin.Errorf("400", "the phone system refused that with HTTP %d and said nothing", res.StatusCode)
		}
		return plugin.Errorf("400", "the phone system would not accept that: %s", detail)
	}

	if into == nil {
		return nil
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(into)
	return nil
}

// patch sends one change to the PBX.
func (c pbx) patch(ctx context.Context, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return plugin.Errorf("500", "the change could not be prepared")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.base+"/xapi/v1/"+path, bytes.NewReader(payload))
	if err != nil {
		return plugin.Errorf("500", "the change could not be prepared")
	}
	req.Header.Set("authorization", "Bearer "+c.token)
	req.Header.Set("content-type", "application/json")

	res, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return plugin.Errorf("502", "the phone system could not be reached")
	}
	defer res.Body.Close() //nolint:errcheck // best effort

	switch {
	case res.StatusCode == http.StatusForbidden:
		return plugin.Errorf("403", "that extension does not have permission to make this change")
	case res.StatusCode == http.StatusNotFound:
		return plugin.Errorf("404", "that no longer exists on this phone system")
	case res.StatusCode >= 400:
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return plugin.Errorf("400", "the phone system would not accept that change: %s", detail)
	}
	return nil
}

// findExtension resolves an extension number to the internal id a change needs.
func (c pbx) findExtension(ctx context.Context, number string) (int64, error) {
	q := url.Values{}
	q.Set("$select", "Id,Number")
	q.Set("$filter", fmt.Sprintf("Number eq '%s'", strings.ReplaceAll(number, "'", "''")))

	var doc struct {
		Value []struct {
			Id int64 `json:"Id"`
		} `json:"value"`
	}
	if err := c.get(ctx, "Users", q, &doc); err != nil {
		return 0, err
	}
	if len(doc.Value) == 0 {
		return 0, plugin.Errorf("404", "there is no extension %s on this phone system", number)
	}
	return doc.Value[0].Id, nil
}

// --- tools -------------------------------------------------------------------

func systemStatus(ctx context.Context, req plugin.Request) (any, error) {
	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	var s struct {
		FQDN                            string `json:"FQDN"`
		Version                         string `json:"Version"`
		Activated                       bool   `json:"Activated"`
		ExtensionsRegistered            int    `json:"ExtensionsRegistered"`
		ExtensionsTotal                 int    `json:"ExtensionsTotal"`
		TrunksRegistered                int    `json:"TrunksRegistered"`
		TrunksTotal                     int    `json:"TrunksTotal"`
		CallsActive                     int    `json:"CallsActive"`
		MaxSimCalls                     int    `json:"MaxSimCalls"`
		HasNotRunningServices           bool   `json:"HasNotRunningServices"`
		HasUnregisteredSystemExtensions bool   `json:"HasUnregisteredSystemExtensions"`
		FreeDiskSpace                   int64  `json:"FreeDiskSpace"`
		TotalDiskSpace                  int64  `json:"TotalDiskSpace"`
	}
	if err := conn.get(ctx, "SystemStatus", nil, &s); err != nil {
		return nil, err
	}

	// The licence, because renewal dates are an MSP's problem and finding out
	// on the day it lapses is finding out too late. Fetched here rather than as
	// its own tool so a dashboard costs one request, not two — and the fields
	// are named so the licence key itself never crosses the network.
	var lic struct {
		ProductCode        string `json:"ProductCode"`
		ExpirationDate     string `json:"ExpirationDate"`
		MaintenanceExpires string `json:"MaintenanceExpiresAt"`
		LicenseActive      bool   `json:"LicenseActive"`
		Support            bool   `json:"Support"`
		MaxSimCalls        int    `json:"MaxSimCalls"`
		CompanyName        string `json:"CompanyName"`
	}
	lq := url.Values{}
	lq.Set("$select", "ProductCode,ExpirationDate,MaintenanceExpiresAt,LicenseActive,Support,MaxSimCalls,CompanyName")
	if err := conn.get(ctx, "LicenseStatus", lq, &lic); err != nil {
		// A status answer without the licence still answers the urgent
		// question, so this degrades rather than failing the call.
		lic.ProductCode = ""
	}

	// Trunk detail, because "1 of 2 registered" is only useful with the name of
	// the one that is not.
	var trunks struct {
		Value []struct {
			Number    string `json:"Number"`
			IsOnline  bool   `json:"IsOnline"`
			Direction string `json:"Direction"`
		} `json:"value"`
	}
	q := url.Values{}
	q.Set("$select", "Number,IsOnline,Direction")
	if err := conn.get(ctx, "Trunks", q, &trunks); err != nil {
		// A status answer without trunk names still answers most of the
		// question, so this degrades rather than failing the whole call.
		trunks.Value = nil
	}

	offline := []string{}
	for _, t := range trunks.Value {
		if !t.IsOnline {
			offline = append(offline, t.Number)
		}
	}

	// Said in words as well as numbers. "3 of 40 extensions registered" is the
	// finding; making a model derive it from two integers is how it gets
	// derived wrong.
	concerns := []string{}
	// Nothing registered at all is the loudest possible finding and the first
	// version did not make it: a site can have every trunk up, every service
	// running and not one handset able to ring.
	if s.ExtensionsTotal > 0 && s.ExtensionsRegistered == 0 {
		concerns = append(concerns, "no extensions are registered — no handset on this system can make or take a call")
	} else if s.ExtensionsTotal > 0 && s.ExtensionsRegistered*2 < s.ExtensionsTotal {
		concerns = append(concerns, fmt.Sprintf("only %d of %d extensions are registered",
			s.ExtensionsRegistered, s.ExtensionsTotal))
	}
	if s.TrunksTotal > 0 && s.TrunksRegistered < s.TrunksTotal {
		concerns = append(concerns, fmt.Sprintf("%d of %d trunks are not registered — outbound and inbound calls will fail on those",
			s.TrunksTotal-s.TrunksRegistered, s.TrunksTotal))
	}
	if s.HasNotRunningServices {
		concerns = append(concerns, "at least one 3CX service is not running")
	}
	if s.HasUnregisteredSystemExtensions {
		concerns = append(concerns, "a system extension is not registered")
	}
	if !s.Activated {
		concerns = append(concerns, "this phone system is not activated")
	}
	if s.TotalDiskSpace > 0 && s.FreeDiskSpace*10 < s.TotalDiskSpace {
		concerns = append(concerns, "less than 10% disk space is free")
	}

	// A licence that lapses in a fortnight is a finding, not a footnote.
	if lic.ExpirationDate != "" {
		if at, err := time.Parse(time.RFC3339, lic.ExpirationDate); err == nil {
			days := int(time.Until(at).Hours() / 24)
			switch {
			case days < 0:
				concerns = append(concerns, "the 3CX licence has expired")
			case days <= 14:
				concerns = append(concerns, fmt.Sprintf("the 3CX licence expires in %d days", days))
			}
		}
	}
	if lic.ProductCode != "" && !lic.LicenseActive {
		concerns = append(concerns, "the 3CX licence is not active")
	}

	healthy := len(concerns) == 0
	return map[string]any{
		"address":                s.FQDN,
		"version":                s.Version,
		"healthy":                healthy,
		"concerns":               concerns,
		"extensions_registered":  s.ExtensionsRegistered,
		"extensions_total":       s.ExtensionsTotal,
		"trunks_registered":      s.TrunksRegistered,
		"trunks_total":           s.TrunksTotal,
		"trunks_offline":         offline,
		"calls_in_progress":      s.CallsActive,
		"max_simultaneous_calls": s.MaxSimCalls,
		"free_disk_bytes":        s.FreeDiskSpace,
		// The licence, minus the key itself — that is never asked for, so it
		// is not here to be forgotten about.
		"licence": map[string]any{
			"product":             lic.ProductCode,
			"company":             lic.CompanyName,
			"active":              lic.LicenseActive,
			"support":             lic.Support,
			"expires":             lic.ExpirationDate,
			"maintenance_expires": lic.MaintenanceExpires,
			"simultaneous_calls":  lic.MaxSimCalls,
		},
	}, nil
}

type extensionArgs struct {
	OnlyUnregistered bool `json:"only_unregistered"`
}

func extensions(ctx context.Context, req plugin.Request) (any, error) {
	var args extensionArgs
	if len(req.Args) > 0 {
		_ = json.Unmarshal(req.Args, &args)
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	type row struct {
		Number       string `json:"Number"`
		DisplayName  string `json:"DisplayName"`
		IsRegistered bool   `json:"IsRegistered"`
		Enabled      bool   `json:"Enabled"`
		Profile      string `json:"CurrentProfileName"`
	}

	// 3CX refuses any $top above 100, so a customer with more extensions than
	// that has to be paged through. Found the hard way: the first version asked
	// for 500 and every site with a real number of phones would have got a 400.
	var all []row
	for skip := 0; skip < maxExtensions; skip += pageSize {
		// The field list is the security boundary. Adding a name here is a
		// decision to send that field to whoever asked, including the
		// assistant — and this endpoint will happily return SIP passwords.
		q := url.Values{}
		q.Set("$select", "Number,DisplayName,IsRegistered,Enabled,CurrentProfileName")
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

	out := make([]map[string]any, 0, len(all))
	var unregistered int
	for _, u := range all {
		if !u.IsRegistered {
			unregistered++
		}
		if args.OnlyUnregistered && u.IsRegistered {
			continue
		}
		out = append(out, map[string]any{
			"extension":  u.Number,
			"name":       u.DisplayName,
			"registered": u.IsRegistered,
			"enabled":    u.Enabled,
			"status":     u.Profile,
		})
	}

	return map[string]any{
		"extensions":   out,
		"total":        len(all),
		"unregistered": unregistered,
	}, nil
}

type callArgs struct {
	Limit int `json:"limit"`
}

func recentCalls(ctx context.Context, req plugin.Request) (any, error) {
	var args callArgs
	if len(req.Args) > 0 {
		_ = json.Unmarshal(req.Args, &args)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 25
	}
	// The PBX will not answer a larger page than this, whatever was asked for.
	if limit > pageSize {
		limit = pageSize
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("$top", fmt.Sprint(limit))
	q.Set("$orderby", "SegmentStartTime desc")

	var doc struct {
		Value []map[string]any `json:"value"`
	}
	if err := conn.get(ctx, "CallHistoryView", q, &doc); err != nil {
		return nil, err
	}

	// Only the fields a person would read off a call log. The view carries
	// more, and passing it through would be handing on whatever 3CX decides to
	// add to it in a future version.
	keep := []string{
		"SegmentStartTime", "SegmentEndTime", "SrcCallerNumber", "SrcDisplayName",
		"DstCallerNumber", "DstDisplayName", "CallTime", "Answered", "SegmentActionName",
	}
	calls := make([]map[string]any, 0, len(doc.Value))
	for _, row := range doc.Value {
		trimmed := map[string]any{}
		for _, k := range keep {
			if v, ok := row[k]; ok && v != nil {
				trimmed[k] = v
			}
		}
		if len(trimmed) > 0 {
			calls = append(calls, trimmed)
		}
	}

	return map[string]any{"calls": calls, "returned": len(calls)}, nil
}

type updateArgs struct {
	Extension string  `json:"extension"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	Email     *string `json:"email"`
	Enabled   *bool   `json:"enabled"`
}

// changes turns the arguments into the PBX's own field names, and says what
// they mean in words the person who authorised it would recognise.
func (a updateArgs) changes() (map[string]any, []string) {
	body := map[string]any{}
	var said []string
	if a.FirstName != nil {
		body["FirstName"] = *a.FirstName
		said = append(said, "first name")
	}
	if a.LastName != nil {
		body["LastName"] = *a.LastName
		said = append(said, "last name")
	}
	if a.Email != nil {
		body["EmailAddress"] = *a.Email
		said = append(said, "email address")
	}
	if a.Enabled != nil {
		body["Enabled"] = *a.Enabled
		if *a.Enabled {
			said = append(said, "enabled")
		} else {
			said = append(said, "disabled")
		}
	}
	return body, said
}

func updateExtension(ctx context.Context, req plugin.Request) (any, error) {
	var args updateArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return nil, plugin.Errorf("400", "arguments could not be parsed")
	}
	if strings.TrimSpace(args.Extension) == "" {
		return nil, plugin.Errorf("400", "which extension should be changed?")
	}

	body, said := args.changes()
	if len(body) == 0 {
		return nil, plugin.Errorf("400", "nothing was asked to change")
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}
	id, err := conn.findExtension(ctx, args.Extension)
	if err != nil {
		return nil, err
	}
	if err := conn.patch(ctx, fmt.Sprintf("Users(%d)", id), body); err != nil {
		return nil, err
	}

	return map[string]any{
		"extension": args.Extension,
		"changed":   said,
		"by":        req.Actor.UserID,
	}, nil
}

type bulkArgs struct {
	Extensions       []string `json:"extensions"`
	Enabled          *bool    `json:"enabled"`
	OutboundCallerID *string  `json:"outbound_caller_id"`
}

// bulkUpdateExtensions applies one change across several extensions.
//
// Each is applied on its own and reported on its own. A run that stops at the
// first failure leaves the caller not knowing which of forty extensions were
// changed, which is worse than the failure — so every one is attempted and the
// answer says exactly what happened to each.
func bulkUpdateExtensions(ctx context.Context, req plugin.Request) (any, error) {
	var args bulkArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return nil, plugin.Errorf("400", "arguments could not be parsed")
	}
	if len(args.Extensions) == 0 {
		return nil, plugin.Errorf("400", "no extensions were named")
	}
	if len(args.Extensions) > 200 {
		return nil, plugin.Errorf("400", "that is more than 200 extensions; do it in smaller batches")
	}

	body := map[string]any{}
	if args.Enabled != nil {
		body["Enabled"] = *args.Enabled
	}
	if args.OutboundCallerID != nil {
		body["OutboundCallerID"] = *args.OutboundCallerID
	}
	if len(body) == 0 {
		return nil, plugin.Errorf("400", "nothing was asked to change")
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	changed := []string{}
	failed := map[string]string{}
	for _, number := range args.Extensions {
		number = strings.TrimSpace(number)
		if number == "" {
			continue
		}
		id, err := conn.findExtension(ctx, number)
		if err != nil {
			failed[number] = err.Error()
			continue
		}
		if err := conn.patch(ctx, fmt.Sprintf("Users(%d)", id), body); err != nil {
			failed[number] = err.Error()
			continue
		}
		changed = append(changed, number)
	}

	return map[string]any{
		"changed":       changed,
		"changed_count": len(changed),
		"failed":        failed,
		"by":            req.Actor.UserID,
	}, nil
}

func recentEvents(ctx context.Context, req plugin.Request) (any, error) {
	var args callArgs
	if len(req.Args) > 0 {
		_ = json.Unmarshal(req.Args, &args)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > pageSize {
		limit = pageSize
	}

	conn, err := connect(ctx, req)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("$top", fmt.Sprint(limit))
	q.Set("$orderby", "TimeGenerated desc")
	q.Set("$select", "Type,Message,Source,TimeGenerated")

	var doc struct {
		Value []struct {
			Type    string `json:"Type"`
			Message string `json:"Message"`
			Source  string `json:"Source"`
			At      string `json:"TimeGenerated"`
		} `json:"value"`
	}
	if err := conn.get(ctx, "EventLogs", q, &doc); err != nil {
		return nil, err
	}

	events := make([]map[string]any, 0, len(doc.Value))
	for _, e := range doc.Value {
		events = append(events, map[string]any{
			"at": e.At, "kind": e.Type, "source": e.Source, "message": e.Message,
		})
	}
	return map[string]any{"events": events, "returned": len(events)}, nil
}
