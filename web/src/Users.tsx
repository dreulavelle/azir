import { useCallback, useEffect, useState } from "react";
import { api, type Actor, type Role, type User } from "./api";
import { Button, Chip, Empty, Icon, PanelHead, Picker, Problem, TextInput, absolute, ago, initials, roleLabel } from "./ui";
import { useToast } from "./Toast";
import { cn } from "@/lib/cn";

/**
 * The accounts that can sign in, and nothing else.
 *
 * What a role may do lives on its own screen. This one answers who works here
 * and which role each of them holds — the question somebody comes back to
 * every time a technician joins or leaves.
 */
export function Users({ actor }: { actor: Actor }) {
  const [users, setUsers] = useState<User[] | null>(null);
  const [roles, setRoles] = useState<Role[]>([]);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [list, roleDoc] = await Promise.all([api.users(), api.roles()]);
      setUsers(list);
      setRoles(roleDoc.roles);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not load users");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (error) return <Problem>{error}</Problem>;
  if (!users) return <div className="h-50 animate-pulse rounded-lg bg-sunken" />;

  return (
    <section className="rounded-lg border border-edge bg-panel shadow-e1">
      <PanelHead>
        <h2>Users</h2>
        <span className="text-xs text-ink-faint">
          {users.length} account{users.length === 1 ? "" : "s"}
        </span>
      </PanelHead>
      <div className="p-4 pt-0">
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th>Name</th>
              <th className="w-[170px]">Role</th>
              <th className="w-[130px]">Last seen</th>
              <th className="w-[210px]" />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <Person
                key={u.id}
                user={u}
                actor={actor}
                roles={roles}
                onChanged={(next) => setUsers(next)}
                onGone={() => void load()}
              />
            ))}
          </tbody>
        </table>

        <NewUser roles={roles} onCreated={() => void load()} />
      </div>
    </section>
  );
}

/**
 * One account, and the things that can be done to it.
 *
 * Role and enabled state change in place — a row that is also the editor,
 * rather than a modal that repeats what is already on screen. The dangerous
 * two, replacing a password and removing an account, ask first, because
 * neither can be undone from here.
 *
 * Every rule about who may do what is enforced on the server; what is hidden
 * here is only what would be pointless to offer. The last administrator cannot
 * demote or disable themselves, and the server refuses it whatever this screen
 * shows.
 */
