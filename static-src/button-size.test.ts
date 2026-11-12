// Every button is the chat toolbar's on every tier: the same painted box (`--btn-h`) and a target that
// reaches `--hit-floor` through the shared expander, with no two targets overlapping. CENSUS names every
// button family and its reference; a family that keeps its own size says where that is documented. On a
// row too narrow for the fullest labelled line, the model and mode pills drop their text.
// The fixture is the production `#app`, so the toolbar reference and the controls share one layout, and the touch
// contract's surfaces (`__test-helpers__/app-surfaces.ts`) for the controls scripts add.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { buildAttachmentPill } from "./attachment-pill.js";
import { iconEl } from "./icon-el.js";
import { ICON_CANCEL, ICON_CLOSE, ICON_HOURGLASS, ICON_MODEL, ICON_SEND } from "./icons.js";
import { setActive, setSessions } from "./store.js";
import type { updateContextBar as updateContextBarFn } from "./status.js";
import { makeSession } from "./__test-helpers__/model.js";
import { BUTTON_REFERENCE, MENU_ROW } from "./__test-helpers__/button-reference.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  SURFACES as CATALOGUE,
  VIEWPORTS as CATALOGUE_VIEWPORTS,
  clearSurface,
  mountSurface as mountCatalogue,
  type Size,
  type Surface as CatalogueSurface,
} from "./__test-helpers__/app-surfaces.js";

type Pointer = "coarse" | "fine";
type State = "idle" | "steer" | "queue";

const PHONE = [390, 844] as const;
const IPAD = [1024, 768] as const;
const DESKTOP = [1440, 900] as const;

/** The reference: the chat toolbar's icon buttons, the phone's menu toggle among them. */
const TOOLBAR = [
  "find-btn",
  "files-btn",
  "git-btn",
  "shell-btn",
  "docs-btn",
  "settings-btn",
  "menu-toggle",
];

/** Icon-only by construction, so square at the reference; the context pie carries its percentage. */
const SQUARE = ["chat-options-btn", "task-list-pill", "midturn-send-btn", "scroll-bottom"];

/** The widest pills kiro-cli's catalog renders: a stand-in with as many characters as its widest model's name,
 *  with its widest tier label, and its widest built-in mode. The fit thresholds in `15-input.css` are decided on them. */
const MODEL_ID = "model-charlie-5.5";
const MODEL = "model charlie 5.5";
const EFFORT = "medium";
const MODE_ID = "semantic_reviewer";
const MODE = "Semantic Reviewer";

/** The production writers of the model and mode pills, loaded once a page is mounted: their module graph
 *  resolves page elements at import, and `initRolePicker` installs a lasting effect, so it runs once. */
let writers: { updateContextBar: typeof updateContextBarFn } | undefined;
let sessionSeq = 0;

function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

let style: HTMLStyleElement;
let entry: { readonly width: number; readonly height: number };

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(async () => {
  style.remove();
  document.body.style.margin = "";
  await page.viewport(entry.width, entry.height);
});

afterEach(() => {
  clearSurface();
  delete document.documentElement.dataset["pointer"];
});

function byId(id: string): HTMLElement {
  const found = document.getElementById(id);
  if (found === null) {
    throw new Error(`fixture: no #${id}`);
  }
  return found;
}

function label(text: string): HTMLSpanElement {
  const span = document.createElement("span");
  span.className = "send-btn-label";
  span.textContent = text;
  return span;
}

/** The model, mode and context pills as `status.ts` and `role-picker.ts` write them. */
async function writeNames(): Promise<void> {
  if (writers === undefined) {
    const [{ updateContextBar }, { initRolePicker }] = await Promise.all([
      import("./status.js"),
      import("./role-picker.js"),
    ]);
    initRolePicker(() => {
      /* no catalog to refresh */
    });
    writers = { updateContextBar };
  }
  sessionSeq += 1;
  const id = `c-census-${String(sessionSeq)}`;
  setSessions([makeSession({ id, current_mode_id: MODE_ID })]);
  setActive(id);
  writers.updateContextBar({
    pct: 66,
    contextSize: 200_000,
    compaction: { band: null, t: 0 },
    credits: 0,
    turnCount: 0,
    lastTurnMs: 0,
    model: MODEL_ID,
    effort: EFFORT,
  });
  await nextFrame();
}

/** The DOM `model-switcher.ts`, `prompt-input.ts` and `attachment-pill.ts` write. */
async function dress(state: State): Promise<void> {
  byId("switch-model-btn").prepend(iconEl(ICON_MODEL));
  await writeNames();
  const send = byId("send-btn");
  if (state === "idle") {
    send.replaceChildren(label("Send"), iconEl(ICON_SEND));
  } else {
    send.replaceChildren(label("Cancel"), iconEl(ICON_CANCEL));
    const midTurn = byId("midturn-send-btn");
    midTurn.hidden = false;
    midTurn.replaceChildren(
      label(state === "steer" ? "Steer" : "Queue"),
      iconEl(state === "steer" ? ICON_SEND : ICON_HOURGLASS),
    );
    byId("task-list-pill").classList.remove("hidden");
  }
  const row = byId("attachment-row");
  row.classList.remove("hidden");
  row.appendChild(
    buildAttachmentPill(
      { path: "/workspace/notes.txt", name: "notes.txt" },
      {
        onRemove: () => {
          /* the chip only has to render */
        },
      },
    ),
  );
  byId("scroll-bottom").classList.remove("hidden");
}

async function mount(
  [width, height]: readonly [number, number],
  pointer: Pointer,
  state: State,
): Promise<void> {
  await page.viewport(width, height);
  expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
    width,
    height,
  ]);
  document.documentElement.dataset["pointer"] = pointer;
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  const app = doc.getElementById("app");
  if (app === null) {
    throw new Error("index.html has no #app");
  }
  document.body.replaceChildren(document.importNode(app, true));
  await dress(state);
}

/** The run tab in place of the chat: its composer's Send and its resume button. */
async function mountRun(viewport: readonly [number, number], pointer: Pointer): Promise<void> {
  await mount(viewport, pointer, "idle");
  byId("chat-view").classList.add("hidden");
  byId("run-view").classList.remove("hidden");
  byId("run-scroll-bottom").classList.remove("hidden");
  byId("run-send-btn").replaceChildren(iconEl(ICON_SEND));
}

function shown(el: Element): boolean {
  return el.getClientRects().length > 0 && el.getBoundingClientRect().width > 0;
}

/** What a control paints: the chip's open button is transparent, so its pill is the painted box. */
function painted(el: HTMLElement): DOMRect {
  return (el.closest<HTMLElement>(".attachment-pill") ?? el).getBoundingClientRect();
}

/** The composer's controls in row order, then the resume button: every one a target the contract covers. */
function controls(): HTMLElement[] {
  const row = [
    ...document.querySelectorAll<HTMLElement>(".prompt-pills .pill, .prompt-pills .send-btn"),
  ];
  const chip = [...document.querySelectorAll<HTMLElement>("#attachment-row .attachment-open")];
  return [...row, ...chip, byId("scroll-bottom")].filter(shown);
}

function toolbar(): HTMLElement[] {
  return TOOLBAR.map(byId).filter(shown);
}

function nameOf(el: HTMLElement): string {
  return el.id === "" ? el.className : el.id;
}

function floorPx(): number {
  const root = getComputedStyle(document.documentElement);
  return Number.parseFloat(root.getPropertyValue("--hit-floor")) * Number.parseFloat(root.fontSize);
}

interface Box {
  readonly left: number;
  readonly top: number;
  readonly right: number;
  readonly bottom: number;
}

const STEP = 0.25;
/** Two probes of one target differ by up to a step on each side, since the scan starts at a fractional centre. */
const SLACK = 4 * STEP;

function owns(el: Element, x: number, y: number): boolean {
  const hit = document.elementFromPoint(x, y);
  return hit !== null && (hit === el || el.contains(hit));
}

