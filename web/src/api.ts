export type Status = "pending" | "approved" | "rejected";

export type Freshness = { soft: string; hard: string };

export type Tool = {
  plugin: string;
  name: string;
  subject: string;
  description: string;
  /** Human-facing wording for settings; `description` is written for the model. */
  summary?: string;
  provides: string[] | null;
  mutates: boolean;
  status: Status;
  /** Whether the plugin says this tool can currently do its job. */
  available: boolean;
  unavailable_reason?: string;
  freshness?: Freshness;
  /** The permission a caller must hold to invoke a mutating tool. */
  requires_permission?: string;
};

export type Plugin = {
  name: string;
  version: string;
  id: string;
  description: string;
  category: string;
  /** "customer" when this plugin is configured once per customer. */
  config_scope?: "customer" | "";
  sdk: string;
  ready: boolean;
  not_ready_reason?: string;
  config_schema?: JsonSchema;
  tools: Tool[];
};

export type Registry = {
  plugins: Plugin[];
  capabilities: Record<string, string[]>;
  at: string;
};

/**
 * The subset of JSON Schema the settings form renders. Plugins publish this
 * themselves, so the console never contains per-integration knowledge.
 */
export type JsonSchema = {
  type?: string;
  title?: string;
  description?: string;
  required?: string[];
  properties?: Record<string, SchemaProperty>;
};

export type SchemaProperty = {
  type?: "string" | "number" | "integer" | "boolean";
  title?: string;
  description?: string;
  format?: string;
  default?: unknown;
  enum?: string[];
  /** Marks credential material: stored in the vault, never returned. */
  "x-azir-secret"?: boolean;
};

export type Settings = {
  plugin: string;
  description: string;
  category: string;
  config_schema?: JsonSchema;
  values: Record<string, unknown>;
  /** Whether each secret field has a stored value — never the value itself. */
  secrets_set: Record<string, boolean>;
  customer_id?: string;
  writes_enabled: boolean;
  has_mutating_tools: boolean;
};

export type Customer = {
  id: string;
  display_name: string;
  created_at: string;
  identities: { plugin: string; external_id: string; created_at: string }[];
};

export type AuditEvent = {
  occurred_at: string;
  actor_user_id: string;
  action: string;
  plugin?: string;
  tool?: string;
  customer_id?: string;
  outcome: string;
  detail?: string;
};

export type Actor = {
  user_id: string;
  email: string;
  display_name: string;
  role: string;
  permissions: string[];
};

export type User = {
  id: string;
  email: string;
  display_name: string;
  role: string;
  provider?: string;
  disabled: boolean;
  created_at: string;
  last_seen_at?: string;
};

export type Role = {
  name: string;
  description: string;
  permissions: string[];
  builtin: boolean;
};

/** Permission names, mirroring internal/identity. */
export const Perm = {
  pluginConfigure: "plugin.configure",
  pluginApprove: "plugin.approve",
  userManage: "user.manage",
  roleManage: "role.manage",
  customerManage: "customer.manage",
  auditRead: "audit.read",
  dataManage: "data.manage",
  toolRead: "tool.read",
  phoneManage: "phone.manage",
} as const;

/**
 * Raised when the server says there is no valid session.
 *
 * Distinguished from an ordinary failure so an expired session sends someone
 * back to the sign-in screen rather than showing "sign in to continue" as an
 * error inside a page they can no longer use.
 */
export class Unauthorized extends Error {
  constructor() {
    super("not signed in");
    this.name = "Unauthorized";
  }
}

let onExpired: (() => void) | null = null;

/** Registers the callback that runs when a session turns out to be over. */
export function onSessionExpired(fn: () => void) {
  onExpired = fn;
}

/**
 * Raised when the browser could not reach Azir at all.
 *
 * Separate from a refusal by Azir: "Failed to fetch" is the browser's own
 * wording for a dead connection, and showing it to a technician tells them
 * nothing they can act on.
 */
export class Unreachable extends Error {
  constructor() {
    super("Azir could not be reached. It may be restarting, or the connection dropped.");
    this.name = "Unreachable";
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      ...init,
      headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    });
  } catch {
    throw new Unreachable();
  }

  if (res.status === 401) {
    // Sign-in attempts handle their own 401; everything else means the session
    // ended underneath us.
    if (!path.endsWith("/login") && !path.endsWith("/me")) onExpired?.();
    throw new Unauthorized();
  }

  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body?.error) {
        message = body.error;
        // A permission refusal names the permission that was missing. Saying
        // which one turns "you can't" into something actionable.
        if (body.required_permission) message += ` (needs ${body.required_permission})`;
      }
    } catch {
      // A non-JSON error body is not worth reporting over the status line.
    }
    throw new Error(message);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export type AuthState = {
  needs_setup: boolean;
  oidc_enabled: boolean;
  oidc_label: string;
};

export type AuthSettings = {
  enabled: boolean;
  issuer: string;
  client_id: string;
  tenant_id: string;
  allowed_domains: string[];
  auto_provision: boolean;
  default_role: string;
  redirect_url: string;
  updated_at: string;
  updated_by: string;
  /** Whether a client secret is stored — never the secret itself. */
  client_secret_set: boolean;
  /** The redirect URI to register with the identity provider. */
  callback_url: string;
};

/**
 * Capability names, from the vocabulary Azir owns.
 *
 * Screens ask for these, never for a plugin. A deployment running a different
 * PSA lights up the same interface, which is the difference between a platform
 * and a client for one vendor.
 */
export const Cap = {
  ticketSearch: "work_items.search",
  ticketGet: "work_items.get",
  ticketTimeline: "work_items.timeline",
  ticketSchema: "work_items.schema",
  customerList: "customers.list",
  customerContacts: "customers.contacts",
  customerGet: "customers.get",
  customerStanding: "customers.standing",
  timeEntries: "time_entries.list",
  assets: "assets.list",
  docs: "documentation.search",
  invoices: "invoices.list",
  ticketComment: "work_items.comment",
  ticketUpdate: "work_items.update",
  phoneStatus: "phone_system.status",
  phoneExtensions: "phone_system.extensions",
} as const;

