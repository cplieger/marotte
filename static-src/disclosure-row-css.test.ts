// The CSS half of the whole-header disclosure: does the row look like a control (`no dead zones` cuts both ways).
// `mountAppCSS` assembles `css/MANIFEST` in order, since equal-specificity ties are decided by it.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();

  host = document.createElement("div");
  document.body.appendChild(host);

  for (const id of ["messages", "messages-wrap", "banner-stack"]) {
    if (document.getElementById(id) === null) {
      const el = document.createElement("div");
      el.id = id;
      document.body.appendChild(el);
    }
  }
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./editor-openers.js", () => ({
  openFile: () => {
    /* noop */
  },
  openFileDiff: () => {
    /* noop */
  },
  openFileGitDiff: () => {
    /* noop */
  },
}));
function mount(node: HTMLElement): HTMLElement {
  host.replaceChildren(node);
  return node;
}

function css(el: Element, prop: string): string {
  return getComputedStyle(el).getPropertyValue(prop);
}

/**
 * The only local read that discriminates a skipped `<details>` subtree: a skipped button still reports its rect and
 * display.
 */
function contentSkipped(details: Element): boolean {
  return getComputedStyle(details, "::details-content").contentVisibility === "hidden";
}

/**
 * Every rule writing `props` for `el`, counting `:hover` rules as hovered, in document order. A CSSOM walk because a
 * synthetic hover drives no recalc and `CSS.forcePseudoState` is devtools-only.
 */
function propertyWriters(el: Element, props: string[]): { selector: string; value: string }[] {
  const out: { selector: string; value: string }[] = [];
  for (const sheet of document.styleSheets) {
    let rules: CSSRuleList;
    try {
      rules = sheet.cssRules;
    } catch {
      continue;
    }
    const walk = (list: CSSRuleList): void => {
      for (const r of list) {
        const grouping = r as CSSRule & { cssRules?: CSSRuleList };
        if (!(r instanceof CSSStyleRule)) {
          if (grouping.cssRules !== undefined) {
            walk(grouping.cssRules);
          }
          continue;
        }
        let value = "";
        for (const p of props) {
          value = r.style.getPropertyValue(p);
          if (value !== "") {
            break;
          }
        }
        if (value === "") {
          continue;
        }
        try {
          if (el.matches(r.selectorText.replaceAll(":hover", ""))) {
            out.push({ selector: r.selectorText, value });
          }
        } catch {
          /* a selector this engine cannot parse writes nothing here */
        }
      }
    };
    walk(rules);
  }
  return out;
}

function backgroundWriters(el: Element): { selector: string; value: string }[] {
  return propertyWriters(el, ["background", "background-color"]);
}

function winningBackground(el: Element): string | undefined {
  return backgroundWriters(el).at(-1)?.value;
}

