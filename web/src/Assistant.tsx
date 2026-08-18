import { useCallback, useEffect, useRef, useState } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  chat,
  work,
  type AssistantStatus,
  type ChatMessage,
  type ChatStep,
  type Conversation,
  type Proposal,
} from "./api";
import { ArrowDown, ChevronRight, Maximize2, Minimize2, Square } from "lucide-react";
import { Tooltip } from "./components";
import { useToast } from "./Toast";
import { cn } from "@/lib/cn";
import { CopyButton, Icon, Label, ago, since } from "./ui";

/**
 * The assistant, docked beside whatever you are looking at.
 *
 * The premise of the product is that you should not have to paste a ticket into
 * a chat window — so the panel is opened *from* a ticket and already knows
 * which one. The subject travels with the conversation, which is why "what's
 * going on here" is a complete question.
 */

/** What a lookup was, in words. Capability names are not for reading aloud. */
const LOOKUP_WORDS: Record<string, string> = {
  "work_items.search": "searched tickets",
  "work_items.get": "read a ticket",
  "work_items.timeline": "read a ticket's history",
  "work_items.schema": "checked the available statuses",
  "customers.list": "searched customers",
  "customers.get": "read a customer record",
  "customers.standing": "checked a customer's balance",
  "time_entries.list": "read logged time",
  "assets.list": "listed equipment",
  "documentation.search": "searched your documentation",
  "access.check": "checked what our key can do",
  "invoices.list": "read invoices",
  "web.search": "searched the internet",
};

function lookupWords(step: ChatStep): string {
  return LOOKUP_WORDS[step.capability] ?? `used ${step.capability}`;
}

/**
 * Makes a half-arrived answer safe to render as markdown.
 *
 * Streaming cuts the text wherever the network happened to split it, which is
 * regularly in the middle of a marker: an opening `**` whose partner is still
 * in flight renders as two literal asterisks, and an unclosed fence turns the
 * rest of the answer into a code block that then unwraps itself a second later.
 * Both are the formatting flickering rather than the answer changing, so the
 * unpaired marker is dropped until its other half turns up.
 */
