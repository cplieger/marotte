// Every time the UI paints follows `navigator.languages`: 24-hour under fr-FR, 12-hour under en-US
// (locale-time.ts owns why).

import { describe, it, expect, afterEach } from "vitest";
import { buildSteerNote } from "./fundamentals/steer-note.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { renderPanel } from "./turn-info-panel.js";
import { absoluteTime, relativeTime } from "./relative-time.js";
import { formatDate } from "./files-shared.js";
import { describeSpec, formatStamp } from "./schedule-picker.js";
import {
  restoreBrowserLanguages,
  setBrowserLanguages,
} from "./__test-helpers__/browser-languages.js";

const WHEN = new Date(2026, 9, 9, 13, 50, 17).getTime();
const TWELVE_HOUR = /\b(?:AM|PM)\b/i;

/** ICU may put a narrow no-break space before AM/PM. */
const plain = (s: string | null | undefined): string => (s ?? "").replace(/\s/g, " ");

function stampOf(sentAt: number): string | null {
  const n = buildSteerNote({ text: "go", origin: "user", dropped: false, sentAt });
  return n.querySelector(".steer-note-times time")?.textContent ?? null;
}

function headerClock(ts: number): string | null {
  const h = buildTurnHeader({ n: 75, outcome: "completed", ts, request: "r", attachments: [] });
  return h.querySelector(".turn-ts")?.textContent ?? null;
}

function startedClock(stamp: number): string | null {
  const panel = document.createElement("div");
  renderPanel(panel, [
    { title: "Turn", rows: [{ kind: "fact", key: "started", label: "Started", value: { stamp } }] },
  ]);
  return panel.querySelector("time")?.textContent ?? null;
}

afterEach(restoreBrowserLanguages);

describe("under fr-FR", () => {
  it("shows the steer note's Sent time on a 24-hour clock with seconds", () => {
    setBrowserLanguages(["fr-FR", "en-US"]);
    expect(plain(stampOf(WHEN))).toBe("13:50:17");
  });

  it("shows the turn header's time as 13:50", () => {
    setBrowserLanguages(["fr-FR"]);
    expect(plain(headerClock(WHEN))).toBe("13:50");
  });

  it("shows the turn panel's Started time as 13:50", () => {
    setBrowserLanguages(["fr-FR"]);
    expect(plain(startedClock(WHEN))).toBe("13:50");
  });

  it("shows every date-and-time string without AM or PM", () => {
    setBrowserLanguages(["fr-FR"]);
    const iso = new Date(WHEN).toISOString();
    const shown = [
      absoluteTime(WHEN),
      formatDate(WHEN),
      formatStamp(iso),
      relativeTime(WHEN - 40 * 86_400_000, WHEN),
    ];
    expect(plain(shown[0])).toContain("13:50:17");
    expect(plain(shown[1])).toContain("13:50");
    expect(plain(shown[2])).toContain("13:50");
    expect(shown[3]).toBe("30 août");
    for (const s of shown.slice(0, 3)) {
      expect(s).not.toMatch(TWELVE_HOUR);
    }
  });
});

describe("under en-US", () => {
  it("keeps the 12-hour clock at every site", () => {
    setBrowserLanguages(["en-US"]);
    const iso = new Date(WHEN).toISOString();
    expect(plain(stampOf(WHEN))).toBe("1:50:17 PM");
    expect(plain(headerClock(WHEN))).toBe("1:50 PM");
    expect(plain(startedClock(WHEN))).toBe("1:50 PM");
    expect(plain(absoluteTime(WHEN))).toContain("1:50:17 PM");
    expect(plain(formatDate(WHEN))).toContain("1:50 PM");
    expect(plain(formatStamp(iso))).toContain("1:50 PM");
    expect(relativeTime(WHEN - 40 * 86_400_000, WHEN)).toBe("Aug 30");
  });
});

describe("the schedule sentence", () => {
  const daily = { freq: "daily", hour: 14, minute: 0 } as const;

  it("reads 24-hour under fr-FR", () => {
    setBrowserLanguages(["fr-FR"]);
    expect(describeSpec(daily)).toBe("Every day at 14:00");
  });

  it("reads 12-hour under en-US", () => {
    setBrowserLanguages(["en-US"]);
    expect(plain(describeSpec(daily))).toBe("Every day at 2:00 PM");
    expect(plain(describeSpec({ freq: "monthly", month_day: 3, hour: 3, minute: 5 }))).toBe(
      "Monthly on the 3rd at 3:05 AM",
    );
  });
});