describe("tool card summary affordance", () => {
  it("a summary with a toggle says its whole area is clickable", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css1",
        output: "3 results\n",
        title: "remote_web_search",
        kind: "fetch",
        status: "completed",
        input: { query: "marotte" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    expect(css(summary, "cursor")).toBe("pointer");
    expect(css(summary, "user-select")).toBe("none");
    expect(css(summary, "transition-property")).toBe("background");
  });

  it("a claim-only summary stays inert", async () => {
    // `readFile` has no depth 1, so no toggle: a pointer cursor would advertise nothing.
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css2",
        title: "readFile",
        kind: "read",
        status: "completed",
        input: { path: "src/main.ts" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    expect(css(summary, "cursor")).toBe("auto");
  });

  // A claim-only card holds the leading chevron column open too, so a mixed group's kind glyphs stay in one column
  // (the chevron leads, and about a fifth of tool calls are claim-only).
  it("keeps the kind-glyph column straight across a mixed group", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const claimOnly = mount(
      buildToolCard({
        id: "css-claim-gutter",
        title: "readFile",
        kind: "read",
        status: "completed",
        input: { path: "src/main.ts" },
        live: false,
      }),
    );
    const withRegion = buildToolCard({
      id: "css-claim-gutter-ref",
      title: "invoke_sub_agent",
      kind: "other",
      status: "completed",
      live: false,
      output: "done\n",
    });
    host.appendChild(withRegion);
    expect(claimOnly.querySelector(".tool-disclosure")).toBeNull();
    const iconX = (card: Element): number =>
      card.querySelector(".tool-icon")!.getBoundingClientRect().x;
    expect(iconX(claimOnly)).toBeCloseTo(iconX(withRegion), 1);
    const header = withRegion.querySelector<HTMLElement>(".tool-header")!;
    const toggle = withRegion.querySelector<HTMLElement>(".tool-disclosure")!;
    const t = toggle.getBoundingClientRect();
    expect(
      header.getBoundingClientRect().x + Number.parseFloat(css(header, "padding-left")),
    ).toBeCloseTo(t.right + 8, 1);
    withRegion.remove();
  });

  it("a summary with nothing to reveal stays inert too", async () => {
    // A card that gave its toggle back withdraws the pointer, but the gutter (keyed on having a region) stays, or the title
    // row would jump 32px.
    const { buildToolCard } = await import("./tool-card.js");
    const bare = mount(
      buildToolCard({
        id: "css-bare",
        title: "invoke_sub_agent",
        kind: "other",
        status: "failed",
        live: false,
      }),
    );
    const summary = bare.querySelector<HTMLElement>(".tool-summary")!;
    const header = bare.querySelector<HTMLElement>(".tool-header")!;
    expect(bare.querySelector(".tool-details")).not.toBeNull();
    expect(bare.querySelector(".tool-disclosure")).toBeNull();
    expect(css(summary, "cursor")).toBe("auto");

    // Compared against a card that still has its chevron: only the region rule writes the gutter's calc.
    const withToggle = buildToolCard({
      id: "css-bare-ref",
      title: "invoke_sub_agent",
      kind: "other",
      status: "failed",
      live: false,
      output: "the delegate refused\n",
    });
    host.appendChild(withToggle);
    const reserved = css(withToggle.querySelector(".tool-header")!, "padding-inline-end");
    expect(css(header, "padding-inline-end")).toBe(reserved);
    withToggle.remove();
  });

  it("hovering a toggle summary paints both title and description", async () => {
    // The hover paints their common parent, leaving no dead strip.
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css3",
        output: "3 results\n",
        title: "remote_web_search",
        kind: "fetch",
        status: "completed",
        input: { query: "marotte" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    const subtitle = card.querySelector<HTMLElement>(".tool-subtitle")!;
    expect(summary.contains(subtitle)).toBe(true);
    expect(winningBackground(summary)).toBe("var(--c-hover)");
    expect(backgroundWriters(subtitle)).toEqual([]);
  });

  // The chevron aligns with the title, not the whole summary. Measured against the title: a header-relative assertion
  // would pass under either rule.
  it("aligns the chevron with the title on a two-line card", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css-chevron",
        output: "3 results\n",
        title: "remote_web_search",
        kind: "fetch",
        status: "completed",
        input: { query: "marotte" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    const title = card.querySelector<HTMLElement>(".tool-title")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    const centre = (el: Element): number => {
      const r = el.getBoundingClientRect();
      return r.y + r.height / 2;
    };
    expect(Math.abs(centre(toggle) - centre(title))).toBeLessThanOrEqual(1);
    // The summary must really be two rows, or the case above proves nothing.
    expect(Math.abs(centre(toggle) - centre(summary))).toBeGreaterThan(4);
  });

  // Out of flow: as the title row's last flex child the 24px box made the header taller than `.tool-group-header`.
  it("contributes no height to the title row", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css-chevron-height",
        output: "ok\n",
        title: "Run Command",
        kind: "execute",
        status: "completed",
        input: { command: "go vet ./..." },
        live: false,
      }),
    );
    const header = card.querySelector<HTMLElement>(".tool-header")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(css(toggle, "position")).toBe("absolute");
    expect(header.getBoundingClientRect().height).toBeCloseTo(
      Number.parseFloat(css(header, "min-height")),
      1,
    );
  });

  // Nothing runs under the chevron at either tier: a literal gutter once let a phone (44px floor) overlap the title.
  // Measured at the content box, where `text-overflow` renders.
  for (const tier of ["fine", "coarse"] as const) {
    it(`keeps every summary row clear of the chevron on the ${tier} tier`, async () => {
      document.documentElement.setAttribute("data-pointer", tier);
      try {
        const { buildToolCard } = await import("./tool-card.js");
        const card = mount(
          buildToolCard({
            id: `css-title-gutter-${tier}`,
            // Disclosable, since the chevron is detached for a card with nothing to reveal.
            output: "ok\tmarotte\t0.5s\n",
            // A model-written title, so the command rides the subtitle row being measured.
            title: "Run the Go checks",
            kind: "execute",
            status: "completed",
            input: { command: "go test ./... && go vet ./... && golangci-lint run ./..." },
            live: false,
          }),
        );
        const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
        const glyphRight = toggle.getBoundingClientRect().right;
        // Every summary row starts at the same edge.
        for (const sel of [".tool-header", ".tool-subtitle"]) {
          const row = card.querySelector<HTMLElement>(sel)!;
          const contentLeft =
            row.getBoundingClientRect().x + Number.parseFloat(css(row, "padding-left"));
          expect(contentLeft, sel).toBeGreaterThanOrEqual(glyphRight);
        }
        // The button's box is pinned to 24px, which keeps the gutter arithmetic true on both tiers.
        expect(toggle.getBoundingClientRect().width).toBeCloseTo(24, 1);
      } finally {
        document.documentElement.removeAttribute("data-pointer");
      }
    });
  }

  // Later rows align with the title, not the chevron: they are the card's lines, not the disclosure's children.
  for (const [name, opts] of [
    [
      "subtitle",
      {
        id: "css-subtitle-align",
        output: "ok\tmarotte\t0.5s\n",
        title: "Run the Go checks",
        kind: "execute",
        input: { command: "go test ./... && go vet ./... && golangci-lint run ./..." },
      },
    ],
    [
      "move row",
      {
        id: "css-move-align",
        output: "moved 1 file\n",
        title: "Move File",
        kind: "move",
        input: {
          sourcePath: "static-src/css/14-tools.css",
          destinationPath: "static-src/css/15-tool-cards.css",
        },
      },
    ],
  ] as const) {
    it(`starts the ${name} on the kind glyph's column`, async () => {
      const { buildToolCard } = await import("./tool-card.js");
      const card = mount(buildToolCard({ ...opts, status: "completed", live: false }));
      const row = card.querySelector<HTMLElement>(
        name === "subtitle" ? ".tool-subtitle" : ".tool-move-row",
      )!;
      const icon = card.querySelector<HTMLElement>(".tool-icon")!;
      const contentLeft =
        row.getBoundingClientRect().x + Number.parseFloat(css(row, "padding-left"));
      expect(contentLeft).toBeCloseTo(icon.getBoundingClientRect().x, 1);
    });
  }

  it("nothing paints a claim-only summary", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css4",
        title: "readFile",
        kind: "read",
        status: "completed",
        input: { path: "src/main.ts" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    expect(backgroundWriters(summary)).toEqual([]);
  });
});

