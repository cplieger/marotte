// THE SCOPE CHIP HOLDS THE ROW'S FINAL SLOT, so the word "workspace" sits on one x down the whole
// card and a reader can compare it without reading each row.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { SessionMode } from "./types.js";

vi.mock("./chat.js", () => ({ createSession: vi.fn() }));
vi.mock("./actions/chat.js", () => ({ setMode: { dispatch: vi.fn() } }));
const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiGet }));
vi.mock("./store.js", () => ({
  getActive: vi.fn(() => undefined),
  activeSession: { value: undefined },
  // Present-but-inert so real-ESM linking succeeds; no case here calls them.
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./pill-expand.js", () => ({
  makeExpandable: (
    _pill: HTMLElement,
    _list: HTMLElement,
    opts: { onExpand: () => void; onCollapse: () => void },
  ) => {
    expandList = opts.onExpand;
    collapseList = opts.onCollapse;
  },
  collapseAll: vi.fn(),
}));
vi.mock("@cplieger/ui-primitives/roving-focus", () => ({
  rovingFocus: () => ({ refresh: vi.fn() }),
}));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
vi.mock("./dom.js", () => ({
  $: {
    get rolePill() {
      return document.getElementById("role-pill") as HTMLElement;
    },
    get roleList() {
      return document.getElementById("role-list") as HTMLElement;
    },
  },
  byId: (id: string) => document.getElementById(id) as HTMLElement,
}));

let expandList: (() => void) | undefined;
let collapseList: (() => void) | undefined;
const refreshCatalog = vi.fn();

const { initRolePicker } = await import("./role-picker.js");
const { setCatalogModes } = await import("./roles.js");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.documentElement.dataset["pointer"] = "fine";
});

afterAll(() => {
  style.remove();
  document.documentElement.removeAttribute("data-pointer");
});

/** The pill and its card as `static/index.html` mounts them: a `.pill-slot` holding the trigger
 *  and, as its SIBLING, the card that IS `#role-list`, open (`is-open`). */
function mountPill(): HTMLElement {
  document.body.innerHTML = `
    <span class="pill-slot">
      <button id="role-pill" class="pill pill-expandable" type="button">
        <span id="role-pill-icon"></span><span id="role-pill-label"></span>
      </button>
      <span id="role-list" class="pill-expand-content pill-role-list is-open"></span>
    </span>
  `;
  return document.getElementById("role-list") as HTMLElement;
}

/** Expand: the card renders the catalog as it stands and reads it only through the injected
 *  `refreshCatalog`. */
function openList(): HTMLElement {
  const list = mountPill();
  initRolePicker(refreshCatalog);
  expandList?.();
  return list;
}

function rowFor(list: HTMLElement, label: string): HTMLElement {
  const row = Array.from(list.querySelectorAll<HTMLElement>(".pill-role-item")).find(
    (r) => r.querySelector(".pill-role-name")?.textContent === label,
  );
  expect(row, `the card must render a row labelled ${label}`).not.toBeUndefined();
  return row as HTMLElement;
}

const CATALOG: readonly SessionMode[] = [
  { id: "vibe", name: "Default", description: "General", source: "bundled" },
  // Two workspace agents, one shadowing a global one, so the card renders a marked row and an
  // unmarked one: the two the alignment is measured across.
  { id: "reviewer", name: "reviewer", source: "workspace", shadows: "global" },
  { id: "only-here", name: "only-here", source: "workspace" },
  // A row long enough to size the card past its 14rem floor, so the two rows measured below have
  // real free space in them. Without it the widest row IS a measured row, every auto margin
  // resolves to 0, and both placements agree.
  { id: "an-agent-whose-name-runs-past-the-floor-on-its-own", name: "", source: "bundled" },
];

beforeEach(() => {
  refreshCatalog.mockClear();
  setCatalogModes(CATALOG);
});

function rowNames(list: HTMLElement): string[] {
  return Array.from(list.querySelectorAll(".pill-role-name")).map((n) => n.textContent ?? "");
}

