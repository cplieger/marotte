// The subagent page: the eviction exemption its tab earns, the demand that ends its lifetime, and
// navigation between stages. Release properties are asked of `messages-blocks.ts`'s render
// registry: a render left registered outlives its DOM.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { signal, touch } from "@cplieger/reactive";
import type { Session } from "./types.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";
import { makeToolCall } from "./__test-helpers__/model.js";

// tabs.ts's real graph reads the shared DOM registry at module scope, and
// `byId` throws on a missing element — so the hosts exist before any import.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
  "tab-strip",
  // The page's own host: `paint` bails silently without it, which would make the
  // repaint case below pass on an empty document.
  "subagent-body",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

// A spy mock rather than a factory: the graph behind subagent-view imports a
// wide slice of tabs.ts's surface, and a hand-kept name list here would rot.
// The spy keeps every export real and lets the cases below steer `hasTab`.
vi.mock("./tabs.js", { spy: true });
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// Spied so the paint effect's re-runs are COUNTABLE. Every export stays real, which
// `blockShape` and `shapeExtends` need — they run on every body render.
vi.mock("./subagent-slice.js", { spy: true });
// The delegation target. Spied rather than replaced, so the module's other exports
// stay real for the graph behind subagent-view; the one case that drives it stubs
// the implementation, because the real refresh reaches the loader.
vi.mock("./chat.js", { spy: true });
const store = await import("./store.js");
const { hasTab, openSubagentRefs } = await import("./tabs.js");
const { subagentRef } = await import("./tab-materialize.js");
const { sliceSubagentGroup } = await import("./subagent-slice.js");
const { openSubagentTab } = await import("./tabs.js");
const { subagentTabProjectsChat, showSubagent, refreshSubagent, openSubagentView } =
  await import("./subagent-view.js");
const { refreshChatView } = await import("./chat.js");
const { clearAllEntrySigs } = await import("./store-signals.js");
const { mountedWindow } = await import("./messages-blocks.js");
const mockHasTab = vi.mocked(hasTab);

/** `messages-blocks.ts`'s unexported `renderIDFor`, mirrored: the registry is the only observable
 *  of a release. */
function detachedRenderID(turnID: string, lane: string): string {
  return `${turnID}#${lane}`;
}

/** Whether the page still holds a mounted render for one member's lane. */
function memberIsMounted(turnID: string, lane: string): boolean {
  return mountedWindow(detachedRenderID(turnID, lane)) !== undefined;
}

// The page's DEMAND input: a controllable fake of `openSubagentRefs`, TRACKED like production's,
// since the real reader answers `[]` here.
let refs: readonly string[] = [];
const refsVersion = signal(0);

/** State the open subagent tabs. Bumps a version the fake reads, which is the whole
 *  subscription: production's reader subscribes to the tab projection's own
 *  `stateVersion` the same way. */
function setSubagentTabs(open: readonly string[]): void {
  refs = open;
  refsVersion.value += 1;
}

/** Open ONE subagent tab and activate it, in production's order: the row lands in the
 *  projection, then the activation reaches `showSubagent` through the tab's `onShow`. */
function show(chatID: string, subtaskID: string): void {
  setSubagentTabs([subagentRef(chatID, subtaskID)]);
  showSubagent(chatID, subtaskID);
}

/** Withdraw the demand entirely: the reader closed every subagent tab. */
function closeSubagentTabs(): void {
  setSubagentTabs([]);
}

// --- The entry-log fixture ---

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: chatID,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: store.defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** The `turn_open` of `turnID`, which is always `seq` 0. Reader-prompted, so the turn
 *  draws. */
function turnOpen(turnID: string): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: `m-${turnID}`, text: "delegate this" } },
  };
}

/** The next `seq` for a turn. `appendEntry` REFUSES an entry whose `seq` is not exactly
 *  `entries.length`, so the fixture cannot hand-number them and stay valid as a case
 *  appends more. */
const seqOf = new Map<string, number>();
function nextSeq(turnID: string): number {
  const n = (seqOf.get(turnID) ?? 0) + 1;
  seqOf.set(turnID, n);
  return n;
}

/** Seed one turn per chat, through the store's own operations: a fixture assembled by
 *  hand could hold a `seq` the store refuses. */