/** The element a press at a point lands on, named for a failure message. */
function answering(x: number, y: number): string {
  const hit = document.elementFromPoint(x, y);
  return hit instanceof HTMLElement ? nameOf(hit) : String(hit?.nodeName ?? "nothing");
}

/** What a press reaches: from the centre outward on each axis until a point stops answering to `el`. */
function hitRect(el: HTMLElement): Box {
  const r = el.getBoundingClientRect();
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;
  const reach = (dx: number, dy: number): number => {
    let last = 0;
    for (let t = STEP; t < 80; t += STEP) {
      if (!owns(el, cx + dx * t, cy + dy * t)) {
        break;
      }
      last = t;
    }
    return last;
  };
  return {
    left: cx - reach(-1, 0),
    right: cx + reach(1, 0),
    top: cy - reach(0, -1),
    bottom: cy + reach(0, 1),
  };
}

/** The declared target: the border box joined with an absolute `::after`, placed in the host's padding box. */
function targetRect(el: HTMLElement): Box {
  const r = el.getBoundingClientRect();
  const pseudo = getComputedStyle(el, "::after");
  if (pseudo.content === "none" || pseudo.position !== "absolute") {
    return r;
  }
  const left = r.left + el.clientLeft + Number.parseFloat(pseudo.left);
  const top = r.top + el.clientTop + Number.parseFloat(pseudo.top);
  return {
    left: Math.min(r.left, left),
    top: Math.min(r.top, top),
    right: Math.max(r.right, left + Number.parseFloat(pseudo.width)),
    bottom: Math.max(r.bottom, top + Number.parseFloat(pseudo.height)),
  };
}

/** The pill row's one gap, which its controls' targets reach half across on each side. */
function rowGap(): number {
  return Number.parseFloat(getComputedStyle(document.querySelector(".prompt-pills")!).columnGap);
}

const width = (b: Box): number => b.right - b.left;
const height = (b: Box): number => b.bottom - b.top;

/** The toolbar's one painted size and one hit size, asserted to be one before it is used. */
function reference(): { painted: { w: number; h: number }; hit: { w: number; h: number } } {
  const buttons = toolbar();
  expect(buttons.length, "the toolbar renders its buttons").toBeGreaterThanOrEqual(6);
  const first = buttons[0]!;
  const painted = first.getBoundingClientRect();
  const hit = hitRect(first);
  for (const b of buttons) {
    const r = b.getBoundingClientRect();
    expect([r.width, r.height], `${b.id} paints the toolbar's one size`).toEqual([
      painted.width,
      painted.height,
    ]);
    const h = hitRect(b);
    expect(
      Math.abs(width(h) - width(hit)),
      `${b.id} reaches the toolbar's one width`,
    ).toBeLessThanOrEqual(SLACK);
    expect(
      Math.abs(height(h) - height(hit)),
      `${b.id} reaches the toolbar's one height`,
    ).toBeLessThanOrEqual(SLACK);
  }
  return {
    painted: { w: painted.width, h: painted.height },
    hit: { w: width(hit), h: height(hit) },
  };
}

const STATES: readonly State[] = ["idle", "steer", "queue"];

describe("the composer's controls and the resume button, against the toolbar's buttons", () => {
  it("gives every one the toolbar button's hit area at the phone preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(PHONE, "coarse", state);
      const ref = reference();
      const floor = floorPx();
      expect(floor, "the coarse floor").toBe(44);
      expect(Math.abs(ref.hit.h - floor), "the toolbar's target is the floor").toBeLessThanOrEqual(
        SLACK,
      );
      const off: string[] = [];
      for (const el of controls()) {
        const hit = hitRect(el);
        // A pill row control reaches half the row's gap past each side, so its width is the toolbar's plus one gap.
        const reach = el.closest(".prompt-pills") === null ? 0 : rowGap();
        if (Math.abs(height(hit) - ref.hit.h) > SLACK) {
          off.push(`${nameOf(el)} reaches ${String(height(hit))}px tall`);
        }
        if (width(hit) < floor - 2 * STEP) {
          off.push(`${nameOf(el)} reaches ${String(width(hit))}px wide`);
        }
        if (SQUARE.includes(el.id) && Math.abs(width(hit) - ref.hit.w - reach) > SLACK) {
          off.push(`${nameOf(el)} reaches ${String(width(hit))}px wide, not the toolbar's`);
        }
      }
      expect(off, `${state}: every target is the toolbar's`).toEqual([]);
    }
  });

  it("meets neighbouring pill row targets across the gap, never overlapping, at the phone preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(PHONE, "coarse", state);
      const row = [
        ...document.querySelectorAll<HTMLElement>(".prompt-pills .pill, .prompt-pills .send-btn"),
      ]
        .filter(shown)
        .sort((a, b) => a.getBoundingClientRect().left - b.getBoundingClientRect().left);
      const spacer = document.querySelector(".pill-spacer")!.getBoundingClientRect();
      const off: string[] = [];
      for (let i = 1; i < row.length; i++) {
        const a = row[i - 1]!;
        const b = row[i]!;
        const ra = a.getBoundingClientRect();
        const rb = b.getBoundingClientRect();
        if (spacer.width > 0 && spacer.left > ra.right - 0.5 && spacer.right < rb.left + 0.5) {
          continue; // the spacer's free space: no neighbour to meet
        }
        const x = (ra.right + rb.left) / 2;
        const y = (ra.top + ra.bottom) / 2;
        if (!owns(a, x, y) && !owns(b, x, y)) {
          off.push(`${nameOf(a)} | ${nameOf(b)}: the gap's middle reaches ${answering(x, y)}`);
        }
        const ta = targetRect(a);
        const tb = targetRect(b);
        if (ta.right > tb.left + 0.01) {
          off.push(
            `${nameOf(a)} | ${nameOf(b)}: targets overlap by ${String(ta.right - tb.left)}px`,
          );
        }
      }
      expect(row.length, `${state}: the row renders its controls`).toBeGreaterThanOrEqual(3);
      expect(off, `${state}: neighbours meet edge to edge`).toEqual([]);
    }
  });

  it("gives Send and Cancel a square hit area at the floor at the phone preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(PHONE, "coarse", state);
      const send = byId("send-btn");
      const painted = send.getBoundingClientRect();
      const hit = hitRect(send);
      expect(painted.width, `${state}: a square button`).toBe(painted.height);
      // Square but for the half gap it reaches on each side.
      expect(
        Math.abs(width(hit) - rowGap() - height(hit)),
        `${state}: a square target`,
      ).toBeLessThanOrEqual(SLACK);
      expect(width(hit), `${state}: at the floor`).toBeGreaterThanOrEqual(floorPx() - 2 * STEP);
    }
  });
});

