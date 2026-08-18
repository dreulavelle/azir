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
  { id: "users", label: "Users", needs: Perm.userManage },
  // Beside Users rather than inside it: who works here is a weekly question,
  // and what a technician is trusted with is one somebody decides once. The
  // second was being read as a footnote to the first.
  { id: "roles", label: "Roles", needs: Perm.userManage },
  // Its own room rather than a panel at the bottom of Users. The two answer
  // different questions — Users is who exists and what they may do, this is
  // how anyone gets in at all — and they are not even guarded by the same
  // permission, so living together meant an administrator who could configure
  // sign-on but not manage accounts could never reach it.
  { id: "auth", label: "Authentication", needs: Perm.pluginConfigure },
  { id: "branding", label: "Branding", needs: Perm.pluginConfigure },
  { id: "data", label: "Data", needs: Perm.dataManage },
  { id: "audit", label: "Activity log", needs: Perm.auditRead },
];

/** Which settings tabs a person has. */
export function settingsTabsFor(actor: Actor): string[] {
  return SETTINGS_TABS.filter((t) => actor.permissions.includes(t.needs)).map((t) => t.id);
}
