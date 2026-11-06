// The client half of the search item oracle (internal/chat/testdata/search_items.json; the server half is
// TestSearchItemsFixture, through searchItems). Read off the `data-find-unit` stamps the production dispatcher
// writes, in the transcript and on each delegate's page, so a unit the renderer draws and the server does not
// count fails here.

import { describe, it, expect, afterEach } from "vitest";
import fixtureRaw from "../internal/chat/testdata/search_items.json?raw";
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
  makeEvent: (e: Entry) => {
    const row = document.createElement("div");
    row.className = `event event-${e.kind}`;
    return row;
  },
});

interface ItemsCase {
  name: string;
  hooks_hidden?: boolean;
  body: { id: string; kind: EntryKind; lane?: string; payload: unknown }[];
  later_wake?: unknown;
  items: { kind: ItemKind; lane: string; anchor: string; entries: string[] }[];
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

/** One stamped unit as the fixture lists it; members only where the element carries them (a prose run). */
function unitsIn(host: HTMLElement, lane: string): ItemsCase["items"] {
  return [...host.querySelectorAll<HTMLElement>(FIND_UNIT_SELECTOR)].map((el) => {
    const anchor = el.dataset["entryId"] ?? "";
    const members = el.dataset["entries"];
    return {
      kind: el.dataset["findUnit"] as ItemKind,
      lane,
      anchor,
      entries: members === undefined ? [anchor] : members.split(" "),
    };
  });
}

/** The fixture's body items, a tool card's and the plan card's folded members reduced to the anchor. */
function wanted(c: ItemsCase): ItemsCase["items"] {
  return c.items
    .filter((it) => !CARD_KINDS.has(it.kind))
    .map((it) => (it.kind === "message" ? it : { ...it, entries: [it.anchor] }));
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
      expect(rendered(c)).toEqual(wanted(c));
    });
  }
});
