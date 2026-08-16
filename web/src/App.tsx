import { useCallback, useEffect, useState } from "react";

type Tool = {
  plugin: string;
  name: string;
  subject: string;
  description: string;
  provides: string[] | null;
  mutates: boolean;
};

type Plugin = {
  name: string;
  version: string;
  id: string;
  description: string;
  category: string;
  sdk: string;
  tools: Tool[];
};

type Snapshot = {
  plugins: Plugin[];
  capabilities: Record<string, string[]>;
  at: string;
};

export function App() {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await fetch("/api/registry");
      if (!res.ok) throw new Error(`registry returned ${res.status}`);
      setSnapshot(await res.json());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not reach core");
    }
  }, []);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), 5000);
    return () => clearInterval(id);
  }, [load]);

  async function invoke(tool: Tool) {
    setResult(null);
    try {
      const res = await fetch(`/api/invoke/${tool.plugin}/${tool.name}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ customer_id: "cust_demo", args: {} }),
      });
      setResult(JSON.stringify(await res.json(), null, 2));
    } catch (e) {
      setResult(e instanceof Error ? e.message : "call failed");
    }
  }

  const capabilities = Object.entries(snapshot?.capabilities ?? {});

  return (
    <main>
      <header>
        <h1>Azir</h1>
        <p className="tagline">Phase 0 — plugin discovery over NATS</p>
      </header>

      {error && <p className="error">Cannot reach core: {error}</p>}

      {!snapshot && !error && <p className="muted">Loading registry…</p>}

      {snapshot && snapshot.plugins.length === 0 && (
        <p className="muted">
          No plugins discovered. Start one and it appears here within five seconds.
        </p>
      )}

      {snapshot?.plugins.map((p) => (
        <section key={p.id} className="plugin">
          <div className="plugin-head">
            <h2>{p.name}</h2>
            <span className="pill">{p.category}</span>
            <span className="muted">
              v{p.version} · sdk {p.sdk}
            </span>
          </div>
          <p className="muted">{p.description}</p>

          <table>
            <thead>
              <tr>
                <th>Tool</th>
                <th>Provides</th>
                <th>Subject</th>
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
                  <td className="mono small">{t.subject}</td>
                  <td>
                    <button onClick={() => void invoke(t)}>Call</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ))}

      {capabilities.length > 0 && (
        <section>
          <h2>Capabilities</h2>
          <p className="muted small">
            What this deployment can do. Features declare the capabilities they
            need and report themselves unavailable, with a reason, when nothing
            supplies them.
          </p>
          <ul className="caps">
            {capabilities.map(([cap, providers]) => (
              <li key={cap}>
                <code>{cap}</code>
                <span className="muted small"> ← {providers.join(", ")}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {result && (
        <section>
          <h2>Response</h2>
          <pre>{result}</pre>
        </section>
      )}
    </main>
  );
}
