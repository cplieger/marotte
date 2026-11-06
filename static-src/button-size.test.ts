// Every button is the chat toolbar's on every tier: the same painted box (`--btn-h`) and a target that
// reaches `--hit-floor` through the shared expander, with no two targets overlapping. CENSUS names every
// button family and its reference; a family that keeps its own size says where that is documented. On a
// row too narrow for the fullest labelled line, the model and mode pills drop their text.
// The fixture is the production `#app`, so the toolbar reference and the controls share one layout.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { buildAttachmentPill } from "./attachment-pill.js";
import { iconEl } from "./icon-el.js";
import { ICON_CANCEL, ICON_HOURGLASS, ICON_MODEL, ICON_SEND } from "./icons.js";
import { setActive, setSessions } from "./store.js";
import type { updateContextBar as updateContextBarFn } from "./status.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

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

/** The longest names the rig's chats carry, so a fit decided on them holds for the common case. */
const MODEL_ID = "claude-opus-5.5";
const MODEL = "claude opus 5.5";
const EFFORT = "High";
const MODE_ID = "default-v2";
const MODE = "Default V2";

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
  document.body.replaceChildren();
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
  it("paints every one at the toolbar button's size at the phone preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(PHONE, "coarse", state);
      const ref = reference();
      expect(ref.painted, "the toolbar paints 36px on touch").toEqual({ w: 36, h: 36 });
      const off: string[] = [];
      for (const el of controls()) {
        const r = painted(el);
        if (r.height !== ref.painted.h) {
          off.push(`${nameOf(el)} is ${String(r.height)}px tall`);
        }
        const square = [...SQUARE, "send-btn", "switch-model-btn", "role-pill"];
        if (square.includes(el.id) && r.width !== ref.painted.w) {
          off.push(`${nameOf(el)} is ${String(r.width)}px wide`);
        }
      }
      expect(off, `${state}: every control paints the toolbar's box`).toEqual([]);
    }
  });

  it("paints every one at the toolbar button's height at the iPad preset and with a mouse", async () => {
    // One height per tier is the consistency rule; a mouse keeps the toolbar's 36px, so the composer meets it there.
    for (const [viewport, pointer] of [
      [IPAD, "coarse"],
      [IPAD, "fine"],
      [PHONE, "fine"],
      [DESKTOP, "fine"],
    ] as const) {
      for (const state of STATES) {
        await mount(viewport, pointer, state);
        const ref = reference();
        const at = `${String(viewport[0])} ${pointer} ${state}`;
        const off = controls()
          .filter((el) => el.id !== "scroll-bottom" || viewport !== DESKTOP)
          .filter((el) => painted(el).height !== ref.painted.h)
          .map((el) => `${nameOf(el)} ${String(painted(el).height)}px`);
        expect(off, `${at}: every control is the toolbar's height`).toEqual([]);
      }
    }
  });

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
        if (Math.abs(height(hit) - ref.hit.h) > SLACK) {
          off.push(`${nameOf(el)} reaches ${String(height(hit))}px tall`);
        }
        if (width(hit) < floor - 2 * STEP) {
          off.push(`${nameOf(el)} reaches ${String(width(hit))}px wide`);
        }
        if (SQUARE.includes(el.id) && Math.abs(width(hit) - ref.hit.w) > SLACK) {
          off.push(`${nameOf(el)} reaches ${String(width(hit))}px wide, not the toolbar's`);
        }
      }
      expect(off, `${state}: every target is the toolbar's`).toEqual([]);
    }
  });

  it("holds every hit area at the floor on both axes at the iPad preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(IPAD, "coarse", state);
      const floor = floorPx();
      const off = controls()
        .map((el) => ({ el, hit: hitRect(el) }))
        .filter(({ hit }) => width(hit) < floor - 2 * STEP || height(hit) < floor - 2 * STEP)
        .map(({ el, hit }) => `${nameOf(el)} ${String(width(hit))}x${String(height(hit))}`);
      expect(off, `${state}: every target clears the floor`).toEqual([]);
    }
  });

  it("gives the chip's remove button the floor's height, and only its height", async () => {
    // Its inline reach would put a destructive target over the filename (15-input.css, the chip's block-only expander).
    await mount(PHONE, "coarse", "idle");
    const close = document.querySelector<HTMLElement>("#attachment-row .attachment-close");
    if (close === null) {
      throw new Error("the chip did not mount");
    }
    expect(height(hitRect(close))).toBeGreaterThanOrEqual(floorPx() - 2 * STEP);
  });

  it("gives Send and Cancel a square hit area at the floor at the phone preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(PHONE, "coarse", state);
      const send = byId("send-btn");
      const painted = send.getBoundingClientRect();
      const hit = hitRect(send);
      expect(painted.width, `${state}: a square button`).toBe(painted.height);
      expect(Math.abs(width(hit) - height(hit)), `${state}: a square target`).toBeLessThanOrEqual(
        SLACK,
      );
      expect(width(hit), `${state}: at the floor`).toBeGreaterThanOrEqual(floorPx() - 2 * STEP);
    }
  });

  it("lets no two neighbouring hit areas overlap, at either touch preset", async () => {
    for (const viewport of [PHONE, IPAD]) {
      for (const state of STATES) {
        await mount(viewport, "coarse", state);
        const at = `${String(viewport[0])} ${state}`;
        const row = [
          ...document.querySelectorAll<HTMLElement>(".prompt-pills .pill, .prompt-pills .send-btn"),
        ].filter(shown);
        const chip = [
          ...document.querySelectorAll<HTMLElement>(
            "#attachment-row .attachment-open, #attachment-row .attachment-close",
          ),
        ];
        const all = [...row, ...chip, byId("scroll-bottom")];
        const crossed: string[] = [];
        for (const [i, a] of all.entries()) {
          const ta = targetRect(a);
          for (const b of all.slice(i + 1)) {
            const tb = targetRect(b);
            const dx = Math.min(ta.right, tb.right) - Math.max(ta.left, tb.left);
            const dy = Math.min(ta.bottom, tb.bottom) - Math.max(ta.top, tb.top);
            if (dx > 0.5 && dy > 0.5) {
              crossed.push(`${nameOf(a)} and ${nameOf(b)} share ${String(dx)}x${String(dy)}px`);
            }
          }
        }
        expect(crossed, `${at}: no two targets overlap`).toEqual([]);
        // The facing edges each answer to their own control, so neither target is covered by the other.
        const covered: string[] = [];
        for (const [i, a] of row.slice(0, -1).entries()) {
          const b = row[i + 1]!;
          const ta = targetRect(a);
          const tb = targetRect(b);
          const ya = (ta.top + ta.bottom) / 2;
          if (!owns(a, ta.right - 1, ya)) {
            covered.push(`${nameOf(a)}'s end edge`);
          }
          if (!owns(b, tb.left + 1, (tb.top + tb.bottom) / 2)) {
            covered.push(`${nameOf(b)}'s start edge`);
          }
        }
        expect(covered, `${at}: each facing edge reaches its own control`).toEqual([]);
      }
    }
  });
});

