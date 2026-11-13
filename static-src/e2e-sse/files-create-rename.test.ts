// Against the REAL binary, in a real browser: the file browser's New file, New folder and Rename
// repeated in one folder each reach the server and make a new entry, so neither the server's
// Idempotency-Key replay cache (internal/server/idempotency.go) nor an existing entry under the
// proposed name, listed or created since, turns a repeat gesture into a no-op.
// Chromium by default; FILES_ENGINES=1 adds Firefox and WebKit (`npx playwright install firefox webkit`).

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { afterAll, beforeAll, beforeEach, describe, expect, inject, it } from "vitest";
import { type Browser, type BrowserType, type Page, chromium, firefox, webkit } from "playwright";

const FIXTURE = process.env["SSE_FIXTURE"];

const ENGINES: readonly (readonly [string, BrowserType])[] =
  process.env["FILES_ENGINES"] === "1"
    ? [
        ["chromium", chromium],
        ["firefox", firefox],
        ["webkit", webkit],
      ]
    : [["chromium", chromium]];

const STEP_TIMEOUT_MS = 5_000;

const rowSelector = (name: string): string => `#fb-list .fb-row[data-name=${JSON.stringify(name)}]`;

async function rowNames(page: Page): Promise<string[]> {
  return page
    .locator("#fb-list .fb-row[data-name]")
    .evaluateAll((rows) => rows.map((r) => (r as HTMLElement).dataset["name"] ?? ""));
}

/** New file or New folder from the toolbar, named through the inline rename it opens. The
 *  toolbar folds both into a menu only on a narrow viewport. */
async function create(page: Page, kind: "file" | "folder", name: string): Promise<void> {
  if (await page.locator(".fb-new-trigger").isVisible()) {
    await page.click(".fb-new-trigger");
  }
  await page.click(kind === "file" ? "#fb-new-file" : "#fb-new-folder");
  const input = page.locator(".fb-name-edit");
  await input.waitFor({ timeout: STEP_TIMEOUT_MS });
  await input.fill(name);
  await input.press("Enter");
  await page.locator(rowSelector(name)).waitFor({ timeout: STEP_TIMEOUT_MS });
}

/** New file or New folder kept under the name it proposes, as Enter on the untouched field does. */
async function createKeepingName(
  page: Page,
  kind: "file" | "folder",
  expected: string,
): Promise<void> {
  if (await page.locator(".fb-new-trigger").isVisible()) {
    await page.click(".fb-new-trigger");
  }
  await page.click(kind === "file" ? "#fb-new-file" : "#fb-new-folder");
  const input = page.locator(`${rowSelector(expected)} .fb-name-edit`);
  await input.waitFor({ timeout: STEP_TIMEOUT_MS });
  await input.press("Enter");
  await page.locator(`${rowSelector(expected)} .fb-name`).waitFor({ timeout: STEP_TIMEOUT_MS });
}

async function rename(page: Page, from: string, to: string): Promise<void> {
  await page.locator(`${rowSelector(from)} .fb-check`).click();
  await page.click("#fb-rename");
  const input = page.locator(".fb-name-edit");
  await input.waitFor({ timeout: STEP_TIMEOUT_MS });
  await input.fill(to);
  await input.press("Enter");
  await page.locator(rowSelector(to)).waitFor({ timeout: STEP_TIMEOUT_MS });
  await page.locator(rowSelector(from)).waitFor({ state: "detached", timeout: STEP_TIMEOUT_MS });
}

