import { useCallback, useEffect, useState } from "react";
import { api, type AuthSettings, type Role } from "./api";
import { Button, PanelHead, Problem, TextInput } from "./ui";

/**
 * Single sign-on settings.
 *
 * Entra is what this was built against, so the form asks for a tenant and
 * derives the issuer rather than asking an administrator to paste a URL they
 * would have to go and look up. Another OpenID Connect provider is the same
 * form with the issuer filled in directly.
 */
/** Splits the domains field into the list the server stores. */
function domainList(text: string): string[] {
  return text
    .split(",")
    .map((d) => d.trim())
    .filter(Boolean);
}

export function SignOn({ roles }: { roles: Role[] }) {
  const [settings, setSettings] = useState<AuthSettings | null>(null);
  const [draft, setDraft] = useState<Partial<AuthSettings> & { client_secret?: string }>({});
  // The domains field holds what was typed, not what it parses to.
  //
  // It used to be derived from the parsed list on every keystroke, which meant
  // typing a comma produced an empty final entry, the empty entry was filtered
  // out, and the comma disappeared from under the cursor — making it literally
  // impossible to enter a second domain. Text stays text until it is saved.
  const [domains, setDomains] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const loaded = await api.authSettings();
      setSettings(loaded);
      setDraft({
        tenant_id: loaded.tenant_id,
        issuer: loaded.issuer,
        client_id: loaded.client_id,
        allowed_domains: loaded.allowed_domains,
        auto_provision: loaded.auto_provision,
        default_role: loaded.default_role || "viewer",
        redirect_url: loaded.redirect_url,
        client_secret: "",
      });
      setDomains((loaded.allowed_domains ?? []).join(", "));
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not load sign-on settings");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(enabled: boolean) {
    setBusy(true);
    setNote(null);
    setError(null);
    try {
      await api.saveAuthSettings({ ...draft, allowed_domains: domainList(domains), enabled });
      setNote(
        enabled
          ? "Saved and verified against the provider. Single sign-on is on."
          : "Saved. Single sign-on is off.",
      );
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not save");
    } finally {
      setBusy(false);
    }
  }

  if (error && !settings) return <Problem>{error}</Problem>;
  if (!settings) return <div className="h-40 animate-pulse rounded-lg bg-sunken" />;

  const set = (patch: Partial<AuthSettings> & { client_secret?: string }) =>
    setDraft((d) => ({ ...d, ...patch }));

  return (
    <section className="rounded-lg border border-edge bg-panel shadow-e1">
      <PanelHead>
        <h2>Single sign-on</h2>
        <span
          className={
            settings.enabled
              ? "inline-flex items-center rounded border border-steady/25 bg-steady/10 px-1.5 py-px text-2xs font-medium text-steady"
              : "inline-flex items-center rounded border border-edge bg-sunken px-1.5 py-px text-2xs font-medium text-ink-dim"
          }
        >
          {settings.enabled ? "on" : "off"}
        </span>
      </PanelHead>

      <div className="p-4">
      <p className="max-w-[70ch] text-xs text-ink-faint">
        The provider says who someone is. Azir still decides what they may do —
        a role is set here, not read from a directory group, so a change over
        there cannot quietly turn a technician into an administrator here.
      </p>

      <div className="flex flex-col gap-2">
        <div className="flex flex-col gap-1.5">
          <label htmlFor="sso-callback">Redirect URI</label>
          <p className="max-w-[70ch] text-xs text-ink-faint">
            Register this exactly, in the app registration's Web platform. The
            provider refuses anything that does not match character for character.
          </p>
          <TextInput id="sso-callback" readOnly value={settings.callback_url} onFocus={(e) => e.target.select()} />
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="sso-tenant">Directory (tenant) ID</label>
          <p className="max-w-[70ch] text-xs text-ink-faint">
            From the app registration's Overview page. Naming the tenant is what
            keeps sign-in to your directory instead of to every Microsoft account
            there is.
          </p>
          <TextInput
            id="sso-tenant"
            value={draft.tenant_id ?? ""}
            placeholder="00000000-0000-0000-0000-000000000000"
            onChange={(e) => set({ tenant_id: e.target.value })}
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="sso-client">Application (client) ID</label>
          <TextInput
            id="sso-client"
            value={draft.client_id ?? ""}
            onChange={(e) => set({ client_id: e.target.value })}
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="sso-secret">
            Client secret
            {settings.client_secret_set && <span className="inline-flex items-center rounded border border-edge bg-sunken px-1.5 py-px text-2xs font-medium text-ink-dim">stored encrypted</span>}
          </label>
          <p className="max-w-[70ch] text-xs text-ink-faint">
            Certificates &amp; secrets → New client secret. Copy the Value, not
            the Secret ID.
          </p>
          <TextInput
            id="sso-secret"
            type="password"
            autoComplete="new-password"
            placeholder={settings.client_secret_set ? "•••••••• stored — type to replace" : ""}
            value={draft.client_secret ?? ""}
            onChange={(e) => set({ client_secret: e.target.value })}
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="sso-domains">Allowed email domains</label>
          <p className="max-w-[70ch] text-xs text-ink-faint">
            Comma separated. Leave empty to allow anyone the provider
            authenticates, which is only reasonable for a directory you control.
          </p>
          <TextInput
            id="sso-domains"
            value={domains}
            placeholder="cooli.ai, example.com"
            onChange={(e) => setDomains(e.target.value)}
          />
          {/* What it parses to, so the effect of the punctuation is visible
              without having to save and reload to find out. */}
          {domainList(domains).length > 0 && (
            <p className="flex flex-wrap gap-1.5 text-2xs">
              {domainList(domains).map((d) => (
                <span
                  key={d}
                  className="inline-flex items-center rounded border border-edge bg-sunken px-1.5 py-px font-mono text-ink-dim"
                >
                  {d}
                </span>
              ))}
            </p>
          )}
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="sso-provision">Create accounts on first sign-in</label>
          <p className="max-w-[70ch] text-xs text-ink-faint">
            Off means someone must already have an account here. On means anyone
            the provider authenticates — within the domains above — gets one.
          </p>
          <input
            id="sso-provision"
            className="size-4"
            type="checkbox"
            checked={draft.auto_provision === true}
            onChange={(e) => set({ auto_provision: e.target.checked })}
          />
        </div>

        {draft.auto_provision && (
          <div className="flex flex-col gap-1.5">
            <label htmlFor="sso-role">Role for new accounts</label>
            <select
              className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
              id="sso-role"
              value={draft.default_role ?? "viewer"}
              onChange={(e) => set({ default_role: e.target.value })}
            >
              {roles.map((r) => (
                <option key={r.name} value={r.name}>
                  {r.name}
                </option>
              ))}
            </select>
            <p className="max-w-[70ch] text-xs text-ink-faint">
              {roles.find((r) => r.name === draft.default_role)?.description ?? ""}
            </p>
          </div>
        )}

        {error && <Problem>{error}</Problem>}
        {note && <p className="rounded-lg border border-steady/30 bg-steady/10 px-3 py-2 text-sm text-steady">{note}</p>}

        <div className="flex items-center gap-2">
          {/* Enabling verifies against the provider before it saves, so a
              typo fails here rather than at the moment someone tries to
              sign in with it. */}
          <Button weight="primary" disabled={busy} onClick={() => void save(true)}>
            {busy ? "Checking…" : settings.enabled ? "Save and re-check" : "Verify and turn on"}
          </Button>
          {settings.enabled && (
            <Button weight="quiet" disabled={busy} onClick={() => void save(false)}>
              Turn off
            </Button>
          )}
        </div>
      </div>

      </div>
      {settings.updated_by && (
        <p className="text-xs text-ink-faint">
          Last changed by {settings.updated_by} on{" "}
          {new Date(settings.updated_at).toLocaleString()}.
        </p>
      )}
    </section>
  );
}
