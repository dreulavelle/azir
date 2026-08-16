import { useState } from "react";
import { Plugins } from "./Plugins";
import { Customers } from "./Customers";
import { Audit } from "./Audit";

const TABS = ["Plugins", "Customers", "Audit"] as const;
type Tab = (typeof TABS)[number];

export function App() {
  const [tab, setTab] = useState<Tab>("Plugins");

  return (
    <main>
      <header>
        <h1>Azir</h1>
        <p className="tagline">administration</p>
        <nav>
          {TABS.map((t) => (
            <button
              key={t}
              className={t === tab ? "tab active" : "tab"}
              aria-current={t === tab ? "page" : undefined}
              onClick={() => setTab(t)}
            >
              {t}
            </button>
          ))}
        </nav>
      </header>

      {tab === "Plugins" && <Plugins />}
      {tab === "Customers" && <Customers />}
      {tab === "Audit" && <Audit />}
    </main>
  );
}