describe("the model and mode pills, by the room the row has", () => {
  const labels = (): HTMLElement[] => [byId("ctx-model-pill"), byId("role-pill-label")];

  it("shows them as icons only at the phone preset in touch mode", async () => {
    for (const state of STATES) {
      await mount(PHONE, "coarse", state);
      for (const el of labels()) {
        expect(shown(el), `${state}: #${el.id} draws no text`).toBe(false);
      }
      for (const id of ["switch-model-btn", "role-pill"]) {
        expect(
          byId(id).querySelector("svg") !== null && shown(byId(id).querySelector("svg")!),
          `${state}: #${id} keeps its icon`,
        ).toBe(true);
      }
    }
  });

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
 *  row height. The other kinds keep their own size and name where that is decided. */
type Family =
  | {
      readonly name: string;
      readonly selector: string;
      readonly kind: "toolbar" | "tab-row";
      /** The surface not yet held to it, by the order the surfaces are being moved. */
      readonly pending?: "sidebar" | "rest";
    }
  | {
      readonly name: string;
      readonly selector: string;
      readonly kind: "own-size" | "row" | "inline" | "dense" | "turn-map";
      readonly documented: string;
      readonly pending?: "sidebar" | "rest";
    };

/** Every button family the app has, each control in exactly one. A new control joins a family or adds one. */
const CENSUS: readonly Family[] = [
  { name: "chat toolbar", selector: ".chat-toolbar .icon-btn", kind: "toolbar" },
  { name: "composer pill", selector: ".prompt-pills .pill", kind: "toolbar" },
  { name: "composer send", selector: ".prompt-pills .send-btn", kind: "toolbar" },
  { name: "run composer send", selector: ".run-composer .send-btn", kind: "toolbar" },
  { name: "resume", selector: ".scroll-resume", kind: "toolbar" },
  { name: "attachment chip", selector: ".attachment-pill .attachment-open", kind: "toolbar" },
  {
    name: "attachment chip remove",
    selector: ".attachment-pill .attachment-close",
    kind: "own-size",
    documented:
      "15-input.css `.attachment-open::after, .attachment-close::after`: a block-only reach keeps the destructive x off the filename",
  },
  { name: "new chat", selector: "#new-chat", kind: "tab-row", pending: "sidebar" },
  { name: "history", selector: "#history-btn", kind: "tab-row", pending: "sidebar" },
  {
    name: "sidebar header",
    selector: ".sidebar-header-actions .icon-btn",
    kind: "toolbar",
    pending: "sidebar",
  },
  { name: "account", selector: "#account-btn", kind: "toolbar", pending: "sidebar" },
  { name: "log out", selector: "#logout-btn", kind: "toolbar", pending: "sidebar" },
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
    documented: "10-shell-app.css `.tab`: `--row-tap`",
  },
  {
    name: "tab close",
    selector: ".tab-close",
    kind: "dense",
    documented: "10-shell-app.css `.tab-close`: a row action",
    pending: "sidebar",
  },
  {
    name: "dialog close",
    selector:
      "dialog .icon-btn, [id$='-modal-close'], [id='filepicker-close'], [id='shortcuts-close']",
    kind: "toolbar",
    pending: "rest",
  },
  {
    name: "view toolbar",
    selector: ".view-toolbar-inner .icon-btn",
    kind: "toolbar",
    pending: "rest",
  },
  { name: "editor find bar", selector: ".editor-find-btn", kind: "toolbar", pending: "rest" },
  {
    name: "text button",
    selector:
      ":is(.btn, .btn-small, .btn-save, .action-pill):not(.add-form > .btn, .list-row .btn-small, [id='knowledge-add-btn'], [id='knowledge-clear-btn'])",
    kind: "toolbar",
    pending: "rest",
  },
  {
    name: "add-form submit",
    selector: ".add-form > .btn",
    kind: "own-size",
    documented:
      "17-settings.css `.add-form > .btn`: the dense field it submits sets the row's height",
  },
  {
    name: "glyphless action pill",
    selector: "[id='knowledge-add-btn'], [id='knowledge-clear-btn']",
    kind: "own-size",
    documented:
      "14-tools.css `.action-pill`: an empty button's text strut, unconfirmed on a real render",
    pending: "rest",
  },
  { name: "run card open", selector: "a.run-open", kind: "toolbar", pending: "rest" },
  { name: "shell header", selector: ".shell-header-btn", kind: "toolbar", pending: "rest" },
  { name: "sign-in", selector: ".modal-btn", kind: "toolbar", pending: "rest" },
  {
    name: "segment",
    selector: ".seg",
    kind: "own-size",
    documented: "11-page-lists.css `.seg`: drawn inside its track, whose height is the control's",
  },
  {
    name: "disclosure and list rows",
    selector:
      ".chat-opt-btn, .chat-opt-strip-btn, .run-bar-open, .run-bar-toggle, .tool-group-header, .reasoning-summary, .run-head, .tool-diff-stats, .native-explain-summary, .turn-file-row, .turn-info-dbtn, .entry, .fb-row, .ev-dock-row",
    kind: "row",
    documented: "each family's `--row-tap*` rule: a row, its own target",
  },
  {
    name: "in a sentence",
    selector: ".inline-file-link, .steer-note-run, .turn-wake-run, .turn-pill-link",
    kind: "inline",
    documented:
      "61-mcp-tools.css: links and inline controls in prose carry no expander (WCAG 2.5.8)",
  },
  {
    name: "turn footer and code actions",
    selector:
      ".turn-action-btn, .turn-action-more, .turn-rewind, .turn-ledger-summary, .code-act-btn, .tool-group-hooks",
    kind: "dense",
    documented:
      "61-mcp-tools.css, the turn footer's control box: `--ctl-h-sm` with the shared expander",
    pending: "rest",
  },
  {
    name: "row chevrons and row actions",
    selector:
      ".tool-disclosure, .turn-fold-toggle, .entry-delete, .list-row-btn, .list-row .btn-small, .git-action-btn, .upload-progress-cancel, .run-icon-btn, .ev-box-toggle",
    kind: "dense",
    documented: "each family's `--ctl-h-dense` or `--ctl-h-sm` rule: an action inside a row",
    pending: "rest",
  },
  {
    name: "turn map steps",
    selector: ".turn-map-step",
    kind: "turn-map",
    documented:
      "29-turns.css `.turn-map-step` and 13-messages.css, the docked resume: the map's step size",
    pending: "rest",
  },
];

const CONTROL = "button, summary, [role='button'], a[href]";

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

/** The docked resume button follows the turn map's step size, not the toolbar's (the "turn map steps" family). */
function docked(el: HTMLElement, viewport: readonly [number, number], pointer: Pointer): boolean {
  return el.id === "scroll-bottom" && viewport === DESKTOP && pointer === "fine";
}

function familiesOf(el: Element): Family[] {
  return CENSUS.filter((f) => el.matches(f.selector));
}

/** The controls on the mounted surface that a held family covers. */
function held(): { el: HTMLElement; family: Family }[] {
  return [...document.querySelectorAll<HTMLElement>(CONTROL)]
    .filter(shown)
    .map((el) => ({ el, family: familiesOf(el)[0] }))
    .filter((c): c is { el: HTMLElement; family: Family } => c.family?.pending === undefined)
    .filter(({ family }) => family.kind === "toolbar" || family.kind === "own-size");
}

const PRESETS = [
  ["phone", PHONE],
  ["iPad", IPAD],
  ["desktop", DESKTOP],
] as const;
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

  for (const [preset, viewport] of PRESETS) {
    for (const pointer of TIERS) {
      const at = `${preset} ${pointer}`;

      it(`paints every held control at the toolbar's --btn-h, ${at}`, async () => {
        for (const surface of SURFACES) {
          await mountSurface(viewport, pointer, surface);
          const btn = tokenPx("--btn-h");
          const ref = byId("settings-btn").getBoundingClientRect();
          expect([ref.width, ref.height], `${surface}: the toolbar paints --btn-h`).toEqual([
            btn,
            btn,
          ]);
          const off: string[] = [];
          for (const { el, family } of held()) {
            if (family.kind !== "toolbar" || docked(el, viewport, pointer)) {
              continue;
            }
            const r = painted(el);
            if (r.height !== btn) {
              off.push(`${nameOf(el)} is ${String(r.height)}px tall`);
            }
            if (iconOnly(el) && r.width !== btn) {
              off.push(`${nameOf(el)} is icon-only and ${String(r.width)}px wide`);
            }
          }
          expect(off, `${surface}: painted at --btn-h (${String(btn)}px)`).toEqual([]);
        }
      });

      it(`gives every held control a target at the floor, ${at}`, async () => {
        for (const surface of SURFACES) {
          await mountSurface(viewport, pointer, surface);
          const floor = floorPx();
          expect(floor, "the tier's floor").toBe(pointer === "coarse" ? 44 : 24);
          const off: string[] = [];
          for (const { el, family } of held()) {
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
            if (!edges.every(([x, y]) => owns(el, x, y))) {
              off.push(`${nameOf(el)}'s target edge answers to another element`);
            }
            if (height(hit) < floor - 2 * STEP) {
              off.push(`${nameOf(el)} reaches ${String(height(hit))}px tall`);
            }
            // The chip's x keeps a block-only reach (its family's documented size).
            if (family.kind === "toolbar" && width(hit) < floor - 2 * STEP) {
              off.push(`${nameOf(el)} reaches ${String(width(hit))}px wide`);
            }
          }
          expect(off, `${surface}: every target clears ${String(floor)}px`).toEqual([]);
        }
      });

      it(`lets no two held targets overlap, ${at}`, async () => {
        for (const surface of SURFACES) {
          await mountSurface(viewport, pointer, surface);
          const all = held().map(({ el }) => el);
          const crossed: string[] = [];
          for (const [i, a] of all.entries()) {
            const ta = targetRect(a);
            for (const b of all.slice(i + 1)) {
              if (a.contains(b) || b.contains(a)) {
                continue;
              }
              const tb = targetRect(b);
              const dx = Math.min(ta.right, tb.right) - Math.max(ta.left, tb.left);
              const dy = Math.min(ta.bottom, tb.bottom) - Math.max(ta.top, tb.top);
              if (dx > 0.5 && dy > 0.5) {
                crossed.push(`${nameOf(a)} and ${nameOf(b)} share ${String(dx)}x${String(dy)}px`);
              }
            }
          }
          expect(crossed, `${surface}: no two targets overlap`).toEqual([]);
        }
      });
    }
  }

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
