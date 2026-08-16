export type Status = "pending" | "approved" | "rejected";

export type Tool = {
  plugin: string;
  name: string;
  subject: string;
  description: string;
  provides: string[] | null;
  mutates: boolean;
  status: Status;
};

export type Plugin = {
  name: string;
  version: string;
  id: string;
  description: string;
  category: string;
  sdk: string;
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body?.error) message = body.error;
    } catch {
      // A non-JSON error body is not worth reporting over the status line.
    }
    throw new Error(message);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
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

  customers: () => request<Customer[]>("/api/customers"),

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

  audit: () => request<AuditEvent[]>("/api/audit"),
};
