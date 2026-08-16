import { useEffect, useState } from "react";
import type { JsonSchema, SchemaProperty } from "./api";

/**
 * Renders a form from a plugin's published JSON Schema.
 *
 * There is deliberately nothing here about any particular integration. A
 * plugin declares its settings, and this draws them — that is the difference
 * between a modular system and one that merely has plugins.
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
    return <p className="muted small">This plugin has no settings.</p>;
  }

  const required = new Set(schema.required ?? []);
  const entries = Object.entries(schema.properties);

  return (
    <form
      className="schema-form"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(draft);
      }}
    >
      {entries.map(([name, prop]) => (
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

      <div className="form-actions">
        <button type="submit" disabled={busy}>
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
  const label = prop.title ?? name;
  const id = `field-${name}`;

  return (
    <div className="field">
      <label htmlFor={id}>
        {label}
        {required && <span className="req" aria-hidden="true"> *</span>}
        {isSecret && <span className="pill-secret">stored encrypted</span>}
      </label>

      {prop.description && <p className="muted small">{prop.description}</p>}

      {prop.enum ? (
        <select
          id={id}
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
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
      ) : prop.type === "number" || prop.type === "integer" ? (
        <input
          id={id}
          type="number"
          value={typeof value === "number" ? value : ""}
          onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))}
        />
      ) : (
        <input
          id={id}
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
        <button type="button" className="ghost small-btn" onClick={onClear}>
          Remove stored value
        </button>
      )}
    </div>
  );
}