/**
 * A `<details>` whose content renders inline above its media rule. Assert the box: a closed `<details>` hides its
 * content via `::details-content`, so elements still compute `display: inline-flex` while unpainted.
 */
function inlineDisclosure(
  menuClass: string,
  contentClass: string,
  btnClass: string,
): HTMLDetailsElement {
  const details = document.createElement("details");
  details.className = menuClass;
  const summary = document.createElement("summary");
  summary.className = `${btnClass} ${menuClass === "turn-actions-more" ? "turn-action-more" : "fb-new-trigger"}`;
  const content = document.createElement("span");
  content.className = contentClass;
  const btn = document.createElement("button");
  btn.className = btnClass;
  btn.textContent = "x";
  content.appendChild(btn);
  details.append(summary, content);
  return details;
}

describe("turn action overflow", () => {
  it("renders the grouped actions inline with the More summary hidden on desktop", () => {
    const details = inlineDisclosure("turn-actions-more", "turn-actions-group", "turn-action-btn");
    const slot = document.createElement("span");
    slot.className = "turn-actions-buttons";
    slot.appendChild(details);
    mount(slot);

    const summary = details.querySelector("summary")!;

    expect(details.open).toBe(false);
    expect(css(summary, "display")).toBe("none");
    expect(contentSkipped(details)).toBe(false);
  });
});

