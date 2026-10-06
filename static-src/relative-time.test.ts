import { describe, it, expect } from "vitest";

import { relativeTime, absoluteTime } from "./relative-time.js";

// The one vocabulary, rung by rung, plus the two boundaries a formatter gets wrong: the day
// boundary (a date, not a count, from 24 hours on) and the year boundary (the year appears exactly
// when it differs from now's).

const at = (y: number, m: number, d: number, h = 0, min = 0, s = 0): number =>
  new Date(y, m - 1, d, h, min, s, 0).getTime();

describe("relativeTime", () => {
  it("answers nothing for an absent timestamp", () => {
    const now = at(2026, 9, 12, 10);
    expect(relativeTime(0, now)).toBe("");
    expect(relativeTime(Number.NaN, now)).toBe("");
  });

  it("reads `just now` under a minute", () => {
    const now = at(2026, 9, 12, 10);
    expect(relativeTime(now, now)).toBe("just now");
    expect(relativeTime(now - 59_000, now)).toBe("just now");
  });

  it("counts whole minutes to the hour, singular at one", () => {
    const now = at(2026, 9, 12, 10);
    expect(relativeTime(now - 60_000, now)).toBe("1 minute ago");
    expect(relativeTime(now - 5 * 60_000, now)).toBe("5 minutes ago");
    expect(relativeTime(now - 59 * 60_000, now)).toBe("59 minutes ago");
  });

  it("counts whole hours to the day, singular at one", () => {
    const now = at(2026, 9, 12, 10);
    expect(relativeTime(now - 60 * 60_000, now)).toBe("1 hour ago");
    expect(relativeTime(at(2026, 9, 12, 7), now)).toBe("3 hours ago");
    expect(relativeTime(now - 23 * 60 * 60_000, now)).toBe("23 hours ago");
    // The previous calendar day is still HOURS while it is under 24 of them.
    expect(relativeTime(at(2026, 9, 11, 23, 59), at(2026, 9, 12, 2))).toBe("2 hours ago");
  });

  it("names the date from a day on, with no coarse count", () => {
    const now = at(2026, 9, 12, 10);
    expect(relativeTime(now - 24 * 60 * 60_000, now)).toBe("Sep 11");
    expect(relativeTime(at(2026, 9, 5, 9), now)).toBe("Sep 5");
    expect(relativeTime(at(2026, 6, 1, 9), now)).toBe("Jun 1");
  });

  it("adds the year to a date only when it differs from now's", () => {
    expect(relativeTime(at(2026, 1, 3, 9), at(2026, 3, 1, 10))).toBe("Jan 3");
    expect(relativeTime(at(2025, 12, 30, 9), at(2026, 1, 2, 10))).toBe("Dec 30, 2025");
  });
});

describe("absoluteTime", () => {
  it("spells the whole timestamp for the tooltip", () => {
    expect(absoluteTime(at(2026, 9, 12, 10))).toBe("9/12/2026, 10:00:00 AM");
  });

  it("answers nothing for an absent timestamp", () => {
    expect(absoluteTime(0)).toBe("");
  });
});
