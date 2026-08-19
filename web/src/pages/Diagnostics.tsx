import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  Perm,
  snapshots,
  type Actor,
  type Finding,
  type Snapshot,
  type SnapshotReport,
} from "../api";
import { Trend } from "../charts";
import type { Route } from "../router";
import { useToast } from "../Toast";
import { cn } from "@/lib/cn";
import {
  Button,
  Empty,
  Icon,
  Label,
  Loading,
  Panel,
  Problem,
  ago,
  absolute,
  until,
  TextInput,
} from "../ui";
import { CustomerSearch } from "../CustomerSearch";

/**
 * Diagnostics: what a phone system looked like at one moment.
 *
 * A 3CX support bundle is what an engineer asks for when a problem has resisted
 * everything else — forty megabytes, four hundred files, and no indication
 * which nine of them matter. This screen is those nine, read.
 *
 * The findings lead, because they are the answer. Everything else on the page
 * is there to be checked against: the facts say what machine this was, the
 * charts say what it had been doing, and each finding carries the log lines it
 * was read from so nobody has to take the software's word for it.
 */

const TONE: Record<string, { text: string; wash: string; rail: string; word: string }> = {
  critical: {
    text: "text-critical",
    wash: "border-critical/30 bg-critical/[0.06]",
    rail: "bg-critical",
    word: "breaking now",
  },
  warning: {
    text: "text-attention",
    wash: "border-attention/30 bg-attention/[0.05]",
    rail: "bg-attention",
    word: "worth acting on",
  },
  note: {
    text: "text-ink-dim",
    wash: "border-edge bg-sunken/50",
    rail: "bg-edge-strong",
    word: "context",
  },
};

