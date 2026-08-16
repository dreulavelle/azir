import { useCallback, useEffect, useState } from "react";
import { api, type Plugin, type Registry, type Settings, type Status, type Tool } from "./api";
import { SchemaForm } from "./SchemaForm";

export function Plugins() {
  const [registry, setRegistry] = useState<Registry | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setRegistry(await api.registry());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not reach core");
    }
  }, []);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), 10000);
    return () => clearInterval(id);
  }, [load]);

  async function decide(tool: Tool, status: Status) {
    await api.decide(tool.plugin, tool.name, status);
    await load();
  }

  async function invoke(tool: Tool) {
    setResult(null);
    try {
      setResult(JSON.stringify(await api.invoke(tool.plugin, tool.name), null, 2));
    } catch (e) {
      setResult(e instanceof Error ? e.message : "call failed");
    }
  }

  if (error) return <p className="error">Cannot reach core: {error}</p>;
  if (!registry) return <p className="muted">Loading…</p>;

  if (registry.plugins.length === 0) {
    return (
      <p className="muted">
        No plugins are running. Start one and it appears here within ten seconds.
      </p>
    );
  }

  return (
    <>
      {registry.plugins.map((p) => (
        <section key={p.id} className="card">
          <div className="card-head">
            <h2>{p.name}</h2>
            <span className="pill">{p.category}</span>
            <span className="muted small">
              v{p.version} · sdk {p.sdk}
            </span>
            <button
              className="ghost"
              onClick={() => setOpen(open === p.name ? null : p.name)}
            >
              {open === p.name ? "Hide settings" : "Settings"}
            </button>
          </div>
          <p className="muted small">{p.description}</p>

          {open === p.name && <PluginSettings plugin={p} />}

          <table>
            <thead>
              <tr>
                <th>Tool</th>
                <th>Provides</th>
                <th>Status</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {p.tools.map((t) => (
                <tr key={t.name}>
                  <td>
                    <strong>{t.name}</strong>
                    <div className="muted small">{t.description}</div>
                  </td>
                  <td className="mono small">{(t.provides ?? []).join(", ")}</td>
                  <td>
                    <span className={`status status-${t.status}`}>{t.status}</span>
                  </td>
                  <td className="actions">
                    {t.status === "approved" ? (
                      <>
                        <button onClick={() => void invoke(t)}>Call</button>
                        <button className="ghost" onClick={() => void decide(t, "rejected")}>
                          Revoke
                        </button>
                      </>
                    ) : (
                      <button onClick={() => void decide(t, "approved")}>Approve</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ))}

      {Object.keys(registry.capabilities).length > 0 && (
        <section className="card">
          <h2>Capabilities</h2>
          <p className="muted small">
            What this deployment can do. Features declare the capabilities they need
            and report themselves unavailable, with a reason, when nothing supplies them.
          </p>
          <ul className="caps">
            {Object.entries(registry.capabilities).map(([cap, providers]) => (
              <li key={cap}>
                <code>{cap}</code>
                <span className="muted small"> ← {providers.join(", ")}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {result && (
        <section className="card">
          <h2>Response</h2>
          <pre>{result}</pre>
        </section>
      )}
    </>
  );
}

function PluginSettings({ plugin }: { plugin: Plugin }) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setSettings(await api.settings(plugin.name));
    } catch (e) {
      setNote(e instanceof Error ? e.message : "could not load settings");
    }
  }, [plugin.name]);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(values: Record<string, unknown>) {
    setBusy(true);
    setNote(null);
    try {
      const res = await api.saveSettings(plugin.name, values);
      setNote(
        res.credentials_saved > 0
          ? `Saved. ${res.credentials_saved} credential(s) sealed into the vault.`
          : "Saved.",
      );
      await load();
    } catch (e) {
      setNote(e instanceof Error ? e.message : "could not save");
    } finally {
      setBusy(false);
    }
  }

  async function clearSecret(field: string) {
    await api.clearSecret(plugin.name, field);
    setNote(`Removed the stored value for ${field}.`);
    await load();
  }

  if (!settings) return <p className="muted small">Loading settings…</p>;

  return (
    <div className="settings-panel">
      <SchemaForm
        schema={settings.config_schema}
        values={settings.values}
        secretsSet={settings.secrets_set}
        busy={busy}
        onSave={(v) => void save(v)}
        onClearSecret={(f) => void clearSecret(f)}
      />
      {note && <p className="note-line">{note}</p>}
    </div>
  );
}