// --- what the work-item capabilities return ----------------------------------
// Named for the domain rather than for a vendor: these are the shapes the
// capability contract promises, and a plugin's job is to produce them.

export type Ticket = {
  id: number;
  number?: string;
  subject: string;
  status?: string;
  priority?: string;
  problem_type?: string;
  customer_id?: number;
  customer?: string;
  assigned_to?: string;
  created_at?: string;
  updated_at?: string;
  /** Who reported it, when the ticket names somebody. Often it names nobody. */
  contact?: Contact;
  /** Where to open this ticket in the system it came from, when it says. */
  url?: string;
  comments?: Comment[];
};

export type Comment = {
  id: number;
  subject?: string;
  body: string;
  hidden: boolean;
  tech?: string;
  created_at?: string;
};

/** A named person at a customer, and how to reach them. */
export type Contact = {
  id?: number;
  name: string;
  email?: string;
  phone?: string;
  mobile?: string;
  title?: string;
  notes?: string;
};

/** Someone a work item can be assigned to, as the connected system knows them. */
export type Technician = {
  id: number;
  name: string;
  email?: string;
};

export type TimelineEntry = {
  at: string;
  kind: string;
  actor?: string;
  body?: string;
  hidden?: boolean;
  minutes?: number;
};

/**
 * A ticket's history and what it says about itself.
 *
 * The computed figures sit alongside the entries rather than nested under a
 * "signals" key — that is what the capability actually returns, and a type that
 * described a tidier shape would only be wrong at runtime.
 */
export type Timeline = {
  ticket: Ticket;
  entries: TimelineEntry[];
  first_response_minutes?: number;
  longest_gap_hours?: number;
  round_trips: number;
  /** The sides could not be told apart, so round_trips means nothing. */
  round_trips_unknown?: boolean;
  total_logged_minutes: number;
  stale: boolean;
  /** What this history cannot show, said plainly by the plugin. */
  notes?: string[];
};

export type CustomerRecord = {
  id: number;
  name?: string;
  /** The wire name. A business is what a customer is called; `name` is a person. */
  business_name?: string;
  email?: string;
  phone?: string;
  address?: string;
  notes?: string;
  created_at?: string;
};

/** What a customer is called: the business if there is one, the person if not. */
export function customerName(c: CustomerRecord): string {
  return c.business_name || c.name || `Customer ${c.id}`;
}

export type Standing = {
  balance_cents?: number;
  overdue_cents?: number;
  unpaid?: number;
  note?: string;
};

export type Asset = {
  id: number;
  name: string;
  type?: string;
  serial?: string;
  customer_id?: number;
};

export type TimeEntry = {
  id: number;
  ticket_id?: number;
  user?: string;
  notes?: string;
  minutes: number;
  billable_minutes?: number;
  billable?: boolean;
  start_at?: string;
};

export type Doc = {
  id: number;
  title?: string;
  slug?: string;
  body?: string;
  updated_at?: string;
};

export type Paged<T> = {
  items: T[] | null;
  page: { page: number; per_page: number; total_pages: number; total_count: number };
};

/** How an answer was obtained, so a reader can weigh it. */
export type Provenance = { source: string; ageSeconds: number };

export type Answer<T> = { data: T; provenance: Provenance };

/**
 * Raised when nothing in the deployment provides a capability.
 *
 * Distinguished so a screen can say "no PSA is connected" rather than showing a
 * generic failure — partial capability is a normal, expected state here.
 */
export class NotProvided extends Error {
  constructor(readonly capability: string) {
    super(`nothing provides ${capability}`);
    this.name = "NotProvided";
  }
}

/** Calls whichever approved tool provides a capability. */
async function perform<T>(
  capability: string,
  args: Record<string, unknown> = {},
  opts: { refresh?: boolean; customerId?: string } = {},
): Promise<Answer<T>> {
  let res: Response;
  try {
    res = await fetch(`/api/do/${capability}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        args,
        refresh: opts.refresh ?? false,
        customer_id: opts.customerId ?? "",
      }),
    });
  } catch {
    throw new Unreachable();
  }

  if (res.status === 401) {
    onExpired?.();
    throw new Unauthorized();
  }
  if (res.status === 404 || res.status === 403) {
    const body = await res.json().catch(() => ({}));
    if (body?.capability) throw new NotProvided(capability);
    throw new Error(body?.error ?? `${res.status} ${res.statusText}`);
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body?.error ?? `${res.status} ${res.statusText}`);
  }

  return {
    data: (await res.json()) as T,
    provenance: {
      source: res.headers.get("X-Azir-Source") ?? "live",
      ageSeconds: Number(res.headers.get("X-Azir-Age-Seconds") ?? 0),
    },
  };
}

/**
 * Making a change in a connected system, because a person pressed something.
 *
 * A separate route from `perform` on purpose: reads resolve through one index
 * and writes through another, so no way of reading a thing can return a way of
 * changing it. The server applies the same gate the assistant's proposals go
 * through — the caller's permission, the plugin's write setting, and approval.
 */
async function change<T>(
  capability: string,
  args: Record<string, unknown>,
  customerId?: string,
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`/api/change/${capability}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ args, customer_id: customerId }),
    });
  } catch {
    throw new Unreachable();
  }
  if (res.status === 401) {
    onExpired?.();
    throw new Unauthorized();
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    if (res.status === 404 && body?.capability) throw new NotProvided(capability);
    let message = body?.error ?? `${res.status} ${res.statusText}`;
    if (body?.required_permission) message += ` (needs ${body.required_permission})`;
    throw new Error(message);
  }
  return (await res.json()) as T;
}

