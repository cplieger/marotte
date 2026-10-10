import { buildPath, parseRoute } from "./route-path.js";
import type { Route } from "./route-path.js";

/** Where a route came from, because the three answer "this names nothing that is open"
 *  differently. */
export type RouteOrigin = "deeplink" | "history" | "restore";

/** Whether the document was RESTORED rather than navigated to. */
export function navigationOrigin(): RouteOrigin {
  const [entry] = performance.getEntriesByType("navigation") as PerformanceNavigationTiming[];
  return entry?.type === "reload" || entry?.type === "back_forward" ? "restore" : "deeplink";
}

/** How many callers are currently suppressing pushes. A COUNT, not a flag, because the boot's
 *  regions run concurrently now: the settings restore and the tab restore each open a window,
 *  and with a boolean whichever closed first un-suppressed the other's — so a restore's own
 *  activation pushed a URL. */
let suppressDepth = 0;

export function suppressPush(v: boolean): void {
  suppressDepth = v ? suppressDepth + 1 : Math.max(0, suppressDepth - 1);
}

/** A CLAIM rather than a second suppression window: a window silences every push, a claim silences
 *  only a push to a DIFFERENT location, so the claimed location's own activation still lands. */
let claimedPath = "";

/** Claim a location for the duration of applying it. `releaseLocation` in a `finally`; a second
 *  claim replaces the first, which is what a mid-boot re-entry means. The claim is a PATHNAME,
 *  so a push that only moves the fragment is a real move inside the claimed location and stays
 *  admissible. */
export function claimLocation(path: string): void {
  claimedPath = pathnameOf(path);
}

export function releaseLocation(): void {
  claimedPath = "";
}

function pathnameOf(path: string): string {
  const hash = path.indexOf("#");
  return hash === -1 ? path : path.slice(0, hash);
}

export function pushRoute(route: Route): void {
  if (suppressDepth > 0) {
    return;
  }
  const target = buildPath(route);
  const current = location.pathname + location.hash;
  if (target === current) {
    return;
  }
  // Guards `pushRoute` ONLY: a replace cannot leave a history entry, which is the whole defect, and
  // `applyInitialRoute`'s own canonicalisation is a replace.
  if (claimedPath !== "" && pathnameOf(target) !== claimedPath) {
    return;
  }
  // A push that only DROPS the current fragment REPLACES instead.
  if (target === location.pathname && location.hash !== "") {
    history.replaceState(null, "", target);
    return;
  }
  history.pushState(null, "", target);
}

export function replaceRoute(route: Route): void {
  if (suppressDepth > 0) {
    return;
  }
  const target = buildPath(route);
  const current = location.pathname + location.hash;
  if (target !== current) {
    history.replaceState(null, "", target);
  }
}

let popstateHandler: ((route: Route) => void) | undefined;

export function onPopState(handler: (route: Route) => void): void {
  popstateHandler = handler;
}

let cachedKey = "";
let cachedRoute: Route | undefined;

window.addEventListener("popstate", () => {
  if (popstateHandler !== undefined) {
    const key = location.pathname + location.hash;
    if (key === cachedKey && cachedRoute !== undefined) {
      popstateHandler(cachedRoute);
      return;
    }
    const route = parseRoute(location.pathname, location.hash);
    cachedKey = key;
    cachedRoute = route;
    popstateHandler(route);
  }
});
