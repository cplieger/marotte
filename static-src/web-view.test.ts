import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import type { PreviewHint, PreviewStamp } from "./wire/types.gen.js";
import type { GrantOutcome } from "./actions/preview.js";
import type * as Tabs from "./tabs.js";
import type * as PreviewActions from "./actions/preview.js";

const kind = signal<string | null>("web");

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  getActiveTabKind: () => kind.value,
}));

const grants = vi.hoisted(() => ({
  hint: undefined as PreviewHint | undefined,
  refuse: null as string | null,
  epoch: "e1",
  stamp: "s1",
  n: 0,
  hold: false,
  held: [] as (() => void)[],
}));
const stamps = vi.hoisted(() => ({ next: null as PreviewStamp | null, calls: 0 }));

vi.mock("./actions/preview.js", async (importOriginal) => ({
  ...(await importOriginal<typeof PreviewActions>()),
  grantPreview: {
    dispatch: vi.fn((path: string): Promise<GrantOutcome> => {
      if (grants.refuse !== null) {
        return Promise.resolve({ kind: "refused", message: grants.refuse });
      }
      grants.n++;
      const name = path.slice(path.lastIndexOf("/") + 1);
      const out: GrantOutcome = {
        kind: "granted",
        grant: {
          url: `/preview/tok${String(grants.n)}/${encodeURIComponent(name)}`,
          base: `/preview/tok${String(grants.n)}/`,
          expires_at: "2026-10-03T12:00:00Z",
          epoch: grants.epoch,
          stamp: grants.stamp,
          ...(grants.hint === undefined ? {} : { hint: grants.hint }),
        },
      };
      if (!grants.hold) {
        return Promise.resolve(out);
      }
      return new Promise<GrantOutcome>((resolve) => {
        grants.held.push(() => {
          resolve(out);
        });
      });
    }),
  },
  previewStamp: {
    dispatch: vi.fn(() => {
      stamps.calls++;
      return Promise.resolve(stamps.next);
    }),
  },
}));

import { grantPreview } from "./actions/preview.js";
import { _resetForTest, releaseWebTab, showWebTab } from "./web-view.js";
import { LS_WEB_VIEWPORT_KEY, clearDeviceKeys } from "./ls-keys.js";

const grantCalls = (): number => vi.mocked(grantPreview.dispatch).mock.calls.length;

function mountView(): void {
  document.body.insertAdjacentHTML(
    "beforeend",
    `<div id="web-view" data-tab-view>
      <div id="web-stage" class="web-stage" style="width: 720px; height: 500px"></div>
      <div class="editor-toolbar bottom-bar"><div class="view-toolbar-inner">
        <span id="web-path" class="web-path"></span>
        <div class="web-actions">
          <div id="web-viewport" class="seg-bar web-viewport" role="radiogroup" aria-label="Preview width"></div>
          <button type="button" id="web-scale-btn" class="icon-btn web-scale-btn" aria-pressed="true" aria-label="Scale to fit"><span class="web-scale-readout">100%</span></button>
          <button type="button" id="web-reload-btn" class="icon-btn" aria-label="Reload preview"></button>
        </div>
      </div></div>
    </div>`,
  );
}

const stage = (): HTMLElement => document.getElementById("web-stage")!;
const frames = (): HTMLIFrameElement[] => [...stage().querySelectorAll("iframe")];
const wrappers = (): HTMLElement[] => [...stage().querySelectorAll<HTMLElement>(".web-frame")];
const radios = (): HTMLElement[] => [
  ...document.querySelectorAll<HTMLElement>('#web-viewport [role="radio"]'),
];
const checked = (): string | undefined =>
  radios().find((r) => r.getAttribute("aria-checked") === "true")?.dataset["mode"];
const scaleBtn = (): HTMLButtonElement =>
  document.getElementById("web-scale-btn") as HTMLButtonElement;

async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(20);
}
async function poll(stamp: PreviewStamp | null): Promise<void> {
  stamps.next = stamp;
  await vi.advanceTimersByTimeAsync(1000);
}
const st = (stamp: string, epoch = "e1"): PreviewStamp => ({
  stamp,
  epoch,
  entries: 1,
  truncated: false,
});

beforeAll(() => {
  mountView();
});