describe("file browser New menu", () => {
  it("renders both actions in the toolbar row with the trigger hidden on desktop", () => {
    const bar = document.createElement("div");
    bar.className = "view-toolbar-inner";
    const details = inlineDisclosure("fb-new-menu", "fb-new-actions", "icon-btn");
    const second = document.createElement("button");
    second.className = "icon-btn";
    second.textContent = "y";
    details.querySelector(".fb-new-actions")!.appendChild(second);
    bar.appendChild(details);
    mount(bar);

    const summary = details.querySelector("summary")!;
    const [a, b] = [...details.querySelectorAll<HTMLElement>(".fb-new-actions > .icon-btn")];

    // The trigger is phone-only.
    expect(css(summary, "display")).toBe("none");
    expect(contentSkipped(details)).toBe(false);
    expect(b!.getBoundingClientRect().y).toBe(a!.getBoundingClientRect().y);
    expect(b!.getBoundingClientRect().x).toBeGreaterThan(a!.getBoundingClientRect().x);
  });
});

describe("turn card header affordance", () => {
  async function turn(
    state: "open" | "folded" | "running" | "no-fold",
    over: { request?: string; n?: number; ts?: number } = {},
  ): Promise<HTMLElement> {
    const { buildTurnHeader } = await import("./fundamentals/turn-header.js");
    const card = document.createElement("div");
    card.className = "turn";
    if (state === "folded") {
      card.setAttribute("data-folded", "");
    }
    if (state === "running") {
      card.setAttribute("data-running", "");
    }
    if (state === "no-fold") {
      card.setAttribute("data-no-fold", "");
    }
    card.appendChild(
      buildTurnHeader({
        n: over.n ?? 1,
        outcome: "completed",
        ts: over.ts ?? Date.now(),
        request: over.request ?? "a request",
        attachments: [],
      }),
    );
    return mount(card);
  }

  it("the whole band is the marked surface, open and folded alike", async () => {
    // One gesture wherever the band is clicked, open or folded, matching the tool and delegate cards.
    for (const state of ["open", "folded"] as const) {
      const card = await turn(state);
      expect(css(card.querySelector(".turn-header")!, "cursor"), state).toBe("pointer");
    }
  });

  it("the prompt stays selectable; the meta row does not", async () => {
    // A target without eating selection: disclosure-row.ts skips a click that ends one, so `user-select: none` stops at
    // the meta row.
    const card = await turn("open");
    expect(css(card.querySelector(".turn-head-row")!, "user-select")).toBe("none");
    expect(css(card.querySelector(".turn-req-text")!, "user-select")).not.toBe("none");
  });

  it("a running turn's header claims nothing", async () => {
    // No fold while running, so no pointer cursor.
    const card = await turn("running");
    expect(css(card.querySelector(".turn-header")!, "cursor")).toBe("auto");
    expect(propertyWriters(card.querySelector(".turn-header")!, ["background-image"])).toEqual([]);
    expect(css(card.querySelector(".turn-fold-toggle")!, "display")).toBe("none");
  });

  it("a no-fold turn's header claims nothing either", async () => {
    // A `data-no-fold` turn has no toggle, so the band stops advertising one.
    const card = await turn("no-fold");
    expect(css(card.querySelector(".turn-header")!, "cursor")).toBe("auto");
    expect(propertyWriters(card.querySelector(".turn-header")!, ["background-image"])).toEqual([]);
    expect(css(card.querySelector(".turn-fold-toggle")!, "display")).toBe("none");
  });

  it("hovering the band washes it as a LAYER, keeping the tint", async () => {
    // `background-image: var(--layer-hover)` (the interaction-ladder contract, 01-tokens.css): in `background` it would
    // replace the band's tint.
    for (const state of ["open", "folded"] as const) {
      const header = (await turn(state)).querySelector(".turn-header")!;
      const writers = propertyWriters(header, ["background-image"]);
      expect(writers.at(-1)?.value, state).toBe("var(--layer-hover)");
      expect(winningBackground(header), `${state}: the tint stays`).toBe("var(--c-bg-tertiary)");
    }
  });

  it("the meta row paints no fill of its own — the band paints once", async () => {
    // Two translucent overlays would darken part of the band.
    for (const state of ["open", "folded"] as const) {
      const row = (await turn(state)).querySelector(".turn-head-row")!;
      expect(backgroundWriters(row), state).toEqual([]);
      expect(propertyWriters(row, ["background-image"]), state).toEqual([]);
    }
  });

  it("the fold toggle clears the 24px hit-target floor", async () => {
    // 24px minimum desktop target.
    const card = await turn("open");
    const btn = card.querySelector<HTMLElement>(".turn-fold-toggle")!;
    const r = btn.getBoundingClientRect();
    expect(r.width).toBeGreaterThanOrEqual(24);
    expect(r.height).toBeGreaterThanOrEqual(24);
  });

  it("the glyph did not grow with its hit target", async () => {
    // Growing the target must not grow the mark: ink stays inside the target, and the glyph uses the shared token.
    const card = await turn("open");
    const btn = card.querySelector<HTMLElement>(".turn-fold-toggle")!;
    const svg = card.querySelector<HTMLElement>(".turn-fold-toggle > .disclosure-chevron > svg")!;
    expect(svg.getBoundingClientRect().width).toBeLessThan(btn.getBoundingClientRect().width);
    // One chevron size across the transcript; compared to a bare chevron so an override fails.
    const glyph = card.querySelector<HTMLElement>(".turn-fold-toggle > .disclosure-chevron")!;
    const bare = document.createElement("span");
    bare.className = "disclosure-chevron";
    document.body.appendChild(bare);
    const base = css(bare, "--chev-size").trim();
    bare.remove();
    expect(base).not.toBe("");
    expect(css(glyph, "--chev-size").trim()).toBe(base);
  });

  it("gives the meta row a line box of its own", async () => {
    // The band is two rows: padding, row, `row-gap`, request text and rule. Read from computed `row-gap`.
    for (const tier of ["fine", "coarse"] as const) {
      document.documentElement.setAttribute("data-pointer", tier);
      try {
        const card = await turn("open", { request: "short request" });
        const header = card.querySelector<HTMLElement>(".turn-header")!;
        const row = card.querySelector<HTMLElement>(".turn-head-row")!;
        const text = card.querySelector<HTMLElement>(".turn-req-text")!;
        // Every member really is in the row.
        for (const sel of [".turn-fold-toggle", ".turn-n", ".turn-ts"]) {
          expect(row.querySelector(sel), `${tier}: ${sel}`).not.toBeNull();
        }
        expect(row.getBoundingClientRect().height, `${tier}: the row has a height`).toBeGreaterThan(
          0,
        );
        const pad = Number.parseFloat(css(header, "padding-top"));
        const gap = Number.parseFloat(css(header, "row-gap"));
        const border = Number.parseFloat(css(header, "border-bottom-width"));
        expect(header.getBoundingClientRect().height, tier).toBeCloseTo(
          pad * 2 +
            row.getBoundingClientRect().height +
            gap +
            text.getBoundingClientRect().height +
            border,
          1,
        );
      } finally {
        document.documentElement.removeAttribute("data-pointer");
      }
    }
  });

  it("clamps a folded prompt to four lines and an open one not at all", async () => {
    const long = "the quick brown fox jumps over the lazy dog. ".repeat(30);

    const folded = await turn("folded", { request: long });
    folded.style.inlineSize = "420px";
    const foldedText = folded.querySelector<HTMLElement>(".turn-req-text")!;
    expect(css(foldedText, "-webkit-line-clamp")).toBe("4");
    expect(foldedText.scrollHeight, "and it really does clip").toBeGreaterThan(
      foldedText.clientHeight,
    );

    const open = await turn("open", { request: long });
    open.style.inlineSize = "420px";
    const openText = open.querySelector<HTMLElement>(".turn-req-text")!;
    expect(css(openText, "-webkit-line-clamp")).not.toBe("4");
    expect(openText.scrollHeight, "a full prompt, however long").toBe(openText.clientHeight);
  });
});

