# Writing a plugin

A plugin is a NATS micro service. Nothing in the SDK's surface mentions NATS —
`plugin.Serve` is the entire transport boundary.

```go
p := plugin.Plugin{
    Name:     "syncro",
    Version:  "0.1.0",
    Category: plugin.CategoryPSA,
    Tools: []plugin.Tool{{
        Name:        "tickets.search",
        Description: "Search tickets by customer, status or free text.",
        Provides:    []plugin.Capability{plugin.CapWorkItemsSearch},
        Schema:      schema,
        Handler:     searchTickets,
    }},
}
return plugin.Serve(ctx, p, plugin.WithSecrets(apiKey))
```

Three rules that are enforced rather than documented:

- **Return `plugin.Errorf` for failures.** Any other error is logged locally
  and reported to the caller as a generic failure, because wrapped vendor
  errors routinely carry request URLs and auth headers.
- **Register every resolved credential via `WithSecrets`.** The redactor scrubs
  those literals wherever they appear, including inside prose that key-name
  rules would never inspect.
- **A tool that declares `Mutates` must name a `RequiresPermission`.** The
  plugin does not start otherwise. A write nobody has to be allowed to make is
  not a write anyone should make.

Declare capabilities from the vocabulary in `pkg/plugin/capability.go`. An
unknown tag is refused at startup — letting plugins invent tags freely is how
the mechanism stops meaning anything.

## Configuring a plugin

A plugin publishes a JSON Schema for its own settings, and the console renders
the form from it — there is no per-integration frontend code. Fields marked
`x-azir-secret` are sealed into the vault; everything else lands in
`plugin_config`. An administrator types a subdomain and an API token into the
same panel without needing to know they are stored entirely differently.

Handlers read both back at request time, so a rotated key or a changed
subdomain takes effect without restarting anything:

```go
cfg, _ := plugin.ConfigFrom(ctx)
subdomain, err := cfg.String(ctx, req.CustomerID, "subdomain")

v, _ := plugin.VaultFrom(ctx)
token, err := v.For(ctx, "", "api_key")

// Azir customer id -> this plugin's identifier, through the spine.
ident, _ := plugin.IdentityFrom(ctx)
external, err := ident.External(ctx, req.CustomerID)
```

That last one is why handlers receive an opaque Azir customer id rather than a
vendor one: memory and context hang off the spine, so switching PSA later does
not orphan them.

## Publishing what a system will accept

Where a plugin can change fields on something, it should publish the field list
as data rather than have Azir keep its own copy: name, label, kind, group, and
what each will accept. Azir builds the spreadsheet columns, the form controls,
the comparison and the write-side allowlist from that one list.

The 3CX extension editor works this way. Fifty-two fields across six tabs, and
Azir knows none of them by name. Keeping a second copy is how a sheet ended up
carrying two fields out of thirty-two while everything looked correct.

Fields carry a few flags worth knowing:

| | |
|---|---|
| `kind: secret` | write-only. Never read back, never staged, never compared. |
| `kind: readonly` | worth showing, not something Azir will change. |
| `unique` | no two records may share a value. A bulk edit setting one value across several is refused before anything is written. |
| `labels` | what to show for each choice, where the stored value is not something to put in front of a person. |

## Freshness, not mirroring

Azir caches tool results; it does not mirror the vendor. Nothing is stored that
nobody asked for, and the vendor stays the source of truth.

Each tool declares a two-level staleness budget, because only the plugin knows
how volatile its own data is — a ticket changes while you are reading it, a
customer's phone number does not:

| tool | serve instantly | must refresh |
|---|---|---|
| `tickets.get`, `tickets.timeline` | 30s | 2m |
| `tickets.search` | 60s | 5m |
| `time.entries` | 5m | 30m |
| `invoices.list`, `customers.standing` | 10m | 1h |
| `customers.*` | 30m | 4h |
| `assets.list` | 1h | 12h |
| `docs.search` | 6h | 24h |
| `access.check` | never cached | |

Below the first threshold a cached answer is returned as-is. Between the two it
is still returned immediately while a refresh runs behind it, so this caller is
fast and the next is current. Beyond the second, the caller waits. Concurrent
refreshes of the same entry collapse into one, because twenty callers arriving
at an expired entry must not become twenty vendor requests.

Two things make this honest rather than merely fast. Every response carries
`X-Azir-Source` and `X-Azir-Age-Seconds`, so a reader — person or model — knows
whether a figure is live or four minutes old. And `{"refresh": true}` bypasses
the cache entirely, which is what the UI sends when a technician opens a ticket
they are about to act on.

When the vendor is unreachable, a stale entry is served with
`X-Azir-Source: cache-stale-vendor-unavailable` rather than an error. Something
old and labelled beats nothing.

A write invalidates the whole plugin's cache. Anything narrower is a guess
about what a change touched, and a wrong guess shows somebody stale data
immediately after they changed it.

## Syncro permissions

Azir needs exactly three: `ticket.read`, `customer.read`, `asset.read`. Nothing
else — it never deletes, and never executes scripts.

`syncro.access.check` reports what a configured token actually grants and names
anything beyond that set, so least privilege is verifiable from inside Azir
rather than by squinting at checkboxes in another product. Run against a full
admin token it reports 21 excessive grants, including `script.execute`, which
would let a compromised Azir run code on customer machines.
