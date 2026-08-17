import { useEffect, useState } from "react";
import type { JsonSchema, SchemaProperty } from "./api";
import { Chip } from "./ui";

/**
 * Renders a form from a plugin's published JSON Schema.
 *
 * There is deliberately nothing here about any particular integration. A plugin
 * declares its settings and this draws them — that is the difference between a
 * modular system and one that merely has plugins.
 */
export function SchemaForm({
  schema,
  values,
  secretsSet,
  busy,
  onSave,
  onClearSecret,
}: {
  schema?: JsonSchema;
  values: Record<string, unknown>;
  secretsSet: Record<string, boolean>;
  busy?: boolean;
  onSave: (values: Record<string, unknown>) => void;
  onClearSecret?: (field: string) => void;
}) {
  const [draft, setDraft] = useState<Record<string, unknown>>(values);

  useEffect(() => setDraft(values), [values]);

  if (!schema?.properties || Object.keys(schema.properties).length === 0) {
    return (
      <p className="text-xs text-ink-faint">
        This plugin has no settings.
      </p>
    );
  }

  const required = new Set(schema.required ?? []);

  return (
    <form
      className="flex flex-col gap-4"
      style={{ gap: 16, maxWidth: 520 }}
      onSubmit={(e) => {
        e.preventDefault();
        onSave(draft);
      }}
    >
      {Object.entries(schema.properties).map(([name, prop]) => (
        <Field
          key={name}
          name={name}
          prop={prop}
          required={required.has(name)}
          value={draft[name]}
          secretStored={secretsSet[name] === true}
          onChange={(v) => setDraft((d) => ({ ...d, [name]: v }))}
          onClear={onClearSecret ? () => onClearSecret(name) : undefined}
        />
      ))}

      <div>
        <button type="submit" className="h-8 self-start rounded-md bg-azir px-3.5 text-sm font-medium text-azir-ink transition-opacity hover:opacity-90 disabled:opacity-50" disabled={busy}>
          {busy ? "Saving…" : "Save settings"}
        </button>
      </div>
    </form>
  );
}

function Field({
  name,
  prop,
  required,
  value,
  secretStored,
  onChange,
  onClear,
}: {
  name: string;
  prop: SchemaProperty;
  required: boolean;
  value: unknown;
  secretStored: boolean;
  onChange: (v: unknown) => void;
  onClear?: () => void;
}) {
  const isSecret = prop["x-azir-secret"] === true;
  const id = `field-${name}`;

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="flex items-center gap-2 text-sm font-medium">
        {prop.title ?? name}
        {required && (
          <span style={{ color: "var(--urgent)" }} aria-hidden="true">
            *
          </span>
        )}
        {isSecret && <Chip tone="accent">stored encrypted</Chip>}
      </label>

      {prop.description && <p className="max-w-[70ch] text-xs text-ink-faint">{prop.description}</p>}

      {prop.enum ? (
        <select
          id={id}
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">—</option>
          {prop.enum.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </select>
      ) : prop.type === "boolean" ? (
        <input
          id={id}
          type="checkbox"
          style={{ width: 16, height: 16 }}
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
      ) : prop.type === "number" || prop.type === "integer" ? (
        <input
          id={id}
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          type="number"
          value={typeof value === "number" ? value : ""}
          onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))}
        />
      ) : (
        <input
          id={id}
          className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
          type={isSecret ? "password" : "text"}
          autoComplete={isSecret ? "new-password" : "off"}
          // A stored secret is never sent back to the browser, so the input
          // starts empty and an empty value means "leave it alone" rather than
          // "delete it" — otherwise editing an unrelated field on this form
          // would wipe a working credential.
          placeholder={isSecret && secretStored ? "•••••••• stored — type to replace" : ""}
          value={typeof value === "string" ? value : ""}
          onChange={(e) => onChange(e.target.value)}
        />
      )}

      {isSecret && secretStored && onClear && (
        <div>
          <button type="button" className="self-start rounded-md px-1.5 py-1 text-xs text-ink-dim transition-colors hover:bg-critical/10 hover:text-critical" onClick={onClear}>
            Remove stored value
          </button>
        </div>
      )}
    </div>
  );
}
