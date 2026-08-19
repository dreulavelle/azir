import { Button, Panel, PanelHead } from "../../ui";

/** One of the two ways in. */
export function Way({
  title,
  what,
  best,
  action,
  onPick,
}: {
  title: string;
  what: string;
  best: string;
  action: string;
  onPick: () => void;
}) {
  return (
    <Panel className="flex flex-col">
      <PanelHead>
        <h2 className="text-sm font-medium">{title}</h2>
      </PanelHead>
      <div className="flex flex-1 flex-col gap-1.5 px-4 py-4">
        <p className="text-sm">{what}</p>
        <p className="text-xs text-ink-faint">{best}</p>
      </div>
      <div className="border-t border-edge px-4 py-3">
        <Button weight="primary" onClick={onPick}>
          {action}
        </Button>
      </div>
    </Panel>
  );
}