describe("the model and mode pills, by the room the row has", () => {
  const labels = (): HTMLElement[] => [byId("ctx-model-pill"), byId("role-pill-label")];

  it("fits the row on one line at the phone preset, with no control cut", async () => {
    for (const pointer of ["coarse", "fine"] as const) {
      for (const state of STATES) {
        await mount(PHONE, pointer, state);
        const at = `${pointer} ${state}`;
        const row = document.querySelector<HTMLElement>(".prompt-pills");
        if (row === null) {
          throw new Error("no pill row");
        }
        expect(row.scrollWidth, `${at}: nothing overflows the row`).toBeLessThanOrEqual(
          row.clientWidth,
        );
        const items = controls().filter((el) => row.contains(el));
        const tops = new Set(items.map((el) => el.getBoundingClientRect().top));
        expect(tops.size, `${at}: one line`).toBe(1);
        // Only a clipping label can be cut; a slot's scroll width also counts its pill's expander.
        const cut = [...row.querySelectorAll<HTMLElement>("span")]
          .filter(shown)
          .filter((el) => getComputedStyle(el).textOverflow === "ellipsis")
          .filter((el) => el.scrollWidth > el.clientWidth)
          .map(
            (el) =>
              `${el.id === "" ? el.className : el.id} hides ${String(el.scrollWidth - el.clientWidth)}px`,
          );
        expect(cut, `${at}: no label is cut by its ellipsis`).toEqual([]);
      }
    }
  });

  it("keeps their text, uncut, at the iPad preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(IPAD, "coarse", state);
      for (const el of labels()) {
        expect(shown(el), `${state}: #${el.id} shows`).toBe(true);
        expect(el.scrollWidth, `${state}: #${el.id} is not cut`).toBeLessThanOrEqual(
          el.clientWidth,
        );
      }
    }
  });

  it("keeps their text on a wide screen with a mouse", async () => {
    await mount(DESKTOP, "fine", "queue");
    for (const el of labels()) {
      expect(shown(el), `#${el.id} shows`).toBe(true);
      expect(el.scrollWidth, `#${el.id} is not cut`).toBeLessThanOrEqual(el.clientWidth);
    }
  });

  it("drops the text exactly where the fullest labelled row stops fitting", async () => {
    // The fullest row (mid-turn, a plan filed) decides, so the labels do not come and go as a turn starts. A phone-shaped
    // screen hides Send's word and the tier, a wider one shows them, so each regime has its own fit.
    const box = (): HTMLElement => byId("prompt-box");
    for (const [viewport, pointer] of [
      [[768, 1024], "coarse"],
      [[768, 1024], "fine"],
      [IPAD, "coarse"],
      [IPAD, "fine"],
    ] as const) {
      await mount(viewport, pointer, "queue");
      const row = document.querySelector<HTMLElement>(".prompt-pills");
      if (row === null) {
        throw new Error("no pill row");
      }
      const content = (): number => {
        const cs = getComputedStyle(row);
        return (
          row.clientWidth - Number.parseFloat(cs.paddingLeft) - Number.parseFloat(cs.paddingRight)
        );
      };
      // The labelled row's natural width, read where it has room: the controls and the gaps between them.
      box().style.width = "60rem";
      const gap = Number.parseFloat(getComputedStyle(row).columnGap);
      const items = [...row.children]
        .filter((el): el is HTMLElement => el instanceof HTMLElement && shown(el))
        .filter((el) => !el.classList.contains("pill-spacer"));
      for (const el of labels()) {
        expect(shown(el), `${String(viewport[0])}: #${el.id} shows with room`).toBe(true);
      }
      const need =
        items.reduce((sum, el) => sum + el.getBoundingClientRect().width, 0) +
        gap * (items.length - 1);
      const wrong: string[] = [];
      for (let w = 760; w >= 280; w -= 4) {
        box().style.width = `${String(w)}px`;
        const avail = content();
        const labelled = labels().every(shown);
        const iconOnly = labels().every((el) => !shown(el));
        if (!labelled && !iconOnly) {
          wrong.push(`${String(w)}: one pill labelled and one not`);
        } else if (labelled && avail < need - 0.5) {
          wrong.push(`${String(w)}: labelled in ${String(avail)}px of a ${String(need)}px row`);
        } else if (iconOnly && avail >= need + 16) {
          wrong.push(
            `${String(w)}: icons only with ${String(avail)}px for a ${String(need)}px row`,
          );
        }
      }
      expect(wrong, `${String(viewport[0])} ${pointer}: the text follows the fit`).toEqual([]);
    }
  });
});

/** What a family is held to. `toolbar`: the chat toolbar's painted size and target. `tab-row`: the sidebar's tab
 *  row height. `dense`: a row action's token. `menu-row`: the popup menus' one row. The other kinds keep their own
 *  size and name where that is decided. */
type Family =
  | {
      readonly name: string;
      readonly selector: string;
      readonly kind: "toolbar" | "tab-row";
      /** A form of the family that keeps another size at one preset and tier, and where that is decided. */
      readonly except?: {
        readonly selector: string;
        readonly at: `${Preset} ${Pointer}`;
        readonly documented: string;
      };
    }
  | {
      readonly name: string;
      readonly selector: string;
      readonly kind: "dense";
      readonly documented: string;
      /** The token the family paints at, an icon-only member as its square. */
      readonly paint: string;
      /** An icon-only member's width is its glyph and padding, not the token's square. */
      readonly square?: false;
    }
  | {
      readonly name: string;
      readonly selector: string;
      readonly kind: "own-size" | "row" | "inline" | "turn-map" | "menu-row";
      readonly documented: string;
    };

/** The popup menus' rows: the mode and model pickers, Chat options, the slash menu, the context menus, the
 *  branch switcher and the file browser's phone New menu. */
const MENU_ROWS =
  ".pill-model-item, .pill-role-item, .chat-opt-btn, .chat-opt-row, .slash-opt, .tab-context-item, .git-branch-popover-row, .fb-new-menu[open] > .fb-new-actions > .icon-btn";