function seedChats(...chats: readonly { readonly chat: string; readonly turn: string }[]): void {
  store.setSessions(chats.map((c) => makeSession(c.chat)));
  for (const c of chats) {
    seqOf.delete(c.turn);
    store.openTurn(c.chat, turnOpen(c.turn));
  }
}

/** Append one sealed entry. `lane` absent is the chat's own lane. */
function push(
  chat: string,
  turn: string,
  kind: Entry["kind"],
  payload: unknown,
  opts: { readonly id?: string; readonly lane?: string } = {},
): void {
  const seq = nextSeq(turn);
  const base: Entry = {
    id: opts.id ?? `${turn}-e${String(seq)}`,
    turn,
    kind,
    seq,
    ts: seq + 1,
    payload,
  };
  store.appendEntry(
    chat,
    opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane }),
  );
}

/** A later entry arriving in `lane`. Ends on a paragraph break, so the incremental
 *  markdown parser has no trailing token to hold and the whole text is on screen or none
 *  of it is — without it every assertion below is one character short. */
function laneText(chat: string, turn: string, lane: string, text: string): void {
  push(chat, turn, "text", { text: `${text}\n\n` }, { lane });
}

function invocation(
  id: string,
  subtask: string,
  status: EntryToolCall["status"] = "completed",
): EntryToolCall {
  return makeToolCall({
    id,
    title: "Sub-agent: reviewer",
    kind: "other",
    status,
    agent_subtask_id: subtask,
    input: { name: "reviewer" },
  });
}

/** ONE plain delegate: the chat's own prose, the invocation in the ISSUER's lane, and the
 *  delegate's own prose in its lane. The call id is deliberately NOT stage-shaped, so
 *  nothing resolves it into a pipeline. */
function delegate(chat: string, turn: string, lane: string, body = "delegate work"): void {
  push(chat, turn, "text", { text: "parent prose" });
  push(chat, turn, "tool_call", invocation(`tc-${lane}`, lane), { id: `tc-${lane}` });
  push(chat, turn, "text", { text: body }, { lane });
}

const DRIVER = "orc_1";
const PLAN_CALL = `invoke_subagent_${DRIVER}_stage_plan`;
const REVIEW_CALL = `invoke_subagent_${DRIVER}_stage_review`;
const PLAN = "st-plan";
const REVIEW = "st-review";

/** A two-stage `orchestrate_subagent` pipeline in one turn; the stage ids carry the driver (the
 *  join). */
function pipeline(
  chat: string,
  turn: string,
  opts: { readonly reviewStatus?: EntryToolCall["status"]; readonly reviewText?: string } = {},
): void {
  push(chat, turn, "text", { text: "parent prose" });
  push(
    chat,
    turn,
    "tool_call",
    {
      id: DRIVER,
      title: "Orchestrate Sub-agent",
      kind: "other",
      status: "in_progress",
      ts: 0,
      input: { task: "review the diff", stages: [{ name: "plan" }, { name: "review" }] },
    },
    { id: DRIVER },
  );
  push(chat, turn, "tool_call", invocation(PLAN_CALL, PLAN), { id: PLAN_CALL });
  push(chat, turn, "text", { text: "the plan stage report" }, { lane: PLAN });
  push(chat, turn, "tool_call", invocation(REVIEW_CALL, REVIEW, opts.reviewStatus ?? "completed"), {
    id: REVIEW_CALL,
  });
  const reviewText = opts.reviewText ?? "the review stage report";
  if (reviewText !== "") {
    push(chat, turn, "text", { text: reviewText }, { lane: REVIEW });
  }
}

/** The page's own host, and the note the detail pane shows for a node with nothing
 *  in it. */
function body(): HTMLElement {
  return document.getElementById("subagent-body") as HTMLElement;
}
function emptyNoteText(): string {
  return body().querySelector(".ev-d-empty")?.textContent ?? "";
}

/** Click a stage's row in the left-hand tree, the way a reader does. `.ev-row-main`
 *  is the row's own click target; the row element carries the path. */
function clickRow(path: string): void {
  const row = body().querySelector<HTMLElement>(`.ev-row[data-path="${path}"] .ev-row-main`);
  expect(row).not.toBeNull();
  row?.click();
}

