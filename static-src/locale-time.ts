// The one place the client turns an instant into a time or date string. `navigator.languages` is
// passed explicitly because Chromium's Intl default follows the browser's UI language
// (https://developer.mozilla.org/en-US/docs/Web/API/Navigator/languages). No literal locale and no
// `hour12`. The Clock choice forces `hourCycle`; Automatic follows the languages, or a 24-hour
// browser default, where Firefox exposes the OS time format and no language list does.

import { clockChoice, setClockChoice, type ClockChoice } from "./device-view.js";

type Cycle = "h23" | "h12";

// A transcript formats hundreds of times per paint. The formatters are keyed by choice, languages
// and options; `languagechange` also drops the browser-default answer, which a language pref moves.
let choice: ClockChoice | null = null;
let default24: boolean | null = null;
const formatters = new Map<string, Intl.DateTimeFormat>();

function forget(): void {
  formatters.clear();
  default24 = null;
}

(globalThis as { addEventListener?: Window["addEventListener"] }).addEventListener?.(
  "languagechange",
  forget,
);

/** Whether the browser's default locale reads 24-hour. Firefox puts the OS regional format there
 *  (LC_TIME on Linux) when "Use your operating system settings" is on, never in
 *  `navigator.languages` (intl/locale/LocaleService.cpp `GetRegionalPrefsLocales`). */
function browserDefaultReads24(): boolean {
  if (default24 === null) {
    const cycle = new Intl.DateTimeFormat([], { hour: "numeric" }).resolvedOptions().hourCycle;
    default24 = cycle === "h23" || cycle === "h24";
  }
  return default24;
}

// Automatic lets a 24-hour browser default overrule a 12-hour language and never the reverse: a
// 12-hour default is the US-English UI fallback far more often than a stated regional format.
function build(options: Intl.DateTimeFormatOptions, picked: ClockChoice): Intl.DateTimeFormat {
  if (picked !== "auto") {
    return new Intl.DateTimeFormat(navigator.languages, { ...options, hourCycle: picked });
  }
  const f = new Intl.DateTimeFormat(navigator.languages, options);
  const cycle = f.resolvedOptions().hourCycle;
  if ((cycle !== "h11" && cycle !== "h12") || !browserDefaultReads24()) {
    return f;
  }
  return new Intl.DateTimeFormat(navigator.languages, { ...options, hourCycle: "h23" });
}

function formatter(options: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const picked = clock();
  const key = `${picked}|${navigator.languages.join(",")}|${JSON.stringify(options)}`;
  let f = formatters.get(key);
  if (f === undefined) {
    f = build(options, picked);
    formatters.set(key, f);
  }
  return f;
}

function format(ms: number, options: Intl.DateTimeFormatOptions): string {
  if (!Number.isFinite(ms)) {
    return "";
  }
  return formatter(options).format(ms);
}

/** This screen's Clock choice. */
export function clock(): ClockChoice {
  choice ??= clockChoice();
  return choice;
}

/** Record this screen's Clock choice; every later format follows it. `redrawClocks` repaints. */
export function setClock(next: ClockChoice): void {
  setClockChoice(next);
  choice = next;
  forget();
}

/** Re-read the choice from storage, after another tab of this screen wrote it. */
export function reloadClock(): void {
  choice = null;
  forget();
}

/** The hour cycle the clock draws in now, Automatic resolved. */
export function resolvedClock(): Cycle {
  const cycle = formatter({ timeStyle: "short" }).resolvedOptions().hourCycle;
  return cycle === "h11" || cycle === "h12" ? "h12" : "h23";
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

// A stamp joins a date-only part to formatClock's clock: `hour: "numeric"` reads 9:05 in French
// where the timeStyle clock reads 09:05, and `"2-digit"` would pad en-US to 09:05 AM.
function stamp(ms: number, date: Intl.DateTimeFormatOptions, seconds: boolean): string {
  const day = format(ms, date);
  return day === "" ? "" : `${day}, ${formatClock(ms, { seconds })}`;
}

/** The numeric date and the clock with seconds, for a tooltip that must name the exact instant. */
export function formatFullStamp(ms: number): string {
  return stamp(ms, { year: "numeric", month: "numeric", day: "numeric" }, true);
}

/** Weekday, day, short month and clock. */
export function formatWeekdayStamp(ms: number): string {
  return stamp(ms, { weekday: "short", day: "numeric", month: "short" }, false);
}

/** Redraw, from its `datetime`, every `<time>` under `root` that names its form: `data-clock` is
 *  `short` or `seconds` (`formatClock`), `data-clock-tip="full"` a `formatFullStamp` tooltip. So a
 *  Clock change reaches the times already on screen. */
export function redrawClocks(root: ParentNode): void {
  for (const t of root.querySelectorAll<HTMLTimeElement>(
    "time[data-clock], time[data-clock-tip]",
  )) {
    const ms = Date.parse(t.dateTime);
    if (!Number.isFinite(ms)) {
      continue;
    }
    const text = t.getAttribute("data-clock");
    if (text === "short" || text === "seconds") {
      t.textContent = formatClock(ms, { seconds: text === "seconds" });
    }
    if (t.getAttribute("data-clock-tip") === "full") {
      t.setAttribute("data-tooltip", formatFullStamp(ms));
    }
  }
}