async function editAndSave(page: Page, name: string, text: string): Promise<void> {
  await page.locator(`${rowSelector(name)} .fb-name`).click();
  await page.locator("#editor-view").waitFor({ timeout: STEP_TIMEOUT_MS });
  await page.click("#editor-edit-btn");
  await page.locator("#editor-content").fill(text);
  await page.click("#editor-save-btn");
  await expect.poll(() => page.locator("#editor-save-btn").isDisabled()).toBe(true);
  await page.click('[data-tab-id][data-kind="files"]');
  await page.locator(rowSelector(name)).waitFor({ timeout: STEP_TIMEOUT_MS });
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("file browser repeated gestures", () => {
  const base = (): string => inject("marotteURL");
  let folder = "";

  beforeEach(() => {
    folder = join(inject("marotteWorkDir"), `files-${Math.random().toString(36).slice(2, 10)}`);
    mkdirSync(folder);
  });

  for (const [name, engine] of ENGINES) {
    describe(name, () => {
      let browser: Browser;

      beforeAll(async () => {
        browser = await engine.launch(name === "chromium" ? { channel: "chromium" } : {});
      });

      afterAll(async () => {
        await browser.close();
      });

      async function openFolder(): Promise<Page> {
        // The file browser needs no service worker, and WebKit's Playwright build on Linux closes
        // the page on pushManager.getSubscription(), which boot calls once the worker is ready.
        const ctx = await browser.newContext({ serviceWorkers: "block" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(STEP_TIMEOUT_MS);
        await page.goto(`${base()}/files${folder}`);
        await page.locator("#fb-list").waitFor();
        // The fixture has no kiro-cli sign-in, so boot raises the login modal over the browser.
        const login = page.locator("#login-modal");
        await login.waitFor();
        await page.keyboard.press("Escape");
        await login.waitFor({ state: "hidden" });
        return page;
      }

      it("starts a second New file after the first was named, edited and saved", async () => {
        const page = await openFolder();
        await create(page, "file", "first.txt");
        await editAndSave(page, "first.txt", "hello");
        await create(page, "file", "second.txt");
        expect(await rowNames(page)).toEqual(["first.txt", "second.txt"]);
        expect(readFileSync(join(folder, "first.txt"), "utf8")).toBe("hello");
        expect(existsSync(join(folder, "second.txt"))).toBe(true);
        await page.context().close();
      });

      it("starts a second New folder after the first was named", async () => {
        const page = await openFolder();
        await create(page, "folder", "one");
        await create(page, "folder", "two");
        expect(await rowNames(page)).toEqual(["one", "two"]);
        await page.context().close();
      });

      it("starts a second New file after the first kept its proposed name and was edited and saved", async () => {
        const page = await openFolder();
        await createKeepingName(page, "file", "new file");
        await editAndSave(page, "new file", "hello");
        await createKeepingName(page, "file", "new file 2");
        expect(await rowNames(page)).toEqual(["new file", "new file 2"]);
        expect(readFileSync(join(folder, "new file"), "utf8")).toBe("hello");
        expect(readFileSync(join(folder, "new file 2"), "utf8")).toBe("");
        await page.context().close();
      });

      it("starts a second New folder after the first kept its proposed name", async () => {
        const page = await openFolder();
        await createKeepingName(page, "folder", "new folder");
        await createKeepingName(page, "folder", "new folder 2");
        expect(await rowNames(page)).toEqual(["new folder", "new folder 2"]);
        expect(existsSync(join(folder, "new folder 2"))).toBe(true);
        await page.context().close();
      });

      it("starts New file on the next free name when another writer took the proposed one", async () => {
        const page = await openFolder();
        writeFileSync(join(folder, "new file"), "theirs");
        await createKeepingName(page, "file", "new file 2");
        expect(await rowNames(page)).toEqual(["new file", "new file 2"]);
        expect(readFileSync(join(folder, "new file"), "utf8")).toBe("theirs");
        expect(readFileSync(join(folder, "new file 2"), "utf8")).toBe("");
        await page.context().close();
      });

      it("renames A to B, back to A, then to B again", async () => {
        const page = await openFolder();
        await create(page, "file", "a.txt");
        await rename(page, "a.txt", "b.txt");
        await rename(page, "b.txt", "a.txt");
        await rename(page, "a.txt", "b.txt");
        expect(existsSync(join(folder, "b.txt"))).toBe(true);
        expect(existsSync(join(folder, "a.txt"))).toBe(false);
        await page.context().close();
      });
    });
  }
});
