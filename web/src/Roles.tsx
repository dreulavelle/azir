import { useCallback, useEffect, useState } from "react";
import { api, Perm, type Actor, type Role } from "./api";
import { Tooltip } from "./components";
import { useToast } from "./Toast";
import { Button, Icon, PanelHead, Problem, TextInput, roleLabel } from "./ui";

/**
 * What each role may do.
 *
 * Its own screen rather than a panel under the account list, because the two
 * are different questions asked at different times: who works here is a weekly
 * question, and what a technician is trusted with is one somebody decides once
 * and then argues about. Keeping them together meant the second was read as a
 * footnote to the first.
 *
 * Edited in the grid rather than in a form beside it. The useful question is
 * which roles hold a given ability, and that reads down a column — so the
 * column is the thing to change, and a separate editor would only repeat what
 * is already on screen.
 *
 * Admin is not editable, here or on the server. It is the way back into a
 * deployment where something else has gone wrong.
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
  "phone.manage": {
    label: "Change a customer's phone system",
    group: "Everyday work",
    note: "Separate from the above because it is a different size of mistake: commenting on the wrong ticket is embarrassing, disabling the wrong extension takes a business's phones off the air.",
  },

  "customer.manage": { label: "Add and edit customer records", group: "Administration" },
  "user.manage": {
    label: "Add and manage people",
    group: "Administration",
    note: "This is how someone could give themselves more access, so grant it carefully.",
  },
  "role.manage": { label: "Change what roles can do", group: "Administration" },
  "audit.read": { label: "See the activity log", group: "Administration" },
  "data.manage": {
    label: "See what is stored, and clear it",
    group: "Administration",
    note: "Includes starting fresh, which removes every conversation, capture and remembered ticket — and the activity log that would have said who did it.",
  },

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

export function Roles({ actor }: { actor: Actor }) {
  const [roles, setRoles] = useState<Role[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState("");
  const toast = useToast();

  const mayEdit = actor.permissions.includes(Perm.roleManage);

  const load = useCallback(async () => {
    try {
      const doc = await api.roles();
      setRoles(doc.roles);
      setPermissions(doc.all_permissions);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not load roles");
    }
  }, []);

  // Saved as it is clicked, the way a role changes on the Users screen. The
  // whole set goes each time, so a request that lands out of order settles on
  // what the grid last showed rather than on half of it.
  async function toggle(role: Role, permission: string, on: boolean) {
    const next = on
      ? [...role.permissions, permission]
      : role.permissions.filter((p) => p !== permission);
    setRoles((all) => all.map((r) => (r.name === role.name ? { ...r, permissions: next } : r)));
    setBusy(role.name);
    try {
      await api.updateRole(role.name, role.description, next);
    } catch (e) {
      toast(e instanceof Error ? e.message : "Could not save", { tone: "bad" });
      await load();
    } finally {
      setBusy("");
    }
  }

  async function remove(role: Role) {
    setBusy(role.name);
    try {
      await api.deleteRole(role.name);
      await load();
      toast(`Removed ${roleLabel(role.name)}`);
    } catch (e) {
      toast(e instanceof Error ? e.message : "Could not remove", { tone: "bad" });
    } finally {
      setBusy("");
    }
  }

  useEffect(() => {
    void load();
  }, [load]);

  if (error) return <Problem>{error}</Problem>;
  if (roles.length === 0) return <div className="h-50 animate-pulse rounded-lg bg-sunken" />;

  return (
      <section className="mb-4 rounded-lg border border-edge bg-panel shadow-e1">
        <PanelHead>
          <h2>Roles</h2>
        </PanelHead>
        <div className="p-4">
          {/* A grid rather than a list per role: the useful question is which
              roles hold a given ability, and that reads down a column. */}
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr>
                  <th>Can they…</th>
                  {roles.map((r) => (
                    <th key={r.name} className="w-[120px] text-center align-bottom">
                      <span className="block">{roleLabel(r.name)}</span>
                      {mayEdit && r.name !== "admin" && (
                        <button
                          className="mt-0.5 text-2xs font-normal normal-case tracking-normal text-ink-faint transition-colors hover:text-critical disabled:opacity-40"
                          disabled={busy === r.name}
                          onClick={() => void remove(r)}
                        >
                          Remove
                        </button>
                      )}
                      {mayEdit && r.name === "admin" && (
                        <Tooltip content="Admin is how you get back in if something else goes wrong.">
                          <span className="mt-0.5 block text-2xs font-normal normal-case tracking-normal text-ink-faint">
                            Fixed
                          </span>
                        </Tooltip>
                      )}
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
                        <tr key={p} className="cursor-default">
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
                              {mayEdit && r.name !== "admin" ? (
                                <input
                                  type="checkbox"
                                  className="size-3.5 cursor-pointer align-middle accent-azir disabled:cursor-wait disabled:opacity-50"
                                  checked={r.permissions.includes(p)}
                                  disabled={busy === r.name}
                                  aria-label={`${label} — ${roleLabel(r.name)}`}
                                  onChange={(e) => void toggle(r, p, e.target.checked)}
                                />
                              ) : /* Allowed reads as a status light; not allowed
                                    reads as an absence rather than as a second
                                    kind of mark, so a column scans as "how much
                                    can this role do" without being counted. */
                              r.permissions.includes(p) ? (
                                <span
                                  className="inline-block size-2 rounded-full bg-steady align-middle"
                                  title={`${roleLabel(r.name)} can`}
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

          {mayEdit && <NewRole open={adding} setOpen={setAdding} onMade={load} />}
        </div>
      </section>
  );
}

/** Adds a role. Name and what it is for; the abilities are ticked in the grid. */
function NewRole({
  open,
  setOpen,
  onMade,
}: {
  open: boolean;
  setOpen: (open: boolean) => void;
  onMade: () => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const toast = useToast();

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setProblem(null);
    try {
      // Nothing ticked to begin with. A role that could do everything the
      // moment it was named would be granted before it was thought about.
      const made = await api.createRole(name.trim(), description.trim(), []);
      setName("");
      setDescription("");
      setOpen(false);
      await onMade();
      toast(`Added ${roleLabel(made.name)}`, { detail: "Tick what it can do above." });
    } catch (err) {
      setProblem(err instanceof Error ? err.message : "Could not add the role");
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <button
        className="mt-4 flex h-7 items-center gap-1.5 rounded-md border border-edge bg-panel px-2.5 text-xs font-medium transition-colors hover:bg-sunken"
        onClick={() => setOpen(true)}
      >
        <Icon.plus />
        Add a role
      </button>
    );
  }

  return (
    <form className="mt-4 flex flex-wrap items-end gap-2 border-t border-edge pt-4" onSubmit={submit}>
      <div className="flex min-w-[160px] flex-col gap-1.5">
        <label htmlFor="role-name" className="text-xs text-ink-dim">
          Name
        </label>
        <TextInput
          id="role-name"
          value={name}
          autoFocus
          placeholder="Senior tech"
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className="flex min-w-[240px] flex-1 flex-col gap-1.5">
        <label htmlFor="role-what" className="text-xs text-ink-dim">
          What it is for
        </label>
        <TextInput
          id="role-what"
          value={description}
          placeholder="Everyday work, plus phones"
          onChange={(e) => setDescription(e.target.value)}
        />
      </div>
      <Button weight="primary" disabled={busy || !name.trim()}>
        {busy ? "Adding…" : "Add"}
      </Button>
      <Button type="button" onClick={() => setOpen(false)} disabled={busy}>
        Cancel
      </Button>
      {problem && (
        <div className="w-full">
          <Problem>{problem}</Problem>
        </div>
      )}
    </form>
  );
}
