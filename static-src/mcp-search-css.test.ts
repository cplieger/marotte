// The registry search panel's geometry, measured in a real browser against the assembled cascade (a DOM emulator
// reports every box as 0). The last case checks the binding asks for the class the pending face keys on.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// Hoisted above module consts, since one factory references both.
const stubs = vi.hoisted(() => ({
  bindLoadingState: vi.fn(() => () => undefined),
  /** Keyed by id, so the wiring case can say which element was bound. */
  byId: (id: string): HTMLElement => {
    const tag = id === "mcp-search-input" ? "input" : id === "mcp-search-btn" ? "button" : "div";
    const el = document.createElement(tag);
    el.id = id;
    return el;
  },
}));

vi.mock("./dom.js", () => ({ byId: stubs.byId }));
vi.mock("./actions/mcp.js", () => ({
  searchRegistry: { cancel: () => undefined, dispatch: async () => null },
  // Present-but-undefined for real-ESM linking; nothing here reaches its branch.
  registryFailureOf: undefined,
}));
vi.mock("./actions/index.js", () => ({
  subscribeToActions: () => () => undefined,
  bindLoadingState: stubs.bindLoadingState,
  debouncedDispatch: () => Object.assign(() => undefined, { cancel: () => undefined }),
  registerCleanup: () => undefined,
}));

import { renderRegistryResult, initSearchPanel } from "./mcp-panels-search.js";
import type { RegistryEntry as Entry } from "./wire/types.gen.js";

/** `40-a11y.css`'s ring: 2px at a 1px offset, painting the 3px outside the border box. */
const RING_PX = 3;

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

function entry(i: number, dual: boolean): Entry {
  const base: Entry = {
    name: `ex/server-${i}`,
    title: `Server ${i}`,
    version: "1.2.3",
    description:
      "a server that does a thing, described at some length by whoever published it to the registry",
    packages: [
      {
        registry_type: "npm",
        identifier: `@ex/server-${i}`,
        env_vars: [
          { name: "TOKEN", description: "an api token", required: true, secret: true },
          { name: "HOST", description: "for a self-hosted instance" },
        ],
      },
    ],
  };
  if (!dual) {
    return base;
  }
  return {
    ...base,
    remotes: [
      {
        type: "http",
        url: `https://server-${i}.example.com/mcp`,
        headers: [{ name: "X-Key", required: true, secret: true }],
      },
    ],
  };
}

interface Panel {
  panel: HTMLElement;
  searchRow: HTMLElement;
  input: HTMLInputElement;
  searchBtn: HTMLButtonElement;
  results: HTMLElement;
  rows: HTMLElement[];
}

/** As `static/index.html` declares it inside the modal card; rows from the production builder. */
function mountPanel(count: number, opts: { dual?: boolean } = {}): Panel {
  const card = document.createElement("div");
  card.className = "uip-modal-dialog mcp-modal-card";

  const tabs = document.createElement("nav");
  tabs.className = "seg-bar mcp-modal-tabs";
  for (const label of ["Search registry", "Remote URL", "npm package", "Paste JSON"]) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "seg";
    const span = document.createElement("span");
    span.className = "seg-label";
    span.textContent = label;
    b.appendChild(span);
    tabs.appendChild(b);
  }
  card.appendChild(tabs);

  const panel = document.createElement("div");
  panel.className = "mcp-mode-panel";
  panel.dataset["mcpMode"] = "search";

  const hint = document.createElement("p");
  hint.className = "mcp-mode-hint";
  hint.textContent = "Search the official MCP registry for published integrations.";
  panel.appendChild(hint);

  const searchRow = document.createElement("div");
  searchRow.className = "mcp-search-row";
  const input = document.createElement("input");
  input.className = "tool-form-input";
  input.type = "search";
  const searchBtn = document.createElement("button");
  searchBtn.type = "button";
  searchBtn.id = "mcp-search-btn";
  searchBtn.className = "action-pill";
  const glyph = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("class", "ic-ui");
  searchBtn.appendChild(glyph);
  searchRow.append(input, searchBtn);
  panel.appendChild(searchRow);

  const results = document.createElement("div");
  results.className = "mcp-search-results";
  const rows: HTMLElement[] = [];
  for (let i = 0; i < count; i++) {
    const row = renderRegistryResult(entry(i, opts.dual === true));
    rows.push(row);
    results.appendChild(row);
  }
  panel.appendChild(results);
  card.appendChild(panel);
  host.replaceChildren(card);
  return { panel, searchRow, input, searchBtn, results, rows };
}

/** Resolved rather than asserted, so a missing part throws naming it. */
function part(row: HTMLElement | undefined, selector: string): HTMLElement {
  const hit = row?.querySelector<HTMLElement>(selector) ?? null;
  if (hit === null) {
    throw new Error(`no ${selector} in the rendered row`);
  }
  return hit;
}

