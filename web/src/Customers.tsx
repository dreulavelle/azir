import { useCallback, useEffect, useState } from "react";
import { api, Perm, type Actor, type Customer } from "./api";
import { Empty, Icon, PanelHead, Problem, TextInput } from "./ui";

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
      <PanelHead>
        <h2>Customer records</h2>
        {customers && <span className="text-xs text-ink-faint">{customers.length}</span>}
      </PanelHead>
      <div className="p-4">
        <p className="max-w-[68ch] text-xs text-ink-dim">
          Azir owns this entity; external systems map onto it. A record with no
          linked systems is valid — a walk-in with no PSA record still gets memory.
        </p>

        {mayManage && (
          <form className="mb-3.5 flex items-center gap-2" onSubmit={(e) => void create(e)}>
            <TextInput
              className="max-w-[280px]"
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
        {!customers && <div className="h-25 animate-pulse rounded-lg bg-sunken" />}
        {customers && customers.length === 0 && <Empty headline="No customer records yet" />}

        {customers && customers.length > 0 && (
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th>Name</th>
                <th className="w-[300px]">Linked systems</th>
              </tr>
            </thead>
            <tbody>
              {customers.map((c) => (
                <tr key={c.id} className="cursor-default">
                  <td>
                    <div className="font-medium">{c.display_name}</div>
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
