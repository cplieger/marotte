import { describe, it, expect, afterEach } from "vitest";
import {
  formatClock,
  formatFullStamp,
  formatShortDate,
  formatWeekdayStamp,
} from "./locale-time.js";
import {
  restoreBrowserLanguages,
  setBrowserLanguages,
} from "./__test-helpers__/browser-languages.js";

const WHEN = new Date(2026, 9, 9, 13, 50, 17).getTime();
const MORNING = new Date(2026, 9, 9, 9, 5, 3).getTime();

const plain = (s: string): string => s.replace(/\s/g, " ");

afterEach(restoreBrowserLanguages);

describe("formatClock", () => {
  it("reads 24-hour under fr-FR, with and without seconds", () => {
    setBrowserLanguages(["fr-FR"]);
    expect(formatClock(WHEN)).toBe("13:50");
    expect(formatClock(WHEN, { seconds: true })).toBe("13:50:17");
    expect(formatClock(MORNING)).toBe("9:05");
  });

  it("reads 12-hour under en-US, with and without seconds", () => {
    setBrowserLanguages(["en-US"]);
    expect(plain(formatClock(WHEN))).toBe("1:50 PM");
    expect(plain(formatClock(WHEN, { seconds: true }))).toBe("1:50:17 PM");
    expect(plain(formatClock(MORNING))).toBe("9:05 AM");
  });

  it("takes the first supported language of the list", () => {
    setBrowserLanguages(["fr-FR", "en-US"]);
    expect(formatClock(WHEN)).toBe("13:50");
    setBrowserLanguages(["en-US", "fr-FR"]);
    expect(plain(formatClock(WHEN))).toBe("1:50 PM");
  });

  it("falls back to the engine default when the browser reports no language", () => {
    setBrowserLanguages([]);
    expect(formatClock(WHEN)).toMatch(/^\d{1,2}:50/);
  });

  it("answers nothing for an instant that is not a number", () => {
    expect(formatClock(Number.NaN)).toBe("");
    expect(formatFullStamp(Number.POSITIVE_INFINITY)).toBe("");
  });
});

describe("the date forms", () => {
  it("carry a 24-hour clock under fr-FR and a 12-hour one under en-US", () => {
    setBrowserLanguages(["fr-FR"]);
    expect(formatFullStamp(WHEN)).toContain("13:50:17");
    expect(formatWeekdayStamp(WHEN)).toContain("13:50");
    setBrowserLanguages(["en-US"]);
    expect(plain(formatFullStamp(WHEN))).toContain("1:50:17 PM");
    expect(plain(formatWeekdayStamp(WHEN))).toContain("1:50 PM");
  });

  it("spells the short date in the reader's language, with the year only on request", () => {
    setBrowserLanguages(["en-US"]);
    expect(formatShortDate(WHEN)).toBe("Oct 9");
    expect(formatShortDate(WHEN, { year: true })).toBe("Oct 9, 2026");
    setBrowserLanguages(["fr-FR"]);
    expect(formatShortDate(WHEN)).toMatch(/^9 oct/);
    expect(formatShortDate(WHEN, { year: true })).toMatch(/2026$/);
  });
});
