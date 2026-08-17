import { Mark, useBranding } from "./branding";
import { SignInScene } from "./scene";
import { useState } from "react";
import { api, type AuthState } from "./api";
import { Problem } from "./ui";

/**
 * The sign-in screen, and the first-run screen that creates the administrator.
 *
 * They share a shell because they are the same moment from the deployment's
 * point of view: nobody is signed in, and exactly one thing can be done about
 * it. Which one is offered is decided by the server, not by the browser.
 */
export function Gate({ state, onDone }: { state: AuthState; onDone: () => void }) {
  const { brand } = useBranding();
  // The identity provider reports its own refusals by sending the browser back
  // here with a reason, since there is no signed-in page to show them on.
  const ssoError = new URLSearchParams(window.location.search).get("sso_error");

  return (
    <div className="relative grid min-h-screen place-items-center overflow-hidden px-4">
      {/* The one screen with nothing to compete with, so the one screen that
          gets atmosphere. */}
      <SignInScene accent={brand.effective_accent || "#7c6bff"} />

      {/* A wash under the card, so the text sits on something solid however
          bright the field behind it happens to get. */}
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(46% 40% at 50% 45%, color-mix(in srgb, var(--ground) 78%, transparent), color-mix(in srgb, var(--ground) 24%, transparent))",
        }}
        aria-hidden="true"
      />

      <div className="relative w-full max-w-sm rounded-xl border border-edge bg-panel/95 p-7 shadow-e3 backdrop-blur">
        <div className="mb-5 flex justify-center">
          <Mark size={44} />
        </div>
        <h1 className="text-center text-xl font-semibold tracking-tight">
          {brand.effective_name}
        </h1>
        <p className="mt-1 text-center text-xs text-ink-dim">{brand.effective_tagline}</p>
        <p className="mb-5 mt-4 text-center font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
          {state.needs_setup ? "first run" : "sign in"}
        </p>

        {ssoError && <Problem>{ssoError}</Problem>}

        {state.needs_setup ? (
          <SetupForm onDone={onDone} />
        ) : (
          <>
            {state.oidc_enabled && (
              <>
                <a className="flex h-9 items-center justify-center gap-2 rounded-md border border-edge bg-sunken text-sm font-medium transition-colors hover:border-azir hover:text-azir" href="/api/auth/oidc/start">
                  Continue with {state.oidc_label}
                </a>
                <div className="my-5 flex items-center gap-3 font-mono text-2xs uppercase tracking-[0.09em] text-ink-faint before:h-px before:flex-1 before:bg-edge after:h-px after:flex-1 after:bg-edge">or sign in with a password</div>
              </>
            )}
            <LoginForm onDone={onDone} />
          </>
        )}
      </div>
    </div>
  );
}

function LoginForm({ onDone }: { onDone: () => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login(email, password);
      onDone();
    } catch (err) {
      // The server answers identically for an unknown account and a wrong
      // password; showing anything more specific here would undo that.
      setError(err instanceof Error ? err.message : "could not sign in");
      setBusy(false);
    }
  }

  return (
    <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
      <label htmlFor="login-email">Email</label>
      <input
        id="login-email"
        type="email"
        autoComplete="username"
        required
        value={email}
        onChange={(e) => setEmail(e.target.value)}
      />

      <label htmlFor="login-password">Password</label>
      <input
        id="login-password"
        type="password"
        autoComplete="current-password"
        required
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />

      {error && <Problem>{error}</Problem>}

      <button type="submit" disabled={busy}>
        {busy ? "Signing in…" : "Sign in"}
      </button>
    </form>
  );
}

function SetupForm({ onDone }: { onDone: () => void }) {
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const tooShort = password.length > 0 && password.length < 12;
  const mismatch = confirm.length > 0 && password !== confirm;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (tooShort || mismatch) return;
    setBusy(true);
    setError(null);
    try {
      await api.setup(email, displayName, password);
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : "could not complete setup");
      setBusy(false);
    }
  }

  return (
    <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
      <p className="text-xs text-ink-dim">
        This creates the administrator account, with a password. It is offered
        once. Single sign-on is set up from inside afterwards — and this account
        keeps working when the identity provider does not.
      </p>

      <label htmlFor="setup-name">Your name</label>
      <input
        id="setup-name"
        required
        autoComplete="name"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
      />

      <label htmlFor="setup-email">Email</label>
      <input
        id="setup-email"
        type="email"
        required
        autoComplete="username"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
      />

      <label htmlFor="setup-password">Password</label>
      <input
        id="setup-password"
        type="password"
        required
        autoComplete="new-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      <p className={tooShort ? "text-xs text-critical" : "text-xs text-ink-faint"}>
        At least 12 characters.
      </p>

      <label htmlFor="setup-confirm">Confirm password</label>
      <input
        id="setup-confirm"
        type="password"
        required
        autoComplete="new-password"
        value={confirm}
        onChange={(e) => setConfirm(e.target.value)}
      />
      {mismatch && <Problem>These do not match.</Problem>}

      {error && <Problem>{error}</Problem>}

      <button type="submit" disabled={busy || tooShort || mismatch}>
        {busy ? "Creating…" : "Create administrator"}
      </button>
    </form>
  );
}
