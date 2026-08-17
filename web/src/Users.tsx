import { useCallback, useEffect, useState } from "react";
import { api, Perm, type Actor, type Role, type User } from "./api";
import { Tooltip } from "./components";
import { SignOn } from "./SignOn";
import { Chip, Empty, Icon, PanelHead, Problem, absolute, ago, initials } from "./ui";

/**
 * What each permission actually lets someone do.
 *
 * The names are how the code checks them and are meant to be unambiguous, not
 * readable. An administrator deciding who gets what is answering a question in
 * their own terms — "can this person reply to customers" — so that is the
 * question the grid should be asking.
 */
const PERMISSIONS: Record<string, { label: string; group: string; note?: string }> = {
  "tool.read": {
    label: "Use Azir at all",
    group: "Everyday work",
    note: "Without this, someone can sign in and see nothing.",
  },
  "ticket.comment": { label: "Reply to tickets", group: "Everyday work" },
  "ticket.status": { label: "Change a ticket's status", group: "Everyday work" },
  "ticket.assign": { label: "Assign tickets to people", group: "Everyday work" },
  "tool.write": {
    label: "Make changes in connected systems",
    group: "Everyday work",
    note: "Still refused unless an administrator has allowed changes for that connection.",
  },

  "customer.manage": { label: "Add and edit customer records", group: "Administration" },
  "user.manage": {
    label: "Add and manage people",
    group: "Administration",
    note: "This is how someone could give themselves more access, so grant it carefully.",
  },
  "role.manage": { label: "Change what roles can do", group: "Administration" },
  "audit.read": { label: "See the activity log", group: "Administration" },

  "plugin.configure": {
    label: "Set up connections and sign-in",
    group: "Setup",
    note: "Includes storing the passwords Azir uses to reach your systems.",
  },
  "plugin.approve": { label: "Choose what Azir is allowed to do", group: "Setup" },
  "credential.manage": { label: "Manage stored passwords", group: "Setup" },
};

function permission(name: string) {
  return PERMISSIONS[name] ?? { label: name.replace(/[._]/g, " "), group: "Other" };
}

const GROUPS = ["Everyday work", "Administration", "Setup", "Other"];

/**
 * Accounts, and what each role may do.
 *
 * The role editor is deliberately read-only for now: roles are rows in a table
 * and adding one is an INSERT, but a half-built editor that can produce a role
 * holding nothing useful is worse than a clear view of the three that exist.
 */
