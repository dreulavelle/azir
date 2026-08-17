import { useCallback, useEffect, useState } from "react";
import { chat, work, type Conversation } from "../api";
import { Tooltip } from "../components";
import { useToast } from "../Toast";
import type { Route } from "../router";
import { Empty, Icon, Label, Loading, Problem, absolute, ago } from "../ui";

/**
 * Every conversation, not only the one about what is on screen.
 *
 * The panel beside a ticket resumes that ticket's chat, which is right when you
 * are working. This is the other half: the reasoning you did last week, findable
 * when you no longer remember which ticket it was attached to.
 */
/**
 * How many subjects are worth naming.
 *
 * Each name is a lookup, and a long history would otherwise fire one per row on
 * a page nobody scrolls to the bottom of. The rest keep their number, which is
 * still enough to open the right thing.
 */
const NAME_LIMIT = 30;

/**
 * Turns "ticket #115781949" into what the ticket is actually called.
 *
 * The number is the connected system's, not anything a technician recognises —
 * they remember the server room UPS, not the eight digits Syncro filed it
 * under. Looked up rather than stored so old chats gain the name too, and so a
 * ticket that has since been renamed reads the way it reads everywhere else.
 */
function useSubjectNames(chats: Conversation[] | null): Record<string, string> {
  const [names, setNames] = useState<Record<string, string>>({});

  useEffect(() => {
    if (!chats) return;
    let cancelled = false;

    const wanted = new Map<string, Conversation>();
    for (const c of chats) {
      if (!c.subject_id || !c.subject_kind) continue;
      const key = `${c.subject_kind}:${c.subject_id}`;
      if (!wanted.has(key)) wanted.set(key, c);
      if (wanted.size >= NAME_LIMIT) break;
    }

    void (async () => {
      for (const [key, c] of wanted) {
        if (cancelled) return;
        try {
          const name =
            c.subject_kind === "ticket"
              ? (await work.getTicket(Number(c.subject_id))).data.subject
              : (await work.getCustomer(Number(c.subject_id))).data.business_name;
          // A blank name is not an improvement on the number it would replace.
          if (!cancelled && name) setNames((prev) => ({ ...prev, [key]: name }));
        } catch {
          // Deleted, or nothing connected that can answer. The number stands.
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [chats]);

  return names;
}

/** What a chat was about, in the words the rest of the product uses. */
function aboutWhat(c: Conversation, names: Record<string, string>): string {
  if (!c.subject_kind || !c.subject_id) return "Not about anything in particular";
  const name = names[`${c.subject_kind}:${c.subject_id}`];
  if (c.subject_kind === "ticket") return name ? `About ${name}` : `About ticket #${c.subject_id}`;
  if (c.subject_kind === "customer") return name ? `About ${name}` : "About a customer";
  return "Not about anything in particular";
}

export function Chats({ go }: { go: (to: Route) => void }) {
  const [chats, setChats] = useState<Conversation[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const names = useSubjectNames(chats);
  const toast = useToast();

  const load = useCallback(async () => {
    try {
      setChats(await chat.list());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Your chats could not be loaded.");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function remove(c: Conversation) {
    // Optimistic: a deleted chat should leave the list at once, and a failure
    // puts it back with an explanation.
    setChats((prev) => (prev ?? []).filter((x) => x.id !== c.id));
    try {
      await chat.remove(c.id);
      toast("Chat deleted");
    } catch (e) {
      await load();
      toast("Could not delete that chat", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    }
  }

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="flex items-baseline justify-between gap-4">
        <h1 className="text-2xl font-semibold tracking-tight">Your chats</h1>
        {chats && chats.length > 0 && (
          <Label>
            {chats.length} {chats.length === 1 ? "conversation" : "conversations"}
          </Label>
        )}
      </div>
      <p className="mb-6 mt-1 max-w-[62ch] text-sm text-ink-dim">
        Only you can see these. Open the assistant beside a ticket and it picks
        up where the last conversation about that ticket left off.
      </p>

      {error && <Problem>{error}</Problem>}
      {!chats && !error && <Loading rows={5} />}

      {chats && chats.length === 0 && (
        <Empty headline="You have not asked anything yet">
          Open a ticket and press{" "}
          <kbd className="rounded border border-edge bg-sunken px-1 font-mono text-2xs">A</kbd>{" "}
          to ask the assistant about it.
        </Empty>
      )}

      {chats && chats.length > 0 && (
        <div className="flex flex-col">
          {chats.map((c) => (
            <div key={c.id} className="group flex items-center border-b border-edge/60 last:border-b-0">
              <button
                className="flex min-w-0 flex-1 items-center gap-3 py-3 pr-2 text-left"
                onClick={() => {
                  // A chat about nothing in particular has nowhere to open to;
                  // the row is still there to be read and deleted.
                  if (!c.subject_id) return;
                  if (c.subject_kind === "ticket") go({ name: "ticket", id: c.subject_id });
                  else if (c.subject_kind === "customer") go({ name: "customer", id: c.subject_id });
                }}
              >
                <span className="grid size-7 shrink-0 place-items-center rounded-md bg-azir/12 text-azir" aria-hidden="true">
                  <Icon.spark />
                </span>
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-sm font-medium">
                    {c.title || "Untitled chat"}
                  </span>
                  <span className="truncate text-xs text-ink-faint">{aboutWhat(c, names)}</span>
                </span>
                <Label title={absolute(c.updated_at)}>{ago(c.updated_at)}</Label>
              </button>

              <Tooltip content="Delete this chat">
                <button
                  className="mr-1 grid size-7 shrink-0 place-items-center rounded-md text-ink-faint opacity-0 transition-all hover:bg-critical/10 hover:text-critical focus-visible:opacity-100 group-hover:opacity-100"
                  onClick={() => void remove(c)}
                  aria-label={`Delete ${c.title || "this chat"}`}
                >
                  ×
                </button>
              </Tooltip>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
