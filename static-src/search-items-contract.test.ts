// The client half of the search item oracle (internal/chat/testdata/search_items.json; the server half is
// TestSearchItemsFixture, through searchItems). Read off the `data-find-unit` stamps the production dispatcher
// writes, in the transcript and on each delegate's page, and each unit's text as the find engine matches it, so a
// unit the renderer draws and the server does not count, or text the two read differently, fails here.

import { describe, it, expect, afterEach } from "vitest";
import fixtureRaw from "../internal/chat/testdata/search_items.json?raw";
import { FindEngine } from "./find-engine.js";
import { FIND_UNIT_SELECTOR } from "./find-unit.js";
import type { Turn } from "./turns.js";
import type { TurnState } from "./types.js";
import type { Entry, EntryKind, ItemKind } from "./wire/types.gen.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}

const { buildAssistantBody, buildDetachedBody, resetBlockRenders, initBlockRenderer } =
  await import("./messages-blocks.js");
const { markWakingRows } = await import("./turn-wake.js");
const { setActive } = await import("./store.js");
const { setHookStatusShown } = await import("./hook-fold.js");
const { buildEvent } = await import("./messages-events.js");

const CHAT_ID = "c-search-items";
setActive(CHAT_ID);

initBlockRenderer({
  pushEntryEffect: (): void => {
    /* noop */
  },
  disposeEntryEffects: (): void => {
    /* noop */
  },
  makeRow: () => {
    const row = document.createElement("div");
    row.className = "msg-row";
    return row;
  },
  makeEvent: buildEvent,
});

interface ItemsCase {
  name: string;
  hooks_hidden?: boolean;
  body: { id: string; kind: EntryKind; lane?: string; payload: unknown }[];
  later_wake?: unknown;
  items: { kind: ItemKind; lane: string; anchor: string; entries: string[]; texts: string[] }[];
}

/** What the turn card draws outside the body this test renders. */
const CARD_KINDS: ReadonlySet<ItemKind> = new Set(["header", "turn_failure"]);

/** Every case's turn id, the fixture's: a minted say is `<turn>:e<k>`. */
const TURN_ID = "t-1";

function loadCases(): ItemsCase[] {
  return (JSON.parse(fixtureRaw) as { cases: ItemsCase[] }).cases;
}

let hosts: HTMLElement[] = [];

afterEach(() => {
  resetBlockRenders();
  setHookStatusShown(true);
  for (const host of hosts) {
    host.remove();
  }
  hosts = [];
});

function mountHost(): HTMLElement {
  const host = document.createElement("div");
  host.className = "turn-body";
  document.body.appendChild(host);
  hosts.push(host);
  return host;
}

function caseTurn(c: ItemsCase): Turn {
  const open: Entry = { id: TURN_ID, turn: TURN_ID, kind: "turn_open", seq: 0, ts: 1, payload: {} };
  const body: Entry[] = c.body.map((e, i) => ({
    id: e.id,
    turn: TURN_ID,
    lane: e.lane ?? "",
    kind: e.kind,
    seq: i + 1,
    ts: i + 2,
    payload: e.payload,
  }));
  const held: TurnState[] = [{ entries: [open, ...body], openEntries: new Map() }];
  if (c.later_wake !== undefined) {
    held.push({
      entries: [
        { id: "t-2", turn: "t-2", kind: "turn_open", seq: 0, ts: 1, payload: {} },
        { id: "t-2:wake", turn: "t-2", kind: "turn_wake", seq: 1, ts: 2, payload: c.later_wake },
      ],
      openEntries: new Map(),
    });
  }
  markWakingRows(held);
  return {
    id: TURN_ID,
    n: 1,
    trigger: { id: "m-1", text: "go" },
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
}

/** Open what a landing opens around a hit (search-reveal.ts): every collapsed tool group, every closed `<details>`
 *  and every tool card's disclosure. */
function openContainers(host: HTMLElement): void {
  for (const group of host.querySelectorAll(".tool-group-collapsed")) {
    group.querySelector<HTMLElement>(":scope > .tool-group-header")?.click();
  }
  for (const d of host.querySelectorAll("details")) {
    d.open = true;
  }
  for (const toggle of host.querySelectorAll<HTMLElement>(
    '.tool-disclosure[aria-expanded="false"]',
  )) {
    toggle.click();
  }
}

/** One stamped unit as the fixture lists it; members only where the element carries them (a prose run). */
function unitsIn(host: HTMLElement, lane: string): ItemsCase["items"] {
  openContainers(host);
  const texts = new FindEngine(host).unitTexts({ selector: FIND_UNIT_SELECTOR });
  return [...host.querySelectorAll<HTMLElement>(FIND_UNIT_SELECTOR)].map((el) => {
    const anchor = el.dataset["entryId"] ?? "";
    const members = el.dataset["entries"];
    return {
      kind: el.dataset["findUnit"] as ItemKind,
      lane,
      anchor,
      entries: members === undefined ? [anchor] : members.split(" "),
      // A folded hook's card is hidden behind its hooks badge, whose list names the hook instead of drawing the card:
      // a landing there shows a notice (find-in-chat.ts HOOK_BADGE_NOTICE), so no text of it is compared.
      texts: el.hasAttribute("data-hook-folded") ? BADGE_TEXT : (texts.get(el) ?? []),
    };
  });
}

/** Stands in for a folded hook's text on both sides. */
const BADGE_TEXT = ["(hooks badge)"];

/** The fixture's body items, a tool card's and the plan card's folded members reduced to the anchor, and each item
 *  the client folds behind a hooks badge given the stand-in text. */
function wanted(c: ItemsCase, got: ItemsCase["items"]): ItemsCase["items"] {
  return c.items
    .filter((it) => !CARD_KINDS.has(it.kind))
    .map((it) => (it.kind === "message" ? it : { ...it, entries: [it.anchor] }))
    .map((it, i) => (got[i]?.texts === BADGE_TEXT ? { ...it, texts: BADGE_TEXT } : it));
}

function rendered(c: ItemsCase): ItemsCase["items"] {
  setHookStatusShown(c.hooks_hidden !== true);
  const turn = caseTurn(c);
  const chat = mountHost();
  buildAssistantBody(chat, turn, CHAT_ID, false);
  const out = unitsIn(chat, "");
  const lanes = new Set(turn.body.map((e) => e.lane ?? "").filter((l) => l !== ""));
  for (const lane of lanes) {
    const page = mountHost();
    buildDetachedBody(page, turn, CHAT_ID, lane, false);
    out.push(...unitsIn(page, lane));
  }
  return out;
}

describe("search items shared with the Go implementation", () => {
  it("reads every case of the shared fixture", () => {
    expect(loadCases().length).toBeGreaterThanOrEqual(12);
  });

  for (const c of loadCases()) {
    it(c.name, () => {
      const got = rendered(c);
      expect(got).toEqual(wanted(c, got));
    });
  }
});
