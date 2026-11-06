// The Clock choice (device-view.ts) over each language list, at every site the reader sees a time.

import { afterEach, describe, expect, it, vi } from "vitest";
import { buildSteerNote } from "./fundamentals/steer-note.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import {
  clock,
  formatClock,
  formatFullStamp,
  formatWeekdayStamp,
  redrawClocks,
  reloadClock,
  resolvedClock,
  setClock,
} from "./locale-time.js";
import {
  restoreBrowserLanguages,
  setBrowserLanguages,
} from "./__test-helpers__/browser-languages.js";

const WHEN = new Date(2026, 9, 9, 13, 50, 17).getTime();
const MORNING = new Date(2026, 9, 9, 9, 5, 3).getTime();
const TWELVE_HOUR = /\b(?:AM|PM)\b/;

/** ICU may put a narrow no-break space before AM/PM. */
const plain = (s: string | null | undefined): string => (s ?? "").replace(/\s/g, " ");

function sent(ms: number): string {
  const n = buildSteerNote({ text: "go", origin: "user", dropped: false, sentAt: ms });
  return plain(n.querySelector(".steer-note-times time")?.textContent);
}

function header(ms: number): string {
  const h = buildTurnHeader({ n: 1, outcome: "completed", ts: ms, request: "r", attachments: [] });
  return plain(h.querySelector(".turn-ts")?.textContent);
}

/** The engine's default locale as Firefox reports it with "Use your operating system settings"
 *  on and the OS format set to France. */
function browserDefaultIs(locale: string): void {
  const Real = Intl.DateTimeFormat;
  const Shadow = function (locales?: string | string[], options?: Intl.DateTimeFormatOptions) {
    const none = locales === undefined || (Array.isArray(locales) && locales.length === 0);
    return new Real(none ? locale : locales, options);
  };
  vi.stubGlobal(
    "Intl",
    Object.assign(Object.create(Intl) as typeof Intl, { DateTimeFormat: Shadow }),
  );
  dispatchEvent(new Event("languagechange"));
}

afterEach(() => {
  restoreBrowserLanguages();
  vi.unstubAllGlobals();
  dispatchEvent(new Event("languagechange"));
  setClock("auto");
  localStorage.clear();
});

describe.each(["en-US", "fr-FR"])("24-hour under %s", (lang) => {
  it("reads 13:50 and pads the morning to 09:05", () => {
    setBrowserLanguages([lang]);
    setClock("h23");
    expect(sent(WHEN)).toBe("13:50:17");
    expect(header(WHEN)).toBe("13:50");
    expect(header(MORNING)).toBe("09:05");
    expect(sent(MORNING)).toBe("09:05:03");
    expect(plain(formatFullStamp(WHEN))).toMatch(/ 13:50:17$/);
    expect(plain(formatFullStamp(MORNING))).toMatch(/ 09:05:03$/);
    expect(plain(formatWeekdayStamp(WHEN))).toMatch(/ 13:50$/);
    expect(plain(formatWeekdayStamp(MORNING))).toMatch(/ 09:05$/);
    expect(resolvedClock()).toBe("h23");
  });
});

describe.each(["en-US", "fr-FR"])("12-hour under %s", (lang) => {
  it("reads 1:50 PM at every site", () => {
    setBrowserLanguages([lang]);
    setClock("h12");
    expect(sent(WHEN)).toMatch(/^0?1:50:17 PM$/);
    expect(header(WHEN)).toMatch(/^0?1:50 PM$/);
    expect(header(MORNING)).toMatch(/^0?9:05 AM$/);
    expect(plain(formatFullStamp(WHEN))).toMatch(/ 0?1:50:17 PM$/);
    expect(plain(formatWeekdayStamp(WHEN))).toMatch(/ 0?1:50 PM$/);
    expect(resolvedClock()).toBe("h12");
  });
});

describe("Automatic", () => {
  it("follows the language list when the browser default agrees with it", () => {
    setBrowserLanguages(["fr-FR"]);
    expect(sent(WHEN)).toBe("13:50:17");
    expect(header(MORNING)).toBe("09:05");
    expect(formatWeekdayStamp(WHEN)).not.toMatch(TWELVE_HOUR);
    setBrowserLanguages(["en-US"]);
    browserDefaultIs("en-US");
    expect(sent(WHEN)).toBe("1:50:17 PM");
    expect(header(MORNING)).toBe("9:05 AM");
    expect(plain(formatFullStamp(WHEN))).toMatch(/ 1:50:17 PM$/);
    expect(plain(formatWeekdayStamp(WHEN))).toMatch(/ 1:50 PM$/);
    expect(resolvedClock()).toBe("h12");
  });

  it("is what a fresh screen reads, and what another tab's write is picked up as", () => {
    setBrowserLanguages(["en-US"]);
    browserDefaultIs("en-US");
    localStorage.setItem("marotte.ui-state", JSON.stringify({ clock: "h23" }));
    reloadClock();
    expect(clock()).toBe("h23");
    expect(header(WHEN)).toBe("13:50");
    localStorage.setItem("marotte.ui-state", JSON.stringify({ clock: "h25" }));
    reloadClock();
    expect(clock()).toBe("auto");
    expect(header(WHEN)).toBe("1:50 PM");
  });

  it("takes a 24-hour browser default over a 12-hour language, keeping the language's words", () => {
    setBrowserLanguages(["en-US", "en"]);
    browserDefaultIs("fr-FR");
    expect(sent(WHEN)).toBe("13:50:17");
    expect(header(MORNING)).toBe("09:05");
    expect(plain(formatWeekdayStamp(WHEN))).toMatch(/^Fri.* 13:50$/);
    expect(resolvedClock()).toBe("h23");
  });

  it("keeps a 24-hour language over a 12-hour browser default", () => {
    setBrowserLanguages(["fr-FR"]);
    browserDefaultIs("en-US");
    expect(header(WHEN)).toBe("13:50");
  });
});

describe("a Clock change", () => {
  it("redraws every live time, its tooltip included, in place", () => {
    setBrowserLanguages(["en-US"]);
    browserDefaultIs("en-US");
    const root = document.createElement("div");
    const note = buildSteerNote({ text: "go", origin: "user", dropped: false, sentAt: WHEN });
    const head = buildTurnHeader({
      n: 1,
      outcome: "completed",
      ts: WHEN,
      request: "r",
      attachments: [],
    });
    root.append(note, head);
    const stamp = note.querySelector(".steer-note-times time");
    expect(plain(stamp?.textContent)).toBe("1:50:17 PM");

    setClock("h23");
    redrawClocks(root);
    expect(plain(stamp?.textContent)).toBe("13:50:17");
    expect(plain(stamp?.getAttribute("data-tooltip"))).toMatch(/ 13:50:17$/);
    expect(plain(head.querySelector(".turn-ts")?.textContent)).toBe("13:50");
    expect(formatClock(WHEN)).toBe("13:50");
  });
});
