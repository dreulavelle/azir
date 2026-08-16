import { useEffect, useState } from "react";
import { api, type AuditEvent } from "./api";

export function Audit() {
  const [events, setEvents] = useState<AuditEvent[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const load = async () => {
      try {
        setEvents(await api.audit());
      } catch (e) {
        setError(e instanceof Error ? e.message : "could not load audit log");
      }
    };
    void load();
    const id = setInterval(() => void load(), 10000);
    return () => clearInterval(id);
  }, []);

  if (error) return <p className="error">{error}</p>;

  return (
    <section className="card">
      <h2>Audit</h2>
      <p className="muted small">
        Every tool call, credential resolution and administrative action.
        Identifiers and outcomes only — never payloads.
      </p>
      <table>
        <thead>
          <tr>
            <th>When</th>
            <th>Actor</th>
            <th>Action</th>
            <th>Target</th>
            <th>Outcome</th>
          </tr>
        </thead>
        <tbody>
          {(events ?? []).map((e, i) => (
            <tr key={`${e.occurred_at}-${i}`}>
              <td className="mono small">
                {new Date(e.occurred_at).toLocaleString()}
              </td>
              <td className="mono small">{e.actor_user_id}</td>
              <td className="small">{e.action}</td>
              <td className="mono small">
                {[e.plugin, e.tool].filter(Boolean).join(".") || "—"}
                {e.detail && <div className="muted">{e.detail}</div>}
              </td>
              <td>
                <span className={`status status-${e.outcome === "ok" ? "approved" : "rejected"}`}>
                  {e.outcome}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {events && events.length === 0 && <p className="muted small">Nothing recorded yet.</p>}
    </section>
  );
}