beforeEach(() => {
  vi.useFakeTimers();
  grants.hint = undefined;
  grants.refuse = null;
  grants.epoch = "e1";
  grants.stamp = "s1";
  grants.n = 0;
  grants.hold = false;
  grants.held = [];
  stamps.next = null;
  stamps.calls = 0;
  kind.value = "web";
  localStorage.removeItem(LS_WEB_VIEWPORT_KEY);
  vi.mocked(grantPreview.dispatch).mockClear();
});

afterEach(() => {
  _resetForTest();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("mounting", () => {
  it("mounts one sandboxed iframe whose src is the grant URL verbatim", async () => {
    showWebTab("/workspace/demo/my page.html");
    await flush();
    const [frame, ...rest] = frames();
    expect(rest).toHaveLength(0);
    expect(frame?.getAttribute("sandbox")).toBe("allow-scripts allow-forms allow-modals");
    expect(frame?.title).toBe("Preview of my page.html");
    expect(frame?.getAttribute("src")).toBe("/preview/tok1/my%20page.html");
  });

  it("does not reload a mounted frame on a second show", async () => {
    showWebTab("/workspace/demo/index.html");
    await flush();
    const first = frames()[0];
    showWebTab("/workspace/demo/index.html");
    await flush();
    expect(frames()[0]).toBe(first);
    expect(grantCalls()).toBe(1);
  });

  it("hides the other preview without re-parenting it", async () => {
    showWebTab("/workspace/a/index.html");
    await flush();
    const a = wrappers()[0]!;
    const aFrame = frames()[0];
    showWebTab("/workspace/b/index.html");
    await flush();
    expect(a.hidden).toBe(true);
    expect(a.parentElement).toBe(stage());
    expect(a.querySelector("iframe")).toBe(aFrame);
    expect(wrappers()[1]?.hidden).toBe(false);
  });

  it("unloads a preview hidden for five minutes and loads it fresh on return", async () => {
    showWebTab("/workspace/demo/index.html");
    await flush();
    kind.value = "chat";
    await vi.advanceTimersByTimeAsync(5 * 60_000 - 1000);
    expect(frames()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(frames()).toHaveLength(0);
    kind.value = "web";
    showWebTab("/workspace/demo/index.html");
    await flush();
    expect(grantCalls()).toBe(2);
    expect(frames()[0]?.getAttribute("src")).toBe("/preview/tok2/index.html");
  });

  it("unloads five minutes after leaving when the grant lands off-screen", async () => {
    grants.hold = true;
    showWebTab("/workspace/demo/index.html");
    await flush();
    kind.value = "chat";
    await flush();
    grants.held.shift()?.();
    await flush();
    expect(frames()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(frames()).toHaveLength(0);
  });

  it("unloads five minutes after leaving when a Retry's grant lands off-screen", async () => {
    grants.refuse = "the page could not be read";
    showWebTab("/workspace/demo/index.html");
    await flush();
    grants.refuse = null;
    grants.hold = true;
    stage().querySelector("button")!.click();
    await flush();
    kind.value = "chat";
    await flush();
    grants.held.shift()?.();
    await flush();
    expect(frames()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(frames()).toHaveLength(0);
  });

  it("keeps at most four frames mounted", async () => {
    for (const name of ["a", "b", "c", "d", "e"]) {
      showWebTab(`/workspace/${name}/index.html`);
      await flush();
    }
    expect(frames()).toHaveLength(4);
    expect(wrappers()[0]?.querySelector("iframe")).toBeNull();
  });

  it("removes the wrapper and stops polling on release", async () => {
    showWebTab("/workspace/demo/index.html");
    await flush();
    releaseWebTab("/workspace/demo/index.html");
    const calls = stamps.calls;
    await poll(st("x"));
    expect(wrappers()).toHaveLength(0);
    expect(stamps.calls).toBe(calls);
  });

  it("shows the server's refusal with a Retry that mints a fresh grant", async () => {
    grants.refuse = "Put the page in its own folder";
    showWebTab("/workspace/root.html");
    await flush();
    expect(stage().textContent).toContain("Put the page in its own folder");
    expect(frames()).toHaveLength(0);
    grants.refuse = null;
    stage().querySelector("button")!.click();
    await flush();
    expect(frames()).toHaveLength(1);
  });

  it("reloads by replacing the iframe element with a fresh grant", async () => {
    showWebTab("/workspace/demo/index.html");
    await flush();
    const before = frames()[0]!;
    const wrapper = before.parentElement;
    document.getElementById("web-reload-btn")!.click();
    await flush();
    const after = frames()[0]!;
    expect(after).not.toBe(before);
    expect(after.parentElement).toBe(wrapper);
    expect(after.getAttribute("src")).toBe("/preview/tok2/index.html");
  });
});

describe("toolbar", () => {
  it("scales a too-wide width and offers 100% instead", async () => {
    grants.hint = { preset: "desktop", source: "meta" };
    showWebTab("/workspace/demo/index.html");
    await flush();
    expect(scaleBtn().disabled).toBe(false);
    expect(scaleBtn().getAttribute("aria-pressed")).toBe("true");
    expect(scaleBtn().textContent).toBe("50%");
    scaleBtn().click();
    expect(scaleBtn().getAttribute("aria-pressed")).toBe("false");
    expect(scaleBtn().textContent).toBe("100%");
    expect(stage().hasAttribute("data-overflow")).toBe(true);
  });

  it("disables the scale toggle when the width fits", async () => {
    showWebTab("/workspace/demo/index.html");
    await flush();
    expect(checked()).toBe("fill");
    expect(scaleBtn().disabled).toBe(true);
    expect(scaleBtn().textContent).toBe("100%");
  });

  it("moves the pick with the arrow keys and stores it on this device", async () => {
    showWebTab("/workspace/demo/index.html");
    await flush();
    const fill = radios()[0]!;
    fill.focus();
    fill.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(checked()).toBe("phone");
    expect(
      radios()
        .filter((r) => r.classList.contains("active"))
        .map((r) => r.dataset["mode"]),
    ).toEqual(["phone"]);
    expect(
      radios()
        .filter((r) => r.tabIndex === 0)
        .map((r) => r.dataset["mode"]),
    ).toEqual(["phone"]);
    expect(JSON.parse(localStorage.getItem(LS_WEB_VIEWPORT_KEY)!)).toEqual({
      "/workspace/demo/index.html": { mode: "phone", fit: true },
    });
  });

  it("forgets every pick when this device's state is reset", async () => {
    const pickPhone = (): void => {
      const fill = radios()[0]!;
      fill.focus();
      fill.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    };
    showWebTab("/workspace/a/index.html");
    await flush();
    pickPhone();
    expect(checked()).toBe("phone");
    clearDeviceKeys();
    releaseWebTab("/workspace/a/index.html");
    showWebTab("/workspace/a/index.html");
    await flush();
    expect(checked()).toBe("fill");
    showWebTab("/workspace/b/index.html");
    await flush();
    pickPhone();
    expect(JSON.parse(localStorage.getItem(LS_WEB_VIEWPORT_KEY)!)).toEqual({
      "/workspace/b/index.html": { mode: "phone", fit: true },
    });
  });

  it("forces fill on a phone-shaped screen", async () => {
    grants.hint = { preset: "desktop", source: "meta" };
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    showWebTab("/workspace/demo/index.html");
    await flush();
    expect(checked()).toBe("fill");
  });
});

describe("live reload", () => {
  async function loaded(): Promise<void> {
    showWebTab("/workspace/demo/index.html");
    await flush();
    await poll(st("s1"));
  }

  it("reloads after a changed stamp reads the same twice", async () => {
    await loaded();
    expect(grantCalls()).toBe(1);
    await poll(st("s2"));
    expect(grantCalls()).toBe(1);
    await poll(st("s2"));
    expect(grantCalls()).toBe(2);
  });

  it("does not reload on a flapping stamp", async () => {
    await loaded();
    await poll(st("s2"));
    await poll(st("s1"));
    await poll(st("s2"));
    expect(grantCalls()).toBe(1);
  });

  it("keeps a pending change across a failed poll", async () => {
    await loaded();
    await poll(st("s2"));
    await poll(null);
    expect(grantCalls()).toBe(1);
    await poll(st("s2"));
    expect(grantCalls()).toBe(2);
  });

  it("reloads at once when the server's epoch changes", async () => {
    await loaded();
    await poll(st("s1", "e2"));
    expect(grantCalls()).toBe(2);
  });

  it("takes a manual reload's document as the new baseline", async () => {
    await loaded();
    await poll(st("s2"));
    grants.stamp = "s2";
    document.getElementById("web-reload-btn")!.click();
    await flush();
    expect(grantCalls()).toBe(2);
    await poll(st("s2"));
    await poll(st("s2"));
    expect(grantCalls()).toBe(2);
  });

  it("takes a reload before any changed poll as the new baseline", async () => {
    await loaded();
    stamps.next = st("s2");
    grants.stamp = "s2";
    document.getElementById("web-reload-btn")!.click();
    await flush();
    await poll(st("s2"));
    await poll(st("s2"));
    expect(grantCalls()).toBe(2);
  });

  it("reloads a document whose folder changed while hidden before any poll read it", async () => {
    showWebTab("/workspace/demo/index.html");
    await flush();
    kind.value = "chat";
    await flush();
    stamps.next = st("s2");
    kind.value = "web";
    showWebTab("/workspace/demo/index.html");
    await vi.advanceTimersByTimeAsync(0);
    expect(grantCalls()).toBe(1);
    await poll(st("s2"));
    expect(grantCalls()).toBe(2);
  });

  it("reloads a change landing after a manual reload and before any poll", async () => {
    await loaded();
    document.getElementById("web-reload-btn")!.click();
    await flush();
    expect(grantCalls()).toBe(2);
    await poll(st("s2"));
    await poll(st("s2"));
    expect(grantCalls()).toBe(3);
  });

  it("lets a later grant's hint govern when an earlier grant lands after it", async () => {
    grants.hint = { preset: "phone", source: "meta" };
    grants.hold = true;
    showWebTab("/workspace/demo/index.html");
    await flush();
    grants.hint = { preset: "desktop", source: "meta" };
    grants.hold = false;
    document.getElementById("web-reload-btn")!.click();
    await flush();
    grants.held.shift()?.();
    await flush();
    expect(grantCalls()).toBe(2);
    expect(checked()).toBe("desktop");
    expect(wrappers()[0]?.style.getPropertyValue("--web-frame-w")).toBe("1440px");
    expect(frames()).toHaveLength(1);
    expect(frames()[0]?.getAttribute("src")).toBe("/preview/tok2/index.html");
  });

  it("ignores stamps while the web view is not active, and polls again on return", async () => {
    await loaded();
    kind.value = "chat";
    await poll(st("s2"));
    await poll(st("s2"));
    expect(grantCalls()).toBe(1);
    const calls = stamps.calls;
    kind.value = "web";
    showWebTab("/workspace/demo/index.html");
    await vi.advanceTimersByTimeAsync(0);
    expect(stamps.calls).toBe(calls + 1);
  });

  it("re-reads the hint on every load, so the mode follows the page", async () => {
    grants.hint = { preset: "phone", source: "meta" };
    await loaded();
    expect(checked()).toBe("phone");
    expect(wrappers()[0]?.style.getPropertyValue("--web-frame-w")).toBe("390px");

    grants.hint = { preset: "desktop", source: "meta" };
    await poll(st("s2"));
    await poll(st("s2"));
    expect(checked()).toBe("desktop");
    expect(wrappers()[0]?.style.getPropertyValue("--web-frame-w")).toBe("1440px");
    expect(wrappers()[0]?.style.getPropertyValue("--web-scale")).toBe("0.5");

    grants.hint = { width: 1024, source: "meta" };
    document.getElementById("web-reload-btn")!.click();
    await flush();
    expect(radios().map((r) => r.getAttribute("aria-label"))).toContain("Page width (1024 px)");
    expect(checked()).toBe("page");

    grants.hint = undefined;
    document.getElementById("web-reload-btn")!.click();
    await flush();
    expect(radios().map((r) => r.dataset["mode"])).toEqual(["fill", "phone", "tablet", "desktop"]);
    expect(checked()).toBe("fill");
  });

  it("keeps a stored pick however the hint changes", async () => {
    localStorage.setItem(
      LS_WEB_VIEWPORT_KEY,
      JSON.stringify({ "/workspace/demo/index.html": { mode: "tablet", fit: true } }),
    );
    grants.hint = { preset: "phone", source: "meta" };
    await loaded();
    expect(checked()).toBe("tablet");
    grants.hint = { width: 1024, source: "meta" };
    document.getElementById("web-reload-btn")!.click();
    await flush();
    expect(checked()).toBe("tablet");
  });
});
