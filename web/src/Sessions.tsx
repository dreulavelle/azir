import { useCallback, useEffect, useState } from "react";
import { api, type Session } from "./api";
import { useToast } from "./Toast";
import { Button, Chip, Empty, PanelHead, Problem, ago } from "./ui";

/**
 * Where accounts are signed in.
 *
 * Every sign-in has recorded the browser and the address since the table was
 * made, and nothing had ever read either — so the first question anybody asks
 * after a laptop goes missing needed a database client to answer.
 *
 * The session making the request is marked and cannot be ended from here.
 * Signing yourself out is what the button in the corner is for, and doing it
 * from a list of hashes is how somebody does it by accident.
 */
export function Sessions() {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState("");
  const toast = useToast();

  const load = useCallback(async () => {
    try {
      setSessions((await api.sessions()).sessions);
      setProblem(null);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not read the open sessions");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function end(session: Session) {
    setBusy(session.id);
    try {
      await api.endSession(session.id);
      await load();
      toast(`Signed out ${session.email}`);
    } catch (e) {
      toast(e instanceof Error ? e.message : "Could not sign it out", { tone: "bad" });
    } finally {
      setBusy("");
    }
  }

  if (problem) return <Problem>{problem}</Problem>;
  if (!sessions) return <div className="h-32 animate-pulse rounded-lg bg-sunken" />;

  return (
    <section className="mt-4 rounded-lg border border-edge bg-panel shadow-e1">
      <PanelHead>
        <h2>Signed in</h2>
        <span className="text-xs text-ink-faint">
          {sessions.length} {sessions.length === 1 ? "session" : "sessions"}
        </span>
      </PanelHead>
      <div className="p-4 pt-0">
        {sessions.length === 0 ? (
          <Empty headline="Nobody is signed in." />
        ) : (
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th>Account</th>
                <th className="w-[230px]">Device</th>
                <th className="w-[130px]">Address</th>
                <th className="w-[110px]">Since</th>
                <th className="w-[100px]" />
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <tr key={s.id}>
                  <td>
                    <span className="font-medium">{s.email}</span>
                    {s.current && <Chip tone="accent">this one</Chip>}
                  </td>
                  <td className="text-ink-dim">{browser(s.device)}</td>
                  <td className="font-mono text-xs text-ink-dim">{s.ip || "—"}</td>
                  <td className="text-ink-dim">{ago(s.started_at)}</td>
                  <td className="text-right">
                    {!s.current && (
                      <Button
                        onClick={() => void end(s)}
                        disabled={busy === s.id}
                      >
                        {busy === s.id ? "Signing out…" : "Sign out"}
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}

/**
 * A user agent, as the thing a person would recognise.
 *
 * The full string is a paragraph of version numbers that nobody reading this
 * screen is trying to answer a question about. What they want to know is
 * whether they recognise the machine, so it is reduced to browser and system.
 */
function browser(agent?: string): string {
  if (!agent) return "—";
  const name =
    /Edg\//.test(agent) ? "Edge"
    : /OPR\//.test(agent) ? "Opera"
    : /Firefox\//.test(agent) ? "Firefox"
    : /Chrome\//.test(agent) ? "Chrome"
    : /Safari\//.test(agent) ? "Safari"
    : /curl\//i.test(agent) ? "curl"
    : "";
  const system =
    /Windows/.test(agent) ? "Windows"
    : /iPhone|iPad/.test(agent) ? "iOS"
    : /Android/.test(agent) ? "Android"
    : /Mac OS X|Macintosh/.test(agent) ? "macOS"
    : /Linux/.test(agent) ? "Linux"
    : "";
  const said = [name, system].filter(Boolean).join(" on ");
  // Anything unrecognised is shown as sent rather than as "Unknown", because
  // an odd string is exactly what somebody is looking for here.
  return said || agent.slice(0, 40);
}
