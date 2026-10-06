// The app's one relative-time vocabulary: relative under a day, a DATE beyond it. A coarse count
// (`2d ago`) loses the day it names; the full timestamp is the tooltip's (`absoluteTime`).

const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;

function plural(n: number, unit: string): string {
  return `${String(n)} ${unit}${n === 1 ? "" : "s"} ago`;
}

/** The relative form of a timestamp: `just now`, `N minutes ago`, `N hours ago`, then `Sep 12`,
 *  carrying the year when it differs from now's. Empty for an absent one (0), because a row with
 *  no timestamp has nothing to say rather than a time at the epoch. */
export function relativeTime(ms: number, now: number = Date.now()): string {
  if (!Number.isFinite(ms) || ms <= 0) {
    return "";
  }
  const delta = now - ms;
  if (delta < MINUTE_MS) {
    return "just now";
  }
  if (delta < HOUR_MS) {
    return plural(Math.floor(delta / MINUTE_MS), "minute");
  }
  if (delta < DAY_MS) {
    return plural(Math.floor(delta / HOUR_MS), "hour");
  }
  const then = new Date(ms);
  const sameYear = then.getFullYear() === new Date(now).getFullYear();
  return then.toLocaleDateString(
    undefined,
    sameYear
      ? { month: "short", day: "numeric" }
      : { month: "short", day: "numeric", year: "numeric" },
  );
}

/** The same timestamp in full, for the row's tooltip. The pair lives in one module so a row
 *  cannot show a relative time whose absolute twin is spelled differently somewhere else. */
export function absoluteTime(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) {
    return "";
  }
  return new Date(ms).toLocaleString();
}