/** Every button family the app has, each control in exactly one. A new control joins a family or adds one. */
const CENSUS: readonly Family[] = [
  { name: "chat toolbar", selector: ".chat-toolbar .icon-btn", kind: "toolbar" },
  { name: "composer pill", selector: ".prompt-pills .pill", kind: "toolbar" },
  { name: "composer send", selector: ".prompt-pills .send-btn", kind: "toolbar" },
  { name: "run composer send", selector: ".run-composer .send-btn", kind: "toolbar" },
  {
    name: "resume",
    selector: ".scroll-resume",
    kind: "toolbar",
    except: {
      selector: "[id='scroll-bottom']",
      at: "desktop fine",
      documented:
        "13-messages.css, `[id=\"scroll-bottom\"]` docked at the turn map's foot for a mouse: the map's column, held by the turn map case",
    },
  },
  { name: "attachment chip", selector: ".attachment-pill .attachment-open", kind: "toolbar" },
  {
    name: "attachment chip remove",
    selector: ".attachment-pill .attachment-close",
    kind: "own-size",
    documented:
      "15-input.css `.attachment-open::after, .attachment-close::after`: a block-only reach keeps the destructive x off the filename",
  },
  { name: "new chat", selector: "#new-chat", kind: "tab-row" },
  { name: "history", selector: "#history-btn", kind: "tab-row" },
  { name: "sidebar header", selector: ".sidebar-header-actions .icon-btn", kind: "toolbar" },
  { name: "account", selector: "#account-btn", kind: "toolbar" },
  { name: "log out", selector: "#logout-btn", kind: "toolbar" },
  {
    name: "account card link",
    selector: ".pill-account",
    kind: "row",
    documented: "15-input.css `.pill-account`: a row, its box the target",
  },
  {
    name: "tab row",
    selector: "#tab-list .tab",
    kind: "row",
    documented: "10-shell-app.css `.tab`: `--tab-row-h`",
  },
  {
    name: "tab close",
    selector: ".tab-close",
    kind: "dense",
    paint: "--ctl-h-sm",
    documented: "10-shell-app.css `.tab-close`: a row action",
  },
  {
    name: "dialog buttons",
    selector:
      ".icon-btn[data-pr-close], .icon-btn[data-pr-merge-close], .mermaid-overlay-actions .icon-btn, [id$='-modal-close'], [id='filepicker-close'], [id='shortcuts-close']",
    kind: "toolbar",
  },
  { name: "notice dismiss", selector: ".banner-dismiss", kind: "toolbar" },
  {
    name: "view toolbar",
    selector: ".view-toolbar-inner .icon-btn:not(.fb-new-menu[open] > .fb-new-actions > *)",
    kind: "toolbar",
  },
  {
    name: "find bars",
    selector: ".editor-find-btn, .chat-find-btn, .page-find-btn",
    kind: "toolbar",
  },
  {
    name: "text button",
    selector:
      ":is(.btn, .btn-small, .btn-save, .action-pill, .tool-output-reveal, .uip-ask-ok, .uip-ask-cancel):not(.add-form > .btn, .list-row .btn-small, .entry-actions > .btn-small, .conflict-hunk-row > .btn-small, .mcp-install-btn, .git-pr-row-actions .btn-small, .git-repo-section-header-row > .btn-small)",
    kind: "toolbar",
  },
  {
    name: "add-form submit",
    selector: ".add-form > .btn",
    kind: "own-size",
    documented:
      "17-settings.css `.add-form > .btn`: the dense field it submits sets the row's height",
  },
  {
    name: "run card open",
    selector: "a.run-open",
    kind: "dense",
    paint: "--btn-h-sm",
    documented: "27-run-card.css `.run-open`: the turn footer's control box, centred in its band",
  },
  { name: "shell header", selector: ".shell-header-btn", kind: "toolbar" },
  { name: "sign-in submit", selector: ".modal-btn:not(:has(> .modal-btn-sub))", kind: "toolbar" },
  {
    name: "sign-in choice",
    selector: ".modal-btn:has(> .modal-btn-sub)",
    kind: "row",
    documented:
      "16-login.css `.modal-btn`: a two-line choice, its providers under its name, a row of the sign-in list",
  },
  { name: "run bar batch", selector: ".run-bar-all", kind: "toolbar" },
  {
    name: "chip remove",
    selector: ".chip-remove",
    kind: "own-size",
    documented:
      "18-pages.css `.chip-remove`: a 24px x beside its chip's label and the next chip's x, which a wider reach would cross",
  },
  {
    name: "weekday chip",
    selector: ".sched-day",
    kind: "own-size",
    documented: "28-docs.css `.sched-day`: a group of one size with no token, gapped to the floor",
  },
  {
    name: "file git letter",
    selector: ".fb-git-clickable",
    kind: "own-size",
    documented: "19-files.css `.fb-git-clickable`: a shaped target inside its file row",
  },
  {
    name: "more hunks",
    selector: ".tool-diff-more",
    kind: "own-size",
    documented:
      "60-mcp.css `.tool-diff-more`: a text control under a diff preview, painted as its label; the preview's end padding pays its reach",
  },
  {
    name: "switch",
    selector: "label.toggle",
    kind: "own-size",
    documented:
      "18-pages.css `--toggle-w`/`--toggle-h`: a switch track at the platform shape, its target the label",
  },
  {
    name: "code block bar",
    selector: '.cp-code [data-part="actions"] > .cp-button',
    kind: "own-size",
    documented:
      '@cplieger/ui css/system.css `.cp-button[data-size="sm"]`: the library\'s code block bar, `--cp-control-h-sm` on a mouse and 44px on touch',
  },
  {
    name: "segment",
    selector: ".seg",
    kind: "own-size",
    documented: "11-page-lists.css `.seg`: drawn inside its track, whose height is the control's",
  },
  {
    name: "disclosure and list rows",
    selector:
      ".chat-opt-strip-btn, .run-bar-open, .run-bar-toggle, .tool-group-header, .reasoning-summary, .run-head, .run-step-head, .subagent-header, .tool-diff-stats, .native-explain-summary, .turn-file-row, .turn-info-dbtn, .turn-info-link, .turn-review-all, .entry, .entry-open, .fb-row, .ev-dock-row, .ev-tl-lane, .ev-dock-more, .ev-dock-ask-more, .dock-ask-more, .user-input-option, .git-repo-section-header, .git-file-path, .git-pr-row-title, .git-recent-commits-summary, .code-refs-summary, .forge-options-summary, .rw-more-summary, .mcp-result-summary, .always-choice-option, .compaction-head, .mcp-discovery-summary, .forge-account-repos-summary, .git-repo-section-header-toggle",
    kind: "row",
    documented: "each family's `--row-tap*` rule: a row, its own target",
  },
  {
    name: "run dock chevron",
    selector: ".ev-box-toggle",
    kind: "row",
    documented:
      "31-exec-view.css `.ev-box-toggle`: it toggles with the head row around it, which is the target",
  },
  {
    name: "in a sentence",
    selector:
      ".inline-file-link, .steer-note-run, .turn-wake-run, .turn-pill-link, .tool-file-link, .approval-policy-link, :is(p, li, td, dd, blockquote, .msg-body, .message) a[href]:not(.btn-small, .git-pr-row-title)",
    kind: "inline",
    documented:
      "61-mcp-tools.css: links and inline controls in prose carry no expander (WCAG 2.5.8)",
  },
  {
    name: "popup menu rows",
    selector: `${MENU_ROWS}, .turn-actions-more[open] > .turn-actions-group > .turn-action-btn`,
    kind: "menu-row",
    documented: "01-tokens.css `--menu-row-h` and `--menu-row-pad`, held by the popup menu case",
  },
  {
    name: "turn footer actions",
    selector:
      ".turn-action-btn:not(.turn-actions-more[open] > .turn-actions-group > *), .turn-action-more, .turn-rewind, .turn-ledger-summary, .tool-group-hooks",
    kind: "dense",
    paint: "--btn-h-sm",
    documented:
      "61-mcp-tools.css, the turn footer's control box: `--btn-h-sm`, the 24px row action on a mouse and the button box on touch",
  },
  {
    name: "row chevrons and row x",
    selector:
      ".tool-disclosure, .turn-fold-toggle, .spec-toggle, .list-row-btn:not(.entry-actions > *), .git-action-btn, .upload-progress-cancel, .native-rule-remove, .steer-act",
    kind: "dense",
    paint: "--ctl-h-sm",
    documented: "each family's `--ctl-h-sm` rule: an action inside a row",
  },
  {
    name: "run controls",
    selector: ".run-icon-btn",
    kind: "dense",
    paint: "--ctl-h-dense",
    documented: "31-exec-view.css `.run-icon-btn`: an action in the run head's row",
  },
  {
    name: "list row actions",
    selector:
      ".entry-actions > :not(label.toggle), .list-row .btn-small, .conflict-hunk-row > .btn-small, .mcp-install-btn, .mcp-pair-del, .mcp-row-actions .icon-btn, .git-pr-row-actions .btn-small, .git-repo-section-header-row > .btn-small, .selection-quote-btn",
    kind: "dense",
    paint: "--ctl-h-dense",
    square: false,
    documented:
      "each family's `--ctl-h-dense` rule: an action in a list row or a selection's popover, its width its glyph or label and padding",
  },
  {
    name: "turn map steps",
    selector: ".turn-map-step",
    kind: "turn-map",
    documented:
      "29-turns.css `.turn-map-step`: the timeline column's square, held by the turn map case",
  },
];

/** Every pressable control; the tab's × is a span because a `role="tab"` row's children are presentational,
 *  and a switch's label is its target because its checkbox is hidden. */
const CONTROL = "button, summary, [role='button'], a[href], .tab-close, label.toggle";

/** The painted size of a length token, measured rather than parsed. */
function tokenPx(token: string): number {
  const probe = document.createElement("div");
  probe.style.blockSize = `var(${token})`;
  document.body.appendChild(probe);
  const px = probe.getBoundingClientRect().height;
  probe.remove();
  return px;
}

/** A control showing no text is icon-only, so the reference's square. */
function iconOnly(el: HTMLElement): boolean {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    const parent = n.parentElement;
    if ((n.textContent ?? "").trim() !== "" && parent !== null && shown(parent)) {
      return false;
    }
  }
  return true;
}

/** Whether `el` is its family's documented exception at this preset and tier. */
function excepted(el: HTMLElement, family: Family, at: string): boolean {
  const except = "except" in family ? family.except : undefined;
  return except !== undefined && except.at === at && el.matches(except.selector);
}

/** Wholly on screen, so a press at each of its target's edges can land. */
function onScreen(el: HTMLElement): boolean {
  const r = targetRect(el);
  return (
    r.left >= 0 && r.top >= 0 && r.right <= window.innerWidth && r.bottom <= window.innerHeight
  );
}

function familiesOf(el: Element): Family[] {
  return CENSUS.filter((f) => el.matches(f.selector));
}

