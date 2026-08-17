import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  snapshots,
  type Customer,
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
  Picker,
  Problem,
  ago,
  absolute,
} from "../ui";

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

export function Diagnostics({ openId, go }: { openId?: string; go: (to: Route) => void }) {
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
    return <Report id={openId} onBack={() => go({ name: "diagnostics" })} />;
  }

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <div className="mb-1 flex items-start justify-between gap-6">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Diagnostics</h1>
          <p className="mt-1 max-w-[62ch] text-sm text-ink-dim">
            Upload a 3CX support info bundle and Azir reads the parts that
            explain a fault — silent calls, a disk filling, a service that keeps
            restarting — instead of you unzipping forty megabytes to find them.
          </p>
        </div>
      </div>

      <Upload onDone={() => void load()} />

      {error && <Problem>{error}</Problem>}
      {!list && !error && <Loading rows={4} />}

      {list && list.length === 0 && (
        <Empty headline="No captures yet">
          In 3CX: Admin → Support → collect support info. Upload the zip above.
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

/** Choosing a customer and handing over the zip. */
function Upload({ onDone }: { onDone: () => void }) {
  const [customers, setCustomers] = useState<Customer[] | null>(null);
  const [customerId, setCustomerId] = useState("");
  const [busy, setBusy] = useState(false);
  const picker = useRef<HTMLInputElement>(null);
  const toast = useToast();

  useEffect(() => {
    // Azir's own customers, not the helpdesk's.
    //
    // A snapshot hangs off the customer record here, which is keyed by Azir's
    // id — and the helpdesk's list is keyed by the helpdesk's. Reading the
    // wrong one puts a number where a uuid belongs and every upload is refused
    // by a customer that does exist.
    api
      .customers()
      .then(setCustomers)
      .catch(() => setCustomers([]));
  }, []);

  async function send(file: File) {
    if (!customerId) {
      toast("Choose whose phone system this is first", { tone: "bad" });
      return;
    }
    setBusy(true);
    try {
      const { report } = await snapshots.upload(customerId, file);
      const bad = report.findings.filter((f) => f.severity === "critical").length;
      toast(
        bad > 0
          ? `Read. ${bad} thing${bad === 1 ? "" : "s"} breaking calls right now.`
          : `Read. ${report.findings.length} findings.`,
        { tone: bad > 0 ? "bad" : "good" },
      );
      onDone();
    } catch (e) {
      toast("That bundle could not be read", {
        tone: "bad",
        detail: e instanceof Error ? e.message : undefined,
      });
    } finally {
      setBusy(false);
      if (picker.current) picker.current.value = "";
    }
  }

  return (
    <Panel className="mt-5 flex flex-wrap items-center gap-3 p-3.5">
      <label className="flex items-center gap-2">
        <Label>Customer</Label>
        <Picker
          className="w-auto"
          value={customerId}
          aria-label="Which customer"
          onChange={(e) => setCustomerId(e.target.value)}
        >
          <option value="">Choose…</option>
          {(customers ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.display_name}
            </option>
          ))}
        </Picker>
      </label>

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
      <Button
        weight="primary"
        disabled={busy || !customerId}
        onClick={() => picker.current?.click()}
      >
        {busy ? "Reading…" : "Upload a support bundle"}
      </Button>

      <span className="text-xs text-ink-faint">
        The zip is read and discarded. Only the findings are kept.
      </span>
    </Panel>
  );
}

/** One capture, read. */
function Report({ id, onBack }: { id: string; onBack: () => void }) {
  const [full, setFull] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    setFull(null);
    snapshots
      .get(id)
      .then(setFull)
      .catch((e) => setError(e instanceof Error ? e.message : "That capture could not be read."));
  }, [id]);

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
        <Button weight="quiet" onClick={() => void remove()}>
          Remove this capture
        </Button>
      </div>

      <h1 className="max-w-[48ch] text-2xl font-semibold tracking-tight text-balance">
        {snapshot?.filename || "Support bundle"}
      </h1>
      {snapshot && (
        <p className="mt-1 text-xs text-ink-faint" title={absolute(snapshot.captured_at)}>
          captured {ago(snapshot.captured_at ?? snapshot.uploaded_at)}
          {snapshot.uploaded_by && ` · uploaded by ${snapshot.uploaded_by}`}
        </p>
      )}

      {error && <div className="mt-4"><Problem>{error}</Problem></div>}
      {!report && !error && <div className="mt-6"><Loading rows={5} /></div>}

      {report && (
        <>
          <Facts report={report} />
          <Findings findings={report.findings} />
          <Charts report={report} />
          <WhatWasRead report={report} />
        </>
      )}
    </div>
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
    };
  }, [series]);

  if (points.cpu.length === 0 && points.disk.length === 0) return null;

  return (
    <section className="mt-7 grid gap-3 md:grid-cols-3">
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
