// The transcript of a chat another program posted into through the API: its prompt and its steer say who sent
// them, and the turn's copy keeps that.
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

function sealed(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id: string,
): Entry {
  return { id, turn, kind, seq, ts: seq + 1, payload };
}

const MAIL = { label: "Agent Mail" };

function load(chatID: string, entries: Entry[]): void {
  const s: Session = makeSession({ id: chatID, name: chatID });
  s.turns.set("t1", { entries, openEntries: new Map() });
  s.turn_order.push("t1");
  s.turn_count = 1;
  store.setSessions([s]);
  store.setActive(chatID);
  store.setPainted(chatID);
  store.bumpMessages(chatID, "load");
}

function card(): HTMLElement {
  const root = messages.activeTranscriptView() ?? document.getElementById("messages")!;
  for (const child of root.children) {
    if (child.getAttribute(KEY_ATTR) === "t1") {
      return child as HTMLElement;
    }
  }
  throw new Error("no card for t1");
}

beforeEach(() => {
  messages.teardownAll();
  localStorage.clear();
  resetFoldState();
  store.setSessions([]);
  copied.length = 0;
});

describe("a chat an API caller posted into", () => {
  it("heads the turn with who sent its prompt and labels its steer the same way", async () => {
    load("c-mail", [
      sealed(
        "t1",
        0,
        "turn_open",
        {
          prompt: { id: "m-1", text: "Agent Mail: 1 new message", caller: MAIL },
          source: "prompt",
          n: 1,
        },
        "t1",
      ),
      sealed("t1", 1, "text", { text: "Reading it now." }, "t1-say"),
      sealed(
        "t1",
        2,
        "steer",
        { text: "Agent Mail: 2 new messages", origin: "user", state: "read", caller: MAIL },
        "steer-m-2",
      ),
      sealed("t1", 3, "turn_close", { outcome: "completed" }, "t1:close"),
    ]);

    await vi.waitFor(() => {
      card();
    });
    const header = card().querySelector<HTMLElement>(":scope > .turn-header")!;
    expect(header.dataset["trigger"]).toBe("caller");
    expect(header.querySelector(".turn-sent-by")?.textContent).toBe("Sent by Agent Mail");
    expect(header.querySelector(".turn-req-text")?.textContent).toBe("Agent Mail: 1 new message");
    expect(card().querySelector(".steer-note .steer-note-label")?.textContent).toBe(
      "Sent by Agent Mail",
    );

    card().querySelector<HTMLButtonElement>('.turn-action-btn[aria-label="Copy as text"]')?.click();
    expect(copied.at(-1)?.startsWith("Sent by Agent Mail\nAgent Mail: 1 new message\n\n")).toBe(
      true,
    );
  });

  it("heads a caller's prompt that sent only a file with who sent it, not as an agent-initiated turn", async () => {
    load("c-file", [
      sealed(
        "t1",
        0,
        "turn_open",
        {
          prompt: {
            id: "m-1",
            text: "",
            attachments: [{ path: "inbox/msg.md", name: "msg.md" }],
            caller: MAIL,
          },
          source: "prompt",
          n: 1,
        },
        "t1",
      ),
      sealed("t1", 1, "text", { text: "Reading it now." }, "t1-say"),
      sealed("t1", 2, "turn_close", { outcome: "completed" }, "t1:close"),
    ]);

    await vi.waitFor(() => {
      card();
    });
    const header = card().querySelector<HTMLElement>(":scope > .turn-header")!;
    expect(header.dataset["trigger"]).toBe("caller");
    expect(header.querySelector(".turn-sent-by")?.textContent).toBe("Sent by Agent Mail");
    expect(header.querySelector(".turn-req-text")?.textContent).not.toBe("Agent-initiated turn");
    expect(header.querySelector(".turn-req-attachments [title='inbox/msg.md']")).not.toBeNull();

    card().querySelector<HTMLButtonElement>('.turn-action-btn[aria-label="Copy as text"]')?.click();
    expect(copied.at(-1)?.startsWith("Sent by Agent Mail\n\n")).toBe(true);
  });

  it("heads the user's own prompt as the user's", async () => {
    load("c-mine", [
      sealed(
        "t1",
        0,
        "turn_open",
        { prompt: { id: "m-1", text: "fix it" }, source: "prompt", n: 1 },
        "t1",
      ),
      sealed("t1", 1, "turn_close", { outcome: "completed" }, "t1:close"),
    ]);
    await vi.waitFor(() => {
      card();
    });
    const header = card().querySelector<HTMLElement>(":scope > .turn-header")!;
    expect(header.dataset["trigger"]).toBe("user");
    expect(header.querySelector(".turn-sent-by")).toBeNull();
  });
});
