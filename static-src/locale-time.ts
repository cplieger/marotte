// The one place the client turns an instant into a time or date string. `navigator.languages` is
// passed explicitly because Chromium's Intl default follows the browser's UI language, so a reader
// who prefers French on an English-UI browser would otherwise see 01:50 PM
// (https://developer.mozilla.org/en-US/docs/Web/API/Navigator/languages). No literal locale and no
// `hour12` here: the languages decide 12- or 24-hour.

function format(ms: number, options: Intl.DateTimeFormatOptions): string {
  if (!Number.isFinite(ms)) {
    return "";
  }
  return new Intl.DateTimeFormat(navigator.languages, options).format(ms);
}

/** The time of day: `13:50` in French, `1:50 PM` in US English; `13:50:17` and `1:50:17 PM` with seconds. */
export function formatClock(ms: number, opts: { seconds?: boolean } = {}): string {
  return format(ms, { timeStyle: opts.seconds === true ? "medium" : "short" });
}

/** A short date, such as `Oct 9`; the year only when `year` is set. */
export function formatShortDate(ms: number, opts: { year?: boolean } = {}): string {
  return format(ms, {
    month: "short",
    day: "numeric",
    ...(opts.year === true ? { year: "numeric" } : {}),
  });
}

/** The numeric date and the clock with seconds, for a tooltip that must name the exact instant. */
export function formatFullStamp(ms: number): string {
  return format(ms, {
    year: "numeric",
    month: "numeric",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
}

/** Weekday, day, short month and clock. */
export function formatWeekdayStamp(ms: number): string {
  return format(ms, {
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "numeric",
    minute: "2-digit",
  });
}