/** Overflow clips at the padding box, so these are the edges a focus ring must stay inside. */
function clip(el: HTMLElement): { left: number; right: number; top: number } {
  const r = el.getBoundingClientRect();
  const cs = getComputedStyle(el);
  return {
    left: r.left + Number.parseFloat(cs.borderLeftWidth || "0"),
    right: r.right - Number.parseFloat(cs.borderRightWidth || "0"),
    top: r.top + Number.parseFloat(cs.borderTopWidth || "0"),
  };
}

/** `clientWidth` is the padding box minus the reserved gutter. */
function reservedGutter(el: HTMLElement): number {
  const cs = getComputedStyle(el);
  const paddingBox =
    el.getBoundingClientRect().width -
    Number.parseFloat(cs.borderLeftWidth || "0") -
    Number.parseFloat(cs.borderRightWidth || "0");
  return paddingBox - el.clientWidth;
}

describe("the premises these measurements rest on", () => {
  it("the search input takes the app's focus ring on programmatic focus", () => {
    // The ring is the clearance case's premise. `02-reset.css` writes `outline: none` on `:focus` at higher specificity
    // and loses only because it is `@layer reset`; if layering moved, the clearance case would measure nothing.
    const { input } = mountPanel(1);
    input.focus();
    const cs = getComputedStyle(input);
    expect(input.matches(":focus-visible")).toBe(true);
    expect(cs.outlineStyle).toBe("solid");
    expect(Number.parseFloat(cs.outlineWidth) + Number.parseFloat(cs.outlineOffset)).toBe(RING_PX);
  });

  it("the panel clips the INLINE axis too, which is why the clearance is needed", () => {
    // `overflow-y: auto` beside a `visible` inline axis computes to `auto` (CSS Overflow 3 §3), so all four sides clip.
    const cs = getComputedStyle(mountPanel(1).panel);
    expect(cs.overflowY).toBe("auto");
    expect(cs.overflowX).toBe("auto");
  });
});

describe("the focus ring has room inside the panel", () => {
  it("on the search input's leading edge and the button's trailing edge", () => {
    // At 0px edges the ring is clipped. A row's own ring sits 13px inside and is not asserted.
    const { panel, input, searchBtn } = mountPanel(3);
    input.focus();
    const edges = clip(panel);
    expect(input.getBoundingClientRect().left - edges.left).toBeGreaterThanOrEqual(RING_PX);
    expect(edges.right - searchBtn.getBoundingClientRect().right).toBeGreaterThanOrEqual(RING_PX);
  });
});

describe("one scroller, so the result column cannot be squished", () => {
  it("leaves the results element a plain block rather than a second scroller", () => {
    // A nested scroller takes its scrollbar out of the result box alone, squishing the rows.
    const { results } = mountPanel(20);
    expect(getComputedStyle(results).overflowY).toBe("visible");
    expect(results.scrollHeight).toBe(results.clientHeight);
  });

  it("puts a result row's edges exactly on the search row's", () => {
    const { searchRow, input, searchBtn, rows } = mountPanel(20);
    const row = rows[0]?.getBoundingClientRect();
    expect(row?.left).toBe(input.getBoundingClientRect().left);
    expect(row?.right).toBe(searchBtn.getBoundingClientRect().right);
    expect(row?.width).toBe(searchRow.getBoundingClientRect().width);
  });

  it("reserves the scrollbar's gutter before there is one, so the column never moves", () => {
    // `scrollbar-gutter: stable`, as `#messages-wrap`, so rows do not jump inward while typing.
    const short = mountPanel(1);
    expect(short.panel.scrollHeight).toBe(short.panel.clientHeight);
    expect(reservedGutter(short.panel)).toBeGreaterThan(0);
    const narrow = short.rows[0]?.getBoundingClientRect().width;
    const shortClient = short.panel.clientWidth;

    const long = mountPanel(20);
    expect(long.panel.scrollHeight).toBeGreaterThan(long.panel.clientHeight);
    expect(long.panel.clientWidth).toBe(shortClient);
    expect(long.rows[0]?.getBoundingClientRect().width).toBe(narrow);
  });

  it("keeps the query box on screen once the panel scrolls", () => {
    // Without a sticky row the input leaves the viewport as the reader browses results.
    const { panel, searchRow } = mountPanel(20);
    expect(getComputedStyle(searchRow).position).toBe("sticky");
    panel.scrollTop = 240;
    expect(panel.scrollTop).toBeGreaterThan(0);
    expect(Math.abs(searchRow.getBoundingClientRect().top - clip(panel).top)).toBeLessThanOrEqual(
      1,
    );
  });
});