/** The changes a technician can make from here, rather than in the vendor's app. */
export const act = {
  /** Replies to the customer, or leaves a note only your team can see. */
  comment: (id: number, body: string, hidden: boolean) =>
    change<Ticket>(Cap.ticketComment, { id, body, hidden }),

  /** Moves a ticket: its status, who owns it, how urgent it is. */
  update: (id: number, fields: { status?: string; user_id?: number; priority?: string }) =>
    change<Ticket>(Cap.ticketUpdate, { id, ...fields }),
};

/** One thing a diagnostic capture found wrong. */
export type Finding = {
  severity: "critical" | "warning" | "note";
  title: string;
  detail: string;
  evidence?: string[];
  occurrences?: number;
  source?: string;
};

export type SnapshotReport = {
  system: {
    version?: string;
    os?: string;
    cpu_model?: string;
    cpu_count?: number;
    total_memory_gb?: number;
    free_memory_gb?: number;
    total_disk_gb?: number;
    free_disk_gb?: number;
    extensions?: number;
    queues?: number;
    ring_groups?: number;
    ivrs?: number;
    virtualised?: string;
    captured_at?: string;
    /** The phone system's own address, which is how a bundle names itself. */
    fqdn?: string;
  };
  health: { name: string; says: string; ok: boolean }[];
  findings: Finding[];
  series: {
    cpu_percent?: { at: string; value: number }[];
    free_memory_gb?: { at: string; value: number }[];
    free_disk_gb?: { at: string; value: number }[];
  };
  network?: {
    interface: string;
    sent_mbps?: { at: string; value: number }[];
    received_mbps?: { at: string; value: number }[];
    peak_sent_mbps?: number;
    peak_received_mbps?: number;
    total_sent_gb?: number;
    total_received_gb?: number;
  };
  services?: {
    name: string;
    memory_mb: number;
    peak_memory_mb?: number;
    growth_mb?: number;
    threads?: number;
    processes?: number;
  }[];
  events: {
    total: number;
    from?: string;
    to?: string;
    groups?: {
      source: string;
      id: string;
      label?: string;
      severity: string;
      count: number;
      first?: string;
      last?: string;
      sample?: string;
      says?: string;
    }[];
  };
  quality?: {
    calls: number;
    rated_legs: number;
    poor: number;
    median_mos?: number;
    worst_mos?: number;
    loss_percent?: number;
    jitter_ms?: number;
    codecs?: Counted[];
    endpoints?: Counted[];
    reasons?: Counted[];
    worst?: {
      number?: string;
      duration?: string;
      mos?: number;
      codec?: string;
      endpoint?: string;
      agent?: string;
      address?: string;
      jitter_ms?: number;
      loss_percent?: number;
      reason?: string;
    }[];
  };
  capture?: {
    packets: number;
    seconds?: number;
    from?: string;
    to?: string;
    truncated?: boolean;
    protocols?: Counted[];
    sip_methods?: Counted[];
    streams?: {
      from: string;
      to: string;
      ssrc: string;
      codec?: string;
      packets: number;
      lost?: number;
      loss_percent?: number;
      jitter_ms?: number;
      seconds?: number;
    }[];
    one_way?: string[];
  };
  phones?: { extension?: string; model?: string; firmware?: string; mac?: string; ip?: string }[];
  changes?: {
    rows: number;
    edits?: {
      at: string;
      user?: string;
      ip?: string;
      object?: string;
      before?: string;
      after?: string;
    }[];
    signins?: Counted[];
    addresses?: Counted[];
    from?: string;
    to?: string;
  };
  files_read: string[];
  files_missing?: string[];
};

/** A thing and how often it was seen. */
export type Counted = { name: string; count: number };

/** A capture of one phone system at one moment. */
export type Snapshot = {
  id: string;
  /** Absent on a capture nobody has attached to a customer yet. */
  customer_id?: string;
  /** The phone system's address, read out of the bundle. */
  fqdn?: string;
  kind: string;
  filename?: string;
  captured_at?: string;
  report?: SnapshotReport;
  findings: number;
  worst?: string;
  uploaded_by?: string;
  uploaded_at: string;
  /** When this is deleted. Absent means somebody pinned it. */
  expires_at?: string;
};

export const snapshots = {
  list: (customerId?: string) =>
    request<Snapshot[]>(`/api/snapshots${customerId ? `?customer_id=${customerId}` : ""}`),

  get: (id: string) => request<Snapshot>(`/api/snapshots/${id}`),

  remove: (id: string) => request<void>(`/api/snapshots/${id}`, { method: "DELETE" }),

  /**
   * Uploads a support bundle.
   *
   * Not through `request`, because that sets a JSON content type and a
   * multipart body needs the browser to set its own with the boundary in it.
   */
  upload: async (file: File, customerId?: string) => {
    const body = new FormData();
    body.append("bundle", file);
    // No customer is the ordinary case: the bundle carries the phone system's
    // own address and Azir matches it against whoever that PBX belongs to.
    const scope = customerId ? `?customer_id=${customerId}` : "";
    let res: Response;
    try {
      res = await fetch(`/api/snapshots${scope}`, { method: "POST", body });
    } catch {
      throw new Unreachable();
    }
    if (res.status === 401) {
      onExpired?.();
      throw new Unauthorized();
    }
    if (!res.ok) {
      const problem = await res.json().catch(() => ({}));
      throw new Error(problem?.error ?? `${res.status} ${res.statusText}`);
    }
    return (await res.json()) as { snapshot: Snapshot; report: SnapshotReport };
  },

  /** Collects a capture from a customer's phone system, without a zip. */
  pull: (customerId: string, days = 7) =>
    request<{ snapshot?: Snapshot; report?: SnapshotReport; empty?: boolean; note?: string }>(
      `/api/snapshots/pull?customer_id=${customerId}&days=${days}`,
      { method: "POST" },
    ),

  /** Links a capture to a customer, or unlinks it when given nothing. */
  attach: (id: string, customerId: string) =>
    request<void>(`/api/snapshots/${id}/attach`, {
      method: "POST",
      body: JSON.stringify({ customer_id: customerId }),
    }),

  /** Pins a capture past its expiry, or hands it back to the sweep. */
  keep: (id: string, keep: boolean) =>
    request<void>(`/api/snapshots/${id}/keep`, {
      method: "POST",
      body: JSON.stringify({ keep }),
    }),
};

