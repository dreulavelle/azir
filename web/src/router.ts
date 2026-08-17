import { useCallback, useEffect, useState } from "react";

/**
 * A router in forty lines, because every view deserves a URL.
 *
 * A technician who finds something worth another person's attention should be
 * able to send them a link to it, and come back to it tomorrow from history.
 * That is worth more than any routing library's feature list, and it is all
 * this application needs.
 */

/** How the ticket list is ordered. Longest untouched first, by default. */
export type TicketSort = "idle" | "newest" | "priority";

/**
 * Whose queue is on screen.
 *
 * "yours" is the working view and the default: what is on you, then what is on
 * nobody. Other people's tickets are left out of it entirely — a queue you
 * cannot act on is a queue you learn to scroll past. "everyone" is the whole
 * helpdesk, for when the question is about the team rather than about today.
 */
export type TicketOwner = "yours" | "everyone";

export type Route =
  | { name: "triage" }
  | {
      name: "tickets";
      query?: string;
      status?: string;
      /** Include tickets that are finished. Off unless asked for. */
      includeDone?: boolean;
      owner?: TicketOwner;
      sort?: TicketSort;
    }
  | { name: "ticket"; id: string }
  | { name: "customers"; query?: string }
  | { name: "customer"; id: string }
  | { name: "chats" }
  | { name: "diagnostics" }
  | { name: "snapshot"; id: string }
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
            // Only the non-default state is spelled in the URL, so a plain
            // /tickets link means the same thing to everyone who opens it.
            includeDone: params.get("show") === "all" || undefined,
            owner: (params.get("who") as TicketOwner) || undefined,
            sort: (params.get("sort") as TicketSort) || undefined,
          };
    case "customers":
      return tail
        ? { name: "customer", id: tail }
        : { name: "customers", query: params.get("q") ?? undefined };
    case "chats":
      return { name: "chats" };
    case "diagnostics":
      return tail ? { name: "snapshot", id: tail } : { name: "diagnostics" };
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
      if (route.includeDone) params.set("show", "all");
      if (route.owner && route.owner !== "yours") params.set("who", route.owner);
      if (route.sort && route.sort !== "idle") params.set("sort", route.sort);
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
    case "diagnostics":
      return "/diagnostics";
    case "snapshot":
      return `/diagnostics/${route.id}`;
    case "settings":
      return `/settings/${route.tab ?? "plugins"}`;
  }
}

/**
 * Whether there is somewhere inside the application to go back to.
 *
 * A "back" control that leaves the application is worse than no back control,
 * and nothing the browser exposes answers this: history.length counts entries
 * from before this site was opened, and a referrer is absent on a fresh tab. So
 * the one thing that does know — our own navigation — records it.
 */
let navigated = false;
export function canGoBack(): boolean {
  return navigated;
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
    if (!replace) navigated = true;
    setRoute(to);
    if (!replace) window.scrollTo(0, 0);
  }, []);

  return [route, go];
}
