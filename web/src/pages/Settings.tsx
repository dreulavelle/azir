import { Perm, type Actor } from "../api";
import { Tabs } from "../components";
import type { Route } from "../router";
import { AssistantSettings } from "../AssistantSettings";
import { Plugins } from "../Plugins";
import { Users } from "../Users";
import { Audit } from "../Audit";
import { BrandingSettings } from "../BrandingSettings";
import { Customers as CustomerSpine } from "../Customers";

/**
 * Administration, out of the way.
 *
 * Everything here configures Azir rather than doing the job Azir exists for.
 * Keeping it behind one door is what stops the product from looking like a
 * settings panel that happens to have a helpdesk attached.
 */

type Tab = { id: string; label: string; needs: string; render: () => React.ReactNode };

/**
 * Which settings tabs this person has.
 *
 * Exported so the navigation can ask the same question the page answers.
 * Deciding it in two places is how a rail ends up hiding a door to a room that
 * exists — and someone who finds the URL wonders which of the two is the bug.
 */
export function settingsTabsFor(actor: Actor): string[] {
  return TAB_PERMISSIONS.filter(([, needs]) => actor.permissions.includes(needs)).map(([id]) => id);
}

const TAB_PERMISSIONS: [string, string][] = [
  ["plugins", Perm.toolRead],
  ["assistant", Perm.pluginConfigure],
  ["people", Perm.userManage],
  ["customers", Perm.toolRead],
  ["audit", Perm.auditRead],
];

export function Settings({
  actor,
  tab,
  go,
}: {
  actor: Actor;
  tab?: string;
  go: (to: Route) => void;
}) {
  const tabs: Tab[] = [
    {
      id: "plugins",
      label: "Plugins",
      needs: Perm.toolRead,
      render: () => <Plugins actor={actor} />,
    },
    {
      id: "assistant",
      label: "Assistant",
      needs: Perm.pluginConfigure,
      render: () => <AssistantSettings />,
    },
    {
      id: "people",
      label: "People",
      needs: Perm.userManage,
      render: () => <Users actor={actor} />,
    },
    {
      id: "customers",
      label: "Customer list",
      needs: Perm.toolRead,
      render: () => <CustomerSpine actor={actor} />,
    },
    {
      id: "branding",
      label: "Branding",
      needs: Perm.pluginConfigure,
      render: () => <BrandingSettings />,
    },
    {
      id: "audit",
      label: "Activity log",
      needs: Perm.auditRead,
      render: () => <Audit />,
    },
  ].filter((t) => actor.permissions.includes(t.needs));

  const current = tabs.find((t) => t.id === tab) ?? tabs[0];

  if (!current) {
    return (
      <div className="mx-auto max-w-[1180px] px-6 py-6">
        <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="mt-1 text-sm text-ink-dim">Your account does not have access to any settings. An administrator can change that.</p>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-[1180px] px-6 py-6">
      <h1 className="mb-4 text-2xl font-semibold tracking-tight">Settings</h1>

      <div style={{ marginBottom: 22 }}>
        <Tabs
          value={current.id}
          onChange={(id) => go({ name: "settings", tab: id })}
          tabs={tabs.map((t) => ({ id: t.id, label: t.label }))}
        />
      </div>

      {current.render()}
    </div>
  );
}
