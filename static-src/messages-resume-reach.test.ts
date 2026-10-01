// ---------------------------------------------------------------------------
// The resume control counts entries the reader can REACH.
//
// `messages.ts entryCount` is a walk over each turn's body asking
// `entryRenders(e, "", firstPlan)` — the renderer's OWN predicate rather than a second
// reading of it. Two populations answer false there, and a count including either
// would promise a distance that does not exist: an entry in another LANE (delegate
// content, which `placeEntry` drops in this view) and the kinds that render at no
// position of their own (`turn_open` as the header, `turn_close` as the footer, a
// `tool_result` on its call's card, a `turn_bind` nowhere). The reader resumes
// expecting N new blocks and lands where they parked.
//
// The label's own word is still "block", which is production's wording and not this
// file's to change.
//
// ONE ORACLE DROPPED OUT LOUD. A describe here asserted that a WORKFLOW STEP's blocks
// are never reachable, over a `wf:<runID>:<node>` subtask id inside the launching
// chat's own message. That is now unrepresentable rather than merely unused: a step's
// entries are appended to the RUN's log (`runs/<workflowId>/entries.jsonl`), never to
// a chat's, so no `wf:` lane exists in the input this walk reads and the dispatcher
// needs no rule to filter one back out. Its surviving half — content this view does
// not draw is not counted — is the delegate case below, over lanes.
//
// The flows are production's own order: park (baseline), the turn's entries land
// through the store's own `openTurn`/`appendEntry`, and each of those bumps a `shape`
// pass, which is where the count recomputes. The label is read through the scroll
// mock's `setResumeLabel`, and the baseline is driven through the reading-state
// callback `initFollowModel` registers — the same path the real scroller drives.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import type { ReadingState } from "./scroll.js";

// messages.ts's graph reads the shared DOM registry at module scope / mount,
// and `byId` throws on a missing element — so the hosts exist before any import
// resolves (the composer pair for the send-state effect the graph wires).
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

// The SHARED scroll mock, not a copy of it. A hand-rolled one here drifts
// silently: messages.ts imports ./turn-rail.js, which imports `scrollableBy` from
// ./scroll.js, and a factory namespace missing that name produces "[vitest] There
// was an error when mocking a module" with no file, no export and no import chain
// — visible only in a FULL-suite run while this file stays green in isolation.
// __test-helpers__/scroll-mock.test.ts guards the helper's totality; it cannot
// guard an inline copy, which is exactly how the copy this replaced went stale.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
import { scrollMock } from "./__test-helpers__/scroll-mock.js";

const store = await import("./store.js");
const messages = await import("./messages.js");

/** The reading-state listener messages.ts registered at mount. Captured once —
 *  `mountChatView` is idempotent, so it registers exactly one. */
let onReading: (s: ReadingState) => void;

messages.mountChatView();
{
  const call = scrollMock.onReadingStateChange.mock.calls[0];
  if (call === undefined) {
    throw new Error("mountChatView registered no reading-state listener");
  }
  onReading = call[0] as (s: ReadingState) => void;
}

function sealed(
  turnID: string,
  at: number,
  kind: Entry["kind"],
  payload: unknown,
  lane?: string,
): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    ...(lane === undefined ? {} : { lane }),
    payload,
  } as Entry;
}

/** One sealed `text` entry, optionally in a DELEGATE's lane. A lane is what replaced
 *  the per-block `agent_subtask_id`: position is intrinsic, so a delegate's entries sit
 *  in the same `seq` space and are told apart by lane alone. */
function text(turnID: string, at: number, body: string, lane?: string): Entry {
  return sealed(turnID, at, "text", { text: body }, lane);
}

/** Chat ids are minted per flow so every mount is a fresh chat switch. */
let chatSeq = 0;

function session(id: string): Session {
  return makeSession({ id, name: id });
}

/** Park the reader on an empty chat (zero baseline), then land ONE turn whose body is
 *  `body` — through the store's own operations, so the entries arrive exactly as a
 *  frame lands them and each append recounts on its own `shape` pass. Returns the
 *  chat id. */
function parkThenLand(body: (turnID: string) => Entry[]): string {
  const chat = `c-${String(++chatSeq)}`;
  const turnID = `t-${String(chatSeq)}`;
  store.setSessions([session(chat)]);
  store.setActive(chat);
  scrollMock.readingState.mockReturnValue("reading");
  onReading("reading"); // baseline: nothing reachable yet
  store.openTurn(
    chat,
    sealed(turnID, 0, "turn_open", {
      prompt: { id: `${turnID}-p`, text: "go" },
      source: "prompt",
      n: 1,
    }),
  );
  for (const e of body(turnID)) {
    store.appendEntry(chat, e);
  }
  return chat;
}

/** The label after the LAST full pass. */
function lastLabel(): string {
  const call = scrollMock.setResumeLabel.mock.calls.at(-1);
  return call === undefined ? "<no label>" : (call[0] as string);
}

beforeEach(() => {
  scrollMock.setResumeLabel.mockClear();
});

describe("the resume label counts only entries the reader can reach", () => {
  it("does NOT count a delegate's entries", () => {
    parkThenLand((t) => [
      text(t, 1, "parent one"),
      text(t, 2, "parent two"),
      text(t, 3, "delegate a", "sa-1"),
      text(t, 4, "delegate b", "sa-1"),
      text(t, 5, "delegate c", "sa-1"),
    ]);
    // The transcript renders none of the three: `placeEntry` drops an entry whose lane
    // is not the view's root, in any fold state — a delegate's card carries no body at
    // all now, and its content is its own page's. So the reader's distance is the two
    // parent entries, against the five a naive body count would promise.
    expect(lastLabel()).toBe("2 new blocks");
  });

  it("counts parent-lane entries whatever else the turn carries", () => {
    parkThenLand((t) => [text(t, 1, "only parent"), text(t, 2, "delegate", "sa-1")]);
    // Also the singular, which is a different branch of the label.
    expect(lastLabel()).toBe("1 new block");
  });

  // The OTHER population `entryRenders` answers false for, which the lane cases cannot
  // reach: a kind that renders at no position of its own. A settled tool call is ONE
  // reachable entry — the card — and its `tool_result` folds onto that same card by id,
  // so counting the pair would promise a row that does not exist. `turn_open` and
  // `turn_close` are in the same population and are asserted here by their absence from
  // the total: the turn carries both and neither is counted.
  it("counts a settled tool call once, not once per entry", () => {
    parkThenLand((t) => [
      text(t, 1, "prose"),
      sealed(t, 2, "tool_call", {
        id: `${t}-tc`,
        title: "Run Command",
        kind: "execute",
        status: "completed",
        ts: 2,
      }),
      sealed(t, 3, "tool_result", { id: `${t}-tc:result`, status: "completed", output: "ok" }),
      sealed(t, 4, "turn_close", { outcome: "completed" }),
    ]);
    expect(lastLabel()).toBe("2 new blocks");
  });
});
