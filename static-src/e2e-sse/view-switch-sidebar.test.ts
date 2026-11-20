// Against the REAL binary, in a real browser: a tab switch paints the view it switches to. On Chrome
// before 158, a style change inside `#chat-area` (an inline-size container) made in the same frame as a
// `:has()` re-match on a size or inline-size container in the sidebar is never applied: the parked
// view keeps its old box and the new one gets none, so the screen stops changing. Set
// VIEW_SWITCH_CHROMIUM to such a Chrome (Chrome for Testing 156: `npx @puppeteer/browsers install
// chrome@156.0.8078.4`) to run the cases where they fail; Playwright's own Chromium has the fix.

import { afterAll, beforeAll, describe, expect, inject, it } from "vitest";
import { type Browser, type Page, chromium } from "playwright";

const FIXTURE = process.env["SSE_FIXTURE"];
const EXECUTABLE = process.env["VIEW_SWITCH_CHROMIUM"];

const STEP_TIMEOUT_MS = 5_000;
/** A switch paints within a frame or two; one that has not in this long never will. */
const SWITCH_TIMEOUT_MS = 2_000;

interface TabRow {
  readonly id: string;
  readonly kind: string;
  readonly ref: string;
  readonly pinned: boolean;
}

/** What `#messages` holds: each transcript view's box, read in the page. */
interface ViewBox {
  readonly active: boolean;
  readonly rects: number;
  readonly height: number;
}

async function views(page: Page): Promise<ViewBox[]> {
  return page.locator("#messages > .transcript-view").evaluateAll((els) =>
    els.map((el) => ({
      active: el.classList.contains("is-active"),
      rects: el.getClientRects().length,
      height: Math.round(el.getBoundingClientRect().height),
    })),
  );
}

/** The active view has a box and every parked view has none: the switch reached the screen. */
async function switched(page: Page): Promise<boolean> {
  const all = await views(page);
  const active = all.filter((v) => v.active);
  return (
    active.length === 1 &&
    active.every((v) => v.rects > 0 && v.height > 0) &&
    all.every((v) => v.active || v.height === 0)
  );
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("a tab switch paints its view", () => {
  const base = (): string => inject("marotteURL");
  let browser: Browser;

  async function command(type: string, payload: unknown): Promise<unknown> {
    const res = await fetch(`${base()}/api/command`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin" },
      body: JSON.stringify({ type, chat_id: "", payload }),
    });
    if (!res.ok) {
      throw new Error(`${type} answered ${String(res.status)}: ${await res.text()}`);
    }
    return res.json();
  }

  async function tabs(): Promise<TabRow[]> {
    const res = await fetch(`${base()}/api/tabs`);
    return ((await res.json()) as { tabs: TabRow[] }).tabs;
  }

  /** A chat with a model, so its composer draws the model pill: a chat with none never trips it. */
  async function chat(name: string): Promise<string> {
    const body = (await command("create_chat", { name, model: "model-a" })) as {
      chat: { id: string };
    };
    const id = body.chat.id;
    if (!(await tabs()).some((t) => t.kind === "chat" && t.ref === id)) {
      await command("open_tab", { kind: "chat", ref: id });
    }
    return id;
  }

  /** Puts `refs`' tabs last in the Open list in that order, then pins `pin`'s tab. */
  async function arrange(refs: readonly string[], pin: string): Promise<Map<string, string>> {
    const open = await tabs();
    const ids = new Map(refs.map((ref) => [ref, open.find((t) => t.ref === ref)?.id ?? ""]));
    const mine = new Set(ids.values());
    await command("reorder_tabs", {
      order: [...open.map((t) => t.id).filter((id) => !mine.has(id)), ...ids.values()],
    });
    await command("pin_tab", { id: ids.get(pin), pinned: true });
    return ids;
  }

  async function open(path: string): Promise<Page> {
    const page = await browser.newPage();
    page.setDefaultTimeout(STEP_TIMEOUT_MS);
    await page.goto(`${base()}${path}`);
    // The fixture has no kiro-cli sign-in, so boot raises the login modal over the app.
    const login = page.locator("#login-modal");
    await login.waitFor();
    await page.keyboard.press("Escape");
    await login.waitFor({ state: "hidden" });
    return page;
  }

  beforeAll(async () => {
    browser = await chromium.launch(
      EXECUTABLE === undefined || EXECUTABLE === ""
        ? { channel: "chromium" }
        : { executablePath: EXECUTABLE },
    );
  });

  afterAll(async () => {
    await browser.close();
  });

  it("paints a pinned chat chosen from an Open chat that has a tab above it", async () => {
    const tag = Math.random().toString(36).slice(2, 8);
    const above = await chat(`above ${tag}`);
    const from = await chat(`from ${tag}`);
    const to = await chat(`to ${tag}`);
    const ids = await arrange([above, from, to], to);
    const page = await open(`/chat/${from}`);
    await expect.poll(() => switched(page)).toBe(true);

    await page.click(`#pinned-list [data-tab-id="${ids.get(to) ?? ""}"]`);
    await page.waitForURL(`**/chat/${to}`);
    await expect.poll(() => switched(page), { timeout: SWITCH_TIMEOUT_MS }).toBe(true);
    await page.close();
  }, 30_000);

  // The case above passes on a fixed Chrome whatever the sheet says, so this one holds the trigger's
  // precondition in every browser.
  it("makes no box outside the chat area a size or inline-size container", async () => {
    const page = await open("/");
    const sizers = await page.evaluate(() =>
      [...document.querySelectorAll("body *")]
        .filter(
          (e) =>
            e.closest("#chat-area") === null &&
            getComputedStyle(e)
              .containerType.split(" ")
              .some((t) => t === "size" || t === "inline-size"),
        )
        .map((e) => (e.id === "" ? e.className : `#${e.id}`)),
    );
    expect(sizers).toEqual([]);
    await page.close();
  }, 30_000);
});
