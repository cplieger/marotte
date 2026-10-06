// The rail does not redraw a rail that has not changed.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { KEY_ATTR } from "@cplieger/reactive";
import type { TurnSummary } from "./turn-rail.js";

vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

// The DOM the scroll controller resolves at import, nested the way the page nests it.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.style.cssText = "height:300px;overflow-y:auto;overflow-anchor:none;position:relative";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
wrap.appendChild(messagesEl);
outer.appendChild(wrap);
document.body.appendChild(outer);
for (const [id, tag] of [
  ["chat-view", "div"],
  ["scroll-bottom", "button"],
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  if (id === "scroll-bottom") {
    e.appendChild(document.createElement("span"));
  }
  document.body.appendChild(e);
}
// Browser Mode serves no CSS, so the scene declares the two boxes the stylesheet would: the track's
// height (a rail with no box holds one marker) and a real beat on a pending marker, which is what
// the first case measures.
const style = document.createElement("style");
style.textContent = `
  .turn-rail{position:absolute;inset-block-start:0;block-size:400px}
  @keyframes rail-beat { 50% { opacity: 0.4 } }
  .rail-marker{ animation: rail-beat 2400ms linear infinite }`;
document.head.appendChild(style);

const rail = await import("./turn-rail.js");
const { apiGet } = await import("./api-client.js");

rail.mountTurnRail(outer);

const MINUTE = 60_000;

function summary(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return { id: `u${String(n)}`, n, outcome: "completed", ts: n * MINUTE, ...over };
}

function card(n: number): HTMLElement {
  const e = document.createElement("div");
  e.className = "turn";
  e.setAttribute(KEY_ATTR, `u${String(n)}`);
  e.style.blockSize = "200px";
  messagesEl.appendChild(e);
  return e;
}

const markers = (): HTMLButtonElement[] => [
  ...document.querySelectorAll<HTMLButtonElement>(".turn-rail > .rail-marker"),
];

function markerFor(n: number): HTMLButtonElement {
  const hit = markers().find((b) => b.firstChild?.textContent === String(n));
  if (hit === undefined) {
    throw new Error(`no rail marker for turn ${String(n)}`);
  }
  return hit;
}

/** Wait until the rail has stopped redrawing on its own. */
async function settle(): Promise<void> {
  const root = outer.querySelector(".turn-rail");
  let last = "";
  let stable = 0;
  for (let i = 0; i < 120 && stable < 3; i++) {
    await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
    const now = root?.innerHTML ?? "";
    stable = now === last ? stable + 1 : 0;
    last = now;
  }
}

/** The chat every paint in a test uses. ONE id per test, and that is what makes the staleness
 *  half of this file able to fail: a NEW chat resets the rail, so re-fetching under a fresh id
 *  re-renders whatever the signature says and the guard is never consulted. */
const CHAT = "c-rail-rebuild";

/** Re-fetch the SAME chat's index and let the rail settle. `force`, because the record for an
 *  unchanged chat is otherwise served from cache without a render. */
async function paint(turns: TurnSummary[]): Promise<void> {
  vi.mocked(apiGet).mockResolvedValue({ turns } as never);
  await rail.loadTurnRail(CHAT, { force: true });
  messagesEl.replaceChildren();
  for (const t of turns) {
    card(t.n);
  }
  rail.setResidentTurns([...messagesEl.children] as HTMLElement[]);
  await settle();
}

/** Re-run the rail's render with the SAME inputs, which is what a transcript paint of an
 *  unchanged rail is. */
function repaint(): void {
  rail.setResidentTurns([...messagesEl.children] as HTMLElement[]);
}

const THREE = [summary(1), summary(2), summary(3)];

beforeEach(() => {
  rail.resetTurnRail();
  messagesEl.replaceChildren();
  vi.mocked(apiGet).mockReset();
});

describe("an unchanged rail", () => {
  it("keeps focus on a marker across a repaint", async () => {
    await paint(THREE);
    const marker = markerFor(2);
    marker.focus();
    expect(document.activeElement).toBe(marker);

    repaint();
    repaint();

    expect(document.activeElement, "a repaint must not take focus off a marker").toBe(marker);
    expect(markerFor(2), "and must not replace the element").toBe(marker);
  });

  it("does not restart a marker's animation across a repaint", async () => {
    // Production's is `vk-dot-beat` on `.rail-marker[data-pending]`; the scene declares one on
    // every marker instead, because the property under test is that a rebuild does not knock an
    // animation on a marker back to zero, whichever state carries it.
    await paint(THREE);
    const marker = markerFor(2);
    expect(marker.isConnected, "the probe must hold a live marker").toBe(true);
    const anim = marker.getAnimations()[0];
    expect(anim, "the marker needs an animation to probe").toBeDefined();
    anim!.pause();
    anim!.currentTime = 250;
    expect(Number(marker.getAnimations()[0]?.currentTime)).toBe(250);

    repaint();
    await new Promise((r) => requestAnimationFrame(r));

    expect(markerFor(2)).toBe(marker);
    const after = marker.getAnimations()[0];
    expect(after, "the marker must still carry its animation").toBeDefined();
    expect(Number(after?.currentTime), "a repaint must not restart it").toBe(250);
    expect(after?.playState, "nor replace it with a fresh running one").toBe("paused");
  });
});

describe("a changed rail still redraws", () => {
  it("when a turn is added", async () => {
    await paint(THREE);
    expect(markers()).toHaveLength(3);
    await paint([...THREE, summary(4)]);
    expect(markers()).toHaveLength(4);
  });

  it("when an outcome changes", async () => {
    await paint(THREE);
    expect(markerFor(2).dataset["outcome"]).toBe("completed");
    await paint([summary(1), summary(2, { outcome: "failed" }), summary(3)]);
    expect(markerFor(2).dataset["outcome"]).toBe("failed");
  });

  it("when the reader picks a marker", async () => {
    await paint(THREE);
    const marker = markerFor(3);
    expect(marker.dataset["selected"]).toBeUndefined();
    marker.click();
    expect(markerFor(3).dataset["selected"]).toBe("");
  });

  it("when the agent-initiated trigger changes", async () => {
    await paint(THREE);
    expect(markerFor(2).dataset["trigger"]).toBeUndefined();
    await paint([summary(1), summary(2, { agent_initiated: true }), summary(3)]);
    expect(markerFor(2).dataset["trigger"]).toBe("system");
  });
});
