// Against the REAL binary, in a real browser: the page Open in browser shows in a new top-level tab,
// served from the preview's tab route. The tab must stay an opaque origin that cannot read marotte's
// cookies or reach /api, load its folder under the preview frame's rules, and download on a click,
// which the frame never does, through either route. Each case runs on loopback and on a plain-HTTP
// origin that is not localhost, where browsers send no Fetch Metadata
// (https://w3c.github.io/webappsec-fetch-metadata/). Chromium by default; PREVIEW_ENGINES=1 adds
// Firefox and WebKit (`npx playwright install firefox webkit`).

import { mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import { type IncomingHttpHeaders, type Server, createServer, request } from "node:http";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, inject, it } from "vitest";
import {
  type Browser,
  type BrowserType,
  type LaunchOptions,
  type Page,
  chromium,
  firefox,
  webkit,
} from "playwright";

const FIXTURE = process.env["SSE_FIXTURE"];
if (FIXTURE === undefined || FIXTURE === "") {
  console.warn(
    "[vitest] marotte fixture not started: set SSE_FIXTURE to a marotte binary built with -tags marotte_test",
  );
}

/** web-view.ts `SANDBOX`, the preview frame's attribute. */
const FRAME_SANDBOX = "allow-scripts allow-forms allow-modals";

/** A name no engine treats as potentially trustworthy, reached through the proxy below. */
const PLAIN_HOST = "marotte.test";

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

const CONFINED = {
  origin: "null",
  css: "loaded",
  js: true,
  img: 1,
  data: 1,
  dotfile: 404,
  symlink: 404,
  outside: "rejected",
  api: "rejected",
};

async function report(page: Page, frameSelector?: string): Promise<PageReport> {
  const out =
    frameSelector === undefined
      ? page.locator("#out:not(:empty)")
      : page.frameLocator(frameSelector).locator("#out:not(:empty)");
  return JSON.parse((await out.textContent({ timeout: 10_000 })) ?? "") as PageReport;
}

/** A forward proxy that sends every request for PLAIN_HOST to marotte, Host kept, and records
 *  the headers each path last arrived with. */