beforeEach(() => {
  clearAllEntrySigs();
  mockHasTab.mockReset();
  mockHasTab.mockReturnValue(false);
  // Re-installed per test: the root config sets `mockReset: true`, which restores a
  // `{ spy: true }` export to the original reader.
  refs = [];
  vi.mocked(openSubagentRefs).mockImplementation(() => {
    touch(refsVersion);
    return [...refs];
  });
});

// Evicting the chat an open subagent tab projects would blank it; answered from the RESIDENT lanes.
describe("subagentTabProjectsChat", () => {
  it("exempts a chat with an open subagent tab for one of its delegates", () => {
    seedChats({ chat: "c1", turn: "t1" });
    delegate("c1", "t1", "st-1");
    mockHasTab.mockImplementation((kind, ref) => kind === "subagent" && ref === "c1/st-1");

    expect(subagentTabProjectsChat("c1")).toBe(true);
    // The lookup is by the tab's own composite ref, chatID/subtaskID.
    expect(mockHasTab).toHaveBeenCalledWith("subagent", "c1/st-1");
  });

  // An OPEN entry never reaches the log, so a delegate whose first words are still
  // arriving has no sealed entry at all — and it is exactly the one whose window must
  // not be evicted.
  it("exempts a chat whose delegate lane holds only an open entry", () => {
    seedChats({ chat: "c1", turn: "t1" });
    push("c1", "t1", "text", { text: "parent prose" });
    store.openEntry("c1", {
      turn: "t1",
      id: "t1-live",
      lane: "st-live",
      kind: "text",
      text: "first words",
      n: 1,
    });
    mockHasTab.mockImplementation((kind, ref) => kind === "subagent" && ref === "c1/st-live");

    expect(subagentTabProjectsChat("c1")).toBe(true);
  });

  it("exempts nothing when no subagent tab is open", () => {
    seedChats({ chat: "c1", turn: "t1" });
    delegate("c1", "t1", "st-1");
    expect(subagentTabProjectsChat("c1")).toBe(false);
  });

  it("exempts nothing for a chat whose entries carry no delegate lane", () => {
    seedChats({ chat: "c1", turn: "t1" });
    push("c1", "t1", "text", { text: "no delegate here" });
    mockHasTab.mockReturnValue(true); // even with tabs open, no lane to ask about
    expect(subagentTabProjectsChat("c1")).toBe(false);
    expect(mockHasTab).not.toHaveBeenCalled();
  });

  it("answers false for an unknown chat", () => {
    store.setSessions([]);
    expect(subagentTabProjectsChat("c-missing")).toBe(false);
  });

  it("does not cross chats: the ref carries the asking chat's id", () => {
    seedChats({ chat: "c1", turn: "t1" }, { chat: "c2", turn: "t2" });
    delegate("c1", "t1", "st-1");
    delegate("c2", "t2", "st-1");
    // A tab open for c2's delegate must not exempt c1, subtask id collision or
    // not — the ref is chat-scoped.
    mockHasTab.mockImplementation((kind, ref) => kind === "subagent" && ref === "c2/st-1");
    expect(subagentTabProjectsChat("c1")).toBe(false);
    expect(subagentTabProjectsChat("c2")).toBe(true);
  });
});

// The page's live input is the LANE: with no transcript view, only its own subscription paints.
describe("a delegate's prose with no transcript view anywhere", () => {
  it("renders an entry that arrives in the delegate's lane after the mount", async () => {
    const chat = "c-stream";
    const turn = "t-stream";
    seedChats({ chat, turn });
    delegate(chat, turn, "st-1");

    show(chat, "st-1");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("delegate work");
    });
    // The page holds the delegate's LANE and nothing else: the chat's own prose is not
    // this render's, which is what makes the routing claim below have content.
    expect(body().textContent).not.toContain("parent prose");

    laneText(chat, turn, "st-1", "and then more");

    await vi.waitFor(() => {
      expect(body().textContent).toContain("and then more");
    });
  });
});