export const work = {
  /**
   * Finds tickets.
   *
   * `open_only` is answered by the connected system rather than by filtering
   * what comes back: a page spent on finished work is a page in which the old
   * open ticket — the one actually worth finding — never arrives.
   */
  searchTickets: (
    args: {
      query?: string;
      status?: string;
      open_only?: boolean;
      customer_id?: number;
      page?: number;
      per_page?: number;
    },
    refresh?: boolean,
  ) => perform<Paged<Ticket>>(Cap.ticketSearch, args, { refresh }),

  getTicket: (id: number, refresh?: boolean) =>
    perform<Ticket>(Cap.ticketGet, { id }, { refresh }),

  timeline: (id: number, refresh?: boolean) =>
    perform<Timeline>(Cap.ticketTimeline, { id }, { refresh }),

  standing: (customer_id: number) =>
    perform<{ balance_cents?: number; overdue_cents?: number; unpaid?: number; note?: string }>(
      Cap.customerStanding,
      { customer_id },
    ),

  /**
   * What the connected system will accept on a work item.
   *
   * Technicians are records rather than names: matching a signed-in person to
   * the technician a ticket is assigned to needs an address to match on, and a
   * display name alone is not one. This was typed as a list of strings, which
   * was simply wrong about what the capability returns.
   */
  schema: () =>
    perform<{ statuses?: string[]; technicians?: Technician[]; note?: string }>(
      Cap.ticketSchema,
      {},
    ),

  searchCustomers: (args: { query?: string; page?: number; per_page?: number }, refresh?: boolean) =>
    perform<Paged<CustomerRecord>>(Cap.customerList, args, { refresh }),

  getCustomer: (id: number) => perform<CustomerRecord>(Cap.customerGet, { id }),

  contacts: (customer_id: number) =>
    perform<Paged<Contact>>(Cap.customerContacts, { customer_id, per_page: 50 }),

  assets: (customer_id: number) => perform<Paged<Asset>>(Cap.assets, { customer_id }),

  /**
   * How a customer's phone system is doing.
   *
   * Keyed by Azir's own customer id rather than the PSA's, because that is what
   * the per-customer credentials hang off. One call answers the whole panel —
   * the plugin folds licence and trunk detail into it so a dashboard is not
   * four requests against somebody's PBX.
   */
  phoneStatus: (customerId: string, refresh?: boolean) =>
    perform<PhoneStatus>(Cap.phoneStatus, {}, { refresh, customerId }),

  timeEntries: (args: { customer_id?: number; page?: number; per_page?: number }) =>
    perform<Paged<TimeEntry>>(Cap.timeEntries, args),

  docs: (query: string) => perform<Paged<Doc>>(Cap.docs, { query }),
};

/** One kind of thing Azir is holding, as the Data screen shows it. */
export type Stored = {
  name: string;
  detail: string;
  count: number;
  bytes: number;
  /** Whether starting fresh removes this. */
  cleared: boolean;
  /** How it goes away on its own, in words. Absent means it stays. */
  expires?: string;
};

/** How the people with accounts currently get in. */
export type SignIn = {
  /** Can sign in whatever happens to the identity provider. */
  with_password: string[] | null;
  /** Would have no way in once the provider is gone. */
  sso_only: string[] | null;
};

/** How long the two things that expire are kept, in days. */
export type Retention = { capture_days: number; cache_days: number };

export type DataUsage = {
  stored: Stored[];
  /** The whole database on disk, which is larger than the parts add up to. */
  total: number;
  /** Read before resetting, because resetting removes single sign-on. */
  sign_in: SignIn;
  retention: Retention;
};

export type Removed = { name: string; rows: number };

/** One field on one extension, before and after. */
export type BulkChange = { field: string; label: string; before: string; after: string };

/** One extension's worth of a bulk edit. */
export type BulkRow = {
  extension: string;
  name: string;
  changes?: BulkChange[];
  /** This extension does not exist and would be created. */
  new?: boolean;
  /** This extension would be removed. Only an undo produces these. */
  gone?: boolean;
  /** What a new extension would be called. */
  wanted?: string;
  /** Why this row will be skipped. Rows with one are never applied. */
  problem?: string;
};

export type BulkPlan = {
  rows: BulkRow[];
  changing: number;
  creating: number;
  removing: number;
  unchanged: number;
  skipped: number;
};

/**
 * One field a sheet can set, as the phone system published it.
 *
 * Azir does not know what a 3CX extension can be set to and does not keep a
 * list: the plugin publishes these, and the columns, the form and the
 * allowlist on the way back out are all built from them.
 */
export type BulkSpec = {
  field: string;
  label: string;
  /** "text", "bool" or "choice". */
  kind: string;
  group: string;
  choices?: string[];
  /**
   * What to show for each choice, where the stored value is not something to
   * put in front of a person. A role is kept as "system_owners" and read as
   * "System Owner".
   */
  labels?: Record<string, string>;
  /**
   * How each choice is doing, where that is a thing a choice can be: "up",
   * "down", or absent when the phone system does not track it. A routing
   * device that is not connected is still a choice, and still one worth
   * making deliberately.
   */
  states?: Record<string, string>;
  /**
   * No two extensions may hold the same value — an email address. Set on one
   * at a time; a bulk edit that gave one value to several would be taken by
   * the first and refused by the rest.
   */
  unique?: boolean;
  /** Belongs in a spreadsheet, not in a form. */
  sheet_only?: boolean;
};

