// The Clock toggle over the shipped markup: the cycle, the live repaint, and that the choice stays
// on this screen.

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { setupWorker } from "msw/browser";
import indexHtml from "../static/index.html?raw";

import { initClockToggle } from "./clock-toggle.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { clock, setClock } from "./locale-time.js";
import {
  restoreBrowserLanguages,
  setBrowserLanguages,
} from "./__test-helpers__/browser-languages.js";

const WHEN = new Date(2026, 9, 9, 13, 50, 17).getTime();
const plain = (s: string | null | undefined): string => (s ?? "").replace(/\s/g, " ");

const worker = setupWorker();
const requests: string[] = [];

function mount(): { btn: HTMLButtonElement; ts: HTMLElement } {
  const page = new DOMParser().parseFromString(indexHtml, "text/html");
  const markup = page.getElementById("clock-btn");
  expect(markup, "static/index.html has #clock-btn").not.toBeNull();
  document.body.replaceChildren(document.importNode(markup!, true));
  const head = buildTurnHeader({
    n: 1,
    outcome: "completed",
    ts: WHEN,
    request: "r",
    attachments: [],
  });
  document.body.append(head);
  const ts = head.querySelector<HTMLElement>(".turn-ts");
  expect(ts).not.toBeNull();
  return { btn: document.getElementById("clock-btn") as HTMLButtonElement, ts: ts! };
}

function face(btn: HTMLElement): string[] {
  return [...btn.querySelectorAll("svg:not(.hidden)")].map((s) => s.getAttribute("class") ?? "");
}

beforeAll(async () => {
  await worker.start({ quiet: true });
  worker.events.on("request:start", ({ request }) => {
    if (new URL(request.url).pathname.startsWith("/api/")) {
      requests.push(request.url);
    }
  });
});

afterAll(() => {
  worker.stop();
});

beforeEach(() => {
  setBrowserLanguages(["en-US"]);
  requests.length = 0;
});

afterEach(() => {
  setClock("auto");
  restoreBrowserLanguages();
  localStorage.clear();
  document.body.replaceChildren();
});

describe("the Clock toggle", () => {
  it("cycles Automatic, 24-hour, 12-hour and repaints the turn header each time", () => {
    const { btn, ts } = mount();
    initClockToggle();
    expect(face(btn)).toEqual(["clock-icon-auto ic-ui"]);
    expect(btn.getAttribute("aria-label")).toBe(
      "Clock: automatic (now 12-hour). Switch to 24-hour",
    );
    expect(plain(ts.textContent)).toBe("1:50 PM");

    btn.click();
    expect(clock()).toBe("h23");
    expect(plain(ts.textContent)).toBe("13:50");
    expect(btn.getAttribute("aria-label")).toBe("Clock: 24-hour. Switch to 12-hour");

    btn.click();
    expect(clock()).toBe("h12");
    expect(plain(ts.textContent)).toBe("1:50 PM");
    expect(btn.getAttribute("data-tooltip")).toBe("Clock: 12-hour. Switch to automatic");

    btn.click();
    expect(clock()).toBe("auto");
  });

  it("keeps the choice in this screen's storage and sends nothing to the server", async () => {
    const { btn } = mount();
    initClockToggle();
    btn.click();
    expect(JSON.parse(localStorage.getItem(LS_UI_STATE_KEY) ?? "{}")).toMatchObject({
      clock: "h23",
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(requests).toEqual([]);
  });

  it("follows another tab's choice", () => {
    const { btn, ts } = mount();
    initClockToggle();
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ clock: "h23" }));
    dispatchEvent(new StorageEvent("storage", { key: LS_UI_STATE_KEY }));
    expect(plain(ts.textContent)).toBe("13:50");
    expect(btn.getAttribute("aria-label")).toBe("Clock: 24-hour. Switch to 12-hour");
  });
});