/** The kinds the per-cell cases measure; the turn map has its own case, and a row or an inline link is listed. */
const MEASURED: ReadonlySet<Family["kind"]> = new Set(["toolbar", "tab-row", "own-size", "dense"]);

/** The token the paint case holds a family to: `--btn-h` for a toolbar family, a dense family's
 *  `paint` field, undefined otherwise (the tab-row case and an own size measure their own way). */
function paintToken(family: Family): string | undefined {
  if (family.kind === "toolbar") {
    return "--btn-h";
  }
  return family.kind === "dense" ? family.paint : undefined;
}

/** The surface a press on `el` is decided in: a modal or a raised layer over the page, or the page. */
function layerOf(el: Element): Element {
  for (let a: Element | null = el; a !== null; a = a.parentElement) {
    const cs = getComputedStyle(a);
    const raised = cs.position === "absolute" && (Number.parseInt(cs.zIndex, 10) || 0) > 0;
    if (a.matches("dialog[open], [popover]") || cs.position === "fixed" || raised) {
      return a;
    }
  }
  return document.body;
}

/** The on-screen, pressable controls of the mounted surface that a measured family covers: inside the open
 *  modal when one is up, and answering a press at their own centre. */
function held(): { el: HTMLElement; family: Family }[] {
  const modal = document.querySelector("dialog:modal");
  return [...document.querySelectorAll<HTMLElement>(CONTROL)]
    .filter((el) => shown(el) && onScreen(el) && el.checkVisibility({ visibilityProperty: true }))
    .filter((el) => el.closest("[inert]") === null && (modal === null || modal.contains(el)))
    .filter((el) => {
      const r = el.getBoundingClientRect();
      return owns(el, r.left + r.width / 2, r.top + r.height / 2);
    })
    .map((el) => ({ el, family: familiesOf(el)[0] }))
    .filter((c): c is { el: HTMLElement; family: Family } => c.family !== undefined)
    .filter(({ family }) => MEASURED.has(family.kind));
}

type Preset = "phone" | "iPad" | "desktop";
const PRESETS: readonly (readonly [Preset, readonly [number, number]])[] = [
  ["phone", PHONE],
  ["iPad", IPAD],
  ["desktop", DESKTOP],
];
const TIERS: readonly Pointer[] = ["coarse", "fine"];
type Surface = "chat idle" | "chat steer" | "chat queue" | "run";
const SURFACES: readonly Surface[] = ["chat idle", "chat steer", "chat queue", "run"];
const CHAT_STATE: Readonly<Record<Exclude<Surface, "run">, State>> = {
  "chat idle": "idle",
  "chat steer": "steer",
  "chat queue": "queue",
};

/** The controls each surface must put in front of the checks, so an empty set cannot pass them. */
const COVERS: Readonly<Record<Surface, readonly string[]>> = {
  "chat idle": [
    "#settings-btn",
    "#context-indicator",
    "#switch-model-btn",
    "#role-pill",
    "#chat-options-btn",
    "#send-btn",
    "#scroll-bottom",
    ".attachment-open",
    ".attachment-close",
  ],
  "chat steer": ["#midturn-send-btn", "#task-list-pill", "#send-btn", ".attachment-close"],
  "chat queue": ["#midturn-send-btn", "#task-list-pill", "#send-btn", "#scroll-bottom"],
  run: ["#settings-btn", "#run-send-btn", "#run-scroll-bottom"],
};

/** The touch contract's surfaces (`app-surfaces.ts`) the census measures, each with the controls it must put in
 *  front of the checks at every preset it mounts at. */
const MOUNTED: Readonly<Record<string, readonly string[]>> = {
  "a notice band": [".banner-dismiss"],
  "the file browser's toolbar": ["#fb-back", "#fb-upload", "#fb-delete"],
  "the editor": ["#editor-edit-btn", ".conflict-hunk-row .btn-small"],
  "the editor's go-to bar": [".editor-goto-submit", "#editor-goto-close"],
  "the web preview's bar": ["#web-reload-btn", "#web-open-btn"],
  "a run card": ["a.run-open"],
  "the shell panel's header": [".shell-header-btn"],
  "the sign-in dialog": ["#modal-sso-submit"],
  "Settings, General": ["#diagnostics-run"],
  "Settings, General, a report ready": [".diagnostics-copy"],
  "Settings, Tools": ["#tool-add-btn", ".list-row-btn", "label.toggle"],
  "Settings, Permissions": [".native-rule-remove", ".chip-remove"],
  "Settings, MCP servers": [".mcp-row-actions .icon-btn"],
  "the MCP dialog": ["#mcp-modal-close"],
  "the MCP dialog's variables": [".mcp-pair-del"],
  "the shortcuts dialog": ["#shortcuts-close"],
  "the merge dialog": ["[data-pr-merge-close]"],
  "the pull request form": ["[data-pr-close]"],
  "a diagram's full view": [".mermaid-overlay-actions .icon-btn"],
  "a turn": [".turn-action-btn", ".tool-disclosure", ".turn-fold-toggle"],
  "a long edit's diff": [".tool-diff-more"],
  "the find bar": [".chat-find-btn"],
  "a page's find popup": [".page-find-btn"],
  "the run bar": [".run-bar-all"],
  "a workflow's schedule": [".sched-day"],
  "the file browser's rows": [".fb-git-clickable"],
  "the Docs page": [".entry-delete"],
  "the Memories tab": [".list-row-btn"],
  "the confirm dialog": [".uip-ask-ok"],
  "the steer dock": [".steer-act"],
  "the Spec tab": [".spec-toggle"],
  "a reply": ['.cp-code [data-part="actions"] > .cp-button'],
  "the run tab": [".run-icon-btn"],
  "the History page": [".entry-delete"],
  "the git page": ["#git-pull-all-btn"],
  "the git page's pull requests": [".git-pr-row-actions .btn-small"],
};

/** The catalogue size a census preset mounts a surface at, or none where the surface has no such form. */
function catalogueSize(surface: CatalogueSurface, preset: Preset): Size | undefined {
  if (preset === "phone") {
    return surface.sizes.includes(390) ? 390 : undefined;
  }
  return surface.sizes.find((s) => s !== 390);
}

async function mountSurface(
  viewport: readonly [number, number],
  pointer: Pointer,
  surface: Surface,
): Promise<void> {
  if (surface === "run") {
    await mountRun(viewport, pointer);
  } else {
    await mount(viewport, pointer, CHAT_STATE[surface]);
  }
  const controls = held().map(({ el }) => el);
  const missing = COVERS[surface].filter((sel) => !controls.some((el) => el.matches(sel)));
  expect(missing, `${surface}: the held controls are on screen`).toEqual([]);
}

/** The sidebar controls the checks must reach at every preset. */
const SIDEBAR_COVERS = [
  "#new-chat",
  "#history-btn",
  "#theme-btn",
  "#account-btn",
  "#logout-btn",
  ".tab-close",
];

/** The sidebar with its drawer open (a phone's closed drawer is off screen) and one chat tab in the
 *  child order `tabs.ts` builds, the row New chat and History are held to. */
async function mountSidebar(
  viewport: readonly [number, number],
  pointer: Pointer,
): Promise<HTMLElement> {
  await mount(viewport, pointer, "idle");
  const sidebar = byId("sidebar");
  sidebar.classList.add("open");
  // The drawer slides in on a transition; the row is measured where it lands.
  for (const slide of sidebar.getAnimations()) {
    slide.finish();
  }
  const tab = document.createElement("div");
  tab.className = "tab";
  tab.dataset["tabId"] = "c1";
  tab.dataset["kind"] = "chat";
  tab.setAttribute("role", "tab");
  const name = document.createElement("span");
  name.className = "tab-name";
  name.textContent = "Rebuild the timeline rail";
  const close = document.createElement("span");
  close.className = "tab-close";
  close.setAttribute("aria-hidden", "true");
  close.appendChild(iconEl(ICON_CLOSE));
  tab.append(name, close);
  byId("tab-list").replaceChildren(tab);
  // `pointer-mode.ts` shows the Touch mode toggle on a touch-capable device; `settings.ts` writes the address.
  byId("pointer-mode-btn").classList.remove("hidden");
  byId("user-email").textContent = "reader@example.com";
  expect(shown(tab) && onScreen(tab), "the tab row is on screen").toBe(true);
  const reached = held().map(({ el }) => el);
  const missing = SIDEBAR_COVERS.filter((sel) => !reached.some((el) => el.matches(sel)));
  expect(missing, "the sidebar's held controls are on screen").toEqual([]);
  return tab;
}

