import { useCallback, useEffect, useState } from "react";
import { api, type Customer } from "./api";

export function Customers() {
  const [customers, setCustomers] = useState<Customer[] | null>(null);
  const [name, setName] = useState("");
  const [note, setNote] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setCustomers(await api.customers());
    } catch (e) {
      setNote(e instanceof Error ? e.message : "could not load customers");
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
      setNote(e instanceof Error ? e.message : "could not create customer");
    }
  }

  return (
    <section className="card">
      <h2>Customers</h2>
      <p className="muted small">
        Azir owns this entity; external systems map onto it. A customer with no
        identities is valid — a walk-in with no PSA record still gets memory.
      </p>

      <form className="inline-form" onSubmit={(e) => void create(e)}>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Customer name"
          aria-label="Customer name"
        />
        <button type="submit">Add</button>
      </form>

      {note && <p className="error">{note}</p>}

      {customers && customers.length === 0 && (
        <p className="muted small">No customers yet.</p>
      )}

      {customers && customers.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Linked systems</th>
            </tr>
          </thead>
          <tbody>
            {customers.map((c) => (
              <tr key={c.id}>
                <td>
                  <strong>{c.display_name}</strong>
                  <div className="mono small muted">{c.id}</div>
                </td>
                <td className="small">
                  {c.identities.length === 0 ? (
                    <span className="muted">none</span>
                  ) : (
                    c.identities.map((i) => (
                      <div key={i.plugin} className="mono">
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
    </section>
  );
}