export function Users({ actor }: { actor: Actor }) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [roles, setRoles] = useState<Role[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [list, roleDoc] = await Promise.all([api.users(), api.roles()]);
      setUsers(list);
      setRoles(roleDoc.roles);
      setPermissions(roleDoc.all_permissions);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not load users");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (error) return <Problem>{error}</Problem>;
  if (!users) return <div className="animate-pulse rounded-lg bg-sunken" style={{ height: 200 }} />;

  return (
    <>
      <section className="rounded-lg border border-edge bg-panel shadow-e1" style={{ marginBottom: 16 }}>
        <PanelHead>
          <h2>People</h2>
          <span className="text-xs text-ink-faint">
            {users.length} account{users.length === 1 ? "" : "s"}
          </span>
        </PanelHead>
        <div className="p-4" style={{ paddingTop: 0 }}>
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th>Name</th>
                <th style={{ width: 190 }}>Role</th>
                <th style={{ width: 150 }}>Last seen</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id} style={{ cursor: "default" }}>
                  <td>
                    <div className="flex items-center gap-3">
                      <span className="grid size-7 shrink-0 place-items-center rounded-md bg-sunken font-mono text-2xs font-semibold text-ink-dim">{initials(u.display_name || u.email)}</span>
                      <span className="flex flex-col gap-2" style={{ gap: 0 }}>
                        <span style={{ fontWeight: 550 }}>
                          {u.display_name || u.email}
                          {u.email === actor.email && (
                            <span className="text-xs text-ink-faint"> — you</span>
                          )}
                        </span>
                        <span className="text-xs text-ink-faint">{u.email}</span>
                      </span>
                    </div>
                  </td>
                  <td>
                    <div className="flex items-center gap-2">
                      <Chip tone="accent">{u.role}</Chip>
                      {/* How someone signs in matters when working out why they
                          cannot: a federated account has no password to reset. */}
                      {u.provider && <Chip>sso</Chip>}
                      {u.disabled && <Chip tone="urgent">disabled</Chip>}
                    </div>
                  </td>
                  <td className="text-xs text-ink-dim" title={absolute(u.last_seen_at)}>
                    {u.last_seen_at ? `${ago(u.last_seen_at)}` : "never"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>

          <NewUser roles={roles} onCreated={() => void load()} />
        </div>
      </section>

      <section className="rounded-lg border border-edge bg-panel shadow-e1" style={{ marginBottom: 16 }}>
        <PanelHead>
          <h2>Roles</h2>
        </PanelHead>
        <div className="p-4">
          <p className="text-xs text-ink-dim" style={{ margin: "0 0 12px", maxWidth: "68ch" }}>
            Every check asks what someone is allowed to do, never what their
            role is called — so a new role with its own mix of these is a
            setting, not a rebuild.
          </p>

          {/* A grid rather than a list per role: the useful question is which
              roles hold a given ability, and that reads down a column. */}
          <div style={{ overflowX: "auto" }}>
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr>
                  <th>Can they…</th>
                  {roles.map((r) => (
                    <th key={r.name} style={{ textAlign: "center", width: 112 }}>
                      {r.name}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {GROUPS.flatMap((group) => {
                  const inGroup = permissions.filter((p) => permission(p).group === group);
                  if (inGroup.length === 0) return [];
                  return [
                    <tr key={`group-${group}`} className="bg-sunken/50">
                      <td colSpan={roles.length + 1} className="font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
                        {group}
                      </td>
                    </tr>,
                    ...inGroup.map((p) => {
                      const { label, note } = permission(p);
                      return (
                        <tr key={p} style={{ cursor: "default" }}>
                          <td>
                            {note ? (
                              <Tooltip content={note}>
                                <span className="font-medium">{label}</span>
                              </Tooltip>
                            ) : (
                              label
                            )}
                          </td>
                          {roles.map((r) => (
                            <td key={r.name} className="text-center">
                              {/* Allowed reads as a status light; not allowed
                                  reads as an absence rather than as a second
                                  kind of mark, so a column scans as "how much
                                  can this role do" without being counted. */}
                              {r.permissions.includes(p) ? (
                                <span
                                  className="inline-block size-2 rounded-full bg-steady align-middle"
                                  title={`${r.name} can`}
                                />
                              ) : (
                                <span className="text-ink-faint/40" aria-label="cannot">
                                  —
                                </span>
                              )}
                            </td>
                          ))}
                        </tr>
                      );
                    }),
                  ];
                })}
              </tbody>
            </table>
          </div>
        </div>
      </section>

      {actor.permissions.includes(Perm.pluginConfigure) && <SignOn roles={roles} />}
    </>
  );
}

function NewUser({ roles, onCreated }: { roles: Role[]; onCreated: () => void }) {
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [role, setRole] = useState("technician");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  const chosen = roles.find((r) => r.name === role);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setNote(null);
    try {
      await api.createUser(email, displayName, role, password);
      setEmail("");
      setDisplayName("");
      setPassword("");
      setOpen(false);
      onCreated();
    } catch (err) {
      setNote(err instanceof Error ? err.message : "could not create the account");
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <button className="flex h-7 items-center gap-1.5 rounded-md border border-edge bg-panel px-2.5 text-xs font-medium transition-colors hover:bg-sunken disabled:opacity-50" style={{ marginTop: 16 }} onClick={() => setOpen(true)}>
        <Icon.plus />
        Add a person
      </button>
    );
  }

  return (
    <form
      className="flex flex-col gap-2"
      style={{
        gap: 15,
        maxWidth: 480,
        marginTop: 18,
        paddingTop: 18,
        borderTop: "1px solid var(--line)",
      }}
      onSubmit={(e) => void submit(e)}
    >
      <div className="flex flex-col gap-1.5">
        <label htmlFor="new-name">Name</label>
        <input
          id="new-name"
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          required
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="new-email">Email</label>
        <input
          id="new-email"
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          type="email"
          required
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="new-role">Role</label>
        <select
          id="new-role"
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          value={role}
          onChange={(e) => setRole(e.target.value)}
        >
          {roles.map((r) => (
            <option key={r.name} value={r.name}>
              {r.name}
            </option>
          ))}
        </select>
        {/* What the role grants, at the moment of granting it — the only time
            anyone is genuinely thinking about the question. */}
        {chosen && <p className="max-w-[70ch] text-xs text-ink-faint">{chosen.description}</p>}
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="new-password">Temporary password</label>
        <input
          id="new-password"
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          type="password"
          required
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <p className="max-w-[70ch] text-xs text-ink-faint">At least 12 characters.</p>
      </div>

      {note && <Problem>{note}</Problem>}

      <div className="flex items-center gap-2">
        <button type="submit" className="h-8 rounded-md bg-azir px-3.5 text-sm font-medium text-azir-ink transition-opacity hover:opacity-90 disabled:opacity-50" disabled={busy}>
          {busy ? "Creating…" : "Create account"}
        </button>
        <button type="button" className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink" onClick={() => setOpen(false)}>
          Cancel
        </button>
      </div>
    </form>
  );
}

export { Empty };
