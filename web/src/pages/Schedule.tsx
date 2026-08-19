import { useCallback, useEffect, useMemo, useState } from "react";
import {
  api,
  type Actor,
  type Closure,
  type Customer,
  type OpenDay,
  type Schedule as Sched,
} from "../api";
import { CustomerSearch } from "../CustomerSearch";
import { Dialog } from "../components";
import { useToast } from "../Toast";
import { Button, Chip, Empty, Icon, Label, Panel, Picker, Problem, TextInput } from "../ui";

/**
 * When a customer is closed.
 *
 * A holiday and an early closing are the same record to a phone system — a
 * span of dates, sometimes narrowed to a span of hours, sometimes repeating
 * every year — so they are one screen.
 *
 * Office hours and office holidays are two things, not one. The hours are the
 * week a department keeps; the closures are the dated exceptions to it. They
 * sit side by side here because that is how somebody reads them — "when are
 * they open, and when are they not" — and they are written separately because
 * they are separate acts.
 *
 * Per department, because that is how the phone system keeps both. A warehouse
 * that shuts at four does not share its hours with the office.
 *
 * A year at a time, because that is the unit closures come in. Most of them
 * repeat, which makes "the 25th of December" the fact and "2026" an accident
 * of when you happened to look; a month view would hide eleven twelfths of the
 * answer behind navigation.
 *
 * Every write goes through the same gate as everywhere else, and the phone
 * system's own rules are checked before anything is sent: a name is used once,
 * and two closures may not cover the same day.
 */

const MONTHS = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];

/** Days in a month, for a year that may or may not be a leap year. */
function daysIn(month: number, year: number) {
  return new Date(year, month + 1, 0).getDate();
}

/** Which weekday the first of a month lands on, Monday first. */
function startsOn(month: number, year: number) {
  return (new Date(year, month, 1).getDay() + 6) % 7;
}

/**
 * The days a closure covers, as "MM-DD" keys.
 *
 * Recurring and dated closures are both reduced to days of the year, because
 * that is what a year view draws. A span that crosses new year is two runs
 * rather than one — December to the 31st, then January onward — which is the
 * same split the plugin makes when it checks for overlaps.
 */
function daysOf(closure: Closure, year: number): Set<string> {
  const out = new Set<string>();
  const at = (text: string) => {
    const parts = text.replace(/^--/, "").split("-");
    const [m, d] = parts.length === 3 ? [parts[1], parts[2]] : parts;
    return { month: Number(m), day: Number(d) };
  };
  if (!closure.starts) return out;

  // A dated closure only appears in its own year.
  if (!closure.repeats && closure.starts.length === 10) {
    const inYear = Number(closure.starts.slice(0, 4));
    const endYear = Number((closure.ends || closure.starts).slice(0, 4));
    if (year < inYear || year > endYear) return out;
  }

  const from = at(closure.starts);
  const to = at(closure.ends || closure.starts);
  let month = from.month;
  let day = from.day;
  // Bounded: a closure cannot cover more than a year, and this stops on the
  // end date or after 366 steps whichever comes first.
  for (let step = 0; step < 366; step++) {
    out.add(`${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`);
    if (month === to.month && day === to.day) break;
    day++;
    if (day > daysIn(month - 1, year)) {
      day = 1;
      month = month === 12 ? 1 : month + 1;
    }
  }
  return out;
}

/** How a closure reads in a list. */
function reads(closure: Closure): string {
  const day = (text: string) => {
    if (!text) return "";
    const parts = text.replace(/^--/, "").split("-");
    const [m, d] = parts.length === 3 ? [parts[1], parts[2]] : parts;
    const month = MONTHS[Number(m) - 1] ?? m;
    return `${Number(d)} ${month}`;
  };
  const span = closure.ends && closure.ends !== closure.starts
    ? `${day(closure.starts)} – ${day(closure.ends)}`
    : day(closure.starts);
  const hours = closure.from_time ? `, ${closure.from_time}–${closure.to_time}` : "";
  return span + hours;
}

