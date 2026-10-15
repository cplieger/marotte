// The subagent page's body rebuild: its dispose and its build are one bracket, so the run-state release the
// dispose makes waits for the claims the build takes. Kept apart from `subagent-view.test.ts` so it carries its
// own host fixture.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal, touch } from "@cplieger/reactive";
import type { Session } from "./types.js";
import type * as BlocksModule from "./messages-blocks.js";
import type * as ScrollModule from "./scroll.js";
import type * as SliceModule from "./subagent-slice.js";
import type * as TabsModule from "./tabs.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";
import { makeToolCall } from "./__test-helpers__/model.js";

// tabs.ts's real graph reads the shared DOM registry at module scope, and `byId` throws on a missing element, so
// the hosts exist before any import.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
  "tab-strip",
  "subagent-body",
  "subagent-exec-dock",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

// `beforeEach` gives the page's two seams their bodies: the root config's `mockReset` drops them.
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsModule>()),
  openSubagentRefs: vi.fn(),
}));
vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ScrollModule>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));
vi.mock("./subagent-slice.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SliceModule>()),
  sliceSubagent: vi.fn(),
}));
// The two calls the rebuild makes are pass-throughs a case can order; the root config's `mockReset` drops their
// bodies, so `beforeEach` installs them again.
vi.mock("./messages-blocks.js", async (importOriginal) => {
  const actual = await importOriginal<typeof BlocksModule>();
  return {
    ...actual,
    rebuildDetached: vi.fn(),
    disposeDetachedBody: vi.fn(),
    buildDetachedBody: vi.fn(),
  };
});
const store = await import("./store.js");
const { openSubagentRefs } = await import("./tabs.js");
const { subagentRef } = await import("./tab-materialize.js");
const { sliceSubagent } = await import("./subagent-slice.js");
const { showSubagent } = await import("./subagent-view.js");
const { clearAllEntrySigs } = await import("./store-signals.js");
const { rebuildDetached, disposeDetachedBody, buildDetachedBody } =
  await import("./messages-blocks.js");
const blocksActual = await vi.importActual<typeof BlocksModule>("./messages-blocks.js");
const sliceActual = await vi.importActual<typeof SliceModule>("./subagent-slice.js");

let refs: readonly string[] = [];
const refsVersion = signal(0);

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

/** `appendEntry` refuses an entry whose `seq` is not exactly `entries.length`, so the fixture numbers them. */
let nextSeq = 0;
function push(
  chat: string,
  turn: string,
  kind: Entry["kind"],
  payload: unknown,
  opts: { readonly id?: string; readonly lane?: string } = {},
): void {
  nextSeq++;
  const base: Entry = {
    id: opts.id ?? `${turn}-e${String(nextSeq)}`,
    turn,
    kind,
    seq: nextSeq,
    ts: nextSeq + 1,
    payload,
  };
  store.appendEntry(
    chat,
    opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane }),
  );
}

function invocation(id: string, subtask: string): EntryToolCall {
  return makeToolCall({
    id,
    title: "Sub-agent: reviewer",
    kind: "other",
    status: "completed",
    agent_subtask_id: subtask,
    input: { name: "reviewer" },
  });
}

function body(): HTMLElement {
  return document.getElementById("subagent-body") as HTMLElement;
}

beforeEach(() => {
  clearAllEntrySigs();
  vi.mocked(rebuildDetached).mockImplementation(blocksActual.rebuildDetached);
  vi.mocked(disposeDetachedBody).mockImplementation(blocksActual.disposeDetachedBody);
  vi.mocked(buildDetachedBody).mockImplementation(blocksActual.buildDetachedBody);
  vi.mocked(sliceSubagent).mockImplementation(sliceActual.sliceSubagent);
  refs = [];
  vi.mocked(openSubagentRefs).mockImplementation(() => {
    touch(refsVersion);
    return [...refs];
  });
});

describe("a delegate's page rebuilding its body", () => {
  it("disposes and builds inside one rebuild bracket", async () => {
    const chat = "c-rebuild";
    const turn = "t-rebuild";
    store.setSessions([makeSession(chat)]);
    store.openTurn(chat, turnOpen(turn));
    push(chat, turn, "text", { text: "parent prose" });
    push(chat, turn, "tool_call", invocation("tc-rb", "rb-A"), { id: "tc-rb" });
    push(chat, turn, "text", { text: "delegate work" }, { lane: "rb-A" });
    refs = [subagentRef(chat, "rb-A")];
    refsVersion.value += 1;
    showSubagent(chat, "rb-A");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("delegate work");
    });

    // The window now holds the delegate under another turn id: the mounted body is not that turn's.
    vi.mocked(sliceSubagent).mockImplementation((...args) => {
      const slice = sliceActual.sliceSubagent(...args);
      return slice.turn === undefined
        ? slice
        : { ...slice, turn: { ...slice.turn, id: "t-rebuilt" } };
    });
    const order: string[] = [];
    vi.mocked(rebuildDetached).mockImplementation((rebuild) => {
      order.push("open");
      blocksActual.rebuildDetached(rebuild);
      order.push("close");
    });
    vi.mocked(disposeDetachedBody).mockImplementation((...args) => {
      order.push("dispose");
      blocksActual.disposeDetachedBody(...args);
    });
    vi.mocked(buildDetachedBody).mockImplementation((...args) => {
      order.push("build");
      blocksActual.buildDetachedBody(...args);
    });
    push(chat, turn, "text", { text: "and then more\n\n" }, { lane: "rb-A" });

    await vi.waitFor(() => {
      expect(order).toEqual(["open", "dispose", "build", "close"]);
    });
  });
});
