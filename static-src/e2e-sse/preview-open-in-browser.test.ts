// Against the REAL binary, in a real browser: the page Open in browser shows in a new top-level tab.
// The tab must stay an opaque origin that cannot read marotte's cookies or reach /api, load its
// folder under the preview frame's rules, and download on a click, which the frame never does.
// Chromium by default; PREVIEW_ENGINES=1 adds Firefox and WebKit
// (`npx playwright install firefox webkit`).

import { mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, inject, it } from "vitest";
import { type Browser, type BrowserType, type Page, chromium, firefox, webkit } from "playwright";

const FIXTURE = process.env["SSE_FIXTURE"];
if (FIXTURE === undefined || FIXTURE === "") {
  console.warn(
    "[vitest] marotte fixture not started: set SSE_FIXTURE to a marotte binary built with -tags marotte_test",
  );
}

/** web-view.ts `SANDBOX`, the preview frame's attribute. */
const FRAME_SANDBOX = "allow-scripts allow-forms allow-modals";

const ENGINES: readonly (readonly [string, BrowserType])[] =
  process.env["PREVIEW_ENGINES"] === "1"
    ? [
        ["chromium", chromium],
        ["firefox", firefox],
        ["webkit", webkit],
      ]
    : [["chromium", chromium]];

const ONE_PIXEL_PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=",
  "base64",
);

// Every check the page can make of itself, written to #out once all have run.
const PAGE = `<!doctype html><html><head><meta charset="utf-8"><title>open in browser</title>
<link rel="stylesheet" href="style.css"><script src="app.js"></script></head><body>
<img id="img" src="img.png" alt=""><a id="dl" href="file.vtt" download>file.vtt</a><pre id="out"></pre>
<script>
(async () => {
  const status = (u) => fetch(u).then((r) => r.status, () => "rejected");
  let cookie;
  try { cookie = "read:" + document.cookie; } catch (e) { cookie = "threw:" + e.name; }
  const img = document.getElementById("img");
  await img.decode().catch(() => {});
  document.getElementById("out").textContent = JSON.stringify({
    origin: self.origin,
    css: getComputedStyle(document.body).getPropertyValue("--probe").trim(),
    js: self.probeScript === true,
    img: img.naturalWidth,
    data: await fetch("data.json").then((r) => r.json()).then((j) => j.a, () => "rejected"),
    dotfile: await status(".env"),
    symlink: await status("alias.json"),
    outside: await status("../other/secret.txt"),
    api: await fetch("/api/test/sse", { credentials: "include" }).then((r) => r.status, () => "rejected"),
    cookie,
  });
})();
</script></body></html>`;

interface PageReport {
  origin: string;
  css: string;
  js: boolean;
  img: number;
  data: unknown;
  dotfile: unknown;
  symlink: unknown;
  outside: unknown;
  api: unknown;
  cookie: string;
}

async function report(page: Page, frameSelector?: string): Promise<PageReport> {
  const out =
    frameSelector === undefined
      ? page.locator("#out:not(:empty)")
      : page.frameLocator(frameSelector).locator("#out:not(:empty)");
  return JSON.parse((await out.textContent({ timeout: 10_000 })) ?? "") as PageReport;
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("Open in browser", () => {
  const base = (): string => inject("marotteURL");
  let pagePath = "";
  let grantURL = "";

  beforeAll(async () => {
    const work = inject("marotteWorkDir");
    const folder = join(work, `open-${Math.random().toString(36).slice(2, 10)}`);
    mkdirSync(folder);
    mkdirSync(join(work, "other"), { recursive: true });
    pagePath = join(folder, "page.html");
    writeFileSync(pagePath, PAGE);
    writeFileSync(join(folder, "style.css"), "body { --probe: loaded; }");
    writeFileSync(join(folder, "app.js"), "self.probeScript = true;");
    writeFileSync(join(folder, "img.png"), ONE_PIXEL_PNG);
    writeFileSync(join(folder, "data.json"), '{"a":1}');
    writeFileSync(join(folder, "file.vtt"), "WEBVTT\n\n00:00.000 --> 00:01.000\nprobe\n");
    writeFileSync(join(folder, ".env"), "SECRET=1");
    symlinkSync("data.json", join(folder, "alias.json"));
    writeFileSync(join(work, "other", "secret.txt"), "outside");
    const res = await fetch(`${base()}/api/preview/grant`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ path: pagePath }),
    });
    expect(res.status).toBe(200);
    grantURL = ((await res.json()) as { url: string }).url;
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

      /** A context holding a cookie for marotte's origin, with marotte's page at `/__host`
       *  answered by the test: `body` is that page's markup. */
      async function hostPage(body: string): Promise<Page> {
        const ctx = await browser.newContext({ acceptDownloads: true });
        await ctx.addCookies([{ name: "marotte_probe", value: "cookie-secret", url: base() }]);
        const page = await ctx.newPage();
        await page.route(`${base()}/__host`, (route) =>
          route.fulfill({
            contentType: "text/html",
            body: `<!doctype html><meta charset=utf-8>${body}`,
          }),
        );
        await page.goto(`${base()}/__host`);
        return page;
      }

      it("opens an opaque-origin tab that loads its folder, cannot read the cookie or the API, and downloads on a click", async () => {
        const page = await hostPage(
          `<a id="open" href="${grantURL}" target="_blank" rel="noopener noreferrer">Open in browser</a>`,
        );
        const [tab] = await Promise.all([page.context().waitForEvent("page"), page.click("#open")]);
        const r = await report(tab);
        expect(r).toEqual({
          origin: "null",
          css: "loaded",
          js: true,
          img: 1,
          data: 1,
          dotfile: 404,
          symlink: 404,
          outside: "rejected",
          api: "rejected",
          cookie: expect.not.stringContaining("cookie-secret"),
        });
        expect(await tab.evaluate(() => window.opener === null)).toBe(true);

        const [download] = await Promise.all([
          tab.waitForEvent("download", { timeout: 5_000 }),
          tab.click("#dl"),
        ]);
        expect(download.suggestedFilename()).toBe("file.vtt");

        // A navigation is outside connect-src, so the server's fetch-metadata gate is what refuses it.
        const [api] = await Promise.all([
          tab.waitForResponse((res) => res.url().endsWith("/api/test/sse")),
          tab.evaluate(() => {
            location.assign("/api/test/sse");
          }),
        ]);
        expect(api.status()).toBe(403);
        await page.context().close();
      }, 30_000);

      it("keeps the preview frame to the same folder rules and starts no download from it", async () => {
        const page = await hostPage(
          `<iframe id="f" sandbox="${FRAME_SANDBOX}" src="${grantURL}"></iframe>`,
        );
        const r = await report(page, "#f");
        expect(r).toMatchObject({
          origin: "null",
          css: "loaded",
          js: true,
          img: 1,
          data: 1,
          dotfile: 404,
          symlink: 404,
          outside: "rejected",
          api: "rejected",
        });
        let downloads = 0;
        page.on("download", () => {
          downloads++;
        });
        const [served] = await Promise.all([
          page.waitForResponse((res) => res.url().endsWith("/file.vtt")),
          page.frameLocator("#f").locator("#dl").click(),
        ]);
        expect(served.status()).toBe(200);
        // The engine decides on the served answer; a download it starts is reported within this window.
        await page.waitForTimeout(1500);
        expect(downloads).toBe(0);
        await page.context().close();
      }, 30_000);
    });
  }
});