/** What one preset and tier found over every measured surface, each case reading its own list. */
interface Findings {
  readonly paint: Set<string>;
  readonly floor: Set<string>;
  readonly overlap: Set<string>;
  /** The families a control was measured for. */
  readonly measured: Set<string>;
}

function check(
  controls: readonly { el: HTMLElement; family: Family }[],
  where: string,
  at: string,
  out: Findings,
): void {
  const floor = floorPx();
  expect(floor, `${where}: the tier's floor`).toBe(at.endsWith("coarse") ? 44 : 24);
  for (const { el, family } of controls) {
    out.measured.add(family.name);
    const token = paintToken(family);
    if (token !== undefined && !excepted(el, family, at)) {
      const px = tokenPx(token);
      const r = painted(el);
      if (r.height !== px) {
        out.paint.add(
          `${where}: ${nameOf(el)} is ${String(r.height)}px tall, ${token} is ${String(px)}px`,
        );
      }
      const square = family.kind !== "dense" || family.square !== false;
      if (square && iconOnly(el) && r.width !== px) {
        out.paint.add(`${where}: ${nameOf(el)} is icon-only and ${String(r.width)}px wide`);
      }
    }
    const hit = hitRect(el);
    // The probe reaches the declared target's edges: a press 1px inside each one answers to the control.
    const t = targetRect(el);
    const cx = (t.left + t.right) / 2;
    const cy = (t.top + t.bottom) / 2;
    const edges = [
      [t.left + 1, cy],
      [t.right - 1, cy],
      [cx, t.top + 1],
      [cx, t.bottom - 1],
    ] as const;
    const missed = edges.find(([x, y]) => !owns(el, x, y));
    if (missed !== undefined) {
      out.floor.add(`${where}: ${nameOf(el)}'s target edge answers to ${answering(...missed)}`);
    }
    if (height(hit) < floor - 2 * STEP) {
      out.floor.add(`${where}: ${nameOf(el)} reaches ${String(height(hit))}px tall`);
    }
    // An own size may keep a narrower reach (the chip's x, a segment), named by its family.
    if (family.kind !== "own-size" && width(hit) < floor - 2 * STEP) {
      out.floor.add(`${where}: ${nameOf(el)} reaches ${String(width(hit))}px wide`);
    }
  }
  for (const [i, { el: a }] of controls.entries()) {
    const ta = targetRect(a);
    for (const { el: b } of controls.slice(i + 1)) {
      if (a.contains(b) || b.contains(a) || layerOf(a) !== layerOf(b)) {
        continue;
      }
      const tb = targetRect(b);
      const dx = Math.min(ta.right, tb.right) - Math.max(ta.left, tb.left);
      const dy = Math.min(ta.bottom, tb.bottom) - Math.max(ta.top, tb.top);
      if (dx > 0.5 && dy > 0.5) {
        out.overlap.add(
          `${where}: ${nameOf(a)} and ${nameOf(b)} share ${String(dx)}x${String(dy)}px`,
        );
      }
    }
  }
}

/** One measuring of every surface per preset and tier answers all three per-cell cases; the first to run pays. */
const CELL_MS = 300_000;

const findings = new Map<string, Promise<Findings>>();

function cellFindings(
  preset: Preset,
  viewport: readonly [number, number],
  pointer: Pointer,
): Promise<Findings> {
  const at = `${preset} ${pointer}`;
  let f = findings.get(at);
  if (f === undefined) {
    f = measureCell(preset, viewport, pointer);
    findings.set(at, f);
  }
  return f;
}

async function measureCell(
  preset: Preset,
  viewport: readonly [number, number],
  pointer: Pointer,
): Promise<Findings> {
  const at = `${preset} ${pointer}`;
  const out: Findings = {
    paint: new Set(),
    floor: new Set(),
    overlap: new Set(),
    measured: new Set(),
  };
  for (const surface of SURFACES) {
    await mountSurface(viewport, pointer, surface);
    const btn = tokenPx("--btn-h");
    expect(btn, `${surface}: --btn-h is the reference box`).toBe(BUTTON_REFERENCE[pointer].box);
    const ref = byId("settings-btn").getBoundingClientRect();
    expect([ref.width, ref.height], `${surface}: the toolbar paints --btn-h`).toEqual([btn, btn]);
    check(held(), surface, at, out);
  }
  await mountSidebar(viewport, pointer);
  // The open drawer is a layer over the chat, not its neighbour.
  const sidebar = byId("sidebar");
  check(
    held().filter(({ el }) => sidebar.contains(el)),
    "sidebar",
    at,
    out,
  );
  for (const [name, covers] of Object.entries(MOUNTED)) {
    const surface = CATALOGUE.find((s) => s.name === name);
    expect(surface, `${name} is a surface of app-surfaces.ts`).toBeDefined();
    const size = surface === undefined ? undefined : catalogueSize(surface, preset);
    if (surface === undefined || size === undefined) {
      continue;
    }
    await page.viewport(viewport[0], viewport[1]);
    await mountCatalogue(surface, size, pointer);
    // A cover below the fold is scrolled to, and what is then on screen is measured again.
    for (const sel of covers) {
      const cover = document.querySelector<HTMLElement>(sel);
      if (cover !== null && !onScreen(cover)) {
        cover.scrollIntoView({ block: "center", inline: "nearest" });
        await nextFrame();
        await nextFrame();
      }
      const controls = held();
      expect(
        controls.some(({ el }) => el.matches(sel)),
        `${name}: ${sel} is held and on screen`,
      ).toBe(true);
      check(controls, name, at, out);
    }
    clearSurface();
  }
  return out;
}

