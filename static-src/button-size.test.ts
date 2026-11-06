// The composer's controls and the resume button are the chat toolbar's buttons on every tier: the same
// painted box (`--btn-h`) and the same target (the shared expander up to `--hit-floor`), with no two targets
// overlapping. On a row too narrow for the fullest labelled line, the model and mode pills drop their text.
// The fixture is the production `#app`, so the toolbar reference and the controls share one layout.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { buildAttachmentPill } from "./attachment-pill.js";
import { iconEl } from "./icon-el.js";
import { ICON_CANCEL, ICON_HOURGLASS, ICON_MODEL, ICON_SEND } from "./icons.js";
import { iconForMode } from "./roles.js";
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
const MODEL = "claude opus 5.5";
const EFFORT = "· High";
const MODE = "Default V2";

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

/** The DOM `model-switcher.ts`, `status.ts`, `role-picker.ts`, `prompt-input.ts` and `attachment-pill.ts` write. */
function dress(state: State): void {
  byId("switch-model-btn").prepend(iconEl(ICON_MODEL));
  byId("ctx-model-pill").textContent = MODEL;
  const effort = byId("ctx-effort-pill");
  effort.textContent = EFFORT;
  effort.classList.remove("hidden");
  byId("role-pill-icon").replaceChildren(iconEl(iconForMode("default-v2")));
  byId("role-pill-label").textContent = MODE;
  byId("context-label").textContent = "66%";
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
  dress(state);
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
