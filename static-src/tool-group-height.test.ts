// A tool group's HEADER and its member rows are one height, and a BARE group renders as a plain
// card. Both read ONE declaration (`.tool-header { min-height: var(--btn-h) }`). Every fixture is
// built by the PRODUCTION BUILDERS: a hand-rolled card measures under the floor and hides a 6px
// gap. Do not replace `buildToolCard` with markup here.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

/** Worst case is six frames: `rendered()`'s three per `run()`, twice. Past this
 *  suite's rAF throttle that is 6.1s, over vitest's 5s default. */
const GROUP_TIMEOUT_MS = testTimeoutFor(framesBudgetMs(6));

// The card builder's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the imports are evaluated,
// which is why the imports below are dynamic.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("./tool-group.js");
const { buildToolCard } = await import("./tool-card.js");

/** A transcript-width column, IN the viewport: off-screen is not a place a `.tool-call`
 *  can be measured from — see `rendered()`. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

/** `page.viewport` has no getter, so the size to go back to is captured before any
 *  case moves it. */
const RUNNER_VIEWPORT = { w: window.innerWidth, h: window.innerHeight };

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(async () => {
  await page.viewport(RUNNER_VIEWPORT.w, RUNNER_VIEWPORT.h);
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

const FILES = ["auth.go", "runtime.go", "translate.go"];

/** A settled read card, as `messages-tools.ts` builds one. */
function card(i: number): HTMLDivElement {
  return buildToolCard({
    id: `t${String(i)}`,
    title: "Read File",
    kind: "read",
    status: "completed",
    live: false,
    input: { path: `internal/agent/${FILES[i] ?? "x.go"}` },
  });
}

/**
 * Wait for Chromium to decide the cards are near the viewport. Until then a card's OWN box IS the
 * 40px `contain-intrinsic-size` fallback while descendants report real geometry, so the
 * bare-vs-standalone case compared two copies of one estimate and stayed GREEN with its defect
 * planted. The decision lands on the second frame and never while the host is off-screen; three
 * lifecycle passes for margin.
 */
async function rendered(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
}

/** A real group shell with `n` real members, mounted, header refreshed — which is
 *  what writes the bare class, so no case here sets it by hand — and RENDERED, so
 *  a card's own box is its content and not a containment estimate. */
async function run(n: number): Promise<HTMLElement> {
  const g = buildToolGroupShell();
  for (let i = 0; i < n; i++) {
    groupBody(g).appendChild(card(i));
  }
  host.appendChild(g);
  refreshGroupHeader(g);
  await rendered();
  return g;
}

/** Border-box height in whole pixels, the unit every claim here uses. `offsetHeight`, not a rect:
 *  a rect height is a float subtraction, and the entry animation plus the fluid root size made two
 *  `var(--btn-h)` boxes read 36 and 35.999996 in one pass. The guarded regressions are 1px and
 *  4px. */
function h(e: Element | null | undefined): number {
  return e instanceof HTMLElement ? e.offsetHeight : -1;
}

/** `--hit-floor` in pixels for the tier currently set. A custom property reads back
 *  as its raw token, so the only honest way to get the length is to let the engine
 *  resolve it on a real box in this host. */
function hitFloorPx(): number {
  const probe = document.createElement("div");
  probe.style.blockSize = "var(--hit-floor)";
  host.appendChild(probe);
  const v = probe.getBoundingClientRect().height;
  probe.remove();
  return v;
}

/** The file badge's declared height for the current tier, probed from the TOKEN so a control-height
 *  retune moves the assertion. */
function badgeBoxPx(): number {
  const probe = document.createElement("div");
  probe.className = "tool-header";
  const inner = document.createElement("div");
  inner.style.blockSize = "var(--tool-badge-h)";
  probe.appendChild(inner);
  host.appendChild(probe);
  const v = inner.offsetHeight;
  probe.remove();
  return v;
}

function headerOf(g: Element): HTMLElement {
  return g.querySelector<HTMLElement>(":scope > .tool-group-header")!;
}

function members(g: Element): HTMLElement[] {
  return [...g.querySelectorAll<HTMLElement>(":scope > .tool-group-body > .tool-call")];
}

describe(
  "a group header and its member rows resolve to one height",
  { timeout: GROUP_TIMEOUT_MS },
  () => {
    it.each(["fine", "coarse"] as const)("declare the SAME floor on a %s pointer", async (tier) => {
      // The floor itself, asserted directly: a header/row token split fails here at both tiers.
      document.documentElement.dataset["pointer"] = tier;
      const g = await run(3);
      const floor = getComputedStyle(headerOf(g)).minHeight;
      expect(
        parseFloat(floor),
        "the header reads a real control-height token",
      ).toBeGreaterThanOrEqual(24);
      for (const [i, m] of members(g).entries()) {
        expect(
          getComputedStyle(m.querySelector(".tool-header")!).minHeight,
          `member ${String(i)} must read the header's own floor, not a dense-tier one`,
        ).toBe(floor);
      }
    });

    it("RESPONDS to the pointer tier, which is what catches a literal", async () => {
      // A hand-tuned literal on either side would satisfy the equality above at one
      // tier and fail here.
      document.documentElement.dataset["pointer"] = "fine";
      const fine = await run(2);
      const fineFloor = getComputedStyle(headerOf(fine)).minHeight;
      const fineRowFloor = getComputedStyle(
        members(fine)[0]!.querySelector(".tool-header")!,
      ).minHeight;
      host.replaceChildren();

      document.documentElement.dataset["pointer"] = "coarse";
      const coarse = await run(2);
      const coarseFloor = getComputedStyle(headerOf(coarse)).minHeight;
      const coarseRowFloor = getComputedStyle(
        members(coarse)[0]!.querySelector(".tool-header")!,
      ).minHeight;

      expect(parseFloat(coarseFloor), "the header follows the tier").toBeGreaterThan(
        parseFloat(fineFloor),
      );
      expect(parseFloat(coarseRowFloor), "and so does the member row").toBeGreaterThan(
        parseFloat(fineRowFloor),
      );
    });

    it("RENDERS a header and its member rows at one height on a fine pointer", async () => {
      // The desktop defect, in the units it was reported in: 36 against 32.
      document.documentElement.dataset["pointer"] = "fine";
      const g = await run(3);
      const head = h(headerOf(g));
      expect(head).toBeGreaterThanOrEqual(24);
      for (const [i, m] of members(g).entries()) {
        expect(
          h(m.querySelector(".tool-header")),
          `member ${String(i)}'s row must be exactly the header's height, not 4px short of it`,
        ).toBe(head);
      }
    });

    it("RENDERS a header and its member rows at one height on a coarse pointer too, because the badge fits the room the row already has", async () => {
      // The badge's box is not where the target lives: it reads `--tool-badge-h`, the content box a
      // STANDALONE header leaves, so it fills that room and cannot grow any row. Asserted against a probe
      // of the token, so a control-height retune moves both.
      document.documentElement.dataset["pointer"] = "coarse";
      const g = await run(3);
      const head = h(headerOf(g));
      const row = members(g)[0]!;
      const chip = row.querySelector<HTMLElement>("button.tool-file-link")!;
      expect(
        h(chip),
        "the badge is exactly the room a standalone header leaves, which is the whole mechanism",
      ).toBe(badgeBoxPx());
      expect(
        h(chip),
        "so it is taller than the kind glyph it used to measure, which is what was reported",
      ).toBeGreaterThan(h(row.querySelector(".tool-header > .tool-icon")));
      expect(h(chip), "and it still stays inside the row it sits in").toBeLessThan(head);
      for (const [i, m] of members(g).entries()) {
        expect(
          h(m.querySelector(".tool-header")),
          `member ${String(i)}'s row must be exactly the header's height on a finger too`,
        ).toBe(head);
      }
    });

    it.each(["fine", "coarse"] as const)(
      "keeps the badge's TARGET on the hit floor at %s, past the box it paints",
      async (tier) => {
        // WCAG 2.5.8 (24px mouse, 44px finger) is carried by the `--hit-floor` expander. A real hit test,
        // because a clipped `::after` reads the same in the cascade, which is why the chip has no
        // `overflow: hidden`.
        document.documentElement.dataset["pointer"] = tier;
        const g = await run(3);
        const chip = members(g)[0]!.querySelector<HTMLElement>("button.tool-file-link")!;
        const box = chip.getBoundingClientRect();
        // How far past the paint the target has to reach. The expander is centred on
        // the badge, so it is half the shortfall on each edge.
        const reach = (hitFloorPx() - box.height) / 2;
        expect(
          reach,
          "the badge paints under the floor, or there is nothing to test",
        ).toBeGreaterThan(1);

        const cx = box.left + box.width / 2;
        expect(
          document.elementFromPoint(cx, box.top - reach + 1),
          "a point just inside the target's top edge activates the badge",
        ).toBe(chip);
        expect(
          document.elementFromPoint(cx, box.bottom + reach - 1),
          "and one just inside its bottom edge",
        ).toBe(chip);
        // The control. Without it an expander of any size would pass, including one
        // overhanging the row into its neighbour's target.
        expect(
          document.elementFromPoint(cx, box.top - reach - 2),
          "and the target stops there: it may not reach past the floor",
        ).not.toBe(chip);
      },
    );

    it("leaves the member's OUTER box exactly 1px taller than its row: the separator hairline", async () => {
      // Named rather than absorbed into a tolerance, so the one legitimate difference
      // between the two boxes is documented by the assertion instead of hidden by it.
      // Measured against the ROW rather than the header, so it holds at both tiers.
      for (const tier of ["fine", "coarse"] as const) {
        document.documentElement.dataset["pointer"] = tier;
        host.replaceChildren();
        const g = await run(3);
        for (const [i, m] of members(g).entries()) {
          expect(h(m), `${tier}: member ${String(i)}'s card is its row plus the separator`).toBe(
            h(m.querySelector(".tool-header")) + 1,
          );
          expect(getComputedStyle(m).borderTopWidth).toBe("1px");
        }
      }
    });
  },
);

describe("a bare group renders as a plain tool card", { timeout: GROUP_TIMEOUT_MS }, () => {
  it("hides its header from the accessibility tree AND from tab order", async () => {
    document.documentElement.dataset["pointer"] = "fine";
    const bare = await run(1);
    // `display: none`, not `visibility`/`aria-hidden`: it is the only one of the
    // three that does both, so a hidden `role="button"` cannot become a dead tab
    // stop advertising a state nobody can change.
    expect(getComputedStyle(headerOf(bare)).display).toBe("none");
    host.replaceChildren();

    const real = await run(2);
    const head = headerOf(real);
    expect(getComputedStyle(head).display).toBe("flex");
    expect(head.getAttribute("aria-expanded")).toBe("true");
  });

  it("matches a standalone card on height, padding and all four chrome properties", async () => {
    document.documentElement.dataset["pointer"] = "fine";
    const standalone = card(0);
    host.appendChild(standalone);
    const bare = await run(1);
    const member = members(bare)[0]!;

    const want = getComputedStyle(standalone);
    const got = getComputedStyle(bare);
    // Read off the DOM, never hardcoded: the two boxes declare these from the same
    // tokens, so a token change must move both or neither.
    expect(got.backgroundColor).toBe(want.backgroundColor);
    expect(got.borderTopWidth).toBe(want.borderTopWidth);
    expect(got.borderTopColor).toBe(want.borderTopColor);
    expect(got.borderTopLeftRadius).toBe(want.borderTopLeftRadius);

    // The bare state drops BOTH row treatments, the separator hairline and the tighter
    // `padding-block`; either left makes a lone call differ from the card it is.
    expect(getComputedStyle(member).borderTopWidth).toBe("0px");
    const memberPad = getComputedStyle(member.querySelector(".tool-header")!);
    const standalonePad = getComputedStyle(standalone.querySelector(".tool-header")!);
    expect(memberPad.paddingBlockStart).toBe(standalonePad.paddingBlockStart);
    expect(memberPad.paddingBlockEnd).toBe(standalonePad.paddingBlockEnd);

    // Which is what makes the outer boxes agree. Measured with real cards: 42 and
    // 42, against 36 for a member of a real group.
    expect(h(bare)).toBe(h(standalone));
  });

  it("takes the ROW treatment back once a second member lands", async () => {
    // A two-member group needs both back, or its rows stop reading as a list. Density is asserted as the
    // DECLARATION: with the badge at `--icon-ui` every row sits on the floor, so no rendered height
    // difference remains to measure.
    document.documentElement.dataset["pointer"] = "fine";
    const bare = await run(1);
    const lonePad = parseFloat(
      getComputedStyle(members(bare)[0]!.querySelector(".tool-header")!).paddingBlockStart,
    );
    const loneRow = h(members(bare)[0]?.querySelector(".tool-header"));
    host.replaceChildren();

    const g = await run(2);
    for (const m of members(g)) {
      expect(getComputedStyle(m).borderTopWidth).toBe("1px");
    }
    expect(
      parseFloat(getComputedStyle(members(g)[0]!.querySelector(".tool-header")!).paddingBlockStart),
      "a member row is DENSER than the same card standing alone",
    ).toBeLessThan(lonePad);
    // And the floor is what both of them render at, which is the property the height
    // comparison above was hiding.
    expect(
      h(members(g)[0]?.querySelector(".tool-header")),
      "while both still render at the one height floor",
    ).toBe(loneRow);
  });

  it("renders a LONE card carrying a file badge at the height of one without", async () => {
    // A single call is a BARE group; a member keeping a card's `padding-block: var(--sp-2)` would
    // grow a lone row with a 24px badge to 40px beside 36px ones.
    for (const tier of ["fine", "coarse"] as const) {
      document.documentElement.dataset["pointer"] = tier;
      host.replaceChildren();

      // BOTH bare groups stay mounted and are measured in one pass: a card read
      // after `host.replaceChildren()` is detached and every box reads 0, which is an
      // equality this case would pass rather than fail on.
      const badged = await run(1);
      // The same card with no path, so the badge is the ONLY difference between the
      // two headers.
      const plain = buildToolCard({
        id: "nofile",
        title: "Read File",
        kind: "read",
        status: "completed",
        live: false,
        input: {},
      });
      const shell = buildToolGroupShell();
      groupBody(shell).appendChild(plain);
      host.appendChild(shell);
      refreshGroupHeader(shell);
      await rendered();

      const badgedRow = members(badged)[0]!.querySelector(".tool-header")!;
      const plainRow = plain.querySelector(".tool-header")!;
      expect(
        badgedRow.querySelector("button.tool-file-link"),
        `${tier}: the fixture has to carry a badge, or this case asserts nothing`,
      ).not.toBeNull();
      expect(plainRow.querySelector("button.tool-file-link"), `${tier}: control`).toBeNull();
      expect(h(plainRow), `${tier}: both fixtures have to be rendered`).toBeGreaterThan(0);

      expect(h(badgedRow), `${tier}: a badge may not make a lone card taller`).toBe(h(plainRow));
    }
  });

  it("holds on a PHONE viewport with no pointer tier declared yet", async () => {
    // No `data-pointer` yet (boot.ts writes it from a real PointerEvent): every device's first paint
    // uses 01-tokens.css's no-JS fallback, and 50-mobile.css could move it unseen.
    document.documentElement.removeAttribute("data-pointer");
    await page.viewport(390, 844);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      390, 844,
    ]);

    const badged = await run(1);
    const plain = buildToolCard({
      id: "nofile-phone",
      title: "Read File",
      kind: "read",
      status: "completed",
      live: false,
      input: {},
    });
    const shell = buildToolGroupShell();
    groupBody(shell).appendChild(plain);
    host.appendChild(shell);
    refreshGroupHeader(shell);
    await rendered();

    const badgedRow = members(badged)[0]!.querySelector(".tool-header")!;
    const plainRow = plain.querySelector(".tool-header")!;
    const badge = badgedRow.querySelector<HTMLElement>("button.tool-file-link")!;
    expect(h(plainRow), "both fixtures have to be rendered").toBeGreaterThan(0);
    expect(h(badgedRow), "a badge may not make a lone card taller on a phone either").toBe(
      h(plainRow),
    );
    expect(h(badge), "and the badge is exactly the room this header leaves").toBe(badgeBoxPx());
    expect(
      h(badgedRow),
      "which puts a lone badged card exactly ON its floor rather than lifted to it — the boundary",
    ).toBe(badgeBoxPx() + 2 * parseFloat(getComputedStyle(badgedRow).paddingBlockStart));
  });
});