describe("the button census", () => {
  it("puts every control on the shipped page in exactly one family", async () => {
    await mount(PHONE, "coarse", "queue");
    const wrong = [...document.querySelectorAll<HTMLElement>(CONTROL)]
      .map((el) => ({ el, names: familiesOf(el).map((f) => f.name) }))
      .filter(({ names }) => names.length !== 1)
      .map(
        ({ el, names }) =>
          `${el.tagName.toLowerCase()}#${el.id}.${el.className}: ${names.length === 0 ? "no family" : names.join(", ")}`,
      );
    expect(wrong, "every control is classified once").toEqual([]);
  });

  it("puts every control the touch contract's surfaces build in exactly one family", async () => {
    // A control a script adds (a turn's footer, a run card, a row's actions) is on one of these surfaces.
    const wrong = new Set<string>();
    for (const surface of CATALOGUE) {
      const size = surface.sizes[0];
      if (size === undefined) {
        continue;
      }
      await page.viewport(...CATALOGUE_VIEWPORTS[size]);
      await mountCatalogue(surface, size, "coarse");
      for (const el of document.querySelectorAll<HTMLElement>(CONTROL)) {
        const names = familiesOf(el).map((f) => f.name);
        if (names.length !== 1) {
          const cls = el.getAttribute("class") ?? "";
          wrong.add(
            `${surface.name}: ${el.tagName.toLowerCase()}#${el.id}.${cls}: ${names.length === 0 ? "no family" : names.join(", ")}`,
          );
        }
      }
      clearSurface();
    }
    expect([...wrong], `every control is classified once:\n${[...wrong].join("\n")}\n`).toEqual([]);
  }, 120_000);

  for (const [preset, viewport] of PRESETS) {
    for (const pointer of TIERS) {
      const at = `${preset} ${pointer}`;

      it(
        `paints every held control at its family's token (--btn-h for the toolbar), ${at}`,
        async () => {
          const found = [...(await cellFindings(preset, viewport, pointer)).paint];
          expect(found, `each paints its family's token:\n${found.join("\n")}\n`).toEqual([]);
        },
        CELL_MS,
      );

      it(
        `gives every held control a target at the floor, ${at}`,
        async () => {
          const found = [...(await cellFindings(preset, viewport, pointer)).floor];
          expect(found, `every target clears the tier's floor:\n${found.join("\n")}\n`).toEqual([]);
        },
        CELL_MS,
      );

      it(
        `lets no two held targets overlap, ${at}`,
        async () => {
          const found = [...(await cellFindings(preset, viewport, pointer)).overlap];
          expect(found, `no two targets overlap:\n${found.join("\n")}\n`).toEqual([]);
        },
        CELL_MS,
      );

      it(`paints every held tab-row control at the sidebar tab's height, ${at}`, async () => {
        const row = (await mountSidebar(viewport, pointer)).getBoundingClientRect().height;
        const controls = held().filter(({ family }) => family.kind === "tab-row");
        const absent = CENSUS.filter((f) => f.kind === "tab-row")
          .filter((f) => !controls.some(({ family }) => family === f))
          .map((f) => f.name);
        expect(absent, "every tab-row family is on screen").toEqual([]);
        const off: string[] = [];
        for (const { el } of controls) {
          const r = el.getBoundingClientRect();
          if (r.height !== row) {
            off.push(`${nameOf(el)} is ${String(r.height)}px tall`);
          }
          if (iconOnly(el) && r.width !== row) {
            off.push(`${nameOf(el)} is icon-only and ${String(r.width)}px wide`);
          }
        }
        expect(off, `the tab row's ${String(row)}px`).toEqual([]);
      });
    }
  }

  it(
    "measures every measured family on some surface at some preset and tier",
    async () => {
      const seen = new Set<string>();
      for (const [preset, viewport] of PRESETS) {
        for (const pointer of TIERS) {
          for (const name of (await cellFindings(preset, viewport, pointer)).measured) {
            seen.add(name);
          }
        }
      }
      const unmeasured = CENSUS.filter((f) => MEASURED.has(f.kind) && !seen.has(f.name)).map(
        (f) => f.name,
      );
      expect(
        unmeasured,
        `a family the cases never reach is unheld: ${unmeasured.join(", ")}`,
      ).toEqual([]);
    },
    PRESETS.length * TIERS.length * CELL_MS,
  );

  for (const pointer of TIERS) {
    it(`shows the model and mode pills as named icons on one row at the phone preset, ${pointer}`, async () => {
      for (const state of STATES) {
        await mount(PHONE, pointer, state);
        const names: [string, string, string][] = [
          ["switch-model-btn", "ctx-model-pill", MODEL],
          ["role-pill", "role-pill-label", MODE],
        ];
        for (const [id, labelId, current] of names) {
          const pill = byId(id);
          expect(shown(byId(labelId)), `${state}: #${id} draws no text`).toBe(false);
          expect(iconOnly(pill), `${state}: #${id} is icon-only`).toBe(true);
          expect(pill.getAttribute("aria-label"), `${state}: #${id}'s name`).toContain(current);
          expect(pill.getAttribute("data-tooltip"), `${state}: #${id}'s tooltip`).toBe(
            pill.getAttribute("aria-label"),
          );
        }
        const row = document.querySelector<HTMLElement>(".prompt-pills");
        if (row === null) {
          throw new Error("no pill row");
        }
        expect(row.scrollWidth, `${state}: nothing overflows the row`).toBeLessThanOrEqual(
          row.clientWidth,
        );
        const tops = new Set(
          [...row.querySelectorAll<HTMLElement>(".pill, .send-btn")]
            .filter(shown)
            .map((el) => el.getBoundingClientRect().top),
        );
        expect(tops.size, `${state}: one line`).toBe(1);
      }
    });
  }
});

describe("the chat toolbar, the reference every button follows", () => {
  for (const [preset, viewport] of PRESETS) {
    for (const pointer of TIERS) {
      const want = BUTTON_REFERENCE[pointer];
      it(`paints ${String(want.box)}px boxes with ${String(want.glyph)}px glyphs, ${String(want.gap)}px apart, ${preset} ${pointer}`, async () => {
        await mount(viewport, pointer, "idle");
        const buttons = toolbar();
        expect(buttons.length, "the toolbar renders its buttons").toBeGreaterThanOrEqual(6);
        const off: string[] = [];
        for (const [i, b] of buttons.entries()) {
          const r = b.getBoundingClientRect();
          if (r.width !== want.box || r.height !== want.box) {
            off.push(`${b.id} paints ${String(r.width)}x${String(r.height)}`);
          }
          const g = b.querySelector("svg")?.getBoundingClientRect();
          if (g?.width !== want.glyph || g.height !== want.glyph) {
            off.push(`${b.id}'s glyph is ${String(g?.width)}x${String(g?.height)}`);
          }
          const hit = hitRect(b);
          const reach = Math.max(want.box, floorPx());
          if (Math.abs(width(hit) - reach) > SLACK || Math.abs(height(hit) - reach) > SLACK) {
            off.push(`${b.id} reaches ${String(width(hit))}x${String(height(hit))}`);
          }
          const next = buttons[i + 1];
          if (next !== undefined) {
            const gap = next.getBoundingClientRect().left - r.right;
            if (Math.abs(gap - want.gap) > 0.01) {
              off.push(`${b.id} to ${next.id}: ${String(gap)}px apart`);
            }
          }
        }
        expect(off, `the reference at ${preset} ${pointer}`).toEqual([]);
      });
    }
  }
});

describe("the turn footer's glyphs, against the reference", () => {
  for (const [tier, size] of [
    ["coarse", 390],
    ["fine", 1180],
  ] as const) {
    it(`draws every footer action's glyph at the reference's ${String(BUTTON_REFERENCE[tier].glyph)}px at ${String(size)}px ${tier}`, async () => {
      const surface = CATALOGUE.find((s) => s.name === "a turn");
      if (surface === undefined) {
        throw new Error("no turn surface");
      }
      await page.viewport(...CATALOGUE_VIEWPORTS[size]);
      await mountCatalogue(surface, size, tier);
      const want = BUTTON_REFERENCE[tier].glyph;
      const controls = [
        ...document.querySelectorAll<HTMLElement>(
          ".turn-footer :is(.turn-action-btn, .turn-rewind):not(.turn-actions-more[open] > .turn-actions-group > *)",
        ),
      ].filter((el) => shown(el) && el.checkVisibility({ visibilityProperty: true }));
      const off: string[] = [];
      for (const el of controls) {
        const g = [...el.querySelectorAll("svg")].find((x) => x.getBoundingClientRect().width > 0);
        const r = g?.getBoundingClientRect();
        if (r?.width !== want || r.height !== want) {
          off.push(
            `${el.getAttribute("aria-label") ?? el.className}'s glyph is ${r === undefined ? "no svg" : `${String(r.width)}x${String(r.height)}`}`,
          );
        }
      }
      clearSurface();
      // Whether the `…` shows is the footer's measured fit (`turn-footer-overflow.test.ts`, which pins its glyph).
      expect(controls.length, "the footer draws its actions").toBeGreaterThan(0);
      expect(off).toEqual([]);
    });
  }
});

