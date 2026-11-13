// A reload paints the boot snapshot, then the server's page lands. The reader opens on the newest
// turn's end, so neither the snapshot's first frames nor the landing may move what is on screen:
// layout shift (https://w3c.github.io/layout-instability/) under 0.01 outside the cover. Real
// store, renderer, scroll controller, cover and the whole app sheet; the snapshot's three reads of
// `tabs.js` are stubbed, since this page has no tab strip.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { page } from "vitest/browser";
import type { Session, TabSubject, TurnState } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Entry } from "./wire/types.gen.js";
import type * as Tabs from "./tabs.js";

const m = vi.hoisted(() => ({
  openTabSubjects: vi.fn((): TabSubject[] => []),
  paintProvisionalTabs: vi.fn(),
  tabSetVersion: vi.fn(() => 0),
}));

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  openTabSubjects: m.openTabSubjects,
  paintProvisionalTabs: m.paintProvisionalTabs,
  tabSetVersion: m.tabSetVersion,
}));

const area = document.createElement("div");
area.id = "chat-area";
area.innerHTML =
  '<div id="chat-view" data-tab-view>' +
  '<div id="messages-wrap-outer"><div id="messages-wrap"><div id="messages"></div></div></div>' +
  '<button id="scroll-bottom" type="button"><span></span></button>' +
  '<button id="send-btn" type="button"></button><textarea id="prompt-input"></textarea>' +
  "</div>";
document.body.appendChild(area);

const store = await import("./store.js");
const messages = await import("./messages.js");
const cover = await import("./transcript-cover.js");
const snapshot = await import("./boot-snapshot.js");

messages.mountChatView();

let seq = 0;

/** Inside and past the cover's 250 ms show window. */
const LANDINGS_MS = [100, 400];
/** Long enough for a cover's lift and the near observer's follow-up frames. */
const SETTLE_MS = 800;

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

function addTurn(s: Session, t: string, entries: Entry[]): void {
  s.turns.set(t, { entries, openEntries: new Map(), closeAt: entries.length - 1 });
  s.turn_order.push(t);
  s.turn_count = s.turn_order.length;
}

function proseTurn(s: Session, n: number): void {
  const t = `${s.id}-t${String(n)}`;
  const text = Array.from(
    { length: 5 },
    (_, i) => `paragraph ${String(i)} of a reply that runs on for a while in plain prose`,
  ).join("\n\n");
  addTurn(s, t, [
    sealed(t, 0, "turn_open", { prompt: { id: `${t}-p`, text: "go on" }, source: "prompt", n }),
    sealed(t, 1, "text", { text }),
    sealed(t, 2, "turn_close", { outcome: "completed" }),
  ]);
}

/** A turn of `groups` tool groups between reply paragraphs. */
function toolTurn(s: Session, n: number, groups: number): void {
  const t = `${s.id}-t${String(n)}`;
  const entries: Entry[] = [
    sealed(t, 0, "turn_open", { prompt: { id: `${t}-p`, text: "fix it" }, source: "prompt", n }),
  ];
  let at = 1;
  for (let g = 0; g < groups; g++) {
    entries.push(sealed(t, at++, "text", { text: `step ${String(g)}: looking at the next file` }));
    for (let c = 0; c <= g % 3; c++) {
      const id = `${t}-c${String(g)}-${String(c)}`;
      entries.push({
        ...sealed(t, at++, "tool_call", {
          id,
          title: `run check ${String(g)}.${String(c)}`,
          kind: "execute",
          status: "in_progress",
          input: { command: `make check-${String(g)}-${String(c)}` },
          ts: at,
        }),
        id,
      });
      entries.push({
        ...sealed(t, at++, "tool_result", { status: "completed" }),
        id: `${id}:result`,
      });
    }
  }
  entries.push(sealed(t, at, "turn_close", { outcome: "completed" }));
  addTurn(s, t, entries);
}

function chatTab(ref: string): TabSubject {
  return { id: `tab-${ref}`, kind: "chat", ref, parent: "", pinned: false, owns: true };
}

async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  }
}

function cloneTurns(src: Session): { turns: Map<string, TurnState>; order: string[] } {
  const turns = new Map<string, TurnState>();
  for (const [id, t] of src.turns) {
    turns.set(id, { ...t, entries: t.entries.map((e) => ({ ...e })), openEntries: new Map() });
  }
  return { turns, order: [...src.turn_order] };
}

function coverUp(): boolean {
  const c = document.getElementById("transcript-cover");
  return c !== null && !c.hidden && c.dataset["state"] !== "released";
}

/** The transcript boxes that start inside the scrollport and are not clipped away in a collapsed
 *  region; none while the cover hides the transcript. */
