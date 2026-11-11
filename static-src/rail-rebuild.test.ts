import { describe, it, expect, vi, beforeEach } from "vitest";
import { KEY_ATTR } from "@cplieger/ui";
import type { TurnSummary } from "./rail-merge.js";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetOrError: vi.fn(),
}));
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

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
// Browser Mode serves no CSS, so the scene declares the heights the stylesheet would give the
// track binning measures and the stack inside it.
const style = document.createElement("style");
style.textContent =
  ".turn-map-track{display:block;block-size:400px}.turn-map-stack{display:block;position:relative;block-size:400px}";
document.head.appendChild(style);

const rail = await import("./turn-rail.js");
const scroll = await import("./scroll.js");
const { apiGet } = await import("./api-client.js");

rail.mountTurnRail(outer);

const MINUTE = 60_000;

function summary(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return { id: `u${String(n)}`, n, outcome: "completed", ts: n * MINUTE, ...over };
}

function card(n: number): void {
  const e = document.createElement("div");
  e.className = "turn";
  e.setAttribute(KEY_ATTR, `u${String(n)}`);
  e.style.blockSize = "200px";
  messagesEl.appendChild(e);
}

const links = (): HTMLAnchorElement[] => [
  ...document.querySelectorAll<HTMLAnchorElement>(".turn-map .turn-pill-link"),
];

function linkFor(n: number): HTMLAnchorElement {
  const hit = links().find((a) => a.getAttribute("href")?.endsWith(`#turn-${String(n)}`));
  if (hit === undefined) {
    throw new Error(`no turn-map row for turn ${String(n)}`);
  }
  return hit;
}

function rowFor(n: number): HTMLElement {
  const li = linkFor(n).parentElement;
  if (li === null) {
    throw new Error("a link outside its row");
  }
  return li;
}