async function startProxy(
  marotte: URL,
): Promise<{ server: Server; port: number; seen: Map<string, IncomingHttpHeaders> }> {
  const seen = new Map<string, IncomingHttpHeaders>();
  const server = createServer((req, res) => {
    const u = new URL(req.url ?? "/", "http://invalid");
    if (u.hostname !== PLAIN_HOST) {
      res.writeHead(502).end();
      return;
    }
    seen.set(u.pathname, req.headers);
    const upstream = request(
      {
        host: "127.0.0.1",
        port: marotte.port,
        method: req.method,
        path: u.pathname + u.search,
        headers: req.headers,
      },
      (up) => {
        res.writeHead(up.statusCode ?? 502, up.headers);
        up.pipe(res);
      },
    );
    upstream.on("error", () => {
      res.destroy();
    });
    req.pipe(upstream);
  });
  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error("could not read the proxy's port");
  }
  return { server, port: address.port, seen };
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("Open in browser", () => {
  const base = (): string => inject("marotteURL");
  let grantURL = "";
  let tabURL = "";
  let proxy: Awaited<ReturnType<typeof startProxy>>;

  beforeAll(async () => {
    const work = inject("marotteWorkDir");
    const folder = join(work, `open-${Math.random().toString(36).slice(2, 10)}`);
    mkdirSync(folder);
    mkdirSync(join(work, "other"), { recursive: true });
    const pagePath = join(folder, "page.html");
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
    const grant = (await res.json()) as { url: string; tab_url: string };
    grantURL = grant.url;
    tabURL = grant.tab_url;
    proxy = await startProxy(new URL(base()));
  });

  afterAll(async () => {
    await new Promise<void>((resolve) => {
      proxy.server.close(() => {
        resolve();
      });
    });
  });

  const ORIGINS = [
    { name: "loopback", plain: false, url: base, launch: (): LaunchOptions => ({}) },
    {
      name: "plain HTTP",
      plain: true,
      url: (): string => `http://${PLAIN_HOST}:${new URL(base()).port}`,
      launch: (): LaunchOptions => ({
        proxy: { server: `http://127.0.0.1:${String(proxy.port)}` },
      }),
    },
  ];

  for (const [name, engine] of ENGINES) {
    for (const origin of ORIGINS) {
      describe(`${name}, ${origin.name}`, () => {
        let browser: Browser;

        beforeAll(async () => {
          browser = await engine.launch({
            ...(name === "chromium" ? { channel: "chromium" } : {}),
            ...origin.launch(),
          });
        });

        afterAll(async () => {
          await browser.close();
        });

        /** A context holding a cookie for marotte's origin, with marotte's page at `/__host`
         *  answered by the test: `body` is that page's markup. */
        async function hostPage(body: string): Promise<Page> {
          const ctx = await browser.newContext({ acceptDownloads: true });
          await ctx.addCookies([
            { name: "marotte_probe", value: "cookie-secret", url: origin.url() },
          ]);
          const page = await ctx.newPage();
          await page.route(`${origin.url()}/__host`, (route) =>
            route.fulfill({
              contentType: "text/html",
              body: `<!doctype html><meta charset=utf-8>${body}`,
            }),
          );
          await page.goto(`${origin.url()}/__host`);
          return page;
        }

        async function openTab(): Promise<Page> {
          const page = await hostPage(
            `<a id="open" href="${tabURL}" target="_blank" rel="noopener noreferrer">Open in browser</a>`,
          );
          const [tab] = await Promise.all([
            page.context().waitForEvent("page"),
            page.click("#open"),
          ]);
          return tab;
        }

        /** Counts the downloads `page` starts, so a test can assert none came. */
        function countDownloads(page: Page): () => number {
          let downloads = 0;
          page.on("download", () => {
            downloads++;
          });
          return () => downloads;
        }

        it("opens an opaque-origin tab that loads its folder, cannot read the cookie or the API, and downloads on a click", async () => {
          const tab = await openTab();
          expect(await report(tab)).toEqual({
            ...CONFINED,
            cookie: expect.not.stringContaining("cookie-secret"),
          });
          expect(await tab.evaluate(() => window.opener === null)).toBe(true);
          const [download] = await Promise.all([
            tab.waitForEvent("download", { timeout: 5_000 }),
            tab.click("#dl"),
          ]);
          expect(download.suggestedFilename()).toBe("file.vtt");
          await tab.context().close();
        }, 30_000);

        it.runIf(origin.plain)(
          "reaches the tab with no fetch metadata, so the route alone grants the download",
          async () => {
            const tab = await openTab();
            await report(tab);
            const sent = proxy.seen.get(new URL(tabURL, origin.url()).pathname);
            expect(sent?.host).toBe(new URL(origin.url()).host);
            expect(sent?.["sec-fetch-dest"]).toBeUndefined();
            expect(sent?.["sec-fetch-site"]).toBeUndefined();
            await tab.context().close();
          },
          30_000,
        );

        // A navigation is outside connect-src, so the server's fetch-metadata gate is what refuses
        // it, and that gate needs the header only a potentially trustworthy origin is sent.
        it.runIf(!origin.plain)(
          "refuses the tab's own navigation to /api",
          async () => {
            const tab = await openTab();
            await report(tab);
            const [api] = await Promise.all([
              tab.waitForResponse((res) => res.url().endsWith("/api/test/sse")),
              tab.evaluate(() => {
                location.assign("/api/test/sse");
              }),
            ]);
            expect(api.status()).toBe(403);
            await tab.context().close();
          },
          30_000,
        );

        it("keeps the preview frame to the same folder rules and starts no download from it, through either route", async () => {
          const page = await hostPage(
            `<iframe id="f" sandbox="${FRAME_SANDBOX}" src="${grantURL}"></iframe>`,
          );
          expect(await report(page, "#f")).toMatchObject(CONFINED);
          const downloads = countDownloads(page);
          const [served] = await Promise.all([
            page.waitForResponse((res) =>
              res.url().endsWith(`${grantURL.replace(/[^/]*$/, "")}file.vtt`),
            ),
            page.frameLocator("#f").locator("#dl").click(),
          ]);
          expect(served.status()).toBe(200);
          const frame = await (await page.$("#f"))?.contentFrame();
          await frame?.evaluate(
            (href) => {
              const a = document.createElement("a");
              a.id = "tabdl";
              a.href = href;
              a.download = "";
              a.textContent = "tab route";
              document.body.append(a);
            },
            tabURL.replace(/[^/]*$/, "file.vtt"),
          );
          const [tabServed] = await Promise.all([
            page.waitForResponse((res) => res.url().endsWith(tabURL.replace(/[^/]*$/, "file.vtt"))),
            page.frameLocator("#f").locator("#tabdl").click(),
          ]);
          expect(tabServed.status()).toBe(200);
          // The engine decides on the served answer; a download it starts is reported within this window.
          await page.waitForTimeout(1500);
          expect(downloads()).toBe(0);
          await page.context().close();
        }, 30_000);

        it("never renders the tab route inside a frame, pointed there or navigating itself there", async () => {
          const page = await hostPage(
            `<iframe id="t" sandbox="${FRAME_SANDBOX}"></iframe><iframe id="g" sandbox="${FRAME_SANDBOX}" src="${grantURL}"></iframe>`,
          );
          expect(await report(page, "#g")).toMatchObject(CONFINED);
          // A rendered page fetches its own folder; Firefox gives Playwright no handle on a frame
          // whose document it refused, so these requests are the engine-neutral sign of a render.
          const tabBase = tabURL.replace(/[^/]*$/, "");
          const rendered: string[] = [];
          page.on("request", (req) => {
            if (req.url().includes(tabBase) && !req.url().endsWith(tabURL)) {
              rendered.push(req.url());
            }
          });
          await Promise.all([
            page.waitForRequest((req) => req.url().endsWith(tabURL), { timeout: 5_000 }),
            page.$eval(
              "#t",
              (f, src) => {
                (f as HTMLIFrameElement).src = src;
              },
              tabURL,
            ),
          ]);
          const frame = await (await page.$("#g"))?.contentFrame();
          await Promise.all([
            page.waitForRequest((req) => req.url().endsWith(tabURL), { timeout: 5_000 }),
            frame?.evaluate((href) => {
              location.assign(href);
            }, tabURL),
          ]);
          // The frame route's page loads its folder well within this window.
          await page.waitForTimeout(1500);
          expect(rendered).toEqual([]);
          await page.context().close();
        }, 30_000);
      });
    }
  }
});
