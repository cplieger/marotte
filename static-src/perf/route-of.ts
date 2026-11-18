// The route a measured request is counted under, shared by the tier 2 driver (view.mjs) and the
// tier 1 read-budget harness (__test-helpers__/read-budget.ts), so both count one name per route.

const ID_SEGMENTS: readonly (readonly [RegExp, string])[] = [
  // A forge id names its host and port, which differ per run; canned.py FORGE_ID is the twin.
  [/^\/api\/forges\/(?!(?:inventory|oauth|login|detect)(?:\/|$))[^/]+/, "/api/forges/{forge}"],
  [/\/(steps|turns|tools)\/[^/]+/g, "/$1/{id}"],
  [/wf_[0-9a-z]+/g, "{run}"],
  [/c-[0-9a-f]{32}/g, "{chat}"],
  [/b[0-9a-f]{31}/g, "{tab}"],
  [/sess_[0-9a-f-]+/g, "{session}"],
];

/** A request's route: method, path with its ids named, and its query keys (`view` keeps its value,
 *  since it picks the answer's shape). tests/perf/measure/servercpu.py replays one URL per route. */
export function routeOf(method: string, url: string, origin: string): string {
  const u = new URL(url, origin);
  let p = u.pathname;
  for (const [re, name] of ID_SEGMENTS) {
    p = p.replace(re, name);
  }
  const keys = [...new Set(u.searchParams.keys())]
    .sort()
    .map((k) => (k === "view" ? `view=${u.searchParams.get(k) ?? ""}` : k));
  return `${method} ${p}${keys.length ? `?${keys.join("&")}` : ""}`;
}
