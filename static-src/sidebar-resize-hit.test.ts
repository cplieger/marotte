// A hit test is the only reader of hit order: boxes and declarations are identical whichever
// element wins a point. The fixture is the production `#app` markup with the rows script would add
// planted by hand, except the editor's own surfaces, which its painter draws.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { makeExpandable } from "./pill-expand.js";
import { parseConflicts } from "./conflict.js";
import { initGoto } from "./editor-goto.js";
import { restoreUI } from "./editor-modes.js";
import { bufferDiffSource, fileStates, freshState, setActiveFilePath } from "./editor-types.js";
import type { FileMode } from "./editor-types.js";
import type * as EditorFind from "./editor-find.js";
import wtuiManifest from "./node_modules/@cplieger/web-terminal-ui/css/MANIFEST.touch?raw";

const wtuiSheets = import.meta.glob<string>("./node_modules/@cplieger/web-terminal-ui/css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

/** web-terminal-ui's sheets, which `cmd/bundle` ships ahead of the app's: the terminal's box is
 *  theirs. */
function terminalCSS(): string {
  return wtuiManifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"))
    .map((name) => {
      const sheet = wtuiSheets[`./node_modules/@cplieger/web-terminal-ui/css/${name}`];
      if (sheet === undefined) {
        throw new Error(`no web-terminal-ui sheet ${name}`);
      }
      return sheet;
    })
    .join("\n");
}

const INTERACTIVE =
  'button, a[href], input, select, textarea, [tabindex]:not([tabindex="-1"]), [role="button"]';

const FLOOR = { fine: 24, coarse: 44 } as const;
type Tier = keyof typeof FLOOR;

let style: HTMLStyleElement;
let wtuiStyle: HTMLStyleElement;
let entry: { readonly width: number; readonly height: number };

beforeAll(async () => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  wtuiStyle = document.createElement("style");
  wtuiStyle.textContent = terminalCSS();
  document.head.appendChild(wtuiStyle);
  style = mountAppCSS();
  document.body.style.margin = "0";
  await page.viewport(1440, 900);
});

afterAll(async () => {
  style.remove();
  wtuiStyle.remove();
  await page.viewport(entry.width, entry.height);
});

afterEach(() => {
  // Before the DOM goes: the go-to form's effect closes it on a path change.
  setActiveFilePath("");
  fileStates.clear();
  document.body.replaceChildren();
  const root = document.documentElement;
  root.style.removeProperty("--sidebar-w-pref");
  delete root.dataset["pointer"];
});

interface Shell {
  sidebar: HTMLElement;
  handle: HTMLElement;
  tabList: HTMLElement;
  scroller: HTMLElement;
  chat: HTMLElement;
}

function q<T extends HTMLElement>(sel: string): T {
  const found = document.querySelector<T>(sel);
  if (found === null) {
    throw new Error(`fixture: no ${sel}`);
  }
  return found;
}

