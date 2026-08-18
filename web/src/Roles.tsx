import { useCallback, useEffect, useState } from "react";
import { api, type Role } from "./api";
import { Tooltip } from "./components";
import { PanelHead, Problem, roleLabel } from "./ui";

/**
 * What each role may do.
 *
 * Its own screen rather than a panel under the account list, because the two
 * are different questions asked at different times: who works here is a weekly
 * question, and what a technician is trusted with is one somebody decides once
 * and then argues about. Keeping them together meant the second was read as a
 * footnote to the first.
 *
 * Read-only for now. Roles are rows in a table and adding one is an INSERT,
 * but a half-built editor that can produce a role holding nothing useful is
 * worse than a clear view of the ones that exist.
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

export function Roles() {
  const [roles, setRoles] = useState<Role[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);

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
          <p className="mb-3 max-w-[68ch] text-xs text-ink-dim">
            Every check asks what someone is allowed to do, never what their
            role is called — so a new role with its own mix of these is a
            setting, not a rebuild.
          </p>

          {/* A grid rather than a list per role: the useful question is which
              roles hold a given ability, and that reads down a column. */}
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr>
                  <th>Can they…</th>
                  {roles.map((r) => (
                    <th key={r.name} className="w-[112px] text-center">
                      {roleLabel(r.name)}
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
                              {/* Allowed reads as a status light; not allowed
                                  reads as an absence rather than as a second
                                  kind of mark, so a column scans as "how much
                                  can this role do" without being counted. */}
                              {r.permissions.includes(p) ? (
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
        </div>
      </section>
  );
}