export function Schedule({ actor }: { actor: Actor }) {
  const [customerID, setCustomerID] = useState("");
  const [customer, setCustomer] = useState<Customer | null>(null);
  const [schedule, setSchedule] = useState<Sched | null>(null);
  const [department, setDepartment] = useState("");
  const [year, setYear] = useState(new Date().getFullYear());
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [making, setMaking] = useState(false);
  const [removing, setRemoving] = useState<Closure | null>(null);
  const toast = useToast();

  const mayManage = actor.permissions.includes("phone.manage");

  const load = useCallback(async (id: string, dept: string) => {
    if (!id) {
      setSchedule(null);
      return;
    }
    setProblem(null);
    try {
      const got = await api.schedule(id, dept);
      setSchedule(got);
      // The phone system decides which department answers when none was asked
      // for, so the picker follows it rather than guessing the same way twice.
      setDepartment(got.department);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not read the schedule");
      setSchedule(null);
    }
  }, []);

  useEffect(() => {
    void load(customerID, department);
    // Deliberately not on `department`: choosing one calls load itself, and
    // load sets it, which would otherwise be a loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [customerID, load]);

  // A different customer has different departments; theirs is not ours.
  useEffect(() => setDepartment(""), [customerID]);

  // One customer is not a choice.
  useEffect(() => {
    void (async () => {
      try {
        const first = await api.findCustomers("", 2);
        if (first.length === 1) {
          setCustomerID(first[0].id);
          setCustomer(first[0]);
        }
      } catch {
        // The search reports its own failures.
      }
    })();
  }, []);

  // Which closure sits on each day of the year, for the grid and the tooltip.
  const onDay = useMemo(() => {
    const out = new Map<string, Closure[]>();
    for (const closure of schedule?.closures ?? []) {
      for (const key of daysOf(closure, year)) {
        out.set(key, [...(out.get(key) ?? []), closure]);
      }
    }
    return out;
  }, [schedule, year]);

  async function add(closure: Parameters<typeof api.addClosure>[1]) {
    setBusy(true);
    setProblem(null);
    try {
      await api.addClosure(customerID, { ...closure, department });
      toast(`${closure.name} scheduled`, { tone: "good" });
      setMaking(false);
      await load(customerID, department);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not schedule that");
    } finally {
      setBusy(false);
    }
  }

  async function remove(closure: Closure) {
    setBusy(true);
    try {
      await api.removeClosure(customerID, closure.id);
      toast(`${closure.name} removed`, { tone: "good" });
      setRemoving(null);
      await load(customerID, department);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : "Could not remove that");
      setRemoving(null);
    } finally {
      setBusy(false);
    }
  }


  return (
    <div className="mx-auto flex max-w-[1400px] flex-col gap-4 p-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Schedule</h1>
          <p className="mt-1 max-w-[62ch] text-sm text-ink-dim">
            When a customer's phones are closed — holidays, and the days they shut early.
          </p>
        </div>
      </header>

      <Panel>
        <div className="flex flex-wrap items-end gap-3 px-4 py-4">
          <div className="flex min-w-[260px] flex-col gap-1.5">
            <Label>Whose phone system</Label>
            <CustomerSearch
              value={customerID}
              ariaLabel="Whose schedule to show"
              placeholder="Type a customer's name"
              onChange={(id, got) => {
                setCustomerID(id);
                setCustomer(got ?? null);
              }}
            />
          </div>

          {customerID && schedule && schedule.departments.length > 0 && (
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="which-department">Department</Label>
              <div className="w-[200px]">
                <Picker
                  id="which-department"
                  value={department}
                  onChange={(e) => {
                    setDepartment(e.target.value);
                    void load(customerID, e.target.value);
                  }}
                >
                  {schedule.departments.map((d) => (
                    <option key={d.number} value={d.name}>
                      {d.name}
                    </option>
                  ))}
                </Picker>
              </div>
            </div>
          )}

          {customerID && (
            <>
              <div className="flex flex-col gap-1.5">
                <Label>Year</Label>
                <div className="flex items-center gap-1">
                  <Button aria-label="The year before" onClick={() => setYear((y) => y - 1)}>
                    ←
                  </Button>
                  <span className="w-[62px] text-center font-mono text-sm">{year}</span>
                  <Button aria-label="The year after" onClick={() => setYear((y) => y + 1)}>
                    →
                  </Button>
                </div>
              </div>
              {mayManage && (
                <Button weight="primary" onClick={() => setMaking(true)}>
                  <Icon.plus />
                  New closure
                </Button>
              )}
            </>
          )}
        </div>

        {problem && (
          <div className="px-4 pb-4">
            <Problem>{problem}</Problem>
          </div>
        )}

        {customerID && schedule?.forced && (
          <div className="border-t border-edge px-4 py-2.5 text-xs">
            <Chip tone="warn">{schedule.forced}</Chip>{" "}
            <span className="text-ink-dim">
              Somebody has overridden this department's schedule by hand. Until that is put
              back, the hours below are not what callers meet.
            </span>
          </div>
        )}
      </Panel>

      {!customerID && (
        <Panel>
          <Empty headline="Choose a customer to start" />
        </Panel>
      )}

      {customerID && schedule && (
        <Hours
          key={schedule.department}
          days={schedule.office_hours?.days ?? []}
          zone={schedule.time_zone}
          department={schedule.department}
          mayManage={mayManage}
          busy={busy}
          onSave={async (days) => {
            setBusy(true);
            setProblem(null);
            try {
              await api.setOfficeHours(customerID, schedule.department, days);
              toast(`Office hours saved for ${schedule.department}`, { tone: "good" });
              await load(customerID, department);
            } catch (e) {
              setProblem(e instanceof Error ? e.message : "Could not save those hours");
            } finally {
              setBusy(false);
            }
          }}
        />
      )}

      {customerID && schedule && (
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
          <Panel>
            <Year year={year} onDay={onDay} />
          </Panel>

          <Panel>
            <div className="flex items-center justify-between border-b border-edge px-4 py-3">
              <h2 className="text-sm font-medium">
                {schedule.count} closure{schedule.count === 1 ? "" : "s"}
              </h2>
            </div>
            {schedule.closures.length === 0 ? (
              <Empty headline="Nothing is scheduled">
                Their phones keep the same hours all year.
              </Empty>
            ) : (
              <ul className="divide-y divide-edge">
                {schedule.closures.map((closure) => (
                  <li key={closure.id} className="flex items-start gap-2 px-4 py-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="text-sm font-medium">{closure.name}</span>
                        {closure.repeats && <Chip tone="accent">every year</Chip>}
                        {closure.from_time && <Chip>part of the day</Chip>}
                      </div>
                      <div className="mt-0.5 text-xs text-ink-dim">{reads(closure)}</div>
                    </div>
                    {mayManage && (
                      <button
                        className="rounded-md px-2 py-1 text-xs text-ink-faint transition-colors hover:bg-sunken hover:text-critical"
                        aria-label={`Remove ${closure.name}`}
                        onClick={() => setRemoving(closure)}
                      >
                        Remove
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </div>
      )}

      {making && (
        <NewClosure
          busy={busy}
          year={year}
          customer={customer?.display_name ?? "this customer"}
          onClose={() => setMaking(false)}
          onAdd={add}
        />
      )}

      {removing && (
        <Dialog
          open
          onOpenChange={(next) => {
            if (!next && !busy) setRemoving(null);
          }}
          title={`Remove ${removing.name}?`}
          description={
            <>
              {reads(removing)}
              {removing.repeats ? ", every year" : ""}. The phones will keep their usual hours on
              those days.
            </>
          }
          footer={
            <>
              <Button onClick={() => setRemoving(null)} disabled={busy}>
                Cancel
              </Button>
              <Button weight="primary" disabled={busy} onClick={() => void remove(removing)}>
                {busy ? "Removing…" : "Remove"}
              </Button>
            </>
          }
        />
      )}
    </div>
  );
}

/**
 * A year, twelve months at a time.
 *
 * Closed days are filled rather than dotted, because the question is "is that
 * day closed" and a mark beside a number is one more thing to decode. Hovering
 * names what closes it; a day covered by two closures should not exist, and if
 * one does this says so rather than showing the first.
 */
function Year({ year, onDay }: { year: number; onDay: Map<string, Closure[]> }) {
  return (
    <div className="grid gap-x-5 gap-y-4 p-4 sm:grid-cols-2 xl:grid-cols-3">
      {MONTHS.map((name, month) => (
        <div key={name}>
          <h3 className="mb-1.5 text-xs font-medium uppercase tracking-wide text-ink-dim">
            {name}
          </h3>
          <div className="grid grid-cols-7 gap-px text-center">
            {["M", "T", "W", "T", "F", "S", "S"].map((d, i) => (
              <span key={i} className="pb-1 text-2xs text-ink-faint">
                {d}
              </span>
            ))}
            {Array.from({ length: startsOn(month, year) }, (_, i) => (
              <span key={`pad${i}`} />
            ))}
            {Array.from({ length: daysIn(month, year) }, (_, i) => {
              const day = i + 1;
              const key = `${String(month + 1).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
              const closures = onDay.get(key);
              const shut = (closures?.length ?? 0) > 0;
              const clash = (closures?.length ?? 0) > 1;
              return (
                <span
                  key={day}
                  title={
                    closures
                      ? closures.map((c) => c.name).join(" and ") +
                        (clash ? " — two closures cover this day" : "")
                      : undefined
                  }
                  className={[
                    "rounded-[3px] py-0.5 font-mono text-2xs tabular-nums",
                    shut ? "font-medium text-azir-ink" : "text-ink-dim",
                    shut && !clash ? "bg-azir" : "",
                    clash ? "bg-critical" : "",
                  ].join(" ")}
                >
                  {day}
                </span>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}

/** The form for a new closure. */
function NewClosure({
  busy,
  year,
  customer,
  onClose,
  onAdd,
}: {
  busy: boolean;
  year: number;
  customer: string;
  onClose: () => void;
  onAdd: (closure: Parameters<typeof api.addClosure>[1]) => void;
}) {
  const [name, setName] = useState("");
  const [starts, setStarts] = useState(`${year}-01-01`);
  const [ends, setEnds] = useState("");
  const [repeats, setRepeats] = useState(false);
  const [partial, setPartial] = useState(false);
  const [from, setFrom] = useState("13:00");
  const [to, setTo] = useState("17:00");

  // A repeating closure has no year: it is the 25th of December, not the 25th
  // of December 2026. The form drops it rather than sending one to be ignored.
  const asDate = (value: string) => (repeats ? `--${value.slice(5)}` : value);
  const ready = name.trim() !== "" && starts !== "";

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !busy) onClose();
      }}
      title="New closure"
      description={`On ${customer}'s phone system. A holiday, or a day they shut early.`}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            weight="primary"
            disabled={busy || !ready}
            onClick={() =>
              onAdd({
                name: name.trim(),
                starts: asDate(starts),
                ends: ends ? asDate(ends) : undefined,
                from_time: partial ? from : undefined,
                to_time: partial ? to : undefined,
                repeats,
              })
            }
          >
            {busy ? "Scheduling…" : "Schedule it"}
          </Button>
        </>
      }
    >
      <div className="mt-4 flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="closure-name">What it is called</Label>
          <TextInput
            id="closure-name"
            value={name}
            autoFocus
            placeholder="Christmas Day"
            onChange={(e) => setName(e.target.value)}
          />
        </div>

        <div className="flex flex-wrap gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="closure-from">First day</Label>
            <TextInput
              id="closure-from"
              type="date"
              value={starts}
              onChange={(e) => setStarts(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="closure-to">Last day</Label>
            <TextInput
              id="closure-to"
              type="date"
              value={ends}
              onChange={(e) => setEnds(e.target.value)}
            />
            <span className="text-2xs text-ink-faint">Leave empty for one day</span>
          </div>
        </div>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            className="size-3.5 accent-azir"
            checked={repeats}
            onChange={(e) => setRepeats(e.target.checked)}
          />
          Every year, on the same dates
        </label>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            className="size-3.5 accent-azir"
            checked={partial}
            onChange={(e) => setPartial(e.target.checked)}
          />
          Only part of the day
        </label>

        {partial && (
          <div className="flex flex-wrap items-end gap-3 rounded-lg border border-edge bg-sunken/60 px-3 py-2.5">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="closure-start">Closed from</Label>
              <div className="w-[110px]">
                <TextInput
                  id="closure-start"
                  type="time"
                  value={from}
                  onChange={(e) => setFrom(e.target.value)}
                />
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="closure-end">Open again at</Label>
              <div className="w-[110px]">
                <TextInput
                  id="closure-end"
                  type="time"
                  value={to}
                  onChange={(e) => setTo(e.target.value)}
                />
              </div>
            </div>
            <span className="pb-2 text-2xs text-ink-faint">
              In the customer's own time zone
            </span>
          </div>
        )}
      </div>
    </Dialog>
  );
}

/** The days of a week, in the order a week is read. */
const WEEK = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];

/**
 * The week a department keeps.
 *
 * Seven rows, because a week has seven days and hiding the closed ones would
 * make "are they open on Saturday" a question you answer by counting. A day
 * with no hours is a day they are shut, which is the same thing the phone
 * system means by leaving it out.
 *
 * Saved whole. The phone system replaces the pattern rather than merging it,
 * so sending one changed day would send a week with one day in it.
 */
function Hours({
  days,
  zone,
  department,
  mayManage,
  busy,
  onSave,
}: {
  days: OpenDay[];
  zone: string;
  department: string;
  mayManage: boolean;
  busy: boolean;
  onSave: (days: OpenDay[]) => void;
}) {
  const asIs = useMemo(() => {
    const out = new Map<string, OpenDay>();
    for (const day of days) {
      // A day the phone system keeps as open and closed at the same moment is
      // a day they are shut, and reads better as an empty row than as 00:00.
      if (day.from !== day.to) out.set(day.day, day);
    }
    return out;
  }, [days]);

  const [week, setWeek] = useState<Map<string, OpenDay>>(() => new Map(asIs));
  const changed =
    JSON.stringify([...week.entries()].sort()) !== JSON.stringify([...asIs.entries()].sort());

  const set = (day: string, patch: Partial<OpenDay>) =>
    setWeek((was) => {
      const next = new Map(was);
      const now = next.get(day) ?? { day, from: "09:00", to: "17:00" };
      next.set(day, { ...now, ...patch });
      return next;
    });

  return (
    <Panel>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-edge px-4 py-3">
        <div>
          <h2 className="text-sm font-medium">Office hours</h2>
          <p className="text-xs text-ink-dim">
            The week {department} keeps. The closures below are the exceptions to it.
            {zone ? ` Times are ${zone}.` : ""}
          </p>
        </div>
        {mayManage && (
          <div className="flex items-center gap-2">
            {changed && (
              <Button disabled={busy} onClick={() => setWeek(new Map(asIs))}>
                Undo
              </Button>
            )}
            <Button
              weight="primary"
              disabled={busy || !changed}
              onClick={() => onSave([...week.values()])}
            >
              {busy ? "Saving…" : "Save hours"}
            </Button>
          </div>
        )}
      </div>

      <div className="flex flex-wrap gap-x-6 gap-y-2 px-4 py-3">
        {WEEK.map((day) => {
          const open = week.get(day);
          return (
            <div key={day} className="flex min-w-[210px] items-center gap-2">
              <label className="flex w-[104px] shrink-0 items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  className="size-3.5 accent-azir"
                  checked={Boolean(open)}
                  disabled={!mayManage || busy}
                  aria-label={`Open on ${day}`}
                  onChange={(e) =>
                    setWeek((was) => {
                      const next = new Map(was);
                      if (e.target.checked) next.set(day, { day, from: "09:00", to: "17:00" });
                      else next.delete(day);
                      return next;
                    })
                  }
                />
                {day.slice(0, 3)}
              </label>
              {open ? (
                <div className="flex items-center gap-1">
                  <div className="w-[92px]">
                    <TextInput
                      type="time"
                      value={open.from}
                      disabled={!mayManage || busy}
                      aria-label={`${day} opens`}
                      onChange={(e) => set(day, { from: e.target.value })}
                    />
                  </div>
                  <span className="text-xs text-ink-faint">–</span>
                  <div className="w-[92px]">
                    <TextInput
                      type="time"
                      value={open.to}
                      disabled={!mayManage || busy}
                      aria-label={`${day} closes`}
                      onChange={(e) => set(day, { to: e.target.value })}
                    />
                  </div>
                </div>
              ) : (
                <span className="text-xs text-ink-faint">closed</span>
              )}
            </div>
          );
        })}
      </div>
    </Panel>
  );
}