export function settle(text: string): string {
  let out = text;

  const odd = (pattern: RegExp) => (out.match(pattern) ?? []).length % 2 === 1;
  const dropLast = (marker: string) => {
    const at = out.lastIndexOf(marker);
    if (at >= 0) out = out.slice(0, at) + out.slice(at + marker.length);
  };

  // A fence is closed rather than dropped: the code inside it is readable, and
  // deleting the opener would reflow the whole block on the next token.
  if (odd(/^ {0,3}```/gm)) out += "\n```";
  if (odd(/\*\*/g)) dropLast("**");
  // Single markers are counted on what is left once the doubles are accounted
  // for, so `**bold**` is not read as four stray asterisks.
  if ((out.replace(/\*\*/g, "").match(/\*/g) ?? []).length % 2 === 1) dropLast("*");
  if ((out.replace(/```/g, "").match(/`/g) ?? []).length % 2 === 1) dropLast("`");

  return out;
}

export function AssistantPanel({
  subject,
  subjectLabel,
  seed,
  onSeedUsed,
  onClose,
  who = "ME",
}: {
  subject?: { kind: string; id: string };
  subjectLabel?: string;
  /** A question asked from the page rather than typed here. */
  seed?: string;
  onSeedUsed?: () => void;
  onClose: () => void;
  /** Initials for the person asking, so a thread shows both sides. */
  who?: string;
}) {
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [draft, setDraft] = useState("");
  const [thinking, setThinking] = useState(false);
  const [liveSteps, setLiveSteps] = useState<ChatStep[]>([]);
  const [liveText, setLiveText] = useState("");
  // What the model said on the way. Kept apart from the answer so three turns
  // of "let me check that" do not arrive as one run-on paragraph.
  const [narration, setNarration] = useState<string[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [ready, setReady] = useState<boolean | null>(null);
  const [policy, setPolicy] = useState<AssistantStatus | null>(null);
  // The model this chat will use. Empty means whatever the default is, which
  // is also what an administrator who has locked the choice will see.
  const [model, setModel] = useState("");
  const [history, setHistory] = useState<Conversation[]>([]);
  const [showHistory, setShowHistory] = useState(false);
  const [loading, setLoading] = useState(true);
  const [subjectName, setSubjectName] = useState<string | null>(null);
  // Full screen, for the times the conversation is the work rather than a
  // sidecar to it — reading a long drafted reply, or going back and forth on
  // something that needs more than a 400px column.
  const [expanded, setExpanded] = useState(false);
  const endRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  // Whether the reader is at the bottom. Following them down is right; yanking
  // them back while they are reading an earlier answer is not, and that is what
  // an unconditional scroll-into-view does during a long streamed reply.
  const [atBottom, setAtBottom] = useState(true);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  // Resolves once the search for an existing conversation has finished.
  // Without it, asking a question before that search returns starts a second
  // chat about the same ticket — which is how a history fragments into
  // near-duplicates that are all half the story.
  const settled = useRef<Promise<void>>(Promise.resolve());
  const resumedRef = useRef<Conversation | null>(null);
  // The last question that arrived from the page, so the same one is never
  // asked twice. An effect can run more than once for a single change — React
  // does exactly that in development to find effects that are not safe to
  // repeat — and this one spends money and appends to a conversation, so
  // repeating it is not harmless.
  const lastSeed = useRef("");
  // Lets a long answer be stopped. Closing the stream cancels the request on
  // the server, so a question asked by mistake stops costing money as soon as
  // somebody says so.
  const abort = useRef<AbortController | null>(null);
  // How long the answer in flight has been running, and what it read. Shown
  // when it lands, because "did that take forty seconds or four" is the thing
  // that decides whether the next question is worth asking.
  const startedAt = useRef(0);
  const [receipt, setReceipt] = useState<{ seconds: number; lookups: number } | null>(null);
  // Changes the assistant has written down. Nothing here has happened.
  const [changes, setChanges] = useState<Proposal[]>([]);

  const loadChanges = useCallback(async (id: string) => {
    try {
      setChanges(await chat.changes(id));
    } catch {
      // A conversation with nothing proposed is the normal case.
    }
  }, []);

  // A chat is created lazily, on the first question. Opening the panel and
  // changing your mind should not leave an empty conversation behind.
  const ensure = useCallback(async (): Promise<Conversation> => {
    await settled.current;
    if (conversation) return conversation;

    // Read through the ref rather than trusting the closure: the resume above
    // may have set it while this was waiting.
    const resumed = resumedRef.current;
    if (resumed) {
      setConversation(resumed);
      return resumed;
    }

    const started = await chat.start(subject, model);
    setConversation(started);
    resumedRef.current = started;
    setHistory((h) => [started, ...h]);
    return started;
  }, [conversation, subject, model]);

  /** Loads a past chat back into the panel. */
  const resume = useCallback(async (id: string) => {
    setShowHistory(false);
    setLoading(true);
    try {
      const { conversation: c, messages: m } = await chat.open(id);
      resumedRef.current = c;
      setConversation(c);
      setMessages(m);
      setModel(c.model ?? "");
      void loadChanges(c.id);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "That chat could not be opened.");
    } finally {
      setLoading(false);
    }
  }, []);

  function startFresh() {
    setShowHistory(false);
    setConversation(null);
    resumedRef.current = null;
    setMessages([]);
    setProblem(null);
    setReceipt(null);
    setChanges([]);
    // A new chat starts on the default rather than inheriting whatever the
    // last one happened to use, which is the honest reading of "new".
    setModel("");
    inputRef.current?.focus();
  }

  useEffect(() => {
    if (atBottom) endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [messages, thinking, liveText, atBottom]);

  // Escape comes back out. Captured rather than bubbled so it is handled here
  // before the shell's own Escape handling closes something else.
  useEffect(() => {
    if (!expanded) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        setExpanded(false);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [expanded]);

  useEffect(() => {
    inputRef.current?.focus();
    // Asked up front rather than discovered by a failed question: someone who
    // types out a paragraph and then learns nobody is listening has been
    // wasted, and it was knowable before they started.
    chat
      .status()
      .then((s) => {
        setReady(s.ready);
        setPolicy(s);
      })
      .catch(() => setReady(false));
  }, []);

  // What the panel is looking at, by its name rather than by its number. The
  // number belongs to the connected system; the person reading this remembers
  // the server room UPS.
  useEffect(() => {
    if (!subject) return;
    let cancelled = false;
    setSubjectName(null);
    void (async () => {
      try {
        const name =
          subject.kind === "ticket"
            ? (await work.getTicket(Number(subject.id))).data.subject
            : (await work.getCustomer(Number(subject.id))).data.business_name;
        if (!cancelled && name) setSubjectName(name);
      } catch {
        // Nothing connected that can answer. The fallback label stands.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [subject?.kind, subject?.id]);

  const looking = subjectName ?? subjectLabel;

  // A model this person may no longer use falls back to the default rather
  // than being sent and refused. The policy is allowed to tighten — a model
  // taken off the list stops working, correctly — but a chat resumed from
  // before that change must not carry the old choice into a new one and fail
  // with something the reader cannot act on.
  useEffect(() => {
    if (!policy || !model) return;
    const permitted =
      policy.model_choice === "free" ||
      model === policy.default_model ||
      policy.allowed_models.includes(model);
    if (!permitted) setModel("");
  }, [policy, model]);

  // Coming back to a ticket picks up where the last conversation about it left
  // off. Starting over every time would throw away the reasoning that made the
  // panel worth opening.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    resumedRef.current = null;

    const resuming = (async () => {
      try {
        const past = await chat.list(subject);
        if (cancelled) return;
        setHistory(past);
        if (subject && past.length > 0) {
          const { conversation: c, messages: m } = await chat.open(past[0].id);
          if (cancelled) return;
          resumedRef.current = c;
          setConversation(c);
          setMessages(m);
          setModel(c.model ?? "");
          void loadChanges(c.id);
        }
      } catch {
        // An unreadable history is not worth a message; the panel still works.
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    settled.current = resuming;
    return () => {
      cancelled = true;
    };
  }, [subject?.kind, subject?.id]);

  // A question asked by pressing a button on the page behind this panel. It is
  // sent rather than merely typed in: the button already said what it would do,
  // and making somebody confirm their own click is a step that buys nothing.
  //
  // Declared after the resume effect on purpose — effects run in order, and
  // sending before that one has claimed `settled` would start a second
  // conversation about a ticket that already has one.
  useEffect(() => {
    // Cleared by the parent once consumed, which also re-arms the same button
    // for a second, deliberate press.
    if (!seed) {
      lastSeed.current = "";
      return;
    }
    if (seed === lastSeed.current) return;
    lastSeed.current = seed;
    onSeedUsed?.();
    if (thinking) {
      // Mid-answer, so it waits its turn in the box rather than being lost.
      setDraft(seed);
      inputRef.current?.focus();
      return;
    }
    void send(seed);
  }, [seed]);

  /** Asks the last question again, replacing the answer that came back. */
  async function retry() {
    const lastQuestion = [...messages].reverse().find((m) => m.role === "user");
    if (!lastQuestion || thinking) return;
    setMessages((prev) => {
      const cut = prev.map((m) => m.id).lastIndexOf(lastQuestion.id);
      return cut < 0 ? prev : prev.slice(0, cut);
    });
    await send(lastQuestion.content);
  }

  function stop() {
    abort.current?.abort();
    abort.current = null;
  }

  async function send(text?: string) {
    const question = (text ?? draft).trim();
    if (!question || thinking) return;

    setDraft("");
    setProblem(null);
    setThinking(true);
    setLiveSteps([]);
    setLiveText("");
    setNarration([]);
    setReceipt(null);
    startedAt.current = Date.now();
    abort.current = new AbortController();

    // Shown immediately, so the conversation reads as a conversation rather
    // than as a form that goes quiet for ten seconds.
    const optimistic: ChatMessage = {
      id: `pending-${Date.now()}`,
      role: "user",
      content: question,
      created_at: new Date().toISOString(),
    };
    setMessages((m) => [...m, optimistic]);

    try {
      const c = await ensure();
      let failure: string | null = null;
      let answered = false;
      let partial = "";

      await chat.stream(c.id, question, {
        asked: (m) => setMessages((prev) => [...prev.filter((x) => x.id !== optimistic.id), m]),
        step: (s) => setLiveSteps((prev) => [...prev, s]),
        token: (t) => {
          partial += t;
          setLiveText((prev) => prev + t);
        },
        narration: (text) => {
          // That text was reasoning, not the answer. It moves out of the
          // answer buffer so what follows starts on a clean line.
          setNarration((prev) => [...prev, text]);
          setLiveText("");
          partial = "";
        },
        reply: (m) => {
          // The live bubble and the saved message hold the same text, so the
          // live one is retired in the same update the real one arrives in —
          // otherwise the answer renders twice for a frame, caret and all.
          answered = true;
          setMessages((prev) => [...prev, m]);
          setLiveText("");
          setLiveSteps([]);
          setNarration([]);
          setReceipt({
            seconds: Math.max(1, Math.round((Date.now() - startedAt.current) / 1000)),
            lookups: m.steps?.length ?? 0,
          });
          // An answer may have staged a change. Read through the conversation
          // ensure() returned rather than through state: when a chat is created
          // by the first question, setConversation has not landed yet and the
          // closure still sees null — which silently skipped the load and left
          // a staged change with nothing on screen to approve it.
          void loadChanges(c.id);
          setThinking(false);
        },
        failed: (error) => (failure = error),
      }, abort.current.signal);

      if (failure) throw new Error(failure);

      // The stream closed without ever delivering an answer. Silence here
      // would look identical to success, so say what happened — and keep any
      // partial text, which is often still worth reading.
      if (!answered) {
        setProblem(
          partial
            ? "The answer stopped partway. What arrived is below; ask again for the rest."
            : "The answer did not come through. Try asking again.",
        );
        if (partial) {
          setMessages((prev) => [
            ...prev,
            {
              id: `partial-${Date.now()}`,
              role: "assistant",
              content: partial,
              created_at: new Date().toISOString(),
            },
          ]);
        }
        setLiveText("");
      }
    } catch (e) {
      // Stopping is not a failure, and saying so would be a lie.
      if (e instanceof DOMException && e.name === "AbortError") {
        setMessages((m) => m.filter((x) => x.id !== optimistic.id));
        setDraft(question);
        return;
      }
      const message = e instanceof Error ? e.message : "The assistant could not answer.";
      if (/not switched on|no API key/i.test(message)) setReady(false);
      else setProblem(message);
      // The question stays on screen so it can be retried without retyping.
      setDraft(question);
      setMessages((m) => m.filter((x) => x.id !== optimistic.id));
    } finally {
      abort.current = null;
      setThinking(false);
      setLiveSteps([]);
      setLiveText("");
      setNarration([]);
    }
  }

  return (
    <aside
      className={cn(
        "flex flex-col bg-panel",
        expanded
          ? "fixed inset-0 z-50 h-screen w-screen"
          : "sticky top-[49px] h-[calc(100vh-49px)] w-[460px] shrink-0 border-l border-edge",
      )}
      aria-label="Assistant"
    >
      <header className={cn("flex items-center gap-1.5 border-b border-edge px-3.5 py-2.5", expanded && "px-5 py-3")}>
        <span className="grid size-7 shrink-0 place-items-center rounded-md bg-azir/15 text-azir" aria-hidden="true">
          <Icon.spark />
        </span>
        <div className="flex min-w-0 flex-1 flex-col">
          <strong className="text-sm font-semibold leading-tight">Assistant</strong>
          {looking && (
            <span className="truncate text-2xs text-ink-faint" title={looking}>
              {looking}
            </span>
          )}
        </div>
        {history.length > 0 && (
          <Tooltip content="Earlier chats">
            <button
              className="flex h-7 items-center gap-1 rounded-md px-1.5 font-mono text-2xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={() => setShowHistory((v) => !v)}
              aria-label="Earlier chats"
              aria-expanded={showHistory}
            >
              <Icon.clock />
              {history.length}
            </button>
          </Tooltip>
        )}
        {messages.length > 0 && (
          <Tooltip content="Start a new chat">
            <button className="grid size-7 place-items-center rounded-md text-ink-dim transition-colors hover:bg-sunken hover:text-ink" onClick={startFresh} aria-label="Start a new chat">
              <Icon.plus />
            </button>
          </Tooltip>
        )}
        {/* Only shown when there is a decision to make. A control that is
            always disabled is a control that should not be drawn. */}
        {policy?.may_choose && (
          <ModelPicker
            policy={policy}
            value={model}
            locked={messages.length > 0}
            onChange={setModel}
          />
        )}

        {/* The way out of full screen is a labelled button, not a glyph.
            Full screen covers the page somebody was working on, so the control
            that gives it back is the most important one in the header — and an
            icon the size of the close cross is not something a reader finds
            when they want it. Docked, the same control is a quiet glyph,
            because there is nothing to escape from. */}
        {expanded ? (
          <button
            className="flex h-8 items-center gap-2 rounded-md border border-edge bg-panel px-3 text-sm font-medium transition-colors hover:border-azir hover:text-azir"
            onClick={() => setExpanded(false)}
            aria-label="Back to the side panel"
            aria-pressed={true}
          >
            <Minimize2 className="size-3.5" />
            {subject?.kind === "ticket"
              ? "Back to the ticket"
              : subject?.kind === "customer"
                ? "Back to the customer"
                : "Exit full screen"}
            <kbd className="rounded border border-edge bg-sunken px-1 font-mono text-2xs text-ink-faint">
              Esc
            </kbd>
          </button>
        ) : (
          <Tooltip content="Fill the screen">
            <button
              className="grid size-7 place-items-center rounded-md text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={() => setExpanded(true)}
              aria-label="Fill the screen"
              aria-pressed={false}
            >
              <Maximize2 className="size-3.5" />
            </button>
          </Tooltip>
        )}
        <Tooltip content="Close the assistant">
          <button className="grid size-7 place-items-center rounded-md text-ink-dim transition-colors hover:bg-sunken hover:text-ink" onClick={onClose} aria-label="Close the assistant">
            ×
          </button>
        </Tooltip>
      </header>

      <div className={cn("flex min-h-0 flex-1", expanded && "flex-row")}>
      {expanded && (
        <div className="flex w-64 shrink-0 flex-col border-r border-edge bg-sunken/30">
          <div className="flex items-center justify-between gap-2 px-4 py-3">
            <Label>Chats</Label>
            <button
              className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
              onClick={startFresh}
            >
              <Icon.plus />
              New
            </button>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
            {history.length === 0 && (
              <p className="px-2 text-xs text-ink-faint">Nothing yet.</p>
            )}
            {history.map((c) => (
              <button
                key={c.id}
                className={cn(
                  "flex w-full min-w-0 flex-col gap-0.5 rounded-md border-l-2 px-2 py-2 text-left transition-colors",
                  c.id === conversation?.id
                    ? "border-azir bg-panel text-ink"
                    : "border-transparent text-ink-dim hover:bg-panel/60",
                )}
                onClick={() => void resume(c.id)}
              >
                <span className="w-full truncate text-xs font-medium">
                  {c.title || "Untitled chat"}
                </span>
                <Label>{since(c.updated_at)}</Label>
              </button>
            ))}
          </div>
        </div>
      )}

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="relative flex min-h-0 flex-1 flex-col">
      {showHistory && !expanded && (
        <div className="max-h-56 overflow-y-auto border-b border-edge bg-sunken/50">
          <div className="px-3.5 pb-1 pt-2.5 font-mono text-2xs font-medium uppercase tracking-[0.09em] text-ink-faint">
            {looking ? `Earlier chats about ${looking}` : "Earlier chats"}
          </div>
          {history.map((c) => (
            <button
              key={c.id}
              className={cn("flex w-full items-center gap-2 px-3.5 py-1.5 text-left text-xs transition-colors hover:bg-sunken", c.id === conversation?.id ? "text-azir" : "text-ink-dim")}
              onClick={() => void resume(c.id)}
            >
              <span className="flex-1 truncate">{c.title || "Untitled chat"}</span>
              <Label>{since(c.updated_at)}</Label>
            </button>
          ))}
        </div>
      )}

      <div
        ref={scrollRef}
        className={cn("relative flex flex-1 flex-col gap-3 overflow-y-auto", expanded ? "px-5 py-6" : "p-3.5")}
        onScroll={(e) => {
          const el = e.currentTarget;
          setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80);
        }}
      >
        <div className={cn("flex flex-col gap-6", expanded && "mx-auto w-full max-w-[1000px]")}>
        {loading ? (
          <div className="flex flex-col gap-2">
            <div className="h-9 w-[72%] animate-pulse rounded-xl bg-sunken" />
            <div className="h-15 w-[58%] animate-pulse self-end rounded-xl bg-sunken" />
          </div>
        ) : ready === false ? (
          <div className="flex flex-col gap-1.5 py-6"><div className="flex flex-col gap-1.5">
            <p className="text-sm font-semibold">The assistant is not set up yet</p>
            <p className="text-xs text-ink-dim">
              An admin can switch it on in Settings and add a provider key.
            </p>
          </div></div>
        ) : messages.length === 0 ? (
          <div className={cn("flex flex-col gap-1.5", expanded ? "py-16" : "py-6")}>
            <div className="flex flex-col gap-1.5">
            {expanded && (
              <span className="mb-2 grid size-11 place-items-center rounded-xl bg-azir/12 text-azir">
                <Icon.spark />
              </span>
            )}
            <p className="text-sm font-semibold">
              {looking ? `Ask about ${looking}` : "Ask about a ticket or a customer"}
            </p>
            <p className="text-xs text-ink-dim">
              Reads your tickets, customers and equipment to answer. Can draft
              a reply, never send one.
            </p>
            <div className={cn("mt-2 gap-1.5", expanded ? "grid grid-cols-2" : "flex flex-col")}>
              {(looking
                ? [
                    "What's actually going on here?",
                    "What should I check next?",
                    "Draft a reply to the customer",
                  ]
                : [
                    "Which tickets have gone quiet?",
                    "What's outstanding for Northwind Dental?",
                    "How do we usually handle a failed backup?",
                  ]
              ).map((s) => (
                <button
                  key={s}
                  className="rounded-md border border-transparent bg-sunken px-3 py-2 text-left text-xs text-ink-dim transition-colors hover:border-azir hover:text-azir"
                  onClick={() => {
                    setDraft(s);
                    inputRef.current?.focus();
                  }}
                >
                  {s}
                </button>
              ))}
            </div>
          </div></div>
        ) : (
          messages.map((m, i) => (
            <Message
              key={m.id}
              message={m}
              who={who}
              // Only the last answer can be retried: regenerating one in the
              // middle would leave everything after it answering a question
              // that no longer has that answer in front of it.
              onRetry={
                m.role === "assistant" && i === messages.length - 1 && !thinking
                  ? () => void retry()
                  : undefined
              }
            />
          ))
        )}

        {thinking && (
          <div className="flex items-start gap-3">
            <span className="mt-1 grid size-7 shrink-0 place-items-center rounded-md bg-azir/15 text-azir">
              <Icon.spark />
            </span>
            <div className="flex min-w-0 flex-1 flex-col gap-2">
            <span className="font-mono text-2xs font-medium uppercase tracking-[0.09em] text-azir">
              Azir
            </span>
            {narration.map((line, i) => (
              <p key={i} className="text-xs italic text-ink-faint">
                {line}
              </p>
            ))}
            {liveSteps.length > 0 && <Steps steps={liveSteps} />}
            {liveText ? (
              <div className="prose-azir text-sm">
                <Markdown remarkPlugins={[remarkGfm]} components={markdownParts}>
                  {settle(liveText)}
                </Markdown>
                <span className="ml-0.5 inline-block h-3.5 w-px animate-pulse bg-azir align-middle" aria-hidden="true" />
              </div>
            ) : (
              <div className="flex gap-1 py-1" aria-label="The assistant is working">
                <span className="size-1.5 animate-bounce rounded-full bg-azir/60 [animation-delay:-0.3s]" />
                <span className="size-1.5 animate-bounce rounded-full bg-azir/60 [animation-delay:-0.15s]" />
                <span className="size-1.5 animate-bounce rounded-full bg-azir/60" />
              </div>
            )}
            </div>
          </div>
        )}

        {/* What the assistant wants to change, and the only place it can
            happen. Rendered after the answer because it is the answer's
            consequence, and a technician should read the reasoning first. */}
        {changes
          .filter((c) => c.status === "pending")
          .map((c) => (
            <ChangeCard
              key={c.id}
              change={c}
              onDecided={() => conversation && void loadChanges(conversation.id)}
            />
          ))}

        {receipt && !thinking && messages.length > 0 && (
          <div className="flex items-center gap-2 pl-10">
            <Label className="opacity-70">
              answered in {receipt.seconds}s
              {receipt.lookups > 0
                ? ` · read ${receipt.lookups} ${receipt.lookups === 1 ? "thing" : "things"}`
                : " · from what was already on screen"}
            </Label>
          </div>
        )}

        {problem && (
          <p className="rounded-lg border border-critical/30 bg-critical/10 px-3 py-2 text-xs text-critical">
            {problem}
          </p>
        )}

        <div ref={endRef} />
        </div>
      </div>

      {!atBottom && (
        <button
          className="absolute bottom-3 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-edge bg-raised px-3 py-1.5 text-xs shadow-e3 transition-colors hover:border-azir hover:text-azir"
          onClick={() => {
            setAtBottom(true);
            endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
          }}
        >
          <ArrowDown className="size-3" />
          Latest
        </button>
      )}
      </div>

      <form
        className={cn("flex flex-col gap-2 border-t border-edge", expanded ? "px-5 py-4" : "p-3")}
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
      >
        <div className={cn("flex flex-col gap-2", expanded && "mx-auto w-full max-w-[1000px]")}>
        <textarea
          ref={inputRef}
          className="w-full resize-none rounded-md border border-edge bg-sunken px-3 py-2 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          rows={expanded ? 4 : 2}
          value={draft}
          disabled={ready === false}
          placeholder="Ask a question…"
          aria-label="Ask the assistant"
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends, Shift+Enter starts a line. A chat where Enter makes
            // a newline is a chat nobody sends anything in.
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <div className="flex items-center justify-between gap-3">
          <span className="text-2xs text-ink-faint">
            It cannot change anything — only read and draft.
          </span>
          {thinking ? (
            <button
              type="button"
              className="flex h-7 items-center gap-1.5 rounded-md border border-edge px-3 text-xs font-medium text-ink-dim transition-colors hover:border-critical hover:text-critical"
              onClick={stop}
            >
              <Square className="size-2.5 fill-current" />
              Stop
            </button>
          ) : (
            <button
              type="submit"
              className="h-7 rounded-md bg-azir px-3 text-xs font-medium text-azir-ink transition-opacity hover:opacity-90 disabled:opacity-40"
              disabled={!draft.trim() || ready === false}
            >
              Ask
            </button>
          )}
        </div>
        </div>
      </form>
      </div>
      </div>
    </aside>
  );
}

/**
 * A change the assistant proposed, and the button that makes it real.
 *
 * This card is the entire difference between an assistant that drafts and one
 * that acts. Everything above it — reading the ticket, working out the change,
 * writing the arguments — happened without touching anything. Pressing Apply is
 * the first moment a connected system hears about it, and it happens under the
 * name of whoever pressed it, not the assistant's.
 *
 * The arguments are shown in full rather than summarised. A person approving a
 * change to somebody's phone system should see exactly what they are agreeing
 * to, and "create ten extensions" hides the ten.
 */
function ChangeCard({ change, onDecided }: { change: Proposal; onDecided: () => void }) {
  const [busy, setBusy] = useState<"apply" | "discard" | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const toast = useToast();

  async function decide(apply: boolean) {
    setBusy(apply ? "apply" : "discard");
    setFailed(null);
    try {
      await chat.decide(change.id, apply);
      toast(apply ? "Change applied" : "Change discarded", {
        tone: apply ? "good" : undefined,
        detail: apply ? change.summary : undefined,
      });
      onDecided();
    } catch (e) {
      setFailed(e instanceof Error ? e.message : "That did not go through.");
    } finally {
      setBusy(null);
    }
  }

  return (
    // A change waiting for approval is the one thing in this panel that must
    // not be scrolled past, so it is the one thing that announces itself.
    <div className="arriving rounded-lg border border-attention/40 bg-attention/[0.05]">
      <div className="flex items-center gap-2 border-b border-attention/25 px-3.5 py-2">
        <Label className="text-attention">Waiting for you</Label>
        <Label className="opacity-70">
          {change.plugin} · {change.tool}
        </Label>
      </div>

      <div className="px-3.5 py-3">
        <p className="text-sm font-medium">{change.summary}</p>
        <pre className="mt-2 overflow-x-auto rounded-md border border-edge bg-panel p-2.5 font-mono text-2xs">
          {JSON.stringify(change.args, null, 2)}
        </pre>

        {failed && (
          <p className="mt-2 rounded-md border border-critical/30 bg-critical/10 px-2.5 py-1.5 text-xs text-critical">
            {failed}
          </p>
        )}

        <div className="mt-3 flex items-center gap-2">
          <button
            className="h-8 rounded-md bg-attention px-3.5 text-sm font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
            disabled={busy !== null}
            onClick={() => void decide(true)}
          >
            {busy === "apply" ? "Applying…" : "Apply this change"}
          </button>
          <button
            className="h-8 rounded-md px-3 text-sm text-ink-dim transition-colors hover:bg-sunken hover:text-ink disabled:opacity-50"
            disabled={busy !== null}
            onClick={() => void decide(false)}
          >
            Discard
          </button>
          <span className="ml-auto text-2xs text-ink-faint">Nothing has changed yet</span>
        </div>
      </div>
    </div>
  );
}

/**
 * Which model answers.
 *
 * Locked once the conversation has started, because changing model mid-thread
 * would silently rewrite what the earlier answers meant — the history is
 * replayed to whichever model is current, and a different model reading another
 * model\'s reasoning is a subtly different conversation. Starting a new chat is
 * one click away and is the honest way to switch.
 */
function ModelPicker({
  policy,
  value,
  locked,
  onChange,
}: {
  policy: AssistantStatus;
  value: string;
  locked: boolean;
  onChange: (next: string) => void;
}) {
  const shown = value || policy.default_model;
  const options = [policy.default_model, ...policy.allowed_models].filter(
    (m, i, all) => m && all.indexOf(m) === i,
  );

  if (locked) {
    return (
      <Tooltip content="Start a new chat to use a different model.">
        <span className="max-w-32 truncate font-mono text-2xs text-ink-faint">{shown}</span>
      </Tooltip>
    );
  }

  // "free" means any name the service accepts, which is a text field rather
  // than a list — there is no list to have.
  if (policy.model_choice === "free") {
    return (
      <Tooltip content="Any model name your service accepts.">
        <input
          className="h-7 w-32 rounded-md border border-edge bg-sunken px-2 font-mono text-2xs placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none"
          placeholder={policy.default_model || "model"}
          value={value}
          aria-label="Model"
          onChange={(e) => onChange(e.target.value.trim())}
        />
      </Tooltip>
    );
  }

  return (
    <Tooltip content="Which model answers this chat.">
      <select
        className="h-7 max-w-36 rounded-md border border-edge bg-sunken px-1.5 font-mono text-2xs focus-visible:border-azir focus-visible:outline-none"
        value={shown}
        aria-label="Model"
        onChange={(e) => onChange(e.target.value)}
      >
        {options.map((m) => (
          <option key={m} value={m}>
            {m}
            {m === policy.default_model ? " (default)" : ""}
          </option>
        ))}
      </select>
    </Tooltip>
  );
}

/**
 * One message, flanked by whoever sent it.
 *
 * The avatar sits on the side the message came from, so a long thread can be
 * skimmed down either gutter without reading a word — which is the thing side
 * alignment was doing before, without the bubble that cost the width a drafted
 * reply and a markdown table both need.
 *
 * The assistant gets the full column. A question is short and a technician
 * wrote it; an answer is long and is the reason the panel is open.
 */
function Message({
  message,
  who,
  onRetry,
}: {
  message: ChatMessage;
  who: string;
  onRetry?: () => void;
}) {
  const mine = message.role === "user";
  const pending = message.id.startsWith("pending-");

  if (mine) {
    return (
      <div className="arriving flex items-start justify-end gap-3">
        <div className="flex min-w-0 flex-col items-end gap-1">
          <Label>{pending ? "sending…" : ago(message.created_at)}</Label>
          <div className="max-w-[46rem] whitespace-pre-wrap break-words rounded-lg rounded-tr-sm border border-edge bg-sunken px-3.5 py-2.5 text-sm">
            {message.content}
          </div>
        </div>
        <span className="mt-5 grid size-7 shrink-0 place-items-center rounded-md bg-sunken font-mono text-[9px] font-semibold text-ink-dim">
          {who}
        </span>
      </div>
    );
  }

  return (
    // Rises into place rather than fading, so the direction itself says the
    // message is new. Keyed on the message id, so a re-render during streaming
    // does not restart it under the words being read.
    <div className="arriving flex items-start gap-3">
      <span className="mt-5 grid size-7 shrink-0 place-items-center rounded-md bg-azir/15 text-azir">
        <Icon.spark />
      </span>

      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex items-center gap-2">
          <span className="font-mono text-2xs font-medium uppercase tracking-[0.09em] text-azir">
            Azir
          </span>
          {!pending && <Label className="opacity-70">{ago(message.created_at)}</Label>}
        </div>

        {message.steps && message.steps.length > 0 && <Evidence steps={message.steps} />}

        <div className="prose-azir text-sm">
          <Markdown remarkPlugins={[remarkGfm]} components={markdownParts}>
            {message.content}
          </Markdown>
        </div>

        {!pending && (
          <div className="flex items-center gap-1 pt-0.5">
            <CopyButton text={message.content} label="Copy" className="-ml-2" />
            {onRetry && (
              <button
                className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
                onClick={onRetry}
              >
                <Icon.refresh />
                Try again
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * What the answer was based on.
 *
 * Folded by default and openable, rather than a wall of chips or a dump of the
 * model\'s reasoning. The claim a technician needs to check is "did it actually
 * read my ticket, or is it guessing" — so this shows what was asked for and
 * whether it came back, and stops there.
 */
function Evidence({ steps }: { steps: ChatStep[] }) {
  const [open, setOpen] = useState(false);
  const failed = steps.filter((s) => s.failed).length;

  return (
    <div className="rounded-lg border border-edge bg-sunken/40">
      <button
        className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <ChevronRight
          className={cn("size-3 shrink-0 text-ink-faint transition-transform", open && "rotate-90")}
          aria-hidden="true"
        />
        <Label>
          Read {steps.length} {steps.length === 1 ? "thing" : "things"}
        </Label>
        {failed > 0 && <Label className="text-critical">{failed} unavailable</Label>}
        {!open && (
          <span className="ml-auto flex min-w-0 gap-1 overflow-hidden">
            {steps.slice(0, 3).map((s, i) => (
              <span key={i} className="truncate font-mono text-2xs text-ink-faint">
                {lookupWords(s)}
                {i < Math.min(steps.length, 3) - 1 ? " ·" : ""}
              </span>
            ))}
          </span>
        )}
      </button>

      {open && (
        <ul className="border-t border-edge px-2.5 py-1.5">
          {steps.map((s, i) => (
            <li key={i} className="flex items-baseline gap-2 py-1">
              <span
                className={cn(
                  "size-1.5 shrink-0 translate-y-1 rounded-full",
                  s.failed ? "bg-critical" : "bg-steady",
                )}
              />
              <span className="text-xs">{lookupWords(s)}</span>
              {s.failed && s.detail && (
                <span className="truncate text-2xs text-critical">{s.detail}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** What the assistant read, in words. */
function Steps({ steps }: { steps: ChatStep[] }) {
  return (
    <div className="flex flex-wrap gap-1">
      {steps.map((s, i) => (
        <span
          key={i}
          className={cn(
            "rounded border px-1.5 py-px font-mono text-2xs",
            s.failed ? "border-critical/30 text-critical" : "border-edge text-ink-faint",
          )}
        >
          {s.failed ? `couldn't ${lookupWords(s)}` : lookupWords(s)}
        </span>
      ))}
    </div>
  );
}

/**
 * How markdown pieces are rendered.
 *
 * A fenced block is treated as the deliverable rather than as decoration,
 * because in this product it usually is one: the assistant cannot send anything
 * itself, so the drafted reply it produces has to be carried out of here by
 * hand, and the whole job of this screen is to make that one step short.
 *
 * So a block gets a header, a name for what it is, and a copy control that is
 * always visible — hover-to-reveal is a discoverability problem on the single
 * most-used control in the panel.
 */
const markdownParts = {
  pre: ({ children }: { children?: React.ReactNode }) => {
    const text = plainText(children);
    return <Artifact text={text}>{children}</Artifact>;
  },
};

/**
 * What a block of text is, from what is in it.
 *
 * A guess, and a cheap one — but the alternative is calling a drafted customer
 * email "code", which is what it looked like before and is the reason the
 * copy button felt like an afterthought.
 */
function describeBlock(text: string): { label: string; reply: boolean } {
  const t = text.trim();
  const looksLikeAMessage =
    /^(hi|hello|dear|good (morning|afternoon|evening))\b/i.test(t) ||
    /\b(kind regards|best regards|many thanks|thanks,|regards,)\b/i.test(t);
  if (looksLikeAMessage) return { label: "Drafted reply", reply: true };
  if (/^\s*(\$|>|PS[ C]|sudo |Get-|Set-|New-|Import-|#!)/m.test(t)) {
    return { label: "Commands", reply: false };
  }
  return { label: "Draft", reply: false };
}

function Artifact({ text, children }: { text: string; children?: React.ReactNode }) {
  const { label, reply } = describeBlock(text);
  const words = text.trim().split(/\s+/).filter(Boolean).length;

  return (
    <div
      className={cn(
        "overflow-hidden rounded-lg border",
        reply ? "border-azir/30 bg-azir/[0.04]" : "border-edge bg-panel",
      )}
    >
      <div
        className={cn(
          "flex items-center gap-2 border-b px-3 py-1.5",
          reply ? "border-azir/20" : "border-edge",
        )}
      >
        <Label className={reply ? "text-azir" : undefined}>{label}</Label>
        <Label className="opacity-60">{words} words</Label>
        <div className="ml-auto flex items-center gap-1">
          {reply && (
            <span className="hidden text-2xs text-ink-faint sm:inline">
              Azir cannot send this
            </span>
          )}
          <CopyButton text={text} label="Copy" className="-mr-1" />
        </div>
      </div>
      <pre>{children}</pre>
    </div>
  );
}

/** Pulls the text out of a rendered node tree, for copying. */
function plainText(node: React.ReactNode): string {
  if (node === null || node === undefined || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(plainText).join("");
  if (typeof node === "object" && "props" in node) {
    return plainText((node as { props?: { children?: React.ReactNode } }).props?.children);
  }
  return "";
}
