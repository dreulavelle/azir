import { useCallback, useEffect, useState } from "react";
import { api, Perm, type Actor, type Customer } from "./api";
import { Empty, Icon, Problem } from "./ui";

/**
 * Azir's own customer records.
 *
 * Distinct from the Customers screen in the product, which shows whoever the
 * connected system knows about. This is the spine those map onto: Azir owns the
 * entity, so a customer with no external identity is still valid — a walk-in
 * with no PSA record still gets memory.
 */
export function Customers({ actor }: { actor: Actor }) {
  const [customers, setCustomers] = useState<Customer[] | null>(null);
  const [name, setName] = useState("");
  const [note, setNote] = useState<string | null>(null);

  const mayManage = actor.permissions.includes(Perm.customerManage);

  const load = useCallback(async () => {
    try {
      setCustomers(await api.customers());
    } catch (e) {
      setNote(e instanceof Error ? e.message : "could not load customer records");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    try {
      await api.createCustomer(name.trim());
      setName("");
      setNote(null);
      await load();
    } catch (e) {
      setNote(e instanceof Error ? e.message : "could not create the record");
    }
  }

  return (
    <section className="rounded-lg border border-edge bg-panel shadow-e1">
      <div className="flex items-baseline justify-between gap-3 border-b border-edge px-4 py-3">
        <h2>Customer records</h2>
        {customers && <span className="text-xs text-ink-faint">{customers.length}</span>}
      </div>
      <div className="p-4">
        <p className="text-xs text-ink-dim" style={{ maxWidth: "68ch" }}>
          Azir owns this entity; external systems map onto it. A record with no
          linked systems is valid — a walk-in with no PSA record still gets memory.
        </p>

        {mayManage && (
          <form className="flex items-center gap-2" style={{ marginBottom: 14 }} onSubmit={(e) => void create(e)}>
            <input
              className="h-8 w-full rounded-md border border-edge bg-sunken px-2.5 text-sm placeholder:text-ink-faint focus-visible:border-azir focus-visible:outline-none disabled:opacity-50"
              style={{ maxWidth: 280 }}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Customer name"
              aria-label="Customer name"
            />
            <button type="submit" className="h-8 rounded-md border border-edge bg-panel px-3 text-sm font-medium transition-colors hover:bg-sunken disabled:opacity-50">
              <Icon.plus />
              Add
            </button>
          </form>
        )}

        {note && <Problem>{note}</Problem>}
        {!customers && <div className="animate-pulse rounded-lg bg-sunken" style={{ height: 100 }} />}
        {customers && customers.length === 0 && <Empty headline="No customer records yet" />}

        {customers && customers.length > 0 && (
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th>Name</th>
                <th style={{ width: 300 }}>Linked systems</th>
              </tr>
            </thead>
            <tbody>
              {customers.map((c) => (
                <tr key={c.id} style={{ cursor: "default" }}>
                  <td>
                    <div style={{ fontWeight: 550 }}>{c.display_name}</div>
                    <div className="font-mono text-2xs tabular-nums text-ink-faint">{c.id}</div>
                  </td>
                  <td className="text-xs">
                    {c.identities.length === 0 ? (
                      <span className="text-ink-faint">none</span>
                    ) : (
                      c.identities.map((i) => (
                        <div key={i.plugin} className="font-mono text-xs text-ink-dim">
                          {i.plugin}:{i.external_id}
                        </div>
                      ))
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}