/**
 * One button on a desk phone.
 *
 * `no` is the phone system's own numbering, not an index: a layout can start
 * at button two, because a key left at its default is not stored at all.
 */
export type BlfKey = {
  no: number;
  kind: string;
  /** What it points at. Its meaning depends on the kind. */
  id: string;
  value: string;
};

/** A kind of button the phone system offers, and what it takes. */
export type BlfKind = {
  kind: string;
  label: string;
  /** What the value means, or empty when the key carries nothing. */
  takes?: string;
  note?: string;
};

/**
 * One row of the extensions table.
 *
 * Deliberately small. A thousand-extension deployment is megabytes of JSON if
 * every field comes with every row, for a screen that shows fifty at a time —
 * the rest is what the editor asks for about one extension when it opens.
 */
export type ExtensionRow = {
  extension: string;
  name: string;
  email: string;
  enabled: boolean;
  recording: boolean;
  voicemail: boolean;
  tunnel_blocked: boolean;
  no_audio: boolean;
  /** Where they are filed, and what is on the desk. Either may be empty. */
  department: string;
  phone: string;
};

/** One extension as the phone system has it right now, field by field. */
export type BulkExtension = {
  extension: string;
  name: string;
  enabled: boolean;
  /** Every editable field, as text. Booleans read "yes" or "no". */
  values: Record<string, string>;
  /** The buttons on the desk phone, in the order they sit on it. */
  keys?: BlfKey[];
};

export type BulkEdit = {
  id: string;
  customer_id: string;
  filename: string;
  uploaded_by: string;
  sheet: { columns: string[]; rows: string[][] };
  mapping?: BulkMapping;
  plan?: BulkPlan;
  outcome?: {
    extension: string;
    ok: boolean;
    new?: boolean;
    gone?: boolean;
    /** Unticked before approving, so nothing was done to it. */
    left?: boolean;
    problem?: string;
  }[];
  status: "draft" | "planned" | "applied" | "cancelled";
  created_at: string;
  decided_at?: string;
  decided_by?: string;
};

/** Which column carries which thing. -1 means not chosen. */
export type BulkMapping = {
  extension: number;
  fields: Record<string, number>;
  /** Turns rows the phone system does not have into new extensions. */
  create?: boolean;
};

/** What the mapping screen offers. Same shape as BulkSpec. */
export type BulkField = BulkSpec;

/** One open sign-in. */
export type Session = {
  /** The token's hash. Safe to show; useless to steal. */
  id: string;
  user_id: string;
  email: string;
  started_at: string;
  expires_at: string;
  device?: string;
  ip?: string;
  /** The one making this request. */
  current: boolean;
};

