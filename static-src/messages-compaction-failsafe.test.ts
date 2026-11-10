// The transcript of a turn marotte stopped at 95% context use, compacted and resumed: the stopped turn says why it
// stopped, and the resume reads as marotte's prompt, never the reader's.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Entry } from "./wire/types.gen.js";
import type * as Scroll from "./scroll.js";
import type * as ActionsMessages from "./actions/messages.js";

const host = (id: string, parent: HTMLElement, tag = "div"): HTMLElement => {
  const d = document.createElement(tag);
  d.id = id;
  parent.appendChild(d);
  return d;
};
host("messages", host("messages-wrap", host("messages-wrap-outer", document.body)));
for (const id of ["chat-view", "scroll-bottom", "send-btn"]) {
  host(id, document.body);
}
host("prompt-input", document.body, "textarea");

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));

const { copied } = vi.hoisted(() => ({ copied: [] as string[] }));
vi.mock("./actions/messages.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ActionsMessages>()),
  copyClipboard: {
    dispatch: (text: string) => {
      copied.push(text);
      return Promise.resolve();
    },
  },
}));

const store = await import("./store.js");
const messages = await import("./messages.js");
const { resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");

mountAppCSS();
messages.mountChatView();

const NOTICE =
  "The context was 95% full, so marotte stopped this turn to compact it before it ran out. " +
  "After the compaction, marotte asks the agent to continue.";
const RESUME =
  "The context window was nearly full, so your last turn was stopped. Continue the task.";

function sealed(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id: string,
): Entry {
  return { id, turn, kind, seq, ts: seq + 1, payload };
}

function opened(turn: string, n: number, prompt: Record<string, unknown>): Entry {
  return sealed(turn, 0, "turn_open", { prompt, source: "prompt", n }, turn);
}

function load(chatID: string, turns: Record<string, Entry[]>): void {
  const s: Session = makeSession({ id: chatID, name: chatID });
  for (const [id, entries] of Object.entries(turns)) {
    s.turns.set(id, { entries, openEntries: new Map() });
    s.turn_order.push(id);
  }
  s.turn_count = s.turn_order.length;
  store.setSessions([s]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
}

function card(turn: string): HTMLElement {
  const root = messages.activeTranscriptView() ?? document.getElementById("messages")!;
  for (const child of root.children) {
    if (child.getAttribute(KEY_ATTR) === turn) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for ${turn}`);
}

beforeEach(() => {
  messages.teardownAll();
  localStorage.clear();
  resetFoldState();
  store.setSessions([]);
  copied.length = 0;
});

describe("a turn the compaction failsafe stopped and resumed", () => {
  function loadStoppedAndResumed(chatID: string): void {
    load(chatID, {
      t1: [
        opened("t1", 1, { id: "m-1", text: "refactor the parser" }),
        sealed("t1", 1, "text", { text: "Reading the files." }, "t1-say"),
        sealed(
          "t1",
          2,
          "turn_close",
          { outcome: "cancelled", stopped_to_compact: true, failure_reason: NOTICE },
          "t1:close",
        ),
      ],
      t2: [
        opened("t2", 2, { id: "m-r", text: RESUME, automatic: true }),
        sealed("t2", 1, "text", { text: "Picking up where I stopped." }, "t2-say"),
        sealed("t2", 2, "turn_close", { outcome: "completed" }, "t2:close"),
      ],
    });
  }

  it("says in the stopped turn why marotte stopped it", async () => {
    loadStoppedAndResumed("c-stop");
    await vi.waitFor(() => {
      card("t1");
    });
    const notice = card("t1").querySelector<HTMLElement>(":scope > .turn-notice");
    expect(notice?.textContent).toBe(NOTICE);
    expect(
      card("t1").querySelector(":scope > .turn-header .turn-dot")?.getAttribute("data-tooltip"),
    ).toBe("marotte stopped this turn to compact the context");
  });

  it("heads the resume with marotte as its sender, and its copy keeps that", async () => {
    loadStoppedAndResumed("c-resume");
    await vi.waitFor(() => {
      card("t2");
    });
    const header = card("t2").querySelector<HTMLElement>(":scope > .turn-header")!;
    expect(header.dataset["trigger"]).toBe("caller");
    expect(header.querySelector(".turn-sent-by")?.textContent).toBe("Sent by marotte");
    expect(header.querySelector(".turn-req-text")?.textContent).toBe(RESUME);

    card("t2")
      .querySelector<HTMLButtonElement>('.turn-action-btn[aria-label="Copy as text"]')
      ?.click();
    expect(copied.at(-1)?.startsWith(`Sent by marotte\n${RESUME}\n\n`)).toBe(true);
  });

  it("heads the reader's own prompt as the reader's", async () => {
    loadStoppedAndResumed("c-mine");
    await vi.waitFor(() => {
      card("t1");
    });
    const header = card("t1").querySelector<HTMLElement>(":scope > .turn-header")!;
    expect(header.dataset["trigger"]).toBe("user");
    expect(header.querySelector(".turn-sent-by")).toBeNull();
  });
});
