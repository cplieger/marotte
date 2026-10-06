// A DELEGATE THAT DIED RENDERS LIKE ONE THAT WORKS unless its chat's OWN turn liveness is
// read: `store.ts`'s `subagentStatusFor` folds an `in_progress` invocation onto `aborted`
// once the chat holds no live turn. `delegate_dot.json`'s `stale` row states what each
// surface then says; this file adds the two DIRECTIONS (stale folds, live does not) and
// the reconcile: a `busy_chats` frame that clears a chat's latch settles its delegate
// cards in the SAME pass, because a page load replays no streamed frame. Browser
// placement with every production import DYNAMIC: the dispatcher's graph reaches
// `scroll.ts`, which builds itself against a real `#messages` at import.

import { describe, it, expect, beforeAll, beforeEach } from "vitest";
import type * as ModBlocks from "./messages-blocks.js";
import type { Turn } from "./turns.js";
import type { ChatHeader, EntryToolCall, ToolStatus } from "./wire/types.gen.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  if (document.getElementById(id) === null) {
    const host = document.createElement(id === "scroll-bottom" ? "button" : "div");
    host.id = id;
    document.body.appendChild(host);
  }
}

const CHAT_ID = "c-stale";
const SUBTASK = "sa-stale";

let delegateStatusFor: (status: ToolStatus, turnLive: boolean) => ToolStatus;
let subagentStatusFor: (status: ToolStatus | undefined, turnLive?: boolean) => string;
let buildAssistantBody: (bodyEl: HTMLElement, turn: Turn, chatID: string, live: boolean) => void;
let resetBlockRenders: () => void;
// The callback record is `messages-blocks.ts`'s own `BlockCbs`, which that module does not
// export; reading the installer's type off the module keeps this in step with it rather than
// restating five members a change there would leave stale.
let initBlockRenderer: typeof ModBlocks.initBlockRenderer;
let upsertHeader: (h: ChatHeader) => void;
let setTurnOpen: (id: string, open: boolean) => void;
let setActive: (id: string) => void;

beforeAll(async () => {
  ({ delegateStatusFor, subagentStatusFor, upsertHeader, setTurnOpen, setActive } =
    await import("./store.js"));
  ({ buildAssistantBody, resetBlockRenders, initBlockRenderer } =
    await import("./messages-blocks.js"));
  initBlockRenderer({
    pushStreamingEffect: () => {
      /* messages.ts owns the effect registry; no case here reads it */
    },
    pushEntryEffect: () => {
      /* same */
    },
    disposeEntryEffects: () => {
      /* same */
    },
    makeRow: () => {
      const row = document.createElement("div");
      row.className = "msg-row";
      return row;
    },
    makeEvent: () => document.createElement("div"),
  });
  setActive(CHAT_ID);
});

/** The invocation call that dispatched the delegate, at `status`. */
function invocation(status: ToolStatus): EntryToolCall {
  return {
    id: "tc-inv",
    title: "Sub-agent: context-gatherer",
    kind: "other",
    status,
    agent_subtask_id: SUBTASK,
    ts: 1,
  };
}

/** One turn holding that invocation in the transcript's own lane. */
function turnWith(status: ToolStatus): Turn {
  const turnID = "t-stale";
  return {
    id: turnID,
    n: 1,
    trigger: { id: `${turnID}-p`, text: "go" },
    body: [
      {
        id: `${turnID}-e1`,
        turn: turnID,
        kind: "tool_call",
        seq: 1,
        ts: 1,
        payload: invocation(status),
      },
    ],
    openEntries: new Map(),
    ts: 1,
    outcome: "running",
    rewindTo: undefined,
  };
}

/** Seed the chat, stating its own turn liveness the way the server's `live` field does. */
function seedChat(turnLive: boolean): void {
  upsertHeader({
    id: CHAT_ID,
    name: CHAT_ID,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turn_count: 1,
    created_at: 0,
    updated_at: 0,
  });
  setTurnOpen(CHAT_ID, turnLive);
}

/** Mount the turn and hand back the delegate's card. */
function mount(status: ToolStatus): HTMLElement {
  const body = document.createElement("div");
  document.getElementById("messages")?.appendChild(body);
  buildAssistantBody(body, turnWith(status), CHAT_ID, true);
  const card = body.querySelector<HTMLElement>(".subagent-block");
  if (card === null) {
    throw new Error("the dispatcher seated no delegate card for the invocation");
  }
  return card;
}

/** The word the card announces, off whichever channel its head owns. A card the dispatcher
 *  seats has an OPENER, so its head is the anchor that opens the delegate's page and names
 *  itself `<delegate>, <word>`; a head with no opener carries an `.sr-only` span instead
 *  (`subagent-block.ts` `refreshName` — one owner, chosen by which head was built). */
function wordOf(card: HTMLElement): string {
  const head = card.querySelector<HTMLElement>(".subagent-header");
  const label = head?.getAttribute("aria-label");
  if (label !== null && label !== undefined) {
    return label.slice(label.lastIndexOf(", ") + 2);
  }
  return head?.querySelector<HTMLElement>(".sr-only")?.textContent ?? "";
}

function iconOf(card: HTMLElement): HTMLElement {
  const icon = card.querySelector<HTMLElement>(".subagent-icon");
  if (icon === null) {
    throw new Error("the card carries no .subagent-icon");
  }
  return icon;
}

describe("a delegate whose chat holds no live turn", () => {
  beforeEach(() => {
    resetBlockRenders();
    document.getElementById("messages")?.replaceChildren();
  });

  it("paints the STOPPED treatment for an in_progress invocation", () => {
    seedChat(false);
    const card = mount("in_progress");
    // The fold's own answer, then the two surfaces that read it. `warn` rather than `fail`:
    // the work was not broken, it was never accounted for.
    expect(delegateStatusFor("in_progress", false)).toBe("aborted");
    expect(subagentStatusFor("in_progress", false)).toBe("done");
    expect(wordOf(card)).toBe("cancelled");
    expect(iconOf(card).classList.contains("is-warn")).toBe(true);
    expect(iconOf(card).classList.contains("subagent-spinner")).toBe(false);
  });

  it("still paints RUNNING while the chat's own turn is live", () => {
    // The guard against over-clearing: the fold may only fire on a chat the server does not
    // report busy, or every working delegate reads as stopped.
    seedChat(true);
    const card = mount("in_progress");
    expect(delegateStatusFor("in_progress", true)).toBe("in_progress");
    expect(subagentStatusFor("in_progress", true)).toBe("working");
    expect(wordOf(card)).toBe("running");
    expect(iconOf(card).classList.contains("subagent-spinner")).toBe(true);
  });

  it("settles its cards in the SAME pass as the reconcile that clears the latch", () => {
    seedChat(true);
    const card = mount("in_progress");
    expect(wordOf(card)).toBe("running");
    const icon = iconOf(card);
    let paints = 0;
    const seen = new MutationObserver((records) => {
      paints += records.filter((r) => r.type === "childList").length;
    });
    seen.observe(icon, { childList: true });
    // What a `busy_chats` frame that omits this chat does to the store: the server's own
    // statement that the chat holds no turn of its own.
    setTurnOpen(CHAT_ID, false);
    seen.takeRecords().forEach(() => {
      paints += 1;
    });
    seen.disconnect();
    expect(wordOf(card)).toBe("cancelled");
    expect(icon.classList.contains("is-warn")).toBe(true);
    // ONE paint: the card's own effect reads the liveness, so nothing re-renders the turn
    // and nothing paints twice.
    expect(paints).toBe(1);
  });
});