function Person({
  user,
  actor,
  roles,
  onChanged,
  onGone,
}: {
  user: User;
  actor: Actor;
  roles: Role[];
  onChanged: (users: User[]) => void;
  onGone: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [resetting, setResetting] = useState(false);
  const [password, setPassword] = useState("");
  const [confirming, setConfirming] = useState(false);
  const toast = useToast();
  const self = user.email === actor.email;

  async function patch(change: { role?: string; disabled?: boolean }, said: string) {
    setBusy(true);
    try {
      onChanged(await api.updateUser(user.id, change));
      toast(said, { tone: "good" });
    } catch (e) {
      toast("That did not go through", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
    }
  }

  async function savePassword() {
    setBusy(true);
    try {
      const out = await api.setUserPassword(user.id, password);
      setResetting(false);
      setPassword("");
      toast(
        out.sessions_ended > 0
          ? `Password set, and ${out.sessions_ended} signed-in ${out.sessions_ended === 1 ? "session was" : "sessions were"} ended`
          : "Password set",
        { tone: "good" },
      );
    } catch (e) {
      toast("That password was not accepted", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    try {
      await api.removeUser(user.id);
      toast(`${user.display_name || user.email} was removed`, { tone: "good" });
      onGone();
    } catch (e) {
      toast("That account was not removed", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
      setBusy(false);
      setConfirming(false);
    }
  }

  return (
    <>
      <tr className="cursor-default">
        <td>
          <div className="flex items-center gap-3">
            <span className="grid size-7 shrink-0 place-items-center rounded-md bg-sunken font-mono text-2xs font-semibold text-ink-dim">
              {initials(user.display_name || user.email)}
            </span>
            <span className="flex flex-col">
              <span className={cn("font-medium", user.disabled && "text-ink-dim line-through")}>
                {user.display_name || user.email}
                {self && <span className="text-xs text-ink-faint"> — you</span>}
              </span>
              <span className="text-xs text-ink-faint">{user.email}</span>
            </span>
          </div>
        </td>
        <td>
          <div className="flex items-center gap-2">
            <Picker
              className="w-auto"
              value={user.role}
              disabled={busy}
              aria-label={`Role for ${user.email}`}
              onChange={(e) => void patch({ role: e.target.value }, `Now a ${roleLabel(e.target.value)}`)}
            >
              {roles.map((r) => (
                <option key={r.name} value={r.name}>
                  {roleLabel(r.name)}
                </option>
              ))}
            </Picker>
            {/* How someone signs in matters when working out why they cannot:
                a federated account has no password to reset. */}
            {user.provider && <Chip>sso</Chip>}
          </div>
        </td>
        <td className="text-xs text-ink-dim" title={absolute(user.last_seen_at)}>
          {user.last_seen_at ? ago(user.last_seen_at) : "never"}
        </td>
        <td>
          <div className="flex items-center justify-end gap-1">
            {!user.provider && (
              <button
                className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink disabled:opacity-40"
                disabled={busy}
                onClick={() => setResetting((v) => !v)}
              >
                Password
              </button>
            )}
            <button
              className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink disabled:opacity-40"
              disabled={busy || self}
              title={self ? "You cannot disable your own account" : undefined}
              onClick={() =>
                void patch(
                  { disabled: !user.disabled },
                  user.disabled ? "Account enabled" : "Account disabled",
                )
              }
            >
              {user.disabled ? "Enable" : "Disable"}
            </button>
            <button
              className="rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-critical/10 hover:text-critical disabled:opacity-40"
              disabled={busy || self}
              title={self ? "You cannot remove your own account" : undefined}
              onClick={() => setConfirming(true)}
            >
              Remove
            </button>
          </div>
        </td>
      </tr>

      {resetting && (
        <tr>
          <td colSpan={4} className="pb-3">
            <form
              className="flex flex-wrap items-center gap-2 rounded-md border border-edge bg-sunken/60 p-3"
              onSubmit={(e) => {
                e.preventDefault();
                void savePassword();
              }}
            >
              <span className="text-xs text-ink-dim">
                New password for {user.email}. Hand it over yourself — nothing is emailed.
              </span>
              <TextInput
                className="w-56"
                type="password"
                autoComplete="new-password"
                value={password}
                placeholder="At least 12 characters"
                onChange={(e) => setPassword(e.target.value)}
              />
              <Button weight="primary" disabled={busy || password.length < 12}>
                Set password
              </Button>
              <Button
                weight="quiet"
                type="button"
                onClick={() => {
                  setResetting(false);
                  setPassword("");
                }}
              >
                Cancel
              </Button>
              <span className="w-full text-2xs text-ink-faint">
                Every signed-in session for this account ends.
              </span>
            </form>
          </td>
        </tr>
      )}

      {confirming && (
        <tr>
          <td colSpan={4} className="pb-3">
            <div className="flex flex-wrap items-center gap-3 rounded-md border border-critical/30 bg-critical/[0.06] p-3">
              <span className="text-xs">
                Remove <strong className="font-medium">{user.email}</strong>? Their history in the
                activity log stays; the account and its sessions do not.
              </span>
              <Button
                weight="quiet"
                className="text-critical hover:bg-critical/10 hover:text-critical"
                disabled={busy}
                onClick={() => void remove()}
              >
                {busy ? "Removing…" : "Remove the account"}
              </Button>
              <Button weight="quiet" onClick={() => setConfirming(false)}>
                Keep it
              </Button>
            </div>
          </td>
        </tr>
      )}
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
      <button className="mt-4 flex h-7 items-center gap-1.5 rounded-md border border-edge bg-panel px-2.5 text-xs font-medium transition-colors hover:bg-sunken disabled:opacity-50" onClick={() => setOpen(true)}>
        <Icon.plus />
        Add a person
      </button>
    );
  }

  return (
    <form
      className="mt-5 flex max-w-[480px] flex-col gap-4 border-t border-edge pt-5"
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
              {roleLabel(r.name)}
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