function mountApp(tier: Tier, widened: boolean): Shell {
  document.documentElement.dataset["pointer"] = tier;
  if (widened) {
    document.documentElement.style.setProperty("--sidebar-w-pref", "9999px");
  }
  const parsed = new DOMParser().parseFromString(indexHtml, "text/html").getElementById("app");
  if (parsed === null) {
    throw new Error("index.html has no #app");
  }
  document.body.replaceChildren(document.importNode(parsed, true));
  // What `initSidebarResize` writes; the tabindex is what the universal hit floor keys on, so a
  // bare div would not exercise the floor opt-out.
  const handle = q("#sidebar-resize");
  handle.setAttribute("role", "separator");
  handle.tabIndex = 0;

  const tabList = q("#tab-list");
  for (let i = 0; i < 60; i++) {
    const row = document.createElement("div");
    row.className = "tab";
    row.setAttribute("role", "tab");
    row.tabIndex = i === 0 ? 0 : -1;
    const name = document.createElement("span");
    name.className = "tab-name";
    name.textContent = `Conversation ${String(i)}`;
    const close = document.createElement("span");
    close.className = "tab-close";
    close.textContent = "×";
    row.append(name, close);
    tabList.appendChild(row);
  }

  const banner = document.createElement("div");
  banner.className = "banner banner-info";
  const msg = document.createElement("span");
  msg.className = "banner-msg";
  msg.textContent = "Kiro CLI is installing. Chats start once it is ready.";
  const dismiss = document.createElement("button");
  dismiss.type = "button";
  dismiss.className = "icon-btn banner-dismiss";
  dismiss.setAttribute("aria-label", "Dismiss");
  banner.append(msg, dismiss);
  q("#banner-stack").appendChild(banner);

  const turn = document.createElement("div");
  turn.className = "turn";
  const head = document.createElement("div");
  head.className = "turn-header";
  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "turn-fold-toggle icon-btn";
  copy.setAttribute("aria-label", "Copy");
  head.append(copy);
  const body = document.createElement("div");
  body.className = "turn-body";
  body.textContent = "A reply long enough to run across the whole measure of the column.";
  turn.append(head, body);
  const transcript = document.createElement("div");
  transcript.className = "transcript-view";
  transcript.appendChild(turn);
  q("#messages").appendChild(transcript);

  const shell = q("#shell-panel");
  shell.classList.remove("shell-closed");
  const bar = q("#shell-resize");
  bar.setAttribute("role", "separator");
  bar.tabIndex = 0;
  // What `createTerminal($.shellTerminal, { layout: "container" })` builds: the panel element is
  // the root itself and `.term` its child.
  const root = q("#shell-terminal");
  root.classList.add("wt-root", "wt-container");
  const term = document.createElement("div");
  term.className = "term";
  const row = document.createElement("div");
  row.className = "term-row";
  row.textContent = "root@marotte:/workspace/_scratch$ ls -la";
  term.appendChild(row);
  root.appendChild(term);

  return {
    sidebar: q("#sidebar"),
    handle,
    tabList,
    scroller: q("#tab-sections"),
    chat: q("#chat-area"),
  };
}

/** The border's inner edge: where the handle starts. */
function edge(s: Shell): number {
  const r = s.sidebar.getBoundingClientRect();
  return r.right - parseFloat(getComputedStyle(s.sidebar).borderInlineEndWidth);
}

/** The box of the first visible selectable glyph under `el`: where a selection drag starts. A
 *  frame's document is out of reach, so a frame stands for its own first column. */
function firstGlyph(el: Element): DOMRect {
  if (el instanceof HTMLIFrameElement) {
    return el.getBoundingClientRect();
  }
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    const at = (n.textContent ?? "").search(/\S/);
    const owner = n.parentElement;
    if (at < 0 || owner === null || getComputedStyle(owner).userSelect === "none") {
      continue;
    }
    const range = document.createRange();
    range.setStart(n, at);
    range.setEnd(n, at + 1);
    const r = range.getBoundingClientRect();
    if (r.width > 0) {
      return r;
    }
  }
  throw new Error("fixture: no visible text");
}

/** What a press at (x, y) reaches once the handle is set aside. */
function beneath(s: Shell, x: number, y: number): Element | undefined {
  return document.elementsFromPoint(x, y).find((e) => e !== s.handle && !e.contains(s.handle));
}

function show(id: string): void {
  for (const v of document.querySelectorAll("[data-tab-view]")) {
    v.classList.toggle("hidden", v.id !== id);
  }
}

function plant(parent: Element, tag: string, className: string, text = ""): HTMLElement {
  const child = document.createElement(tag);
  child.className = className;
  child.textContent = text;
  parent.appendChild(child);
  return child;
}

function showEditorPane(id: string): HTMLElement {
  show("editor-view");
  const pane = q(`#${id}`);
  pane.classList.remove("hidden");
  return pane;
}

/** Every surface whose text can sit against the sidebar's edge, each with the markup its renderer
 *  writes planted by hand. `text` is where the first selectable glyph is looked for. */