async function settle(): Promise<void> {
  const root = outer.querySelector(".turn-map");
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
 *  half of this file able to fail: a NEW chat resets the map, so re-fetching under a fresh id
 *  re-renders whatever the signature says and the guard is never consulted. */
const CHAT = "c-rail-rebuild";

/** Re-fetch the SAME chat's index and let the map settle. `force`, because the record for an
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

/** Re-run the map's render with the SAME inputs, which is what a transcript paint of an
 *  unchanged map is. */
function repaint(): void {
  rail.setResidentTurns([...messagesEl.children] as HTMLElement[]);
}

const THREE = [summary(1), summary(2), summary(3)];

beforeEach(() => {
  // A case's own scroll is the reader's, so it leaves them Reading; each case starts at the live edge.
  scroll.resetScrollState();
  rail.resetTurnRail();
  messagesEl.replaceChildren();
  vi.mocked(apiGet).mockReset();
});

describe("an unchanged map", () => {
  it("keeps focus on a row across a repaint", async () => {
    await paint(THREE);
    const link = linkFor(2);
    link.focus();
    expect(document.activeElement).toBe(link);

    repaint();
    repaint();

    expect(document.activeElement, "a repaint must not take focus off a row").toBe(link);
    expect(linkFor(2), "and must not replace the element").toBe(link);
  });

  it("performs no DOM write on a repaint", async () => {
    await paint(THREE);
    const root = outer.querySelector(".turn-map");
    if (root === null) {
      throw new Error("no turn map");
    }
    const seen = new MutationObserver(() => undefined);
    seen.observe(root, { attributes: true, childList: true, subtree: true, characterData: true });

    repaint();
    repaint();

    expect(seen.takeRecords()).toEqual([]);
    seen.disconnect();
  });
});

describe("a map whose only change is the band of turns on screen", () => {
  it("moves the band in place and keeps a keyboard reader's focus", async () => {
    await paint(THREE);
    // The paint pins the live edge, so the last two cards are on screen.
    expect(rowFor(1).dataset["inView"]).toBeUndefined();
    expect(rowFor(3).dataset["inView"]).toBe("");
    const link = linkFor(2);
    link.focus();

    wrap.scrollTop = 0;
    wrap.dispatchEvent(new Event("scroll"));
    await settle();

    expect(rowFor(1).dataset["inView"], "the band reached turn 1").toBe("");
    expect(rowFor(3).dataset["inView"], "and left turn 3").toBeUndefined();
    expect(document.activeElement, "a band move must not take focus off a row").toBe(link);
    expect(linkFor(2)).toBe(link);
  });
});

describe("a changed map still redraws", () => {
  it("when a turn is added", async () => {
    await paint(THREE);
    expect(links()).toHaveLength(3);
    await paint([...THREE, summary(4)]);
    expect(links()).toHaveLength(4);
  });

  it("when an outcome changes", async () => {
    await paint(THREE);
    expect(rowFor(2).dataset["severity"]).toBe("clean");
    await paint([summary(1), summary(2, { outcome: "failed" }), summary(3)]);
    expect(rowFor(2).dataset["severity"]).toBe("broken");
  });

  it("when the reader picks a row", async () => {
    await paint(THREE);
    // The live edge marks turn 3, so the pick is turn 1.
    expect(rowFor(1).dataset["current"]).toBeUndefined();
    linkFor(1).click();
    expect(rowFor(1).dataset["current"]).toBe("");
    expect(linkFor(1).getAttribute("aria-current")).toBe("location");
    expect(rowFor(3).dataset["current"]).toBeUndefined();
  });

  it("when the agent-initiated trigger changes", async () => {
    await paint(THREE);
    expect(rowFor(2).dataset["trigger"]).toBeUndefined();
    await paint([summary(1), summary(2, { agent_initiated: true }), summary(3)]);
    expect(rowFor(2).dataset["trigger"]).toBe("system");
  });
});

describe("a re-point to another chat", () => {
  const FIVE = [summary(1), summary(2), summary(3), summary(4), summary(5)];

  /** Record, for every row the repaint places, whether the stack was in the document then. */
  function watchInserts(): { connected: boolean[]; stop: () => void } {
    const stack = outer.querySelector<HTMLElement>(".turn-map-stack");
    if (stack === null) {
      throw new Error("no turn-map stack");
    }
    const connected: boolean[] = [];
    const insert = stack.insertBefore.bind(stack);
    stack.insertBefore = <T extends Node>(node: T, child: Node | null): T => {
      connected.push(stack.isConnected);
      return insert(node, child);
    };
    return {
      connected,
      stop: () => {
        Reflect.deleteProperty(stack, "insertBefore");
      },
    };
  }

  async function paintOther(turns: TurnSummary[]): Promise<void> {
    vi.mocked(apiGet).mockResolvedValue({ turns } as never);
    await rail.loadTurnRail("c-rail-other", { force: true });
    messagesEl.replaceChildren();
    for (const t of turns) {
      card(t.n);
    }
    rail.setResidentTurns([...messagesEl.children] as HTMLElement[]);
    await settle();
  }

  it("swaps the rows with the stack out of the document", async () => {
    await paint(THREE);
    const seen = watchInserts();

    await paintOther(FIVE);
    seen.stop();

    expect(links()).toHaveLength(5);
    expect(seen.connected.length, "the new chat's rows were placed").toBeGreaterThan(0);
    expect(
      seen.connected.every((c) => !c),
      "every row placed off the document",
    ).toBe(true);
    expect(outer.querySelector(".turn-map-track > .turn-map-stack")).not.toBeNull();
  });

  it("keeps a keyboard reader's focus on a row the new chat keeps", async () => {
    await paint(THREE);
    const link = linkFor(2);
    link.focus();
    const seen = watchInserts();

    await paintOther(FIVE);
    seen.stop();

    expect(document.activeElement, "a re-point must not take focus off a row").toBe(link);
    expect(
      seen.connected.every((c) => c),
      "the focused stack stays in place",
    ).toBe(true);
  });
});