function visibleBoxes(): Map<Element, number> {
  const port = (document.getElementById("messages-wrap") as HTMLElement).getBoundingClientRect();
  const out = new Map<Element, number>();
  if (coverUp()) {
    return out;
  }
  const view = messages.activeTranscriptView();
  for (const b of view?.querySelectorAll(
    ".turn-header, .turn-footer, .tool-group, .tool-call, .msg-row",
  ) ?? []) {
    if (b.closest("[inert]") !== null) {
      continue;
    }
    const r = b.getBoundingClientRect();
    if (r.height > 0 && r.top >= port.top && r.top < port.bottom) {
      out.set(b, r.top);
    }
  }
  return out;
}

interface ReloadRun {
  readonly shift: number;
  readonly shown: number;
}

/**
 * The screen showed `server` (captured into the snapshot); the page reloads, paints the snapshot
 * through the activation's cover rule, and the server's page lands `landAfterMs` later.
 */
async function reload(server: Session, landAfterMs: number): Promise<ReloadRun> {
  store.setSessions([server]);
  store.setActive(server.id);
  m.openTabSubjects.mockReturnValue([chatTab(server.id)]);
  const snap = snapshot.captureBootSnapshot();

  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
  await frames(2);

  let shift = 0;
  const coverLog: [number, boolean][] = [[performance.now(), coverUp()]];
  const coveredAt = (t: number): boolean => {
    let up = false;
    for (const [at, state] of coverLog) {
      if (at <= t) {
        up = state;
      }
    }
    return up;
  };
  const obs = new PerformanceObserver((list) => {
    for (const e of list.getEntries() as (PerformanceEntry & {
      value: number;
      hadRecentInput: boolean;
    })[]) {
      // A shift under the cover is one the reader does not see.
      if (!e.hadRecentInput && !coveredAt(e.startTime)) {
        shift += e.value;
      }
    }
  });
  obs.observe({ type: "layout-shift" });

  expect(snapshot.paintBootSnapshot(snap), "the premise: the snapshot paints").toBe(true);
  if (!cover.transcriptPainted(server.id)) {
    cover.holdCover(server.id, 0);
  }
  store.setActive(server.id);
  store.setPainted(server.id);
  store.bumpMessages(server.id, "load");

  const sample = (): void => {
    const up = coverUp();
    if (up !== coverLog[coverLog.length - 1]?.[1]) {
      coverLog.push([performance.now(), up]);
    }
  };
  const painted = performance.now();
  while (performance.now() - painted < landAfterMs) {
    await frames(1);
    sample();
  }

  const s = store.get(server.id);
  if (s === undefined) {
    throw new Error("the snapshot left no chat row");
  }
  const answer = cloneTurns(server);
  s.turns = answer.turns;
  s.turn_order = answer.order;
  s.turn_count = server.turn_count;
  s.has_more = false;
  s.residency = "loaded";
  store.bumpMessages(s.id, "load");
  const landed = performance.now();
  while (performance.now() - landed < SETTLE_MS) {
    await frames(1);
    sample();
  }
  // The shift entries of the last frames arrive after them.
  await new Promise((r) => setTimeout(r, 0));
  obs.disconnect();
  return { shift, shown: visibleBoxes().size };
}

let sheet: HTMLStyleElement;

beforeAll(async () => {
  sheet = mountAppCSS();
  area.style.cssText = "position:fixed;inset:0";
  await page.viewport(1440, 900);
  document.documentElement.setAttribute("data-pointer", "fine");
});

afterAll(async () => {
  sheet.remove();
  area.remove();
  document.documentElement.removeAttribute("data-pointer");
  await page.viewport(1280, 720);
});

beforeEach(() => {
  cover.dropCover();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
});

afterEach(() => {
  cover.dropCover();
  document.getElementById("transcript-cover")?.remove();
  snapshot._resetForTest();
});

describe("a reload from the boot snapshot moves nothing on screen", () => {
  for (const landAfter of LANDINGS_MS) {
    it(`when the newest turn is over the snapshot's budget, the page landing at ${String(landAfter)} ms`, async () => {
      const s = makeSession({ id: `rl-${String(++seq)}`, name: "big" });
      proseTurn(s, 1);
      toolTurn(s, 2, 40);
      expect(
        s.turns.get(s.turn_order[1] ?? "")?.entries.length,
        "the premise: one turn over the record's 80-entry cap",
      ).toBeGreaterThan(80);

      const run = await reload(s, landAfter);

      expect(run.shown, "the premise: the reload ends on visible boxes").toBeGreaterThan(3);
      expect(run.shift, "layout shift across the reload").toBeLessThan(0.01);
    });

    it(`when the newest turns fit, the page landing at ${String(landAfter)} ms`, async () => {
      const s = makeSession({ id: `rl-${String(++seq)}`, name: "fits" });
      proseTurn(s, 1);
      proseTurn(s, 2);
      toolTurn(s, 3, 10);
      expect(
        s.turns.get(s.turn_order[2] ?? "")?.entries.length,
        "the premise: the newest turn fits the record",
      ).toBeLessThan(80);

      const run = await reload(s, landAfter);

      expect(run.shown, "the premise: the reload ends on visible boxes").toBeGreaterThan(3);
      expect(run.shift, "layout shift across the reload").toBeLessThan(0.01);
    });
  }
});