describe("the popup menus' rows", () => {
  /** The menus the touch contract's surfaces open, the slash menu planted on the chat page. */
  const MENUS = [
    "the model popup",
    "the role popup",
    "the chat options popup",
    "the tab menu",
    "a turn's More menu",
    "the branch switcher",
    "the file browser's New menu",
  ];

  /** Distinct text lines a row draws, so a two-line row may grow past the one-line height. */
  function lines(el: HTMLElement): number {
    const range = document.createRange();
    range.selectNodeContents(el);
    return new Set(
      [...range.getClientRects()].filter((r) => r.height > 0).map((r) => Math.round(r.bottom)),
    ).size;
  }

  function measureRows(where: string, tier: Pointer, off: string[]): number {
    const want = MENU_ROW[tier];
    const rows = [
      ...document.querySelectorAll<HTMLElement>(
        `${MENU_ROWS}, .turn-actions-more[open] > .turn-actions-group > .turn-action-btn`,
      ),
    ].filter((el) => shown(el) && el.checkVisibility({ visibilityProperty: true }));
    for (const row of rows) {
      const name = `${where}: ${row.className.split(" ")[0] ?? ""} "${(row.textContent ?? "").trim().slice(0, 20)}"`;
      const cs = getComputedStyle(row);
      const h = row.getBoundingClientRect().height;
      if (h < want.h - 0.01 || (lines(row) <= 1 && Math.abs(h - want.h) > 0.01)) {
        off.push(`${name} is ${String(h)}px tall`);
      }
      const inset = [
        Number.parseFloat(cs.paddingTop) + Number.parseFloat(cs.borderTopWidth),
        Number.parseFloat(cs.paddingLeft) + Number.parseFloat(cs.borderLeftWidth),
      ];
      if (inset[0] !== want.pad[0] || inset[1] !== want.pad[1]) {
        off.push(`${name} insets its content ${inset.join("/")}px`);
      }
      for (const g of row.querySelectorAll("svg")) {
        const r = g.getBoundingClientRect();
        if (r.width > 0 && (r.width !== want.glyph || r.height !== want.glyph)) {
          off.push(`${name}'s glyph is ${String(r.width)}x${String(r.height)}`);
        }
      }
      const r = row.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      if (!owns(row, cx, r.top + 1) || !owns(row, cx, r.bottom - 1)) {
        off.push(`${name}: a press 1px inside its edge misses it`);
      }
    }
    return rows.length;
  }

  for (const [tier, size] of [
    ["coarse", 390],
    ["fine", 1180],
  ] as const) {
    it(`gives every menu row one height, padding and glyph at ${String(size)}px ${tier}`, async () => {
      const off: string[] = [];
      for (const name of MENUS) {
        const surface = CATALOGUE.find((s) => s.name === name);
        expect(surface, `${name} is a surface of app-surfaces.ts`).toBeDefined();
        const at = surface?.sizes.includes(size) === true ? size : surface?.sizes[0];
        if (surface === undefined || at === undefined) {
          continue;
        }
        await page.viewport(...CATALOGUE_VIEWPORTS[at]);
        await mountCatalogue(surface, at, tier);
        expect(measureRows(name, tier, off), `${name} draws its rows`).toBeGreaterThan(0);
        clearSurface();
      }
      await mount(size === 390 ? PHONE : DESKTOP, tier, "idle");
      const menu = byId("slash-menu");
      for (const [cmd, desc] of [
        ["/compact", "Summarise the chat"],
        ["/review", "Review the changes"],
      ] as const) {
        const li = document.createElement("li");
        li.className = "slash-opt";
        li.setAttribute("role", "option");
        const n = document.createElement("span");
        n.className = "slash-opt-name";
        n.textContent = cmd;
        const d = document.createElement("span");
        d.className = "slash-opt-desc";
        d.textContent = desc;
        li.append(n, d);
        menu.appendChild(li);
      }
      menu.hidden = false;
      expect(measureRows("the slash menu", tier, off), "the slash menu draws its rows").toBe(2);
      expect(off, `one menu row at ${tier}:\n${off.join("\n")}\n`).toEqual([]);
    }, 60_000);
  }
});

describe("the turn map's buttons on desktop", () => {
  it("paint the timeline's column on its centre line, each glyph centred and each target at the floor", async () => {
    await mount(DESKTOP, "fine", "idle");
    const { mountTurnRail } = await import("./turn-rail.js");
    mountTurnRail(byId("messages-wrap-outer"));
    // The renderer's writes for a navigable chat, in `turn-rail.ts`'s shape: the map shown and one row.
    const nav = document.querySelector<HTMLElement>(".turn-map");
    if (nav === null) {
      throw new Error("mountTurnRail drew no map");
    }
    nav.toggleAttribute("data-shown", true);
    const row = document.createElement("li");
    row.className = "turn-pill";
    const rowLink = document.createElement("a");
    rowLink.className = "turn-pill-link";
    rowLink.href = "#turn-1";
    const hit = document.createElement("span");
    hit.className = "turn-pill-hit";
    rowLink.appendChild(hit);
    row.appendChild(rowLink);
    nav.querySelector(".turn-map-stack")?.appendChild(row);
    const off: string[] = [];
    for (const pointer of TIERS) {
      document.documentElement.dataset["pointer"] = pointer;
      await nextFrame();
      await nextFrame();
      // A tier flip resizes the track, and the renderer, holding no turns, hides the map again.
      nav.toggleAttribute("data-shown", true);
      // The timeline the reader sees is the rows' bar, drawn in a column of the map's box.
      const link = document.querySelector<HTMLElement>(".turn-map .turn-pill-link");
      const track = document.querySelector<HTMLElement>(".turn-map-track");
      if (link === null || track === null) {
        throw new Error("the map has no rows");
      }
      const bar = getComputedStyle(link, "::before");
      const colLeft = link.getBoundingClientRect().left + Number.parseFloat(bar.left);
      const colWidth = Number.parseFloat(bar.width);
      const centre = colLeft + colWidth / 2;
      const buttons = [...document.querySelectorAll<HTMLElement>(".turn-map-step")];
      expect(buttons.length, "the up and down steps").toBe(2);
      // The resume control docks at the map's foot for a mouse only.
      if (pointer === "fine") {
        buttons.push(byId("scroll-bottom"));
      }
      const floor = floorPx();
      const rows = track.getBoundingClientRect();
      for (const b of buttons) {
        const at = `${pointer} ${b.getAttribute("aria-label") ?? b.id}`;
        const r = b.getBoundingClientRect();
        if (Math.abs(r.width - colWidth) > 0.5) {
          off.push(`${at} paints ${String(r.width)}px wide over a ${String(colWidth)}px column`);
        }
        if (Math.abs(r.left + r.width / 2 - centre) > 0.5) {
          off.push(
            `${at} centres at ${String(r.left + r.width / 2)}, the column at ${String(centre)}`,
          );
        }
        const glyph = b.querySelector("svg")?.getBoundingClientRect();
        if (glyph === undefined || Math.abs(glyph.left + glyph.width / 2 - centre) > 0.5) {
          off.push(`${at}'s glyph is off the column's centre line`);
        }
        const t = targetRect(b);
        const cx = (t.left + t.right) / 2;
        const cy = (t.top + t.bottom) / 2;
        const edges = [
          [t.left + 1, cy],
          [t.right - 1, cy],
          [cx, t.top + 1],
          [cx, t.bottom - 1],
        ] as const;
        const missed = edges.find(([x, y]) => !owns(b, x, y));
        if (missed !== undefined) {
          off.push(`${at}'s target edge answers to ${answering(...missed)}`);
        }
        if (width(t) < floor - 0.5 || height(t) < floor - 0.5) {
          off.push(`${at} reaches ${String(width(t))}x${String(height(t))}px`);
        }
        const into = Math.min(t.bottom, rows.bottom) - Math.max(t.top, rows.top);
        if (b.classList.contains("turn-map-step") && into > 0.5) {
          off.push(`${at}'s target reaches ${String(into)}px into the rows`);
        }
      }
    }
    expect(off, `on the timeline's column, at the floor:\n${off.join("\n")}\n`).toEqual([]);
  }, 60_000);
});