describe("a result is a compact row", () => {
  it("is one line tall, whatever the entry declares", () => {
    // An upper bound rather than a pinned number, so a font-metric shift does not fail it.
    const single = mountPanel(1).rows[0]?.getBoundingClientRect().height ?? 0;
    const dual = mountPanel(1, { dual: true }).rows[0]?.getBoundingClientRect().height ?? 0;
    expect(single).toBeGreaterThan(24);
    expect(single).toBeLessThanOrEqual(48);
    // A second install path adds a button, not a block.
    expect(dual).toBe(single);
  });

  it("reads as one row: the install button and the summary are the same height", () => {
    // `.btn-small`'s `--btn-h` would floor the row at 36px; an action in a dense row takes the dense tier.
    const { rows } = mountPanel(1);
    const h = part(rows[0], ".mcp-result-summary").getBoundingClientRect().height;
    expect(h).toBeGreaterThan(24);
    expect(part(rows[0], ".mcp-install-btn").getBoundingClientRect().height).toBe(h);
  });

  it("shows the whole list rather than two of it", () => {
    // The viewport is the browser project's fixed 1280x720.
    const { panel, rows } = mountPanel(20);
    const rowH = rows[0]?.getBoundingClientRect().height ?? 1;
    expect(Math.floor(panel.clientHeight / rowH)).toBeGreaterThanOrEqual(8);
  });

  it("carries a chevron that says it expands, and turns it when it does", () => {
    // The glyph has a real box (`10-shell-app.css` sizes the SVG, not the span) and the turn keys off `[open]`.
    const { rows } = mountPanel(1);
    const chevron = part(rows[0], ".disclosure-chevron");
    expect(chevron.getBoundingClientRect().width).toBeGreaterThan(8);
    // Read off `--chev-turn`: the transform transitions, so the computed matrix just after the toggle is still closed.
    const closed = getComputedStyle(chevron).getPropertyValue("--chev-turn").trim();
    expect(closed).toBe("-90deg");
    expect(getComputedStyle(chevron).transform).not.toBe("none");

    const disc = part(rows[0], ".mcp-result-disc");
    if (disc instanceof HTMLDetailsElement) {
      disc.open = true;
    }
    expect(getComputedStyle(chevron).getPropertyValue("--chev-turn").trim()).toBe("0deg");
  });

  it("grows when opened, and gives the description its own line", () => {
    const { rows } = mountPanel(1);
    const row = rows[0];
    const disc = part(row, ".mcp-result-disc");
    const desc = part(row, ".mcp-result-desc");
    const closedH = row?.getBoundingClientRect().height ?? 0;
    const closedDesc = desc.getBoundingClientRect();
    expect(getComputedStyle(desc).whiteSpace).toBe("nowrap");

    if (disc instanceof HTMLDetailsElement) {
      disc.open = true;
    }
    const openDesc = desc.getBoundingClientRect();
    expect(row?.getBoundingClientRect().height).toBeGreaterThan(closedH);
    expect(getComputedStyle(desc).whiteSpace).toBe("normal");
    // Its own line: wider than the space beside the name.
    expect(openDesc.width).toBeGreaterThan(closedDesc.width);
    expect(openDesc.top).toBeGreaterThan(closedDesc.top);
  });
});

describe("the search button reports a query in flight", () => {
  it("swaps its magnifier for the shared spinning ring, at the shared period", () => {
    // `bindLoadingState` adds the class; the glyph is replaced since the pill is icon-only. Period from `--spin-dur`.
    const { searchBtn } = mountPanel(1);
    const idle = searchBtn.getBoundingClientRect();
    const glyph = part(searchBtn, "svg");
    expect(getComputedStyle(glyph).display).not.toBe("none");

    searchBtn.classList.add("is-searching");
    searchBtn.disabled = true;
    searchBtn.setAttribute("aria-busy", "true");

    expect(getComputedStyle(glyph).display).toBe("none");
    const ring = getComputedStyle(searchBtn, "::after");
    expect(ring.animationName).toBe("vk-spin");
    expect(ring.animationDuration).toBe("0.6s");
    expect(ring.animationIterationCount).toBe("infinite");
    // The busy face reads the aria-busy this binding also sets, so the ring is not dimmed.
    expect(getComputedStyle(searchBtn).opacity).toBe("1");
    // The pending face must not resize its control.
    expect(searchBtn.getBoundingClientRect().width).toBe(idle.width);
    expect(searchBtn.getBoundingClientRect().height).toBe(idle.height);
  });

  it("asks bindLoadingState for that class, on the search button", () => {
    // The class must be asked for at the binding; a rule nothing applies looks like no rule.
    stubs.bindLoadingState.mockClear();
    initSearchPanel();
    const calls = stubs.bindLoadingState.mock.calls as unknown as [
      string,
      HTMLElement,
      Record<string, unknown>?,
    ][];
    const bound = calls.filter((c) => c[0] === "mcp.search_registry");
    expect(bound).toHaveLength(1);
    expect(bound[0]?.[1].id).toBe("mcp-search-btn");
    expect(bound[0]?.[2]).toEqual({ pendingClass: "is-searching" });
  });
});
