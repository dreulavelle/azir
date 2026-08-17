import { useCallback, useEffect, useState } from "react";

/**
 * A router in forty lines, because every view deserves a URL.
 *
 * A technician who finds something worth another person's attention should be
 * able to send them a link to it, and come back to it tomorrow from history.
 * That is worth more than any routing library's feature list, and it is all
 * this application needs.
 */

export type Route =
  | { name: "triage" }
  | { name: "tickets"; query?: string; status?: string }
  | { name: "ticket"; id: string }
  | { name: "customers"; query?: string }
  | { name: "customer"; id: string }
  | { name: "chats" }
  | { name: "settings"; tab?: string };

export function parse(path: string, search: string): Route {
  const params = new URLSearchParams(search);
  const [, head, tail] = path.split("/");

  switch (head) {
    case "tickets":
      return tail
        ? { name: "ticket", id: tail }
        : {
            name: "tickets",
            query: params.get("q") ?? undefined,
            status: params.get("status") ?? undefined,
          };
    case "customers":
      return tail
        ? { name: "customer", id: tail }
        : { name: "customers", query: params.get("q") ?? undefined };
    case "chats":
      return { name: "chats" };
    case "settings":
      return { name: "settings", tab: tail || "plugins" };
    default:
      return { name: "triage" };
  }
}

export function href(route: Route): string {
  switch (route.name) {
    case "triage":
      return "/";
    case "tickets": {
      const params = new URLSearchParams();
      if (route.query) params.set("q", route.query);
      if (route.status) params.set("status", route.status);
      const qs = params.toString();
      return qs ? `/tickets?${qs}` : "/tickets";
    }
    case "ticket":
      return `/tickets/${route.id}`;
    case "customers":
      return route.query ? `/customers?q=${encodeURIComponent(route.query)}` : "/customers";
    case "customer":
      return `/customers/${route.id}`;
    case "chats":
      return "/chats";
    case "settings":
      return `/settings/${route.tab ?? "plugins"}`;
  }
}

export function useRoute(): [Route, (to: Route, replace?: boolean) => void] {
  const [route, setRoute] = useState<Route>(() =>
    parse(window.location.pathname, window.location.search),
  );

  useEffect(() => {
    const onPop = () => setRoute(parse(window.location.pathname, window.location.search));
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const go = useCallback((to: Route, replace = false) => {
    const url = href(to);
    // replace, for things like a filter changing as someone types: those
    // should not each become a separate press of the back button.
    window.history[replace ? "replaceState" : "pushState"]({}, "", url);
    setRoute(to);
    if (!replace) window.scrollTo(0, 0);
  }, []);

  return [route, go];
}