describe("folded turn face", () => {
  // `turn-notice` is a card-level sibling of the face (29-turns.css); mounted where production puts it.
  function face(kind: "turn-face-prose" | "turn-notice", lines: number): HTMLElement {
    const card = document.createElement("div");
    card.className = "turn";
    card.setAttribute("data-folded", "");
    const faceEl = document.createElement("div");
    faceEl.className = "turn-face";
    const content = document.createElement("div");
    if (kind === "turn-face-prose") {
      content.className = "message assistant turn-face-prose";
      for (let i = 0; i < lines; i++) {
        const p = document.createElement("p");
        p.textContent = `line ${String(i)}`;
        content.appendChild(p);
      }
      faceEl.appendChild(content);
      card.appendChild(faceEl);
    } else {
      content.className = kind;
      content.dataset["severity"] = "broken";
      content.textContent = Array.from({ length: lines }, (_, i) => `line ${String(i)}`).join("\n");
      card.appendChild(faceEl);
      card.appendChild(content);
    }
    mount(card);
    return content;
  }

  it("the answer renders in full — the fold hides work, never the reply", () => {
    // The folded turn keeps the whole final answer; compactness comes from hiding tool cards, reasoning and delegates.
    for (const kind of ["turn-face-prose", "turn-notice"] as const) {
      const content = face(kind, 40);
      expect(content.scrollHeight, `${kind}: nothing clipped`).toBeLessThanOrEqual(
        content.clientHeight + 1,
      );
    }
  });
});