const SURFACES: readonly (readonly [string, () => { surface: Element; text: Element }])[] = [
  [
    "the shell terminal",
    () => ({ surface: q("#shell-terminal"), text: q("#shell-terminal .term") }),
  ],
  [
    "the file viewer",
    () => {
      const viewer = showEditorPane("editor-viewer");
      plant(viewer, "div", "viewer-spacer");
      const row = plant(plant(viewer, "div", "viewer-rows"), "div", "viewer-row");
      plant(row, "span", "viewer-ln", "1");
      plant(row, "span", "viewer-text", "package main");
      return { surface: viewer, text: viewer };
    },
  ],
  [
    "the markdown preview",
    () => {
      const md = showEditorPane("editor-markdown");
      plant(plant(md, "div", "message assistant"), "p", "", "A paragraph of rendered Markdown.");
      return { surface: md, text: md };
    },
  ],
  [
    "the file notice",
    () => {
      const notice = showEditorPane("editor-notice");
      plant(notice, "p", "editor-notice-text", "This file is too large to show.");
      return { surface: notice, text: notice };
    },
  ],
  [
    "the web preview",
    () => {
      show("web-view");
      const frame = plant(q("#web-stage"), "div", "web-frame");
      frame.dataset["mode"] = "fill";
      return { surface: frame, text: frame.appendChild(document.createElement("iframe")) };
    },
  ],
  [
    "a run tab",
    () => {
      show("run-view");
      const page = plant(q("#run-body"), "div", "ev-page");
      const pane = plant(page, "div", "ev-pane ev-pane-detail");
      plant(pane, "p", "", "A step's output.");
      return { surface: page, text: page };
    },
  ],
  [
    "a delegate's page",
    () => {
      show("subagent-view");
      const body = q("#subagent-body");
      plant(body, "p", "", "A delegate's reply.");
      return { surface: body, text: body };
    },
  ],
  [
    "the spec page",
    () => {
      show("spec-view");
      const body = q("#spec-body");
      plant(body, "p", "", "A spec's requirements.");
      return { surface: body, text: body };
    },
  ],
  [
    "the transcript",
    () => {
      show("chat-view");
      // The planted banner overlays the transcript's first line.
      q("#banner-stack").replaceChildren();
      return { surface: q("#messages"), text: q("#messages .turn-body") };
    },
  ],
  [
    "the settings page",
    () => {
      show("settings-view");
      const shell = q("#settings-view .page-shell");
      return { surface: shell, text: q("#settings-view .section-hint") };
    },
  ],
];

// The title is chrome text: the tier never moves it, so the handle's reach yields its band.
describe.each([false, true])("the title bar's heading, widened %s", (widened) => {
  it("starts at the bar's house inset on both tiers, and a press there reaches it", () => {
    const at: Partial<Record<Tier, number>> = {};
    for (const tier of ["fine", "coarse"] as const) {
      const s = mountApp(tier, widened);
      const title = q("#titlebar-title");
      const g = firstGlyph(title);
      const bar = q(".chat-toolbar").getBoundingClientRect();
      // `--sp-3`, the bar's own leading inset.
      expect(g.left - bar.left, `${tier}: the house inset`).toBeCloseTo(12, 0);
      expect(g.left, `${tier}: premise: the glyph sits inside the reach`).toBeLessThan(
        edge(s) + FLOOR[tier],
      );
      const hit = document.elementFromPoint(g.left + 1, g.top + g.height / 2);
      expect(title.contains(hit), `${tier}: a press on the first glyph`).toBe(true);
      at[tier] = g.left;
    }
    expect(at.coarse).toBe(at.fine);
  });
});

const CASES = [
  ["fine", false],
  ["fine", true],
  ["coarse", false],
  ["coarse", true],
] as const;