// A switch must drop the previous page's RENDER: the repaint gate is a union over registered renders.
describe("switching delegates releases the page's render", () => {
  it("unregisters the outgoing delegate's render and keeps the incoming one", async () => {
    const chat = "c-switch";
    const turn = "t-switch";
    seedChats({ chat, turn });
    push(chat, turn, "text", { text: "parent prose" });
    push(chat, turn, "tool_call", invocation("tc-A", "sw-A"), { id: "tc-A" });
    push(chat, turn, "text", { text: "first delegate" }, { lane: "sw-A" });
    push(chat, turn, "tool_call", invocation("tc-B", "sw-B"), { id: "tc-B" });
    push(chat, turn, "text", { text: "second delegate" }, { lane: "sw-B" });

    // BOTH tabs open, so this tests SUPERSEDE, not the demand effect.
    setSubagentTabs([subagentRef(chat, "sw-A"), subagentRef(chat, "sw-B")]);
    showSubagent(chat, "sw-A");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("first delegate");
    });
    expect(memberIsMounted(turn, "sw-A")).toBe(true);

    // The SWITCH is the property. One delegate cannot express it: the release only asks
    // for the wrong pair where the page's own key and the visible lane disagree, which
    // is true for exactly one paint after a switch.
    showSubagent(chat, "sw-B");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("second delegate");
    });
    expect(body().textContent).not.toContain("first delegate");

    expect(memberIsMounted(turn, "sw-A")).toBe(false);
    expect(memberIsMounted(turn, "sw-B")).toBe(true);
  });
});