export const api = {
  authState: () => request<AuthState>("/api/setup"),

  authSettings: () => request<AuthSettings>("/api/auth/settings"),

  saveAuthSettings: (settings: Partial<AuthSettings> & { client_secret?: string }) =>
    request<{ enabled: boolean }>("/api/auth/settings", {
      method: "PUT",
      body: JSON.stringify(settings),
    }),

  setup: (email: string, displayName: string, password: string) =>
    request<{ email: string }>("/api/setup", {
      method: "POST",
      body: JSON.stringify({ email, display_name: displayName, password }),
    }),

  login: (email: string, password: string) =>
    request<{ email: string }>("/api/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    }),

  logout: () => request<void>("/api/logout", { method: "POST" }),

  me: () => request<Actor>("/api/me"),

  data: () => request<DataUsage>("/api/data"),

  clearData: () =>
    request<{ removed: Removed[] }>("/api/data/clear", {
      method: "POST",
      body: JSON.stringify({ confirm: "clear" }),
    }),

  saveRetention: (keeping: Retention) =>
    request<Retention>("/api/data/retention", {
      method: "PUT",
      body: JSON.stringify(keeping),
    }),

  resetData: () =>
    request<{ removed: Removed[] }>("/api/data/reset", {
      method: "POST",
      body: JSON.stringify({ confirm: "reset" }),
    }),

  /** What the phone system says right now, to pick from. */
  extensions: (customerID: string) =>
    request<{ extensions: BulkExtension[]; fields: BulkSpec[]; columns: string[] }>(
      `/api/bulk/extensions?customer_id=${encodeURIComponent(customerID)}`,
    ),

  /**
   * Downloads the customer's extensions as the sheet to edit.
   *
   * Fetched rather than linked, because the thing most likely to go wrong is
   * that the phone system cannot be reached — and a plain link would answer
   * that by navigating the console to a page of JSON.
   */
  startingSheet: async (
    customerID: string,
    fields: string[],
    saveAs: (blob: Blob, name: string) => void,
  ) => {
    const columns = fields.length > 0 ? `&fields=${encodeURIComponent(fields.join(","))}` : "";
    const res = await fetch(
      `/api/bulk/starting-sheet?customer_id=${encodeURIComponent(customerID)}${columns}`,
    );
    if (!res.ok) {
      let message = `${res.status} ${res.statusText}`;
      try {
        const body = await res.json();
        if (body?.error) message = body.error;
      } catch {
        // Not JSON. The status line will have to do.
      }
      throw new Error(message);
    }
    const name =
      /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "")?.[1] ??
      "extensions.csv";
    saveAs(await res.blob(), name);
  },

  /**
   * Extensions ticked in the console, compared the same way a sheet is.
   *
   * `wanted` is what each extension should say, keyed by number then by field.
   * A field left out is left alone.
   */
  planChosen: (customerID: string, wanted: Record<string, Record<string, string>>) =>
    request<{ edit: BulkEdit; plan: BulkPlan }>(
      `/api/bulk/chosen?customer_id=${encodeURIComponent(customerID)}`,
      { method: "POST", body: JSON.stringify({ wanted }) },
    ),

  /**
   * A page of the extensions table, searched on the server.
   *
   * `complete` is false when the phone system had more extensions than it
   * would hand over in one sweep — which the screen has to say, because a list
   * that quietly ends is read as the whole list.
   */
  extensionPage: (customerID: string, q: string, limit: number, offset: number) =>
    request<{
      extensions: ExtensionRow[];
      total: number;
      all: number;
      fields: BulkSpec[];
      complete: boolean;
      offset: number;
    }>(
      `/api/extensions?customer_id=${encodeURIComponent(customerID)}` +
        `&q=${encodeURIComponent(q)}&limit=${limit}&offset=${offset}`,
    ),

  /**
   * Resolves a written selection — "102-110, 119" — into extensions that
   * exist.
   *
   * Parsed on the server because that is where what exists is known, and
   * because a range that quietly selects the wrong extensions is the mistake
   * this whole screen is built to prevent.
   */
  selectExtensions: (customerID: string, select: string) =>
    request<{ selected: string[]; missing: string[]; asked: string }>(
      `/api/extensions/numbers?customer_id=${encodeURIComponent(customerID)}` +
        `&select=${encodeURIComponent(select)}`,
    ),

  /** Everything about the extensions named, for the editor. */
  extensionValues: (customerID: string, extensions: string[]) =>
    request<{
      extensions: BulkExtension[];
      fields: BulkSpec[];
      missing: string[];
      kinds: BlfKind[];
    }>(
      `/api/extensions/values?customer_id=${encodeURIComponent(customerID)}` +
        `&extensions=${encodeURIComponent(extensions.join(","))}`,
    ),

  /**
   * Sets the buttons on one or more phones, or copies a layout between them.
   *
   * The whole layout goes every time: the phone system keeps it as one value,
   * so there is no changing a single button.
   */
  setExtensionKeys: (
    customerID: string,
    extensions: string[],
    layout: { keys?: BlfKey[]; from?: string },
  ) =>
    request<{ changed: number; problems: Record<string, string> }>(
      `/api/extensions/keys?customer_id=${encodeURIComponent(customerID)}`,
      { method: "POST", body: JSON.stringify({ extensions, ...layout }) },
    ),

  /** Applies a form's changes to one extension. */
  setExtension: (customerID: string, number: string, values: Record<string, string>) =>
    request<{ extension: string; changed: number }>(
      `/api/extensions/${encodeURIComponent(number)}?customer_id=${encodeURIComponent(customerID)}`,
      { method: "PATCH", body: JSON.stringify({ values }) },
    ),

  /** Makes one, then sets everything else on it. */
  createExtension: (customerID: string, number: string, values: Record<string, string>) =>
    request<{ extension: string; settings_problem?: string }>(
      `/api/extensions?customer_id=${encodeURIComponent(customerID)}`,
      { method: "POST", body: JSON.stringify({ number, values }) },
    ),

  /** Deletes the ones named, and only the ones named. */
  removeExtensions: (customerID: string, extensions: string[]) =>
    request<{ removed: number }>(
      `/api/extensions/remove?customer_id=${encodeURIComponent(customerID)}`,
      { method: "POST", body: JSON.stringify({ extensions }) },
    ),

  bulkEdits: (customerID: string) =>
    request<{ edits: BulkEdit[] }>(`/api/bulk?customer_id=${encodeURIComponent(customerID)}`),

  bulkEdit: (id: string) =>
    request<{ edit: BulkEdit; editable: BulkField[] }>(`/api/bulk/${encodeURIComponent(id)}`),

  uploadSheet: (customerID: string, file: File) => {
    const body = new FormData();
    body.append("sheet", file);
    return request<{
      edit: BulkEdit;
      columns: string[];
      suggests: BulkMapping;
      editable: BulkField[];
    }>(`/api/bulk?customer_id=${encodeURIComponent(customerID)}`, { method: "POST", body });
  },

  planSheet: (id: string, mapping: BulkMapping) =>
    request<{ edit: BulkEdit; plan: BulkPlan }>(`/api/bulk/${encodeURIComponent(id)}/plan`, {
      method: "POST",
      body: JSON.stringify(mapping),
    }),

  revertSheet: (id: string) =>
    request<{ edit: BulkEdit; plan: BulkPlan }>(`/api/bulk/${encodeURIComponent(id)}/revert`, {
      method: "POST",
    }),

  /**
   * `skip` is the extensions unticked in the before-and-after.
   *
   * `secrets` are the write-only fields — a voicemail PIN — sent now rather
   * than staged. A plan is stored, drawn, logged and kept as an undo, which is
   * four places a credential would then be at rest for a value nothing ever
   * reads back. So it is held in the form until this moment and sent once.
   */
  applySheet: (id: string, skip: string[] = [], secrets: Record<string, string> = {}) =>
    request<{
      edit: BulkEdit;
      changed: number;
      created: number;
      removed: number;
      left: number;
      failed: number;
    }>(`/api/bulk/${encodeURIComponent(id)}/apply`, {
      method: "POST",
      body: JSON.stringify({ confirm: "apply", skip, secrets }),
    }),

  cancelSheet: (id: string) =>
    request<{ edit: BulkEdit }>(`/api/bulk/${encodeURIComponent(id)}/cancel`, { method: "POST" }),

  registry: () => request<Registry>("/api/registry"),

  decide: (plugin: string, tool: string, status: Status) =>
    request<void>(`/api/capabilities/${plugin}/${tool}/decide`, {
      method: "POST",
      body: JSON.stringify({ status }),
    }),

  invoke: (plugin: string, tool: string, customerId?: string) =>
    request<unknown>(`/api/invoke/${plugin}/${tool}`, {
      method: "POST",
      body: JSON.stringify({ customer_id: customerId ?? "", args: {} }),
    }),

  settings: (plugin: string, customerId?: string) =>
    request<Settings>(
      `/api/plugins/${plugin}/settings${customerId ? `?customer_id=${customerId}` : ""}`,
    ),

  saveSettings: (plugin: string, values: Record<string, unknown>, customerId?: string) =>
    request<{ saved: number; credentials_saved: number }>(
      `/api/plugins/${plugin}/settings${customerId ? `?customer_id=${customerId}` : ""}`,
      { method: "PUT", body: JSON.stringify({ values }) },
    ),

  clearSecret: (plugin: string, field: string, customerId?: string) =>
    request<void>(
      `/api/plugins/${plugin}/settings/${field}${customerId ? `?customer_id=${customerId}` : ""}`,
      { method: "DELETE" },
    ),

  setWrites: (plugin: string, enabled: boolean) =>
    request<{ writes_enabled: boolean }>(`/api/plugins/${plugin}/writes`, {
      method: "PUT",
      body: JSON.stringify({ enabled }),
    }),

  customers: () => request<Customer[]>("/api/customers"),

  /**
   * Customers matching what somebody typed.
   *
   * A real search rather than a filter over what happens to be loaded: it runs
   * against Postgres trigram similarity, so a name half-remembered off a
   * ticket still finds the customer. An empty term is the state a picker opens
   * in and answers with the first screenful.
   */
  findCustomers: (q: string, limit = 20) =>
    request<Customer[]>(
      `/api/customers?q=${encodeURIComponent(q)}&limit=${encodeURIComponent(String(limit))}`,
    ),

  /**
   * The customers you last changed something on, most recent first.
   *
   * What a picker shows before anything is typed. Short and personal, which is
   * what the first screenful of customers by name was not.
   */
  recentCustomers: (limit = 5) =>
    request<Customer[]>(`/api/customers/recent?limit=${encodeURIComponent(String(limit))}`),

  /** One customer, for putting a name to an id somebody arrived holding. */
  customer: (id: string) => request<Customer>(`/api/customers/${encodeURIComponent(id)}`),

  createCustomer: (displayName: string) =>
    request<Customer>("/api/customers", {
      method: "POST",
      body: JSON.stringify({ display_name: displayName }),
    }),

  linkIdentity: (id: string, plugin: string, externalId: string) =>
    request<void>(`/api/customers/${id}/identities`, {
      method: "POST",
      body: JSON.stringify({ plugin, external_id: externalId }),
    }),

  /** Where a connected system should post when something changes. */
  webhook: (plugin: string) =>
    request<{ path: string; deliveries: number; rejected: number; last_seen?: string }>(
      `/api/webhooks/${plugin}`,
    ),

  rotateWebhook: (plugin: string) =>
    request<{ path: string }>(`/api/webhooks/${plugin}/rotate`, { method: "POST" }),

  users: () => request<User[]>("/api/users"),

  // Managing an account after it exists. Every rule about who may do what is
  // enforced on the server; these are only the ways of asking.
  updateUser: (id: string, patch: { role?: string; disabled?: boolean }) =>
    request<User[]>(`/api/users/${id}`, { method: "PATCH", body: JSON.stringify(patch) }),

  setUserPassword: (id: string, password: string) =>
    request<{ ok: boolean; sessions_ended: number }>(`/api/users/${id}/password`, {
      method: "POST",
      body: JSON.stringify({ password }),
    }),

  removeUser: (id: string) => request<void>(`/api/users/${id}`, { method: "DELETE" }),

  createUser: (email: string, displayName: string, role: string, password: string) =>
    request<User>("/api/users", {
      method: "POST",
      body: JSON.stringify({ email, display_name: displayName, role, password }),
    }),

  roles: () => request<{ roles: Role[]; all_permissions: string[] }>("/api/roles"),

  sessions: () => request<{ sessions: Session[] }>("/api/sessions"),

  endSession: (id: string) =>
    request<{ ended: boolean }>(`/api/sessions/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),

  createRole: (name: string, description: string, permissions: string[]) =>
    request<Role>("/api/roles", {
      method: "POST",
      body: JSON.stringify({ name, description, permissions }),
    }),

  updateRole: (name: string, description: string, permissions: string[]) =>
    request<Role>(`/api/roles/${encodeURIComponent(name)}`, {
      method: "PATCH",
      body: JSON.stringify({ description, permissions }),
    }),

  deleteRole: (name: string) =>
    request<{ removed: string }>(`/api/roles/${encodeURIComponent(name)}`, {
      method: "DELETE",
    }),

  audit: () => request<AuditEvent[]>("/api/audit"),
};

// --- the assistant -----------------------------------------------------------

export type ChatStep = {
  capability: string;
  tool?: string;
  args?: unknown;
  failed?: boolean;
  detail?: string;
};

export type ChatMessage = {
  id: string;
  role: "user" | "assistant";
  content: string;
  /** What the assistant looked up while answering. */
  steps?: ChatStep[];
  created_at: string;
};

export type Conversation = {
  id: string;
  title: string;
  /** Which model answers this chat. Empty means whatever the default is. */
  model?: string;
  subject_kind?: string;
  subject_id?: string;
  created_at: string;
  updated_at: string;
};

export type PhoneStatus = {
  address: string;
  version: string;
  healthy: boolean;
  concerns: string[];
  extensions_registered: number;
  extensions_total: number;
  trunks_registered: number;
  trunks_total: number;
  trunks_offline: string[];
  calls_in_progress: number;
  max_simultaneous_calls: number;
  free_disk_bytes: number;
  licence?: {
    product: string;
    company: string;
    active: boolean;
    support: boolean;
    expires: string;
    maintenance_expires: string;
    simultaneous_calls: number;
  };
};

export type AssistantSettings = {
  enabled: boolean;
  provider: string;
  model: string;
  base_url: string;
  max_turns: number;
  max_answer_tokens: number;
  /** Who picks the model: everyone on one, a curated list, or anything. */
  model_choice: "fixed" | "listed" | "free";
  allowed_models: string[];
  /** The instructions themselves. Empty means whatever this version ships. */
  system_prompt: string;
  /** Added after the instructions above. */
  house_prompt: string;
  updated_at: string;
  updated_by: string;
  /** Whether a key is stored — never the key itself. */
  api_key_set: boolean;
  /** What the assistant can currently look up. */
  available_lookups: string[];
  /** What Azir ships, so the page can show it and offer a way back to it. */
  shipped_prompt: string;
  /** The sentence whose removal is worth warning about. */
  injection_defence: string;
};

/** What the panel is allowed to offer, answered in one request. */
/** A change the assistant has written down and nobody has agreed to yet. */
export type Proposal = {
  id: string;
  conversation_id: string;
  proposed_for: string;
  plugin: string;
  tool: string;
  args: Record<string, unknown>;
  customer_id?: string;
  summary: string;
  status: "pending" | "applied" | "discarded" | "failed";
  outcome?: string;
  created_at: string;
  decided_at?: string;
  decided_by?: string;
};

export type AssistantStatus = {
  ready: boolean;
  default_model: string;
  model_choice: "fixed" | "listed" | "free";
  allowed_models: string[];
  may_choose: boolean;
};

export const chat = {
  /** Whether the assistant can answer at all — readable by anyone signed in. */
  status: () => request<AssistantStatus>("/api/assistant/status"),

  /**
   * What the configured service says it can run.
   *
   * Never fails: a provider that publishes no list says why instead, because
   * "we could not ask" is a fine answer and typing a name still works.
   */
  models: () => request<{ models: string[]; reason?: string }>("/api/assistant/models"),

  /** Past chats, optionally only those about one thing. */
  list: (subject?: { kind: string; id: string }) =>
    request<Conversation[]>(
      subject
        ? `/api/chats?subject_kind=${encodeURIComponent(subject.kind)}&subject_id=${encodeURIComponent(subject.id)}`
        : "/api/chats",
    ),

  start: (subject?: { kind: string; id: string }, model?: string) =>
    request<Conversation>("/api/chats", {
      method: "POST",
      body: JSON.stringify({
        subject_kind: subject?.kind ?? "",
        subject_id: subject?.id ?? "",
        model: model ?? "",
      }),
    }),

  open: (id: string) =>
    request<{ conversation: Conversation; messages: ChatMessage[] }>(`/api/chats/${id}`),

  remove: (id: string) => request<void>(`/api/chats/${id}`, { method: "DELETE" }),

  /** What this conversation has proposed changing. */
  changes: (id: string) => request<Proposal[]>(`/api/chats/${id}/changes`),

  /** Applies or discards one. Applying is the only thing that changes anything. */
  decide: (id: string, apply: boolean) =>
    request<{ status: string; result?: unknown }>(`/api/changes/${id}`, {
      method: "POST",
      body: JSON.stringify({ apply }),
    }),

  /**
   * Asks, reporting each lookup as it happens.
   *
   * Answering can take a minute. Without this the panel sits silent for that
   * whole minute and reads as broken, so progress is streamed even though the
   * text still arrives in one piece.
   */
  stream: async (
    id: string,
    message: string,
    on: {
      asked?: (m: ChatMessage) => void;
      step?: (s: ChatStep) => void;
      token?: (text: string) => void;
      /** What the model said on its way to an answer, not the answer itself. */
      narration?: (text: string) => void;
      reply?: (m: ChatMessage) => void;
      failed?: (error: string) => void;
    },
    /** Lets the caller stop a long answer. Closing the stream cancels the
        request context on the server, so the model stops being paid for. */
    signal?: AbortSignal,
  ): Promise<void> => {
    let res: Response;
    try {
      res = await fetch(`/api/chats/${id}/stream`, {
        method: "POST",
        signal,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ message }),
      });
    } catch {
      throw new Unreachable();
    }

    if (res.status === 401) {
      onExpired?.();
      throw new Unauthorized();
    }
    if (!res.ok || !res.body) {
      const body = await res.json().catch(() => ({}));
      throw new Error(body?.error ?? `${res.status} ${res.statusText}`);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      // Events are separated by a blank line; anything after the last one is a
      // partial event and stays in the buffer for the next chunk.
      const events = buffer.split("\n\n");
      buffer = events.pop() ?? "";

      for (const raw of events) {
        let name = "";
        let data = "";
        for (const line of raw.split("\n")) {
          if (line.startsWith("event:")) name = line.slice(6).trim();
          else if (line.startsWith("data:")) data += line.slice(5).trim();
        }
        if (!name || !data) continue;

        try {
          const payload = JSON.parse(data);
          if (name === "asked") on.asked?.(payload);
          else if (name === "step") on.step?.(payload);
          else if (name === "token") on.token?.(payload.text ?? "");
          else if (name === "narration") on.narration?.(payload.text ?? "");
          else if (name === "reply") on.reply?.(payload);
          else if (name === "failed") on.failed?.(payload.error ?? "The assistant could not answer.");
        } catch {
          // A malformed event is not worth failing the whole answer over.
        }
      }
    }
  },

  send: (id: string, message: string) =>
    request<{ asked: ChatMessage; reply: ChatMessage }>(`/api/chats/${id}/messages`, {
      method: "POST",
      body: JSON.stringify({ message }),
    }),

  settings: () => request<AssistantSettings>("/api/assistant/settings"),

  saveSettings: (settings: Partial<AssistantSettings> & { api_key?: string }) =>
    request<{ enabled: boolean }>("/api/assistant/settings", {
      method: "PUT",
      body: JSON.stringify(settings),
    }),
};