describe("prompt pill centring", () => {
  it("centres an icon when the touch floor makes its pill wider", () => {
    const pill = document.createElement("button");
    pill.className = "pill";
    pill.style.width = "44px";
    const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.setAttribute("width", "14");
    icon.setAttribute("height", "14");
    pill.appendChild(icon);
    mount(pill);

    const p = pill.getBoundingClientRect();
    const i = icon.getBoundingClientRect();
    expect(Math.abs(i.x + i.width / 2 - (p.x + p.width / 2))).toBeLessThanOrEqual(1);
  });
});

describe("turn body surface", () => {
  it("uses a dedicated body token while header and footer keep their tint", () => {
    const card = document.createElement("div");
    card.className = "turn";
    const header = document.createElement("div");
    header.className = "turn-header";
    const face = document.createElement("div");
    face.className = "turn-face";
    const footer = document.createElement("div");
    footer.className = "turn-footer";
    card.append(header, face, footer);
    mount(card);

    expect(winningBackground(card)).toBe("var(--c-turn-body)");
    expect(winningBackground(face)).toBe("var(--c-turn-body)");
    expect(winningBackground(header)).toBe("var(--c-bg-tertiary)");
    expect(winningBackground(footer)).toBe("var(--c-bg-tertiary)");
  });
});

