// The Clock row over the shipped markup: where it lives, the stored choice, the live repaint, and
// that the choice stays on this screen.

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { setupWorker } from "msw/browser";
import indexHtml from "../static/index.html?raw";

import { initClockSetting } from "./clock-setting.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { clock, reloadClock, setClock } from "./locale-time.js";
import {
  restoreBrowserLanguages,
  setBrowserLanguages,
} from "./__test-helpers__/browser-languages.js";

const WHEN = new Date(2026, 9, 9, 13, 50, 17).getTime();
const plain = (s: string | null | undefined): string => (s ?? "").replace(/\s/g, " ");
const page = new DOMParser().parseFromString(indexHtml, "text/html");

const worker = setupWorker();
const requests: string[] = [];

function generalPanel(): Element {
  const general = page.querySelector('[data-settings-panel="general"]');
  expect(general, "static/index.html has the General panel").not.toBeNull();
  return general!;
}

/** The General panel and a turn header, as the page holds them. */
function mount(): { select: HTMLSelectElement; ts: HTMLElement } {
  document.body.replaceChildren(document.importNode(generalPanel(), true));
  const head = buildTurnHeader({
    n: 1,
    outcome: "completed",
    ts: WHEN,
    request: "r",
    attachments: [],
  });
  document.body.append(head);
  const select = document.querySelector<HTMLSelectElement>("select#clock-choice");
  const ts = head.querySelector<HTMLElement>(".turn-ts");
  expect(select, "the General panel has the Clock select").not.toBeNull();
  expect(ts).not.toBeNull();
  return { select: select!, ts: ts! };
}

function pick(select: HTMLSelectElement, value: string): void {
  select.value = value;
  select.dispatchEvent(new Event("change", { bubbles: true }));
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

describe("the main menu", () => {
  it("has no Clock control", () => {
    const sidebar = page.getElementById("sidebar");
    expect(sidebar, "static/index.html has the sidebar").not.toBeNull();
    expect(page.getElementById("clock-btn")).toBeNull();
    const named = [...sidebar!.querySelectorAll("[aria-label], [data-tooltip]")].filter((e) =>
      /clock/i.test(
        `${e.getAttribute("aria-label") ?? ""} ${e.getAttribute("data-tooltip") ?? ""}`,
      ),
    );
    expect(named.map((e) => e.outerHTML.slice(0, 80))).toEqual([]);
  });
});

describe("the Clock row in Settings > General", () => {
  it("renders the Clock row with the three choices and says it is this device's", () => {
    const general = generalPanel();
    const select = general.querySelector<HTMLSelectElement>("select#clock-choice");
    expect(select, "General has select#clock-choice").not.toBeNull();
    expect(general.querySelector('label[for="clock-choice"]')?.textContent).toBe("Clock");
    expect([...select!.options].map((o) => [o.value, o.textContent])).toEqual([
      ["auto", "Automatic"],
      ["h23", "24-hour"],
      ["h12", "12-hour"],
    ]);
    expect(select!.classList.contains("rf-select")).toBe(true);
    const hint = page.getElementById(select!.getAttribute("aria-describedby") ?? "");
    expect(hint?.textContent).toMatch(/this device only/);
  });

  it("shows a stored choice selected", () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ clock: "h12" }));
    reloadClock();
    const { select } = mount();
    initClockSetting();
    expect(select.value).toBe("h12");
    expect(select.selectedOptions[0]?.textContent).toBe("12-hour");
  });

  it("names what Automatic resolves to while it is the choice", () => {
    const { select } = mount();
    initClockSetting();
    expect(select.value).toBe("auto");
    expect(select.selectedOptions[0]?.textContent).toBe("Automatic (now 12-hour)");
    pick(select, "h23");
    expect(select.querySelector('option[value="auto"]')?.textContent).toBe("Automatic");
  });

  it("updates a rendered time on change, without a reload", () => {
    const { select, ts } = mount();
    initClockSetting();
    expect(plain(ts.textContent)).toBe("1:50 PM");

    pick(select, "h23");
    expect(clock()).toBe("h23");
    expect(plain(ts.textContent)).toBe("13:50");

    pick(select, "h12");
    expect(clock()).toBe("h12");
    expect(plain(ts.textContent)).toBe("1:50 PM");
  });

  it("keeps the choice in this screen's storage and sends nothing to the server", async () => {
    const { select } = mount();
    initClockSetting();
    pick(select, "h23");
    expect(JSON.parse(localStorage.getItem(LS_UI_STATE_KEY) ?? "{}")).toMatchObject({
      clock: "h23",
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(requests).toEqual([]);
  });

  it("follows another tab's choice", () => {
    const { select, ts } = mount();
    initClockSetting();
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ clock: "h23" }));
    dispatchEvent(new StorageEvent("storage", { key: LS_UI_STATE_KEY }));
    expect(plain(ts.textContent)).toBe("13:50");
    expect(select.value).toBe("h23");
  });
});
