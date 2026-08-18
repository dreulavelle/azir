import { type Actor } from "../api";
import { SETTINGS_TABS } from "./settingsTabs";
import { Tabs } from "../components";
import type { Route } from "../router";
import { AssistantSettings } from "../AssistantSettings";
import { Plugins } from "../Plugins";
import { Users } from "../Users";
import { Roles } from "../Roles";
import { Audit } from "../Audit";
import { BrandingSettings } from "../BrandingSettings";
import { DataSettings } from "../DataSettings";
import { SignOn } from "../SignOn";

/**
 * Administration, out of the way.
 *
 * Everything here configures Azir rather than doing the job Azir exists for.
 * Keeping it behind one door is what stops the product from looking like a
 * settings panel that happens to have a helpdesk attached.
 */

type Tab = { id: string; label: string; needs: string; render: () => React.ReactNode };


export function Settings({
  actor,
  tab,
  go,
}: {
  actor: Actor;
  tab?: string;
  go: (to: Route) => void;
}) {
  // What each tab shows. The list of tabs and who may see them lives in
  // settingsTabs, so the navigation rail and this page cannot disagree about
  // which rooms exist.
  const render: Record<string, () => React.ReactNode> = {
    plugins: () => <Plugins actor={actor} />,
    assistant: () => <AssistantSettings />,
    users: () => <Users actor={actor} />,
    roles: () => <Roles />,
    auth: () => <SignOn />,
    branding: () => <BrandingSettings />,
    data: () => <DataSettings />,
    audit: () => <Audit />,
  };

  const tabs: Tab[] = SETTINGS_TABS.filter((t) => actor.permissions.includes(t.needs)).map((t) => ({
    ...t,
    render: render[t.id],
  }));

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

      <div className="mb-5">
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