describe("sub-page menu bars", () => {
  it("shows icon before label until measured icon-only mode", () => {
    const bar = document.createElement("nav");
    bar.className = "seg-bar";
    const tab = document.createElement("button");
    tab.className = "seg";
    const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.classList.add("seg-icon");
    const label = document.createElement("span");
    label.className = "seg-label";
    label.textContent = "General";
    tab.append(icon, label);
    bar.appendChild(tab);
    mount(bar);

    expect(css(tab, "display")).toBe("flex");
    expect(css(icon, "display")).toBe("block");
    expect(css(label, "display")).not.toBe("none");
    expect(tab.firstElementChild).toBe(icon);

    bar.classList.add("seg-bar-icons");
    expect(css(icon, "display")).toBe("block");
    expect(css(label, "display")).toBe("none");
  });

  // The subtitle defers to a labelled bar: `tab-bar-fit.ts` publishes `.seg-bar-named` while a bar shows its labels,
  // and 12-chat.css suppresses the subtitle then, or the section name prints twice.
  it("suppresses the title bar subtitle while the menu bar names its own section", () => {
    const area = document.createElement("div");
    area.id = "chat-area";
    const heading = document.createElement("div");
    heading.className = "titlebar-heading";
    const subtitle = document.createElement("span");
    subtitle.className = "titlebar-subtitle";
    subtitle.textContent = "Tools";
    heading.append(subtitle);
    const bar = document.createElement("nav");
    bar.className = "seg-bar";
    area.append(heading, bar);
    mount(area);

    bar.classList.add("seg-bar-named");
    expect(css(subtitle, "display")).toBe("none");

    bar.classList.remove("seg-bar-named");
    expect(css(subtitle, "display")).not.toBe("none");
  });
});

// The steer note is a card on the tool-card box, not a left rail: the leading rail is reserved for work this agent did
// not do itself (run card, delegated work).
describe("the mid-turn steer note's box", () => {
  function note(state: "read" | "dropped", origin: "user" | "agent"): HTMLElement {
    const el = document.createElement("div");
    el.className = "steer-note";
    el.dataset["state"] = state;
    el.dataset["origin"] = origin;
    const head = document.createElement("div");
    head.className = "steer-note-head";
    const label = document.createElement("span");
    label.className = "steer-note-label";
    label.textContent = "Mid-turn message";
    head.append(label);
    const body = document.createElement("div");
    body.className = "steer-note-body";
    const text = document.createElement("div");
    text.className = "steer-note-text";
    text.textContent = "actually target main";
    body.append(text);
    el.append(head, body);
    return mount(el);
  }

  it("resolves to the tool card's own fill and radius", () => {
    const n = note("read", "user");
    const card = document.createElement("div");
    card.className = "tool-call";
    const reference = mount(card);
    // Read off `.tool-call`: the claim is that they are the same box.
    const wantBG = css(reference, "background-color");
    const wantRadius = css(reference, "border-top-left-radius");

    mount(n);
    expect(css(n, "background-color")).toBe(wantBG);
    expect(css(n, "border-top-left-radius")).toBe(wantRadius);
  });

  it("carries a 1px border on every side, and no rail on any of them", () => {
    for (const state of ["read", "dropped"] as const) {
      const n = note(state, "user");
      for (const side of ["top", "right", "bottom", "left"] as const) {
        const w = Number.parseFloat(css(n, `border-${side}-width`));
        expect(w, `${state} border-${side}-width`).toBeCloseTo(1, 1);
      }
    }
  });

  // Targets the rail's writer, so a re-added wide border fails.
  it("has no rule anywhere writing a leading border wider than 1px", () => {
    const sheet = loadCSS("13-messages.css");
    for (const sel of [".steer-note", '.steer-note[data-state="dropped"]']) {
      const body = ruleBody(sheet, sel);
      expect(body, sel).not.toMatch(/border-inline-start:\s*[2-9]/);
      expect(body, sel).not.toMatch(/border-(inline-start|left)-width:\s*[2-9]/);
    }
    expect(sheet).not.toMatch(/\.steer-note[^{]*\{[^}]*border-inline-start:\s*[2-9]/);
  });

  // The origin is carried by label and glyph, not hue (WCAG 1.4.1).
  it("gives the two origins the same border, since the label is what separates them", () => {
    const mine = note("read", "user");
    const mineBorder = css(mine, "border-top-color");
    const theirs = note("read", "agent");
    expect(css(theirs, "border-top-color")).toBe(mineBorder);
  });

  // The card is the transcript's one measure; no `--content-max-w` of its own.
  it("takes the card's own width rather than capping its own measure", () => {
    host.style.inlineSize = "900px";
    const n = note("read", "user");
    expect(css(n, "max-width")).toBe("none");
    expect(n.getBoundingClientRect().width).toBeCloseTo(900, 0);
    host.style.removeProperty("inline-size");
  });
});
