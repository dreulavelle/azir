import { Perm, type Actor } from "../api";

/**
 * The settings tabs, and what each one needs.
 *
 * In its own module because two things need the answer at two different times:
 * the navigation rail asks on every render to decide whether to show the door,
 * and the settings page asks once somebody walks through it. Keeping the list
 * here means the rail can ask without pulling in the page — and the page, with
 * every administrative screen behind it, is fetched only when it is opened.
 *
 * Labels and permissions live here; what each tab renders lives with the page,
 * because a component import here would put every settings screen back into the
 * first load and undo the split.
 *
 * One list, because deciding it twice is how a rail ends up hiding a door to a
 * room that exists — and someone who finds the URL wonders which of the two is
 * the bug. It had already drifted: the page offered six tabs and the rail's
 * copy knew about five, having never been told about Branding.
 */
export type SettingsTab = {
  id: string;
  label: string;
  /** The permission required to see it at all. */
  needs: string;
};

export const SETTINGS_TABS: SettingsTab[] = [
  { id: "plugins", label: "Plugins", needs: Perm.toolRead },
  { id: "assistant", label: "Assistant", needs: Perm.pluginConfigure },
  { id: "people", label: "People", needs: Perm.userManage },
  { id: "customers", label: "Customer list", needs: Perm.toolRead },
  { id: "branding", label: "Branding", needs: Perm.pluginConfigure },
  { id: "audit", label: "Activity log", needs: Perm.auditRead },
];

/** Which settings tabs a person has. */
export function settingsTabsFor(actor: Actor): string[] {
  return SETTINGS_TABS.filter((t) => actor.permissions.includes(t.needs)).map((t) => t.id);
}
