import { expect, vi } from "vitest";
import { cdp } from "vitest/browser";

import { loadTurnRail, mountTurnRail } from "../turn-rail.js";
import { apiGet } from "../api-client.js";
import { mountAppCSS } from "./css-rules.js";
import type { TurnSummary } from "../rail-merge.js";

export interface Point {
  x: number;
  y: number;
}

/** Mount the map for chat `id` with `count` turns in a `width`px chat area; the caller mocks
 *  `../api-client.js` and `../scroll.js`. Returns the teardown. */
export async function mountRail(id: string, count: number, width: number): Promise<() => void> {
  const style = mountAppCSS();
  document.body.style.margin = "0";
  const area = document.createElement("div");
  area.id = "chat-area";
  area.style.cssText = `position:fixed;top:0;left:0;margin:0;width:${String(width)}px;height:700px;display:flex;`;
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  area.appendChild(outer);
  document.body.appendChild(area);
  mountTurnRail(outer);
  const turns: TurnSummary[] = Array.from({ length: count }, (_, i) => ({
    id: `m${String(i + 1)}`,
    n: i + 1,
    outcome: "completed",
    ts: (i + 1) * 60_000,
    first_line: `Question ${String(i + 1)}`,
  }));
  vi.mocked(apiGet).mockResolvedValue({ turns });
  await loadTurnRail(id);
  await vi.waitFor(() => {
    expect(rows()).toHaveLength(count);
  });
  await frames();
  return () => {
    area.remove();
    style.remove();
  };
}

function rows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(".turn-map-stack > .turn-pill")];
}

function turnOf(row: Element | null): string | null {
  return (
    row
      ?.querySelector("a")
      ?.getAttribute("href")
      ?.replace(/^.*#turn-/u, "") ?? null
  );
}

export function rowFor(n: number): HTMLElement {
  const row = rows().find((li) => turnOf(li) === String(n));
  if (row === undefined) {
    throw new Error(`no row for turn ${String(n)}`);
  }
  return row;
}

/** The middle of turn `n`'s row, on the rail's centre line, in this page's coordinates. */
export function pointOn(n: number): Point {
  const track = document.querySelector(".turn-map-track")?.getBoundingClientRect();
  const r = rowFor(n).getBoundingClientRect();
  return { x: (track?.left ?? 0) + (track?.width ?? 0) / 2, y: r.top + r.height / 2 };
}

/** The turn whose row wears the scrub mark, or null. */
export function scrubbedTurn(): string | null {
  return turnOf(document.querySelector(".turn-pill[data-scrub]"));
}

/** The turn the map marks current, or null. */
export function currentTurn(): string | null {
  return turnOf(document.querySelector(".turn-pill[data-current]"));
}

/** The text of the tooltip on screen, or null. */
export function tooltipText(): string | null {
  return document.querySelector(".uip-tooltip:not(.is-leaving)")?.textContent ?? null;
}

export function frames(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

/** `p` in the top-level page's coordinates, which CDP input takes: the test page is an iframe. */
function topLevel(p: Point): Point {
  const frame = window.frameElement?.getBoundingClientRect();
  const scale = frame === undefined ? 1 : frame.width / window.innerWidth;
  return { x: (frame?.left ?? 0) + p.x * scale, y: (frame?.top ?? 0) + p.y * scale };
}

/** Real input through CDP, so the page sees the pointer events a device produces, with pointer ids
 *  `setPointerCapture` accepts. */
export async function dispatchTouch(
  type: "touchStart" | "touchMove" | "touchEnd" | "touchCancel",
  at?: Point,
): Promise<void> {
  await cdp().send("Input.dispatchTouchEvent", {
    type,
    touchPoints: at === undefined ? [] : [topLevel(at)],
  });
}

export async function dispatchMouse(
  type: "mousePressed" | "mouseMoved" | "mouseReleased",
  at: Point,
  pressed: boolean,
): Promise<void> {
  const p = topLevel(at);
  await cdp().send("Input.dispatchMouseEvent", {
    type,
    x: p.x,
    y: p.y,
    button: pressed || type !== "mouseMoved" ? "left" : "none",
    buttons: pressed ? 1 : 0,
    clickCount: type === "mouseMoved" ? 0 : 1,
  });
}
