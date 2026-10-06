// A hit test is the only reader of hit order: boxes and declarations are identical whichever
// element wins a point. The fixture is the production `#app` markup with the rows script would add
// planted by hand.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { makeExpandable } from "./pill-expand.js";

const INTERACTIVE =
  'button, a[href], input, select, textarea, [tabindex]:not([tabindex="-1"]), [role="button"]';

const FLOOR = { fine: 24, coarse: 44 } as const;
type Tier = keyof typeof FLOOR;

let style: HTMLStyleElement;
let entry: { readonly width: number; readonly height: number };

beforeAll(async () => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
  await page.viewport(1440, 900);
});

afterAll(async () => {
  style.remove();
  await page.viewport(entry.width, entry.height);
});

afterEach(() => {
  document.body.replaceChildren();
  const root = document.documentElement;
  root.style.removeProperty("--sidebar-w-pref");
  delete root.dataset["pointer"];
});

interface Shell {
  sidebar: HTMLElement;
  handle: HTMLElement;
  tabList: HTMLElement;
  chat: HTMLElement;
}

function q<T extends HTMLElement>(sel: string): T {
  const found = document.querySelector<T>(sel);
  if (found === null) {
    throw new Error(`fixture: no ${sel}`);
  }
  return found;
}

/** Production `#app`, with the rows script would add. */
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
  q("#messages").appendChild(turn);

  const shell = q("#shell-panel");
  shell.classList.remove("shell-closed");
  const bar = q("#shell-resize");
  bar.setAttribute("role", "separator");
  bar.tabIndex = 0;
  const root = document.createElement("div");
  root.className = "wt-root wt-container";
  const term = document.createElement("div");
  term.className = "term";
  term.textContent = "$ ls -la";
  root.appendChild(term);
  q("#shell-terminal").appendChild(root);

  return { sidebar: q("#sidebar"), handle, tabList, chat: q("#chat-area") };
}

/** The border's inner edge: where the handle starts. */
function edge(s: Shell): number {
  const r = s.sidebar.getBoundingClientRect();
  return r.right - parseFloat(getComputedStyle(s.sidebar).borderInlineEndWidth);
}

/** What a press at (x, y) reaches once the handle is set aside. */
function beneath(s: Shell, x: number, y: number): Element | undefined {
  return document.elementsFromPoint(x, y).find((e) => e !== s.handle && !e.contains(s.handle));
}

/** Show one view of the chat area, as the tab projection would. */
function show(id: string): void {
  for (const v of document.querySelectorAll("[data-tab-view]")) {
    v.classList.toggle("hidden", v.id !== id);
  }
}

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
    const list = s.tabList.getBoundingClientRect();
    expect(s.tabList.scrollHeight, "premise: the list overflows").toBeGreaterThan(
      s.tabList.clientHeight,
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
        q("#editor-gutter").textContent = "1\n2\n3";
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
    q("#editor-gutter").textContent = "1\n2\n3";
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

  it("gives the terminal's first glyph columns to the handle, and nothing more", () => {
    const s = mountApp(tier, widened);
    const b = edge(s);
    const terminal = q("#shell-terminal");
    const t = terminal.getBoundingClientRect();
    const y = t.top + t.height / 2;
    expect(terminal.contains(beneath(s, b + 2, y) ?? null), "premise: under the reach").toBe(true);
    expect(document.elementFromPoint(b + 2, y)).toBe(s.handle);
    expect(terminal.contains(document.elementFromPoint(b + FLOOR[tier] + 1, y))).toBe(true);
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