describe("the mode card's shadow marker", () => {
  it("marks a catalog mode that shadows another and names both scopes in its tooltip", () => {
    const row = rowFor(openList(), "Reviewer");
    expect(row.querySelector(".pill-role-shadow")?.textContent).toBe("shadows global");
    expect(row.dataset["tooltip"]).toBe(
      "This workspace agent shadows the global agent of the same name. The workspace definition is the one a run uses.",
    );
    expect(rowFor(document.body, "Only Here").querySelector(".pill-role-shadow")).toBeNull();
  });

  it("reads the catalog only through the injected refresh when it opens", () => {
    openList();
    expect(apiGet).not.toHaveBeenCalled();
    expect(refreshCatalog).toHaveBeenCalledTimes(1);
  });
});

describe("the open mode card and the catalog", () => {
  it("re-reads the catalog each time it opens", () => {
    openList();
    collapseList?.();
    expandList?.();
    expect(refreshCatalog).toHaveBeenCalledTimes(2);
  });

  it("shows a custom agent that lands while the card is open", () => {
    // Opened during startup: no catalog yet, so the card holds the static bundled list.
    setCatalogModes([]);
    const list = openList();
    expect(rowNames(list)).not.toContain("Only Here");

    setCatalogModes(CATALOG);
    expect(rowNames(list)).toContain("Only Here");
    expect(list.querySelector(".pill-role-hint")?.textContent).toBe("Custom agents");
  });

  it("leaves a closed card alone when the catalog changes", () => {
    const list = openList();
    collapseList?.();
    const before = list.querySelector(".pill-role-item");
    setCatalogModes([...CATALOG, { id: "late", name: "late", source: "workspace" }]);
    expect(list.querySelector(".pill-role-item")).toBe(before);
  });

  it("keeps the rows, and the focus on them, when an answer changes nothing", () => {
    const list = openList();
    const row = rowFor(list, "Only Here");
    row.focus();
    setCatalogModes(CATALOG.map((m) => ({ ...m })));
    expect(rowFor(list, "Only Here")).toBe(row);
    expect(document.activeElement).toBe(row);
  });

  it("keeps focus on the same agent when an answer adds a row", () => {
    const list = openList();
    rowFor(list, "Only Here").focus();
    setCatalogModes([...CATALOG, { id: "late", name: "late", source: "workspace" }]);
    expect(rowNames(list)).toContain("Late");
    expect(document.activeElement).toBe(rowFor(list, "Only Here"));
  });
});

describe("the mode card's trailing chips", () => {
  it("builds the shadow badge ahead of the scope chip", () => {
    const list = openList();
    const classes = Array.from(rowFor(list, "Reviewer").children).map((c) => c.className);

    expect(classes.indexOf("pill-role-shadow")).toBeGreaterThan(-1);
    expect(
      classes.indexOf("pill-role-shadow"),
      "the scope chip owns the row's final slot, so the badge is built before it",
    ).toBeLessThan(classes.indexOf("pill-role-scope"));
  });

  it("ends every scope chip on one x, badge or no badge", () => {
    const list = openList();
    const marked = rowFor(list, "Reviewer").querySelector<HTMLElement>(".pill-role-scope");
    const plain = rowFor(list, "Only Here").querySelector<HTMLElement>(".pill-role-scope");
    expect(marked, "the marked row must render a scope chip").not.toBeNull();
    expect(plain, "the unmarked row must render a scope chip").not.toBeNull();

    const right = (n: HTMLElement): number => Math.round(n.getBoundingClientRect().right);
    expect(
      right(marked as HTMLElement),
      "a badge on the row must not move the scope chip: the name takes the free " +
        "space, so the chips end where the card's content box does",
    ).toBe(right(plain as HTMLElement));
  });

  it("keeps the badge beside the scope chip, not beside the name", () => {
    // What the name's `flex-grow` buys that the reorder alone does not: an auto margin on the scope
    // chip would put the row's whole free space BETWEEN the two chips, stranding the badge against
    // the name.
    const list = openList();
    const row = rowFor(list, "Reviewer");
    const badge = row.querySelector<HTMLElement>(".pill-role-shadow");
    const scope = row.querySelector<HTMLElement>(".pill-role-scope");
    expect(badge, "the marked row must render a badge").not.toBeNull();
    expect(scope, "the marked row must render a scope chip").not.toBeNull();

    const between =
      (scope as HTMLElement).getBoundingClientRect().left -
      (badge as HTMLElement).getBoundingClientRect().right;
    expect(Math.round(between)).toBe(Math.round(parseFloat(getComputedStyle(row).columnGap)));
  });
});
