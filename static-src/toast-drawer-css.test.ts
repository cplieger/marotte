// A toast over the open phone drawer hides no tab row and no drawer control. Under the notice
// band, where it stands over the chat, it lands on the drawer's actions or its first rows, so a
// refused close's toast hid the row it reported putting back. Rendered geometry at a phone
// viewport, the drawer and the chat area sliced from `static/index.html`.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const W = 390;
const H = 844;

let style: HTMLStyleElement;
/** The size later files in this worker measure at; `page.viewport` has no getter. */
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(async () => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
  await page.viewport(W, H);
  expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([W, H]);
});

afterEach(() => {
  document.body.replaceChildren();
});

afterAll(async () => {
  style.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

function slice(from: string, to: string): HTMLElement {
  const start = indexHtml.indexOf(from);
  const end = indexHtml.indexOf(to, start);
  expect(start, `${from} not found`).toBeGreaterThan(-1);
  expect(end, `${to} not found`).toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = indexHtml.slice(start, end + to.length);
  const el = host.firstElementChild;
  if (!(el instanceof HTMLElement)) {
    throw new Error(`${from} did not parse`);
  }
  return el;
}

interface Mounted {
  rows: HTMLElement[];
  scroller: HTMLElement;
  toast: HTMLElement;
}

/** The drawer with `count` rows, the chat area with `banners` banners in its band, and one
 *  error toast in a body-level stack, where the library mounts it. */
function mount(open: boolean, count: number, banners: number): Mounted {
  const sidebar = slice('<nav id="sidebar"', "</nav>");
  sidebar.classList.toggle("open", open);
  const list = sidebar.querySelector<HTMLElement>("#tab-list");
  const scroller = sidebar.querySelector<HTMLElement>("#tab-sections");
  const history = sidebar.querySelector<HTMLElement>("#history-btn");
  if (list === null || scroller === null || history === null) {
    throw new Error("#tab-list, #tab-sections or #history-btn missing");
  }
  const rows = Array.from({ length: count }, (_, i) => {
    const row = document.createElement("div");
    row.className = "tab";
    row.setAttribute("role", "tab");
    const name = document.createElement("span");
    name.className = "tab-name";
    name.textContent = `Chat ${String(i + 1)}`;
    row.appendChild(name);
    return row;
  });
  list.replaceChildren(...rows);

  const area = document.createElement("main");
  area.id = "chat-area";
  const toolbar = document.createElement("div");
  toolbar.className = "chat-toolbar";
  const view = document.createElement("div");
  view.id = "chat-view";
  view.dataset["tabView"] = "";
  const band = document.createElement("div");
  band.id = "banner-stack";
  band.className = "banner-stack";
  for (let i = 0; i < banners; i++) {
    const banner = document.createElement("div");
    banner.className = "banner banner-error";
    const msg = document.createElement("span");
    msg.className = "banner-msg";
    msg.textContent = "Reconnecting to the server.";
    banner.appendChild(msg);
    band.appendChild(banner);
  }
  view.appendChild(band);
  area.append(toolbar, view);

  const stack = document.createElement("div");
  stack.className = "uip-toast-stack";
  const toast = document.createElement("div");
  toast.className = "uip-toast uip-toast--error is-shown";
  const msg = document.createElement("span");
  msg.className = "uip-toast-msg";
  msg.textContent = "Seadex-scout webui: Could not close that tab, so it was put back.";
  const action = document.createElement("button");
  action.type = "button";
  action.className = "uip-toast-retry";
  action.textContent = "Open";
  toast.append(msg, action);
  stack.appendChild(toast);

  document.body.replaceChildren(sidebar, area, stack);
  return { rows: [...rows, history], scroller, toast };
}

function overlaps(a: DOMRect, b: DOMRect): boolean {
  return a.top < b.bottom && b.top < a.bottom && a.left < b.right && b.left < a.right;
}

/** The part of `r` the strip's scroller shows, or null when the strip clips all of it. */
function visibleIn(r: DOMRect, clip: DOMRect): DOMRect | null {
  const top = Math.max(r.top, clip.top);
  const bottom = Math.min(r.bottom, clip.bottom);
  return bottom > top ? new DOMRect(r.left, top, r.width, bottom - top) : null;
}

describe("a toast over the open phone drawer", () => {
  for (const banners of [0, 1]) {
    for (const count of [1, 9, 40]) {
      it(`covers no visible row of a ${String(count)}-row strip, ${String(banners)} banner(s) up`, () => {
        const { rows, scroller, toast } = mount(true, count, banners);
        const t = toast.getBoundingClientRect();
        expect(t.height, "the toast is laid out").toBeGreaterThan(0);
        expect(t.top, "on screen").toBeGreaterThanOrEqual(0);
        expect(t.bottom, "on screen").toBeLessThanOrEqual(H);
        const clip = scroller.getBoundingClientRect();
        for (const [i, row] of rows.entries()) {
          const shown = visibleIn(row.getBoundingClientRect(), clip);
          expect(
            shown !== null && overlaps(t, shown),
            `row ${String(i)} is not under the toast (toast ${String(t.top)}..${String(t.bottom)})`,
          ).toBe(false);
        }
      });
    }

    it(`covers none of the drawer's header or action controls, ${String(banners)} banner(s) up`, () => {
      const { toast } = mount(true, 3, banners);
      const t = toast.getBoundingClientRect();
      for (const el of document.querySelectorAll<HTMLElement>(
        "#sidebar .sidebar-header button:not(.hidden), #sidebar .sidebar-actions button",
      )) {
        expect(overlaps(t, el.getBoundingClientRect()), `#${el.id} is not under the toast`).toBe(
          false,
        );
      }
    });
  }

  it("stands under the notice band while the drawer is closed, clear of the composer", () => {
    const { toast } = mount(false, 3, 0);
    expect(toast.getBoundingClientRect().top).toBeLessThan(H / 4);
  });
});
