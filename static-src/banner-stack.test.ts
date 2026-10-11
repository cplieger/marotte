// banner-stack is one bindList over a computed active-chat view. Pinned: a banner shows
// only for its own active chat and a cleared one never resurrects; A -> B -> A reuses the
// SAME node; ensureBound() is idempotent; the stack container is the only aria-live
// region. `./store.js` is mocked to a writable `activeSession` signal and `$.bannerStack`
// to a bare container. A signal write flushes before it returns, so no drain is needed.

import { describe, it, expect, vi, afterEach } from "vitest";
import { signal } from "@cplieger/reactive";
import { LS_DISMISSED_BANNERS_KEY } from "./ls-keys.js";
import type * as BannerStack from "./banner-stack.js";

/** `vi.resetModules()` does not re-evaluate a module in Browser Mode (the map is URL-keyed), so
 *  busting the specifier mints a fresh instance. The `.ts` extension matters for coverage
 *  attribution. Only the module under test is busted, so `vi.mock` still intercepts its
 *  dependencies. */
let bootSeq = 0;

interface MiniSession {
  readonly id: string;
}

// ONE signal for the file, reset per test: the mocks' factories run once, so a re-created
// signal would leave later instances subscribed to a dead one.
const activeSig = signal<MiniSession | undefined>(undefined);
let container: HTMLDivElement;

// Registered ONCE at module scope, reaching per-test values through module state: a
// mocked module is evaluated once, so a per-test `vi.doMock` never runs again. `$` stays
// a getter because a property read on the exported object is live.
vi.mock("./store.js", () => ({ activeSession: activeSig }));
// No ui-state mock: dismissals are per-chat localStorage, which jsdom provides, and the
// stored document is the shape under test.
vi.mock("./dom.js", () => ({
  $: {
    get bannerStack(): HTMLDivElement {
      return container;
    },
  },
  // Present-but-inert so real-ESM linking succeeds; no case calls them.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

async function setup(): Promise<{
  container: HTMLDivElement;
  mod: typeof BannerStack;
}> {
  vi.resetModules();
  bootSeq++;
  // One reactive instance exists for the run, so the test's signal is the module's.
  activeSig.value = undefined;
  localStorage.clear();
  container = document.createElement("div");
  const mod = (await import(
    /* @vite-ignore */ `./banner-stack.ts?boot=${bootSeq}`
  )) as typeof BannerStack;
  return { container, mod };
}

afterEach(() => {
  // No doUnmock: the mocks above are module-scoped and permanent by necessity.
  vi.resetModules();
  bootSeq++;
});

describe("banner-stack: active-chat scoping", () => {
  it("shows a banner only for its active chat and never resurrects a cleared one", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);

    // Switch away: chat A's banner must hide (chat B has none).
    activeSig.value = { id: "B" };
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    // Clear chat A's banner while it is hidden, then switch back: the bug was
    // that a stale render source resurrected it. It must stay gone.
    mod.clearBannerCodes("A", ["x"]);
    activeSig.value = { id: "A" };
    expect(container.querySelectorAll(".banner")).toHaveLength(0);
  });

  it("reuses the same banner node across A -> B -> A switches", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    const node1 = container.querySelector(".banner");
    expect(node1).not.toBeNull();

    activeSig.value = { id: "B" };
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    activeSig.value = { id: "A" };
    const node2 = container.querySelector(".banner");
    expect(node2).toBe(node1);
  });

  it("ensureBound() is idempotent: a second call does not double-bind", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.ensureBound();
    mod.showBanner("A", "x", "boom", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);
  });
});

describe("banner-stack: composite key (keyenc)", () => {
  // Dismissals are stored per CHAT, asserted through the public API.
  it("suppresses a banner this reader dismissed, per chat", async () => {
    const { container, mod } = await setup();
    localStorage.setItem(
      LS_DISMISSED_BANNERS_KEY,
      JSON.stringify({ "c-1750000000000-ab12cd": ["rate_limit"] }),
    );

    activeSig.value = { id: "c-1750000000000-ab12cd" };
    mod.ensureBound();
    mod.showBanner("c-1750000000000-ab12cd", "rate_limit", "slow down", "warning", true);
    expect(container.querySelectorAll(".banner")).toHaveLength(0);

    // A different chat's identical code is a different acknowledgement.
    activeSig.value = { id: "c-other" };
    mod.showBanner("c-other", "rate_limit", "slow down", "warning", true);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);
  });

  // A dismissal is the VIEWER's, so it lives in this device's storage, not the arrangement.
  it("records a dismissal in this device's own storage, not the arrangement", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "rate_limit", "slow down", "warning", true);
    container.querySelector<HTMLButtonElement>(".banner-dismiss")?.click();

    const raw = localStorage.getItem(LS_DISMISSED_BANNERS_KEY);
    expect(raw).not.toBeNull();
    expect(JSON.parse(raw ?? "{}")).toEqual({ A: ["rate_limit"] });
    // And nothing was written to the synced arrangement's key.
    expect(localStorage.getItem("marotte.ui-state")).toBeNull();
  });

  it("does not let one field's content forge the other's boundary", async () => {
    // Pins the join's separation, so ("a:b" + "c") and ("a" + "b:c") never share a slot.
    const { container, mod } = await setup();

    activeSig.value = { id: "a:b" };
    mod.ensureBound();
    mod.showBanner("a:b", "c", "first", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);

    // Same forged key under the old scheme, different chat: must not be seen
    // as the same entry, and must not show on chat "a:b".
    mod.showBanner("a", "b:c", "second", "error", false);
    expect(container.querySelectorAll(".banner")).toHaveLength(1);
    expect(container.textContent).toContain("first");

    activeSig.value = { id: "a" };
    expect(container.textContent).toContain("second");
  });

  it("clearBannersForChat prefix scan: chat \u201cabc\u201d does not clear chat \u201cabcd\u201d", async () => {
    // clearBannersForChat's `${chatID}:` prefix scan over the collection's keys: the trailing
    // ":" bounds it. In-memory only.
    const { container, mod } = await setup();

    activeSig.value = { id: "abc" };
    mod.ensureBound();
    mod.showBanner("abc", "x", "short", "error", true);
    mod.showBanner("abcd", "x", "long", "error", true);
    expect(container.textContent).toContain("short");

    mod.clearBannersForChat("abc");

    // The sibling chat's in-memory banner survived the sweep.
    activeSig.value = { id: "abcd" };
    expect(container.textContent).toContain("long");

    // And a dismissal for the sibling is untouched, because nothing prunes
    // storage here any more.
    localStorage.setItem(LS_DISMISSED_BANNERS_KEY, JSON.stringify({ abcd: ["x"] }));
    mod.clearBannersForChat("abc");
    expect(JSON.parse(localStorage.getItem(LS_DISMISSED_BANNERS_KEY) ?? "{}")).toEqual({
      abcd: ["x"],
    });
  });
});