describe.each(CASES)("at the %s tier, widened %s", (tier, widened) => {
  it("is exactly the tier's floor, all of it outward", () => {
    const s = mountApp(tier, widened);
    const b = edge(s);
    const y = window.innerHeight / 2;
    expect(s.handle.getBoundingClientRect().width, "the painted box stays 3px").toBe(3);
    expect(document.elementFromPoint(b + 0.5, y)).toBe(s.handle);
    expect(document.elementFromPoint(b + FLOOR[tier] - 0.5, y)).toBe(s.handle);
    expect(document.elementFromPoint(b + FLOOR[tier] + 0.5, y)).not.toBe(s.handle);
  });

  it("reaches nothing inside the sidebar: not the scrollbar column, not a row", () => {
    const s = mountApp(tier, widened);
    const b = edge(s);
    const list = s.scroller.getBoundingClientRect();
    expect(s.scroller.scrollHeight, "premise: the sections overflow").toBeGreaterThan(
      s.scroller.clientHeight,
    );
    for (const dx of [1, 8]) {
      const hit = document.elementFromPoint(b - dx, list.top + list.height / 2);
      expect(hit, `${String(dx)}px inside`).not.toBe(s.handle);
      expect(s.sidebar.contains(hit), `${String(dx)}px inside`).toBe(true);
    }
    const close = s.tabList.querySelector(".tab-close");
    if (close === null) {
      throw new Error("fixture: no close mark");
    }
    const c = close.getBoundingClientRect();
    expect(document.elementFromPoint(c.left + c.width / 2, c.top + c.height / 2)).toBe(close);
  });

  it("leaves nothing interactive under its reach, in every chat-area view", () => {
    const s = mountApp(tier, widened);
    const b = edge(s);
    const hits = new Set<string>();
    for (const view of ["chat-view", "editor-view", "files-view", "settings-view"]) {
      show(view);
      const editor = q("#editor-content");
      if (view === "editor-view") {
        editor.classList.remove("hidden");
        q("#editor-edit-gutter").classList.remove("hidden");
        q(".editor-body").classList.add("is-editing");
      }
      const textStart =
        editor.getBoundingClientRect().left + parseFloat(getComputedStyle(editor).paddingLeft);
      const area = s.chat.getBoundingClientRect();
      for (let y = area.top + 2; y < area.bottom; y += 6) {
        for (let x = b + 0.5; x < b + FLOOR[tier]; x += 3) {
          const owner = beneath(s, x, y)?.closest(INTERACTIVE);
          // The shell's own separator spans the panel's full width, so the two separators share its
          // leading corner; it keeps the rest of its width.
          if (owner === null || owner === undefined || owner.id === "shell-resize") {
            continue;
          }
          // The editor's textarea may start under the coarse reach, but only its left padding does:
          // a press on text always reaches the editor.
          if (owner === editor && x < textStart) {
            continue;
          }
          hits.add(`${view}: ${owner.id === "" ? owner.className : `#${owner.id}`}`);
        }
      }
    }
    expect([...hits]).toEqual([]);
  });

  it("starts the editor's text past the reach", () => {
    const s = mountApp(tier, widened);
    show("editor-view");
    const editor = q("#editor-content");
    editor.classList.remove("hidden");
    q("#editor-edit-gutter").classList.remove("hidden");
    q(".editor-body").classList.add("is-editing");
    const textStart =
      editor.getBoundingClientRect().left + parseFloat(getComputedStyle(editor).paddingLeft);
    expect(textStart).toBeGreaterThanOrEqual(edge(s) + FLOOR[tier]);
  });

  it("shares only the shell bar's leading corner", () => {
    const s = mountApp(tier, widened);
    const b = edge(s);
    const bar = q("#shell-resize").getBoundingClientRect();
    const y = bar.top + bar.height / 2;
    expect(document.elementFromPoint(b + 1, y)).toBe(s.handle);
    expect(document.elementFromPoint(b + FLOOR[tier] + 1, y)).toBe(q("#shell-resize"));
  });
});

describe.each(CASES)("at the %s tier, widened %s, every text surface", (tier, widened) => {
  it.each(SURFACES)("starts %s's text past the reach", (_name, mount) => {
    const s = mountApp(tier, widened);
    const { surface, text } = mount();
    const g = firstGlyph(text);
    expect(g.left).toBeGreaterThanOrEqual(edge(s) + FLOOR[tier]);
    const hit = document.elementFromPoint(g.left + 1, g.top + Math.min(g.height / 2, 8));
    const at = hit === null ? "nothing" : `${hit.tagName}#${hit.id}.${hit.className}`;
    expect(surface.contains(hit), `a press on the first column reaches ${at}`).toBe(true);
  });
});

const FILE = "/workspace/a.go";

/** `text` on the pane in `mode`, drawn by the editor's one painter. */
function paintFile(mode: FileMode, text: string, original = text, error = ""): void {
  show("editor-view");
  const state = freshState(FILE);
  state.loaded = true;
  state.facts.value = {
    kind: "small",
    binary: false,
    utf8: true,
    conflict: mode.kind === "conflict",
    readOnly: false,
    size: text.length,
  };
  state.original.value = original;
  state.current.value = text;
  state.mode.value = mode;
  state.error.value = error;
  fileStates.set(FILE, state);
  setActiveFilePath(FILE);
  restoreUI(state);
}

/** Where a field's typed text starts, past its border and padding. */
function fieldText(input: HTMLInputElement): DOMRect {
  const r = input.getBoundingClientRect();
  const left = r.left + input.clientLeft + parseFloat(getComputedStyle(input).paddingLeft);
  return new DOMRect(left, r.top, r.right - left, r.height);
}

let findSeq = 0;

const CONFLICTED = ["<<<<<<< HEAD", "ours", "=======", "theirs", ">>>>>>> incoming", ""].join("\n");

/** Each case returns where its first selectable text starts. */
const EDITOR_SURFACES: readonly (readonly [
  string,
  () => Promise<{ surface: Element; glyph: DOMRect }>,
])[] = [
  [
    "the error banner",
    () => {
      paintFile({ kind: "text", editing: false }, "", "", "The file could not be read.");
      const banner = q("#editor-error");
      return Promise.resolve({ surface: banner, glyph: firstGlyph(banner) });
    },
  ],
  [
    "the conflict overlay",
    () => {
      paintFile(
        { kind: "conflict", conflict: parseConflicts(CONFLICTED), editing: true },
        CONFLICTED,
      );
      const overlay = q("#editor-conflict-overlay");
      return Promise.resolve({ surface: overlay, glyph: firstGlyph(overlay) });
    },
  ],
  [
    "the diff's caption",
    () => {
      paintFile(
        { kind: "diff", diffSource: bufferDiffSource() },
        "package mine\n",
        "package main\n",
      );
      const pane = q("#editor-diff-pane");
      return Promise.resolve({ surface: pane, glyph: firstGlyph(q(".diff-pane-label-old")) });
    },
  ],
  [
    "the diff's old column",
    () => {
      paintFile(
        { kind: "diff", diffSource: bufferDiffSource() },
        "package mine\n",
        "package main\n",
      );
      const pane = q("#editor-diff-pane");
      return Promise.resolve({ surface: pane, glyph: firstGlyph(q(".diff-col-old")) });
    },
  ],
  [
    "the go-to-line field",
    () => {
      paintFile({ kind: "text", editing: false }, "package main\n");
      initGoto();
      q("#editor-goto-btn").click();
      const input = q<HTMLInputElement>("#editor-goto-input");
      return Promise.resolve({ surface: input, glyph: fieldText(input) });
    },
  ],
  [
    "the find bar's field",
    async () => {
      paintFile({ kind: "text", editing: false }, "package main\n");
      // A fresh module per mount: the bar it built for an earlier fixture is detached.
      findSeq++;
      const find = (await import(
        /* @vite-ignore */ `./editor-find.ts?reach=${String(findSeq)}`
      )) as typeof EditorFind;
      expect(find.openEditorFind(), "premise: the bar opens over source").toBe(true);
      const input = q<HTMLInputElement>("#editor-find .editor-find-input");
      return { surface: input, glyph: fieldText(input) };
    },
  ],
];

describe.each(CASES)("at the %s tier, widened %s, the editor's own surfaces", (tier, widened) => {
  it.each(EDITOR_SURFACES)("start %s's text past the reach", async (_name, mount) => {
    const s = mountApp(tier, widened);
    const { surface, glyph } = await mount();
    expect(glyph.left).toBeGreaterThanOrEqual(edge(s) + FLOOR[tier]);
    const hit = document.elementFromPoint(
      glyph.left + 1,
      glyph.top + Math.min(glyph.height / 2, 8),
    );
    const at = hit === null ? "nothing" : `${hit.tagName}#${hit.id}.${hit.className}`;
    expect(surface.contains(hit), `a press on the first column reaches ${at}`).toBe(true);
  });
});

describe.each(CASES)("at the %s tier, widened %s, the shell's grid", (tier, widened) => {
  // web-terminal-engine's computeSize: cols = floor((termWrap.clientWidth - padding) / cell width).
  it("fills the panel past the reach: no horizontal scroll, no clipped column", () => {
    const s = mountApp(tier, widened);
    const panel = q("#shell-terminal").getBoundingClientRect();
    const term = q("#shell-terminal .term");
    const t = term.getBoundingClientRect();
    expect(t.left).toBeGreaterThanOrEqual(edge(s) + FLOOR[tier]);
    expect(t.left - panel.left, "the grid gives up the reach and nothing more").toBeLessThanOrEqual(
      FLOOR[tier],
    );
    expect(t.right, "the grid reaches the panel's trailing edge").toBe(panel.right);

    const cs = getComputedStyle(term);
    const padL = parseFloat(cs.paddingLeft);
    const content = term.clientWidth - padL - parseFloat(cs.paddingRight);
    const row = q("#shell-terminal .term-row");
    row.textContent = "M";
    const cell = firstGlyph(row).width;
    const cols = Math.floor(content / cell);
    row.textContent = "M".repeat(cols);
    const range = document.createRange();
    range.selectNodeContents(row);
    const text = range.getBoundingClientRect();
    expect(text.left, "the first column is not clipped").toBeGreaterThanOrEqual(t.left + padL);
    expect(text.right, "the last column fits").toBeLessThanOrEqual(t.left + padL + content + 0.5);
    expect(content - text.width, "one more column would not fit").toBeLessThan(cell + 0.5);
    expect(term.scrollWidth).toBeLessThanOrEqual(term.clientWidth);
  });
});

describe("with the handle hidden", () => {
  afterEach(async () => {
    await page.viewport(1440, 900);
  });

  /** How far the grid starts in from the panel's leading edge. */
  function terminalInset(): number {
    const panel = q("#shell-terminal").getBoundingClientRect();
    return q("#shell-terminal .term").getBoundingClientRect().left - panel.left;
  }

  // 1181px is the handle's own breakpoint (`73.75rem + 1px`); 390px is the phone drawer.
  it.each([1181, 390])("at %ipx no surface pays for a reach", async (width) => {
    await page.viewport(width, 900);
    mountApp("coarse", false);
    expect(getComputedStyle(q("#sidebar-resize")).display).toBe("none");
    expect(terminalInset(), "the terminal").toBe(0);
    const md = showEditorPane("editor-markdown");
    plant(plant(md, "div", "message assistant"), "p", "", "A paragraph of rendered Markdown.");
    // Its own padding on both sides, nothing more on the leading one.
    expect(firstGlyph(md).left - md.getBoundingClientRect().left, "the markdown preview").toBe(
      parseFloat(getComputedStyle(md).paddingInlineEnd),
    );
    show("web-view");
    const stage = q("#web-stage");
    expect(stage.getBoundingClientRect().left, "the web preview").toBe(
      q("#web-view").getBoundingClientRect().left,
    );
  });

  it("in the shell's full screen, which covers the sidebar", () => {
    mountApp("coarse", false);
    q("#shell-panel").classList.add("shell-fullscreen");
    expect(terminalInset()).toBe(0);
  });
});

describe("the pointer toggle", () => {
  // web-terminal-ui recounts columns when the root's content box resizes; the toggle moves the
  // reach and nothing else, so the reach must reach that box.
  it("resizes the box the terminal observes by the change in reach", async () => {
    mountApp("fine", false);
    const root = q("#shell-terminal");
    const widths: number[] = [];
    let seen = (): void => undefined;
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) {
        widths.push(e.contentRect.width);
      }
      seen();
    });
    const next = (): Promise<void> =>
      new Promise((resolve) => {
        seen = resolve;
      });
    let observed = next();
    ro.observe(root);
    await observed;
    // Out of the delivery: a style write from its microtask is a ResizeObserver loop.
    await new Promise((r) => setTimeout(r, 0));
    observed = next();
    document.documentElement.dataset["pointer"] = "coarse";
    // Observations are delivered within a frame; a box that did not resize delivers none.
    await Promise.race([observed, new Promise((r) => setTimeout(r, 500))]);
    ro.disconnect();
    const [fine, coarse] = widths;
    expect(fine).toBeDefined();
    expect(coarse, "a resize was observed").toBeDefined();
    expect((fine ?? 0) - (coarse ?? 0)).toBeCloseTo(FLOOR.coarse - FLOOR.fine, 1);
  });
});

describe("the open status card", () => {
  it("wins over the handle where it overflows a default-width panel", () => {
    const s = mountApp("fine", false);
    const b = edge(s);
    const card = q("#status-card");
    const wide = document.createElement("span");
    wide.className = "pill-detail";
    wide.style.cssText = "display:block;width:300px;height:64px";
    card.appendChild(wide);
    const btn = q<HTMLButtonElement>("#account-btn");
    makeExpandable(btn, card);
    btn.click();
    for (const a of card.getAnimations()) {
      a.finish();
    }
    const r = card.getBoundingClientRect();
    expect(r.right, "premise: the card overflows the panel").toBeGreaterThan(b + 10);
    const hit = document.elementFromPoint(b + 5, r.top + r.height / 2);
    expect(card.contains(hit)).toBe(true);
  });
});