export function Diagnostics({
  openId,
  go,
  actor,
}: {
  openId?: string;
  go: (to: Route) => void;
  actor: Actor;
}) {
  const [list, setList] = useState<Snapshot[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setList(await snapshots.list());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Snapshots could not be loaded.");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // The report has its own address, so a technician who finds the thing that
  // explains a fault can send somebody the link to it rather than the zip.
  if (openId) {
    return <Report id={openId} actor={actor} onBack={() => go({ name: "diagnostics" })} />;
  }

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="mb-1 flex items-start justify-between gap-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Diagnostics</h1>
          <p className="mt-1 max-w-[62ch] text-sm text-ink-dim">
            Upload a 3CX support bundle, or collect one from a customer's phone
            system. Azir reads it for what explains the fault — silent calls,
            packet loss, a trunk that keeps dropping — so you don't have to
            unzip forty megabytes.
          </p>
        </div>
      </div>

      <Collect onDone={() => void load()} />

      {error && <Problem>{error}</Problem>}
      {!list && !error && <Loading rows={4} />}

      {list && list.length === 0 && (
        <Empty headline="No captures yet">
          Collect one above, or in 3CX go to Admin → Support → collect support
          info and upload the zip.
        </Empty>
      )}

      {list && list.length > 0 && (
        <div className="mt-6 flex flex-col">
          <Label className="mb-2 block">Captures</Label>
          {list.map((s) => {
            const tone = TONE[s.worst ?? "note"] ?? TONE.note;
            return (
              <button
                key={s.id}
                className="relative flex items-center gap-4 border-b border-edge/60 py-3 pl-4 pr-2 text-left transition-colors last:border-b-0 hover:bg-sunken/70"
                onClick={() => go({ name: "snapshot", id: s.id })}
              >
                <span
                  className={cn("absolute inset-y-2 left-0 w-[3px] rounded-full", tone.rail)}
                  aria-hidden="true"
                />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">
                    {s.filename || "support bundle"}
                  </span>
                  <span className="mt-0.5 flex flex-wrap items-center gap-x-3 text-xs text-ink-faint">
                    <span title={absolute(s.captured_at ?? s.uploaded_at)}>
                      captured {ago(s.captured_at ?? s.uploaded_at)}
                    </span>
                    {s.fqdn && <span className="font-mono text-2xs">{s.fqdn}</span>}
                    {/* Not attached to anybody is worth saying on the list,
                        because it is the one thing here somebody can fix in a
                        click and would otherwise never think to look for. */}
                    {!s.customer_id && <span className="text-attention">· not attached</span>}
                    {!s.expires_at && <span>· kept</span>}
                    {s.uploaded_by && <span>· by {s.uploaded_by}</span>}
                  </span>
                </span>
                <span className={cn("shrink-0 font-mono text-2xs tabular-nums", tone.text)}>
                  {s.findings} {s.findings === 1 ? "finding" : "findings"}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/**
 * Getting a capture in, by either route.
 *
 * Uploading no longer asks whose phone system it is. A bundle carries the
 * FQDN of the system it came off, which is the same address an administrator
 * typed into that customer's 3CX settings, so Azir matches it — and one that
 * matches nothing is still read and can be attached afterwards. Making
 * somebody pick from a list first was a step that could only be got wrong.
 *
 * Pulling asks the phone system to build one and reads it where it lands. It
 * takes as long as 3CX takes to walk its own logs and zip them, which on a
 * large site is minutes rather than seconds — so the button says so rather
 * than leaving somebody watching a spinner wondering whether it has hung.
 */
function Collect({ onDone }: { onDone: () => void }) {
  const [customerId, setCustomerId] = useState("");
  const [busy, setBusy] = useState<"upload" | "pull" | null>(null);
  // Assumed ready until the registry says otherwise, so a slow load never
  // makes a working button look broken.
  const [collecting, setCollecting] = useState<"ready" | "pending" | "absent">("ready");
  const picker = useRef<HTMLInputElement>(null);
  const toast = useToast();

  // The picker below searches Azir's own customers, not the helpdesk's. A
  // snapshot hangs off the customer record here, which is keyed by Azir's id —
  // and the helpdesk's list is keyed by the helpdesk's. Reading the wrong one
  // puts a number where a uuid belongs and every upload is refused by a
  // customer that does exist.
  useEffect(() => {
    /*
      Whether collecting is switched on at all.

      Every tool is discovered before it is allowed, and between those two
      moments the button is present and refused. Finding that out by pressing
      it — and being told the phone system could not be read — sends somebody
      to look at a PBX that is perfectly well. Better to say so on the face of
      the control, before it is worth pressing.
    */
    api
      .registry()
      .then((reg) => {
        const collects = reg.plugins
          .flatMap((p) => p.tools)
          .filter((tool) => tool.provides?.includes("phone_system.capture"));
        setCollecting(
          collects.length === 0
            ? "absent"
            : collects.some((tool) => tool.status === "approved")
              ? "ready"
              : "pending",
        );
      })
      .catch(() => setCollecting("ready"));
  }, []);

  function announce(findings: Finding[], prefix: string) {
    const bad = findings.filter((f) => f.severity === "critical").length;
    toast(
      bad > 0
        ? `${prefix} ${bad} thing${bad === 1 ? "" : "s"} breaking calls right now.`
        : `${prefix} ${findings.length} findings.`,
      { tone: bad > 0 ? "bad" : "good" },
    );
  }

  async function send(file: File) {
    setBusy("upload");
    try {
      const { snapshot, report } = await snapshots.upload(file, customerId || undefined);
      announce(report.findings, snapshot.customer_id ? "Read." : "Read, but not matched to a customer.");
      onDone();
    } catch (e) {
      toast("That bundle could not be read", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(null);
      if (picker.current) picker.current.value = "";
    }
  }

  async function pull() {
    if (!customerId) return;
    setBusy("pull");
    try {
      const result = await snapshots.pull(customerId);
      if (result.empty || !result.report) {
        toast("Nothing to report", { tone: "good", detail: result.note });
      } else {
        announce(result.report.findings, "Collected.");
      }
      onDone();
    } catch (e) {
      // An unapproved capability is not a broken phone system, and telling
      // somebody it is sends them to look at the PBX for a problem that is in
      // Azir's own settings. The server distinguishes them; so should this.
      const reason = e instanceof Error ? e.message : "";
      const waiting = reason.includes("approved first");
      toast(waiting ? "Not approved yet" : "That phone system could not be read", {
        tone: waiting ? "info" : "bad",
        detail: reason || undefined,
      });
    } finally {
      setBusy(null);
    }
  }

  return (
    <Panel className="mt-5 flex flex-wrap items-center gap-3 p-3.5">
      <input
        ref={picker}
        type="file"
        accept=".zip"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) void send(file);
        }}
      />
      <Button weight="primary" disabled={busy !== null} onClick={() => picker.current?.click()}>
        {busy === "upload" ? "Reading…" : "Upload a support bundle"}
      </Button>

      <span className="text-xs text-ink-faint">or</span>

      <div className="w-[240px]">
        <CustomerSearch
          value={customerId}
          ariaLabel="Which customer's phone system to collect from"
          placeholder="Type a customer's name"
          onChange={(id) => setCustomerId(id)}
        />
      </div>
      <Button
        disabled={busy !== null || !customerId || collecting !== "ready"}
        onClick={() => void pull()}
      >
        {busy === "pull" ? "Collecting… this can take minutes" : "Collect from their PBX"}
      </Button>
      {collecting === "pending" && (
        <span className="text-xs text-attention">
          Needs approving in Settings → Capabilities first.
        </span>
      )}

      <span className="w-full text-xs text-ink-faint sm:w-auto">
        The bundle is read and discarded either way. Findings expire on their
        own unless you keep one.
      </span>
    </Panel>
  );
}


/**
 * A section that is worth a heading but not worth a panel each.
 *
 * There are now eight of these on a full report, and the page reads as a stack
 * of equals rather than a wall only if they all announce themselves the same
 * way.
 */
function Section({
  title,
  aside,
  children,
}: {
  title: string;
  aside?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="mt-7">
      <div className="mb-2.5 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <Label>{title}</Label>
        {aside}
      </div>
      {children}
    </section>
  );
}

/** A small run of counts, for codecs, protocols and the like. */
function Tally({ items }: { items?: { name: string; count: number }[] }) {
  if (!items?.length) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {items.map((c) => (
        <span
          key={c.name}
          className="inline-flex items-baseline gap-1.5 rounded border border-edge bg-sunken px-2 py-0.5"
        >
          <span className="text-2xs text-ink-dim">{c.name}</span>
          <span className="font-mono text-2xs tabular-nums text-ink-faint">{c.count}</span>
        </span>
      ))}
    </div>
  );
}

/**
 * What callers actually heard.
 *
 * The most direct evidence on the page: 3CX scores every call it can measure,
 * both ways, and files the result in the event log. MOS is the number people
 * know — 4.4 is a good landline, below 3.6 is where somebody says the line was
 * breaking up.
 *
 * The unmeasured count is deliberately as prominent as the scores. A system
 * that cannot score its own calls is usually one where RTCP is not getting
 * back, which is the same fault that produces silent calls.
 */
function CallQuality({ report }: { report: SnapshotReport }) {
  const q = report.quality;
  if (!q || q.calls === 0) return null;

  const legs = q.calls * 2;
  const unrated = Math.max(legs - q.rated_legs, 0);

  return (
    <Section title="Call quality" aside={<Label>{q.calls} calls scored</Label>}>
      <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-edge bg-edge sm:grid-cols-4">
        {[
          { value: q.median_mos ? q.median_mos.toFixed(2) : "—", label: "median MOS" },
          { value: q.worst_mos ? q.worst_mos.toFixed(2) : "—", label: "worst MOS" },
          { value: `${q.jitter_ms ?? 0} ms`, label: "median jitter" },
          { value: `${q.loss_percent ?? 0}%`, label: "median loss" },
        ].map((c) => (
          <div key={c.label} className="bg-panel px-4 py-3">
            <dd className="font-mono text-xl font-medium tabular-nums tracking-tight">{c.value}</dd>
            <dt className="mt-0.5 font-mono text-2xs uppercase tracking-[0.09em] text-ink-faint">
              {c.label}
            </dt>
          </div>
        ))}
      </dl>

      <p className="mt-2 text-xs text-ink-faint">
        {q.rated_legs} of {legs} call legs could be scored
        {unrated > 0 && ` · ${unrated} too short to measure, or with no RTCP coming back`}
        {q.poor > 0 && ` · ${q.poor} below 3.6`}
      </p>

      <div className="mt-3 flex flex-wrap gap-x-6 gap-y-2">
        <Tally items={q.codecs} />
        <Tally items={q.endpoints} />
      </div>

      {q.worst && q.worst.length > 0 && (
        <div className="mt-3 overflow-x-auto rounded-lg border border-edge">
          <table className="w-full min-w-[640px] border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-edge bg-sunken/60">
                {["Call", "MOS", "Jitter", "Loss", "Endpoint", "Why"].map((h) => (
                  <th key={h} className="px-3 py-2 font-mono text-2xs uppercase tracking-[0.09em] text-ink-faint">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {q.worst.map((c, i) => (
                <tr key={i} className="border-b border-edge/60 last:border-b-0">
                  <td className="px-3 py-2">{c.number || "—"}</td>
                  <td className="px-3 py-2 font-mono tabular-nums text-critical">{c.mos?.toFixed(2)}</td>
                  <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{c.jitter_ms ?? 0} ms</td>
                  <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{c.loss_percent ?? 0}%</td>
                  <td className="px-3 py-2 text-ink-dim">{c.endpoint || "—"}</td>
                  <td className="max-w-[28ch] truncate px-3 py-2 text-ink-faint" title={c.reason}>
                    {c.reason || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  );
}

/**
 * The packet capture: the only thing here that cannot argue.
 *
 * Every other section is the phone system's account of itself. This is what
 * went down the wire, and for "we answered and there was no sound" it is the
 * evidence that settles whose problem it is.
 */
function Wire({ report }: { report: SnapshotReport }) {
  const c = report.capture;
  const [open, setOpen] = useState(false);
  if (!c) return null;

  const streams = c.streams ?? [];
  const shown = open ? streams : streams.slice(0, 8);

  return (
    <Section
      title="On the wire"
      aside={
        <Label>
          {c.packets.toLocaleString()} packets over {Math.round(c.seconds ?? 0)}s
          {c.truncated && " · truncated"}
        </Label>
      }
    >
      <div className="flex flex-wrap gap-x-6 gap-y-2">
        <Tally items={c.protocols} />
        <Tally items={c.sip_methods} />
      </div>

      {streams.length > 0 ? (
        <div className="mt-3 overflow-x-auto rounded-lg border border-edge">
          <table className="w-full min-w-[620px] border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-edge bg-sunken/60">
                {["Audio from", "to", "Codec", "Packets", "Lost", "Jitter"].map((h) => (
                  <th key={h} className="px-3 py-2 font-mono text-2xs uppercase tracking-[0.09em] text-ink-faint">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {shown.map((s) => (
                <tr key={`${s.from}-${s.to}-${s.ssrc}`} className="border-b border-edge/60 last:border-b-0">
                  <td className="px-3 py-2 font-mono text-2xs">{s.from}</td>
                  <td className="px-3 py-2 font-mono text-2xs">{s.to}</td>
                  <td className="px-3 py-2 text-ink-dim">{s.codec || "—"}</td>
                  <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{s.packets}</td>
                  <td
                    className={cn(
                      "px-3 py-2 font-mono tabular-nums",
                      (s.loss_percent ?? 0) >= 1 ? "text-critical" : "text-ink-faint",
                    )}
                  >
                    {s.lost ? `${s.lost} (${s.loss_percent}%)` : "—"}
                  </td>
                  <td className="px-3 py-2 font-mono tabular-nums text-ink-dim">{s.jitter_ms ?? 0} ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="mt-3 max-w-[70ch] text-xs text-ink-faint">
          No audio was flowing while this capture ran. That is not a fault on its
          own — the capture is seconds long and may simply have missed the calls.
        </p>
      )}
      {streams.length > 8 && (
        <button
          className="mt-2 text-xs text-ink-faint underline-offset-4 transition-colors hover:text-ink hover:underline"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? "Show fewer" : `Show all ${streams.length} streams`}
        </button>
      )}
    </Section>
  );
}

/**
 * The event log, grouped.
 *
 * Fifty thousand rows collapse into perhaps thirty kinds of event, and the
 * count against each is what says which one is the fault. 3CX's own severity is
 * shown beside ours only where they disagree — it files successful backups as
 * errors, and the reinterpretation should be visible rather than silent.
 */
function Events({ report }: { report: SnapshotReport }) {
  const groups = report.events?.groups ?? [];
  if (groups.length === 0) return null;

  return (
    <Section title="Event log" aside={<Label>{report.events.total.toLocaleString()} events</Label>}>
      <div className="overflow-hidden rounded-lg border border-edge">
        {groups.map((g) => {
          const tone = TONE[g.severity] ?? TONE.note;
          // Worth showing only where we talked something down. 3CX files its
          // own successful backups as errors, and that reinterpretation should
          // be visible; "3CX: INFO" next to our note is agreement, and putting
          // a badge on it is noise on every row.
          const dampened = g.says?.toLowerCase() === "error" && g.severity !== "critical";
          return (
            <div
              key={`${g.source}-${g.id}`}
              className="flex items-start gap-3 border-b border-edge/60 px-3 py-2.5 last:border-b-0"
            >
              <span className={cn("mt-1.5 size-1.5 shrink-0 rounded-full", tone.rail)} aria-hidden="true" />
              <span className="min-w-0 flex-1">
                <span className="block text-xs font-medium">
                  {g.label || `${g.source} event ${g.id}`}
                </span>
                {g.sample && (
                  <span className="mt-0.5 block truncate font-mono text-2xs text-ink-faint" title={g.sample}>
                    {g.sample}
                  </span>
                )}
              </span>
              <span className="flex shrink-0 items-center gap-2">
                {dampened && <Label title="3CX files this as an error; Azir does not">3CX says error</Label>}
                <span className="font-mono text-2xs tabular-nums text-ink-faint">{g.source}</span>
                <span className={cn("w-14 text-right font-mono text-2xs tabular-nums", tone.text)}>
                  {g.count.toLocaleString()}×
                </span>
              </span>
            </div>
          );
        })}
      </div>
    </Section>
  );
}

/** What each 3CX service was doing with the machine. */
function Services({ report }: { report: SnapshotReport }) {
  const services = report.services ?? [];
  if (services.length === 0) return null;

  const largest = Math.max(...services.map((s) => s.memory_mb), 1);

  return (
    <Section title="Services" aside={<Label>memory held at the end of the capture</Label>}>
      <div className="overflow-hidden rounded-lg border border-edge">
        {services.map((s) => (
          <div key={s.name} className="flex items-center gap-3 border-b border-edge/60 px-3 py-2 last:border-b-0">
            <span className="w-44 shrink-0 truncate text-xs">{s.name}</span>
            {/* A bar rather than a number alone: which service is holding the
                machine is a comparison, and a column of numbers is not one. */}
            <span className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-sunken">
              <span
                className="block h-full rounded-full bg-edge-strong"
                style={{ width: `${(s.memory_mb / largest) * 100}%` }}
              />
            </span>
            <span className="w-20 shrink-0 text-right font-mono text-2xs tabular-nums text-ink-dim">
              {s.memory_mb.toFixed(0)} MB
            </span>
            <span
              className={cn(
                "w-20 shrink-0 text-right font-mono text-2xs tabular-nums",
                (s.growth_mb ?? 0) >= 256 ? "text-attention" : "text-ink-faint",
              )}
              title="How much more it held at the end than at the beginning"
            >
              {(s.growth_mb ?? 0) >= 0 ? "+" : ""}
              {(s.growth_mb ?? 0).toFixed(0)} MB
            </span>
          </div>
        ))}
      </div>
    </Section>
  );
}

/** The handsets with no provisioning template. */
function Handsets({ report }: { report: SnapshotReport }) {
  const phones = report.phones ?? [];
  const [open, setOpen] = useState(false);
  if (phones.length === 0) return null;

  const shown = open ? phones : phones.slice(0, 8);

  return (
    <Section title="Phones without a template" aside={<Label>{phones.length} handsets</Label>}>
      <div className="overflow-x-auto rounded-lg border border-edge">
        <table className="w-full min-w-[560px] border-collapse text-left text-xs">
          <thead>
            <tr className="border-b border-edge bg-sunken/60">
              {["Extension", "Model", "Firmware", "MAC", "Address"].map((h) => (
                <th key={h} className="px-3 py-2 font-mono text-2xs uppercase tracking-[0.09em] text-ink-faint">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((p) => (
              <tr key={p.mac ?? p.extension} className="border-b border-edge/60 last:border-b-0">
                <td className="px-3 py-2 font-mono tabular-nums">{p.extension || "—"}</td>
                <td className="px-3 py-2">{p.model || "—"}</td>
                <td className="px-3 py-2 font-mono text-2xs text-ink-dim">{p.firmware || "—"}</td>
                <td className="px-3 py-2 font-mono text-2xs text-ink-faint">{p.mac || "—"}</td>
                <td className="px-3 py-2 font-mono text-2xs text-ink-faint">{p.ip || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {phones.length > 8 && (
        <button
          className="mt-2 text-xs text-ink-faint underline-offset-4 transition-colors hover:text-ink hover:underline"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? "Show fewer" : `Show all ${phones.length}`}
        </button>
      )}
    </Section>
  );
}

/**
 * Who changed what.
 *
 * The answer to "it was working last week", and the reason the audit log is
 * read at all. Half a million rows of somebody opening the web client reduce to
 * the few dozen that actually changed a setting, with the value before and
 * after — which is the part that needs no key to the enums 3CX has not
 * published.
 */
function WhatChanged({ report }: { report: SnapshotReport }) {
  const changes = report.changes;
  const [open, setOpen] = useState(false);
  if (!changes?.edits?.length) return null;

  const shown = open ? changes.edits : changes.edits.slice(0, 6);

  return (
    <Section
      title="Configuration changes"
      aside={<Label>{changes.edits.length} of {changes.rows.toLocaleString()} audit entries</Label>}
    >
      <div className="flex flex-col gap-2">
        {shown.map((e, i) => (
          <div key={i} className="rounded-lg border border-edge bg-panel p-3">
            <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
              <strong className="text-xs font-medium">{e.object || "a setting"}</strong>
              <span className="font-mono text-2xs text-ink-faint">
                {e.user}
                {e.ip && ` · ${e.ip}`} · {absolute(e.at)}
              </span>
            </div>
            <div className="mt-2 grid gap-1.5 sm:grid-cols-2">
              <pre className="overflow-x-auto rounded border border-edge bg-sunken/60 p-2 font-mono text-2xs text-ink-faint">
                {e.before || "—"}
              </pre>
              <pre className="overflow-x-auto rounded border border-edge bg-sunken/60 p-2 font-mono text-2xs text-ink-dim">
                {e.after || "—"}
              </pre>
            </div>
          </div>
        ))}
      </div>
      {changes.edits.length > 6 && (
        <button
          className="mt-2 text-xs text-ink-faint underline-offset-4 transition-colors hover:text-ink hover:underline"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? "Show fewer" : `Show all ${changes.edits.length}`}
        </button>
      )}
      {changes.signins && changes.signins.length > 0 && (
        <div className="mt-3">
          <p className="mb-1.5 text-xs text-ink-faint">Who has been using this phone system</p>
          <Tally items={changes.signins} />
        </div>
      )}
    </Section>
  );
}

/** One capture, read. */
function Report({ id, actor, onBack }: { id: string; actor: Actor; onBack: () => void }) {
  const [full, setFull] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const toast = useToast();

  const load = useCallback(() => {
    snapshots
      .get(id)
      .then(setFull)
      .catch((e) => setError(e instanceof Error ? e.message : "That capture could not be read."));
  }, [id]);

  useEffect(() => {
    setFull(null);
    load();
  }, [load]);

  async function keep(pinned: boolean) {
    try {
      await snapshots.keep(id, pinned);
      toast(pinned ? "Kept. This one will not expire." : "Back to the 14-day clock.", { tone: "good" });
      load();
    } catch (e) {
      toast("That could not be changed", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    }
  }

  const snapshot = full;
  const report = full?.report;

  async function remove() {
    try {
      await snapshots.remove(id);
      toast("Capture removed", { tone: "good" });
      onBack();
    } catch (e) {
      toast("That capture was not removed", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    }
  }

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="mb-4 flex items-center justify-between gap-3">
        <button
          className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-ink-dim transition-colors hover:bg-sunken hover:text-ink"
          onClick={onBack}
        >
          <Icon.back /> Captures
        </button>
        <span className="flex items-center gap-2">
          {snapshot && (
            <Button weight="quiet" onClick={() => void keep(!!snapshot.expires_at)}>
              {snapshot.expires_at ? "Keep this one" : "Stop keeping"}
            </Button>
          )}
          <Button weight="quiet" onClick={() => void remove()}>
            Remove this capture
          </Button>
        </span>
      </div>

      <h1 className="max-w-[48ch] text-2xl font-semibold tracking-tight text-balance">
        {snapshot?.filename || "Support bundle"}
      </h1>
      {snapshot && (
        <p className="mt-1 text-xs text-ink-faint" title={absolute(snapshot.captured_at)}>
          captured {ago(snapshot.captured_at ?? snapshot.uploaded_at)}
          {snapshot.fqdn && ` · ${snapshot.fqdn}`}
          {snapshot.uploaded_by && ` · uploaded by ${snapshot.uploaded_by}`}
          {snapshot.expires_at ? ` · expires ${until(snapshot.expires_at)}` : " · kept"}
        </p>
      )}

      {snapshot && !snapshot.customer_id && (
        <Attach snapshot={snapshot} actor={actor} onDone={load} />
      )}

      {error && <div className="mt-4"><Problem>{error}</Problem></div>}
      {!report && !error && <div className="mt-6"><Loading rows={5} /></div>}

      {report && (
        <>
          <Facts report={report} />
          <Findings findings={report.findings} />
          <CallQuality report={report} />
          <Wire report={report} />
          <Charts report={report} />
          <Events report={report} />
          <Services report={report} />
          <Handsets report={report} />
          <WhatChanged report={report} />
          <WhatWasRead report={report} />
        </>
      )}
    </div>
  );
}

/**
 * A capture that arrived without a customer.
 *
 * Most attach themselves: the bundle carries the phone system's FQDN and Azir
 * matches it against whoever that PBX belongs to. The ones that land here came
 * off a system Azir has never been pointed at — a prospect, or a customer whose
 * 3CX settings nobody has filled in — and somebody has to say whose they are.
 *
 * Shown as a prompt rather than a warning. Not knowing yet is an ordinary state
 * and the report above it is perfectly readable without an answer.
 */
function Attach({
  snapshot,
  actor,
  onDone,
}: {
  snapshot: Snapshot;
  actor: Actor;
  onDone: () => void;
}) {
  const [chosen, setChosen] = useState("");
  const [named, setNamed] = useState("");
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  // Offering to create one to somebody who cannot is a button that only ever
  // fails; they can still attach the capture to a customer that exists.
  const mayCreate = actor.permissions.includes(Perm.customerManage);

  async function attach(customerId: string) {
    try {
      await snapshots.attach(snapshot.id, customerId);
      toast("Attached", { tone: "good" });
      onDone();
    } catch (e) {
      toast("That could not be attached", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    }
  }

  /*
    Creating the customer here rather than sending somebody to another screen.

    The bundle already carries the address, so once the customer exists Azir
    can be told which phone system is theirs — and every later capture from
    that PBX attaches itself. That last step is the point: without it the same
    zip would arrive unattached again next month.

    Setting it needs the permission to configure plugins, which a technician
    may not have. That failure is deliberately quiet: the customer was created
    and the capture is attached, which is what was asked for, and refusing the
    whole thing over the part that only saves work later would be worse.
  */
  async function create() {
    const name = named.trim();
    if (!name) return;
    setBusy(true);
    try {
      const customer = await api.createCustomer(name);
      if (snapshot.fqdn) {
        try {
          await api.saveSettings("3cx", { fqdn: snapshot.fqdn }, customer.id);
        } catch {
          // Nothing to say: the attach below is the part that was asked for.
        }
      }
      await attach(customer.id);
    } catch (e) {
      toast("That customer could not be created", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel className="mt-4 flex flex-wrap items-center gap-3 border-dashed p-3.5">
      <span className="text-sm text-ink-dim">
        {snapshot.fqdn ? (
          <>
            This came off <span className="font-mono text-xs">{snapshot.fqdn}</span>, which is not a
            phone system Azir knows about.
          </>
        ) : (
          "This capture is not attached to a customer."
        )}
      </span>
      <div className="w-[240px]">
        <CustomerSearch
          value={chosen}
          ariaLabel="Attach this capture to a customer"
          placeholder="Type a customer's name"
          onChange={(id) => setChosen(id)}
        />
      </div>
      <Button disabled={!chosen || busy} onClick={() => void attach(chosen)}>
        Attach
      </Button>

      {mayCreate && (
        <>
          <span className="text-xs text-ink-faint">or</span>
          <TextInput
            className="w-48"
            placeholder="New customer name"
            aria-label="Create a customer for this capture"
            value={named}
            onChange={(e) => setNamed(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void create();
            }}
          />
          <Button disabled={!named.trim() || busy} onClick={() => void create()}>
            {busy ? "Creating…" : "Create and attach"}
          </Button>
        </>
      )}
    </Panel>
  );
}

/** The page of facts about the machine. */
function Facts({ report }: { report: SnapshotReport }) {
  const s = report.system;
  const cells: { value: string; label: string }[] = [
    { value: s.version || "—", label: "3CX version" },
    { value: String(s.extensions ?? "—"), label: "extensions" },
    {
      value: s.total_disk_gb ? `${Math.round(s.free_disk_gb ?? 0)}/${Math.round(s.total_disk_gb)} GB` : "—",
      label: "disk free",
    },
    {
      value: s.total_memory_gb ? `${(s.free_memory_gb ?? 0).toFixed(1)}/${s.total_memory_gb.toFixed(1)} GB` : "—",
      label: "memory free",
    },
  ];

  return (
    <>
      <dl className="mt-6 grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-edge bg-edge sm:grid-cols-4">
        {cells.map((c) => (
          <div key={c.label} className="bg-panel px-4 py-3">
            <dd className="font-mono text-xl font-medium tabular-nums tracking-tight">{c.value}</dd>
            <dt className="mt-0.5 font-mono text-2xs uppercase tracking-[0.09em] text-ink-faint">
              {c.label}
            </dt>
          </div>
        ))}
      </dl>
      <p className="mt-2 text-xs text-ink-faint">
        {[s.os, s.cpu_model, s.cpu_count ? `${s.cpu_count} CPU` : "", s.virtualised]
          .filter(Boolean)
          .join(" · ")}
      </p>
    </>
  );
}

/** The answer. */
function Findings({ findings }: { findings: Finding[] }) {
  if (findings.length === 0) {
    return (
      <div className="mt-6">
        <Empty headline="Nothing stood out">
          The checks Azir knows how to make all came back clean. That is not the
          same as nothing being wrong — it means the fault is not one of the
          patterns it reads for.
        </Empty>
      </div>
    );
  }

  return (
    <section className="mt-7">
      <Label className="mb-2.5 block">What it found</Label>
      <div className="flex flex-col gap-3">
        {findings.map((f, i) => (
          <Detail key={`${f.title}-${i}`} finding={f} />
        ))}
      </div>
    </section>
  );
}

function Detail({ finding }: { finding: Finding }) {
  const [showEvidence, setShowEvidence] = useState(false);
  const tone = TONE[finding.severity] ?? TONE.note;

  return (
    <div className={cn("rounded-lg border p-4", tone.wash)}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <strong className={cn("text-sm font-semibold", tone.text)}>{finding.title}</strong>
        <span className="flex items-center gap-2">
          <Label className={tone.text}>{tone.word}</Label>
          {finding.occurrences ? <Label>seen {finding.occurrences}×</Label> : null}
        </span>
      </div>

      <p className="mt-1.5 max-w-[80ch] text-sm leading-relaxed text-ink-dim">{finding.detail}</p>

      {finding.evidence && finding.evidence.length > 0 && (
        <div className="mt-2.5">
          {/* The lines it was read from. A finding nobody can check is one that
              will eventually be wrong and believed anyway. */}
          <button
            className="text-xs text-ink-faint underline-offset-4 transition-colors hover:text-ink hover:underline"
            onClick={() => setShowEvidence((v) => !v)}
          >
            {showEvidence ? "Hide" : "Show"} the lines this came from
          </button>
          {showEvidence && (
            <pre className="mt-2 overflow-x-auto rounded-md border border-edge bg-panel p-2.5 font-mono text-2xs leading-relaxed">
              {finding.evidence.join("\n")}
            </pre>
          )}
        </div>
      )}
      {finding.source && <p className="mt-2 font-mono text-2xs text-ink-faint">{finding.source}</p>}
    </div>
  );
}

/** What the machine had been doing, which a single reading cannot show. */
function Charts({ report }: { report: SnapshotReport }) {
  const series = report.series;

  const points = useMemo(() => {
    const shape = (raw?: { at: string; value: number }[]) =>
      (raw ?? []).map((p) => ({
        label: new Date(p.at).toLocaleString(undefined, {
          month: "short",
          day: "numeric",
          hour: "2-digit",
        }),
        value: p.value,
      }));
    return {
      cpu: shape(series.cpu_percent),
      memory: shape(series.free_memory_gb),
      disk: shape(series.free_disk_gb),
      sent: shape(report.network?.sent_mbps),
      received: shape(report.network?.received_mbps),
    };
  }, [series, report.network]);

  if (points.cpu.length === 0 && points.disk.length === 0 && points.sent.length === 0) return null;

  return (
    <section className="mt-7 grid gap-3 md:grid-cols-2 lg:grid-cols-4">
      {points.cpu.length > 0 && (
        <Panel className="p-4">
          <div className="mb-3 flex items-baseline justify-between gap-3">
            <h2 className="text-sm font-medium">CPU</h2>
            <Label>percent</Label>
          </div>
          <Trend points={points.cpu} unit="%" spanLabel="readings" />
        </Panel>
      )}
      {points.memory.length > 0 && (
        <Panel className="p-4">
          <div className="mb-3 flex items-baseline justify-between gap-3">
            <h2 className="text-sm font-medium">Free memory</h2>
            <Label>GB</Label>
          </div>
          <Trend points={points.memory} unit=" GB" spanLabel="readings" zeroed={false} />
        </Panel>
      )}
      {points.disk.length > 0 && (
        <Panel className="p-4">
          <div className="mb-3 flex items-baseline justify-between gap-3">
            <h2 className="text-sm font-medium">Free disk</h2>
            <Label>GB</Label>
          </div>
          <Trend points={points.disk} unit=" GB" spanLabel="readings" zeroed={false} />
        </Panel>
      )}
      {points.sent.length > 0 && (
        <Panel className="p-4">
          <div className="mb-3 flex items-baseline justify-between gap-3">
            <h2 className="text-sm font-medium">Network</h2>
            <Label>{report.network?.interface?.slice(0, 12) ?? "Mbps"}</Label>
          </div>
          {/* Sent as the line and received as the ground beneath it, so which
              way the traffic goes reads without a legend to work it out. */}
          <Trend
            points={points.sent}
            against={points.received}
            unit=" Mbps"
            spanLabel="readings"
            legend={{ points: "sent", against: "received" }}
          />
        </Panel>
      )}
    </section>
  );
}

/**
 * Which files were actually understood.
 *
 * A bundle from a version that moved something would otherwise produce a short,
 * confident report with no sign that half of it was never read.
 */
function WhatWasRead({ report }: { report: SnapshotReport }) {
  const [open, setOpen] = useState(false);

  return (
    <details
      className="mt-7 rounded-lg border border-dashed border-edge px-4 py-3"
      open={open}
      onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary className="cursor-pointer list-none">
        <Label className="transition-colors hover:text-ink">
          Read {report.files_read.length} files
          {report.files_missing?.length ? ` · ${report.files_missing.length} expected files were not there` : ""}
        </Label>
      </summary>

      <div className="mt-3 flex flex-col gap-1">
        {report.files_read.map((f) => (
          <span key={f} className="font-mono text-2xs text-ink-faint">
            {f}
          </span>
        ))}
      </div>

      {report.files_missing && report.files_missing.length > 0 && (
        <p className="mt-3 max-w-[70ch] text-xs text-attention">
          Not in this bundle: {report.files_missing.join(", ")}. Whatever those
          would have said is missing from the findings above.
        </p>
      )}

      {report.health.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-2">
          {report.health.map((h) => (
            <span
              key={h.name}
              className={cn(
                "inline-flex items-center gap-1.5 rounded border px-2 py-0.5 text-2xs",
                h.ok ? "border-edge bg-sunken text-ink-dim" : "border-attention/30 bg-attention/10 text-attention",
              )}
            >
              {h.name}: {h.says}
            </span>
          ))}
        </div>
      )}
    </details>
  );
}