describe("banner-stack: single live region", () => {
  it("marks only the stack container as a live region; banners are not separately live", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    // The stack container is the SINGLE polite live region.
    expect(container.getAttribute("aria-live")).toBe("polite");

    mod.showBanner("A", "err", "boom", "error", false);
    mod.showBanner("A", "inf", "note", "info", false);

    const nodes = container.querySelectorAll(".banner");
    expect(nodes).toHaveLength(2);
    // No nested live region on individual banners: dropping role="alert"/"status"
    // is what prevents the double-announce.
    for (const node of nodes) {
      expect(node.hasAttribute("role")).toBe(false);
      expect(node.hasAttribute("aria-live")).toBe(false);
    }
  });
});

// A banner's level must carry a shape, not colour alone (WCAG 1.4.1). The left border is
// the one colour channel surviving `forced-colors: active`, and 40-a11y.css's
// forced-colors block does not cover banners, so the glyph is what carries it.

describe("banner-stack: severity carries a shape, not colour alone", () => {
  it("gives every level its own conventional character", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);
    mod.showBanner("A", "w", "careful", "warning", false);
    mod.showBanner("A", "i", "note", "info", false);

    const glyphFor = (code: string): string =>
      container.querySelector(
        `.banner-${code === "e" ? "error" : code === "w" ? "warning" : "info"} .banner-glyph`,
      )?.textContent ?? "";

    // A banner is a text notice, so its levels are characters, pinned literally; their
    // distinctness is asserted next.
    expect(glyphFor("e")).toBe("\u2717");
    expect(glyphFor("w")).toBe("\u26A0");
    expect(glyphFor("i")).toBe("\u2139");
  });

  it("makes the three levels distinguishable by SHAPE, which is the point", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);
    mod.showBanner("A", "w", "careful", "warning", false);
    mod.showBanner("A", "i", "note", "info", false);

    const glyphs = [...container.querySelectorAll(".banner-glyph")].map((g) => g.textContent);
    expect(glyphs).toHaveLength(3);
    // Three banners, three DIFFERENT characters. A shared glyph would leave the
    // level readable only by hue.
    expect(new Set(glyphs).size).toBe(3);
  });

  it("hides the glyph from assistive tech, so the message is the whole announcement", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);

    const glyph = container.querySelector(".banner-glyph");
    expect(glyph?.getAttribute("aria-hidden")).toBe("true");
    // The glyph is for the eye: the live region announces the message text alone.
    expect(container.querySelector(".banner-msg")?.textContent).toBe("boom");
  });

  it("keeps the glyph when a repeat call replaces the message in place", async () => {
    const { container, mod } = await setup();

    activeSig.value = { id: "A" };
    mod.ensureBound();
    mod.showBanner("A", "e", "boom", "error", false);
    // Same (chat, code): showBanner rewrites the text node on the entry-owned
    // element rather than rebuilding it, so the channel must survive that path.
    mod.showBanner("A", "e", "still boom", "error", false);

    expect(container.querySelectorAll(".banner")).toHaveLength(1);
    expect(container.querySelectorAll(".banner-glyph")).toHaveLength(1);
    expect(container.querySelector(".banner-msg")?.textContent).toBe("still boom");
  });
});

describe("banner-stack: the CTA is the app's shared button, not a text-link skin", () => {
  // `.banner-link`'s `onClick` branch emits a `<button>`, and 02-reset.css's
  // `* { padding: 0 }` beats the UA sheet, so the padding must come from `.btn-small` while
  // `.banner-link` is layout only. Both element kinds share the class.
  for (const [kind, link] of [
    ["button", { label: "Run diagnostics", onClick: () => undefined }],
    ["anchor", { label: "Open docs", href: "https://example.invalid/" }],
  ] as const) {
    it(`gives the ${kind} CTA the btn-small class`, async () => {
      const { container, mod } = await setup();

      activeSig.value = { id: "A" };
      mod.ensureBound();
      mod.showBanner("A", "runtime", "installing", "info", false, link);

      const cta = container.querySelector(".banner-link");
      expect(cta, "the banner rendered its CTA").not.toBeNull();
      expect([...(cta?.classList ?? [])], `the ${kind} CTA carries the shared skin`).toContain(
        "btn-small",
      );
    });
  }
});