// Every stage row is selectable, so a click renders the clicked stage IN PLACE.
describe("selecting a sibling stage in the tree", () => {
  it("renders that stage's transcript in the same sub-tab", async () => {
    const chat = "c-nav";
    const turn = "t-nav";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });

    clickRow(REVIEW);

    // Both hosts stay in the DOM — a delegate's entries are persisted but a mounted
    // render is not free to rebuild — so which one is SHOWN is the observable.
    const review = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`);
    const plan = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${PLAN}"]`);
    expect(review?.hidden).toBe(false);
    expect(review?.textContent).toContain("the review stage report");
    expect(plan?.hidden).toBe(true);
    // No note at all: the transcript is here.
    expect(emptyNoteText()).not.toContain("own page");
    expect(body().querySelector<HTMLElement>(".ev-d-empty")?.hidden).toBe(true);
  });

  it("keeps the delegate the tab names mounted, so going back costs no rebuild", async () => {
    const chat = "c-back";
    const turn = "t-back";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    clickRow(PLAN);

    const plan = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${PLAN}"]`);
    expect(plan?.hidden).toBe(false);
    expect(plan?.textContent).toContain("the plan stage report");
    expect(body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`)?.hidden).toBe(
      true,
    );
  });

  // A sibling that has produced nothing yet is the one case the note is still FOR, and
  // its two sentences are the whole vocabulary: the projection covers the stage either
  // way, so the note answers "nothing yet" rather than "not here".
  it("says what an empty sibling is doing rather than sending the reader away", async () => {
    for (const [status, sentence] of [
      ["completed", "This delegate finished without producing any transcript."],
      ["in_progress", "Waiting for this delegate to produce output\u2026"],
    ] as const) {
      const chat = `c-empty-${status}`;
      const turn = `t-empty-${status}`;
      seedChats({ chat, turn });
      pipeline(chat, turn, { reviewText: "", reviewStatus: status });

      show(chat, PLAN);
      await vi.waitFor(() => {
        expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
      });
      clickRow(REVIEW);

      expect(emptyNoteText()).toBe(sentence);
      // Nothing is mounted for it, which is what leaves the note on screen.
      expect(body().querySelector(`.ev-d-body[data-path="${REVIEW}"]`)).toBeNull();
      expect(memberIsMounted(turn, REVIEW)).toBe(false);
    }
  });

  // The routing property: an entry lands in the render of the member whose LANE it
  // names, and in no other.
  it("routes a later entry to the selected sibling's own render", async () => {
    const chat = "c-nav-stream";
    const turn = "t-nav-stream";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    const review = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`);
    expect(review?.textContent).toContain("the review stage report");

    laneText(chat, turn, REVIEW, "and one more finding");

    await vi.waitFor(() => {
      expect(review?.textContent).toContain("and one more finding");
    });
    // And into the sibling's render, not the tab's own.
    expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)?.textContent).not.toContain(
      "and one more finding",
    );
  });

  // A hidden mounted body must stay current, or revisiting shows a stale snapshot.
  it("keeps a mounted-but-hidden stage up to date", async () => {
    const chat = "c-bg";
    const turn = "t-bg";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    // Mount the review stage, then leave it: it stays in the DOM, hidden.
    clickRow(REVIEW);
    clickRow(PLAN);
    const review = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`);
    expect(review?.hidden).toBe(true);

    laneText(chat, turn, REVIEW, "a late finding");

    await vi.waitFor(() => {
      expect(review?.textContent).toContain("a late finding");
    });
  });

  // The release property, extended to the multi-body map: a switch to another chat
  // disposes EVERY member's render, not just the one the outgoing tab named.
  it("releases every mounted stage when the reader switches chat", async () => {
    const chat = "c-nav-drop";
    const turn = "t-nav-drop";
    seedChats({ chat, turn }, { chat: "c-other", turn: "t-other" });
    pipeline(chat, turn);
    delegate("c-other", "t-other", "st-other");

    // Both tabs open across the switch, for the reason the delegate-switch case above
    // states: leaving only the incoming tab open would have the demand effect drop the
    // outgoing page, which is not the release this case is about.
    setSubagentTabs([subagentRef(chat, PLAN), subagentRef("c-other", "st-other")]);
    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    expect(memberIsMounted(turn, PLAN)).toBe(true);
    expect(memberIsMounted(turn, REVIEW)).toBe(true);

    showSubagent("c-other", "st-other");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("delegate work");
    });

    expect(memberIsMounted(turn, PLAN)).toBe(false);
    expect(memberIsMounted(turn, REVIEW)).toBe(false);
  });
});

// DEMAND IS AN INPUT: an effect over the open-tab set releases the page; the membership test makes
// the shared-group case structural.
describe("demand for the mounted page", () => {
  /** Both members of the pipeline, because a release that reached one render and not the
   *  other is exactly the shape a hand-written reset produces. */
  function neitherStageIsMounted(turn: string): void {
    expect(memberIsMounted(turn, PLAN)).toBe(false);
    expect(memberIsMounted(turn, REVIEW)).toBe(false);
  }

  it("drops the page and every body it mounted when the last subagent tab closes", async () => {
    const chat = "c-close";
    const turn = "t-close";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    // TWO bodies mounted, which is what makes "every body" a claim with content.
    clickRow(REVIEW);
    expect(memberIsMounted(turn, PLAN)).toBe(true);
    expect(memberIsMounted(turn, REVIEW)).toBe(true);

    closeSubagentTabs();

    // The page LEFT the host, so "no page mounted" and "the host holds no page" agree.
    expect(body().querySelector(".ev-page")).toBeNull();
    neitherStageIsMounted(turn);
  });

  // Two stage tabs share ONE page; the membership test (`Map.has` on the projection) keeps it.
  it("keeps the page when one of two stage tabs sharing its group closes", async () => {
    const chat = "c-sibling";
    const turn = "t-sibling";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    setSubagentTabs([subagentRef(chat, PLAN), subagentRef(chat, REVIEW)]);
    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    const page = body().querySelector(".ev-page");
    expect(page).not.toBeNull();

    // PLAN's tab closes; REVIEW's stays open, and it names a member of the same group.
    setSubagentTabs([subagentRef(chat, REVIEW)]);

    // The SAME element, not a rebuilt one: the page was never dropped, so the bodies
    // mounted into it are still the reader's.
    expect(body().querySelector(".ev-page")).toBe(page);
    // And a member whose OWN tab is the one that closed is still rendered and still
    // refreshed: the page is the unit of demand, not the tab.
    expect(memberIsMounted(turn, PLAN)).toBe(true);
    laneText(chat, turn, PLAN, "still here");
    await vi.waitFor(() => {
      expect(
        body().querySelector<HTMLElement>(`.ev-d-body[data-path="${PLAN}"]`)?.textContent,
      ).toContain("still here");
    });
  });

  // The chat compare in the membership test, and the id compare, each on their own. A
  // subtask id is only unique WITHIN a chat, which `subagentTabProjectsChat`'s own
  // cases already pin from the other side.
  it.each([
    {
      what: "a tab for the same subtask id in a different chat",
      chat: "c-outside-chat",
      turn: "t-outside-chat",
      ref: subagentRef("c-elsewhere", PLAN),
    },
    {
      what: "a tab for a non-member subtask of the same chat",
      chat: "c-outside-member",
      turn: "t-outside-member",
      ref: subagentRef("c-outside-member", "st-not-a-member"),
    },
  ])("does not count $what as demand", async ({ chat, turn, ref }) => {
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);

    setSubagentTabs([ref]);

    expect(body().querySelector(".ev-page")).toBeNull();
    neitherStageIsMounted(turn);
  });

  // Superseding is the other release path: the tab set does not move, so `mountPage`'s own release
  // is under test.
  it("still supersedes: mounting another group's page releases the previous one", async () => {
    const chat = "c-supersede";
    const turn = "t-supersede";
    seedChats({ chat, turn }, { chat: "c-super-other", turn: "t-super-other" });
    pipeline(chat, turn);
    delegate("c-super-other", "t-super-other", "st-other");

    setSubagentTabs([
      subagentRef(chat, PLAN),
      subagentRef(chat, REVIEW),
      subagentRef("c-super-other", "st-other"),
    ]);
    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    const page = body().querySelector(".ev-page");

    showSubagent("c-super-other", "st-other");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("delegate work");
    });

    expect(body().querySelector(".ev-page")).not.toBe(page);
    neitherStageIsMounted(turn);
  });

  // The drop writes `shown`: the paint effect depends on `shown` and the chat's version, so the next
  // entry would re-mount a page for a closed tab.
  it("does not re-mount the page on the launching chat's next entry", async () => {
    const chat = "c-resurrect";
    const turn = "t-resurrect";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    closeSubagentTabs();
    expect(body().querySelector(".ev-page")).toBeNull();

    // A REAL entry for the launching chat, in its OWN lane so the cause is structural,
    // and its version bump is asserted as the PREMISE: without it this test cannot fail.
    const version = store.messagesVersionOf(chat);
    const before = version.peek();
    push(chat, turn, "text", { text: "the parent carries on" });
    expect(version.peek()).not.toBe(before);

    expect(body().querySelector(".ev-page")).toBeNull();
    neitherStageIsMounted(turn);
  });

  // A SECOND effect: taking the tab set in the paint effect would re-project on every tab mutation.
  it("does not re-project the group on a tab-set change that leaves the demand alone", async () => {
    const chat = "c-churn";
    const turn = "t-churn";
    seedChats({ chat, turn });
    pipeline(chat, turn);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });

    const projections = vi.mocked(sliceSubagentGroup).mock.calls.length;
    const page = body().querySelector(".ev-page");
    expect(page).not.toBeNull();

    // A new tab-set version carrying the SAME demand: the strip moved (a pin, a
    // reorder, a tab of another kind opening), this page's group did not.
    setSubagentTabs([subagentRef(chat, PLAN)]);

    expect(vi.mocked(sliceSubagentGroup).mock.calls.length).toBe(projections);
    expect(body().querySelector(".ev-page")).toBe(page);
  });
});

// The page projects the launching chat's entries, so the chat's window is what refreshes it.

describe("refreshSubagent delegates to the launching chat", () => {
  it("refreshes the launching chat and nothing else", () => {
    vi.mocked(refreshChatView).mockImplementation(() => undefined);

    refreshSubagent("c-launcher", "task-9");

    expect(refreshChatView).toHaveBeenCalledTimes(1);
    expect(refreshChatView).toHaveBeenCalledWith("c-launcher");
    expect(vi.mocked(sliceSubagentGroup)).not.toHaveBeenCalled();
  });
});

describe("the page's own door", () => {
  // A DEEP LINK awaits this: `app.ts`'s `subagent` branch holds the router's claim until it settles,
  // so no projection emit overwrites the URL the reader opened.
  it("returns the tab open, so a deep link can await it", async () => {
    let release = (): void => undefined;
    vi.mocked(openSubagentTab).mockImplementationOnce(
      () =>
        new Promise<void>((res) => {
          release = res;
        }),
    );
    let settled = false;
    const opening = openSubagentView("c-launcher", "sub-1").then(() => {
      settled = true;
    });

    await Promise.resolve();
    expect(settled, "must not settle before the tab open does").toBe(false);
    release();
    await opening;
    expect(settled).toBe(true);
  });
});

// `.page-content` is ONE scroller for every subagent tab, so each TAB keeps its own
// offset. Sibling stages of one pipeline share a page, which is the case the offset
// cannot be keyed by the page for.
describe("the page's scroll position", () => {
  const long = Array.from({ length: 150 }, (_, i) => `Paragraph ${String(i)}.`).join("\n\n");

  function scroller(): HTMLElement {
    const el = document.querySelector<HTMLElement>("[id='subagent-view'] > .page-content");
    if (el === null) {
      throw new Error("no .page-content");
    }
    return el;
  }

  /** Two frames: the scroll event is dispatched in the rendering step, and the
   *  offset is recorded from it. */
  async function frames(): Promise<void> {
    for (let i = 0; i < 2; i++) {
      await new Promise<void>((resolve) => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    }
  }

  // The production shape: `#subagent-body` inside the view's scrolling `.page-content`.
  // Unwrapped afterwards, because Browser Mode keeps the body across cases.
  let view: HTMLElement;
  beforeEach(() => {
    view = document.createElement("div");
    view.id = "subagent-view";
    view.style.cssText = "display:flex;flex-direction:column;height:300px;width:600px";
    const content = document.createElement("div");
    content.className = "page-content";
    content.style.cssText = "flex:1 1 0;min-height:0;overflow-y:auto";
    content.appendChild(body());
    view.appendChild(content);
    document.body.appendChild(view);
  });
  afterEach(() => {
    closeSubagentTabs();
    document.body.appendChild(body());
    view.remove();
  });

  it("is per delegate: A, then B, then A lands each where it was left", async () => {
    const chat = "c-scroll";
    const turn = "t-scroll";
    seedChats({ chat, turn });
    delegate(chat, turn, "sc-A", `first ${long}`);
    push(chat, turn, "tool_call", invocation("tc-sc-B", "sc-B"), { id: "tc-sc-B" });
    push(chat, turn, "text", { text: `second ${long}` }, { lane: "sc-B" });
    setSubagentTabs([subagentRef(chat, "sc-A"), subagentRef(chat, "sc-B")]);

    showSubagent(chat, "sc-A");
    await vi.waitFor(() => {
      expect(scroller().scrollHeight).toBeGreaterThan(scroller().clientHeight + 800);
    });
    scroller().scrollTop = 700;
    await frames();
    showSubagent(chat, "sc-B");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("second Paragraph 0.");
    });
    expect(scroller().scrollTop).toBe(0);
    scroller().scrollTop = 300;
    await frames();
    showSubagent(chat, "sc-A");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("first Paragraph 0.");
    });
    expect(scroller().scrollTop).toBe(700);
  });

  it("is per stage: two sibling stages of one pipeline keep separate offsets", async () => {
    const chat = "c-scroll-pipe";
    const turn = "t-scroll-pipe";
    seedChats({ chat, turn });
    pipeline(chat, turn, { reviewText: long });
    laneText(chat, turn, PLAN, long);
    setSubagentTabs([subagentRef(chat, PLAN), subagentRef(chat, REVIEW)]);

    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(scroller().scrollHeight).toBeGreaterThan(scroller().clientHeight + 800);
    });
    scroller().scrollTop = 700;
    await frames();
    showSubagent(chat, REVIEW);
    await vi.waitFor(() => {
      expect(body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`)?.hidden).toBe(
        false,
      );
    });
    expect(scroller().scrollTop).toBe(0);
    scroller().scrollTop = 300;
    await frames();
    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector<HTMLElement>(`.ev-d-body[data-path="${PLAN}"]`)?.hidden).toBe(
        false,
      );
    });
    expect(scroller().scrollTop).toBe(700);
    showSubagent(chat, REVIEW);
    expect(scroller().scrollTop).toBe(300);
  });
});
