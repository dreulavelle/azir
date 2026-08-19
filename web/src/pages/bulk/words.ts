import type { BulkEdit } from "../../api";

/**
 * The words the bulk-edit screens share.
 *
 * Here rather than in whichever screen happened to need them first, because
 * the button and the confirmation say the same facts two ways and drifting
 * apart is exactly the failure this whole feature exists to avoid.
 */

/** What Apply would do, once the unticked rows are taken out. */
export type Counts = { change: number; create: number; remove: number };

/**
 * "Change 3, remove 20" — what the button does, said as an instruction.
 *
 * Separate from saying() below, which is the same facts in the third person
 * for the dialog. The button used to borrow that one and read "Changes 1",
 * which scans as a count of changes rather than as a thing it will do.
 */
export function doing(counts: Counts): string {
  return [
    counts.change > 0 ? `Change ${counts.change}` : "",
    counts.create > 0 ? `Create ${counts.create}` : "",
    counts.remove > 0 ? `Remove ${counts.remove}` : "",
  ]
    .filter(Boolean)
    .join(", ");
}

/** "changes 3 and removes 20", for the dialog. */
export function saying(counts: Counts): string {
  const parts = [
    counts.change > 0 ? `changes ${counts.change}` : "",
    counts.create > 0 ? `creates ${counts.create}` : "",
    counts.remove > 0 ? `removes ${counts.remove}` : "",
  ].filter(Boolean);
  if (parts.length === 0) return "changes nothing";
  if (parts.length === 1) return parts[0];
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

export function upperFirst(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** How a staged sheet reads in a list. "planned" is not a word for this. */
export function statusWord(status: BulkEdit["status"]): string {
  switch (status) {
    case "draft":
      return "not compared";
    case "planned":
      return "waiting on you";
    case "applied":
      return "applied";
    case "cancelled":
      return "discarded";
  }
}

/** How many rows a stored sheet has, tolerating the ones written as null. */
export function rowCount(edit: BulkEdit): number {
  return edit.sheet?.rows?.length ?? 0;
}

/** Hands a generated file to the browser. */
export function saveAs(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
