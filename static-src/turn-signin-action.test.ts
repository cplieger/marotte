// A turn the engine failed on its credential says the sign-in cause in its own row and offers "Sign in again" on the
// newest turn only. Through the real paint, as the Compact action's test does.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";
import type * as Scroll from "./scroll.js";
import type * as ApiClient from "./api-client.js";
import type * as ChatActions from "./actions/chat.js";
import type * as Modals from "./modals.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { dispatch, showLoginModal } = vi.hoisted(() => ({
  dispatch: vi.fn(),
  showLoginModal: vi.fn(),
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  compactChat: { dispatch },
}));
vi.mock("./modals.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Modals>()),
  showLoginModal,
}));

const { mountChatView, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive, setPainted, bumpMessages } = await import("./store.js");

mountChatView();

/** marotte's own sentence for a turn refused on its credential during an outage (internal/marotte). */
const SIGN_IN =
  "Kiro sign-in failed: the Kiro sign-in service is unreachable. marotte keeps trying, so send the message again once it answers.";

function sealed(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    lane: "",
    kind,
    seq,
    ts: seq + 1,
    payload,
  } as Entry;
}

function turnEntries(
  id: string,
  n: number,
  close: Record<string, unknown>,
  after: Entry["kind"][] = [],
): Entry[] {
  const rows = [
    sealed(id, 0, "turn_open", { prompt: { id: `${id}-p`, text: "go" }, source: "prompt", n }),
    sealed(id, 1, "text", { text: "Working." }),
    sealed(id, 2, "turn_close", close),
  ];
  after.forEach((kind, i) => rows.push(sealed(id, 3 + i, kind, { summary: "s" })));
  return rows;
}

const signInFailure = { outcome: "failed", failure_reason: SIGN_IN, failure_kind: "sign_in" };
const otherFailure = { outcome: "failed", failure_reason: "ACP bridge exited" };

function cards(): HTMLElement[] {
  const root = activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

async function mount(chatID: string, turnRows: Entry[][]): Promise<HTMLElement[]> {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnRows) {
    const first = entries[0];
    if (first !== undefined) {
      turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
      order.push(first.turn);
    }
  }
  setSessions([
    {
      ...makeSession({ id: chatID, name: chatID }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chatID);
  setPainted(chatID);
  bumpMessages(chatID, "load");
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (cards().length !== order.length) {
    if (Date.now() > deadline) {
      throw new Error("timed out waiting for cards");
    }
    await new Promise((r) => setTimeout(r, 8));
  }
  return cards();
}

const button = (card: HTMLElement | undefined): HTMLButtonElement | null =>
  card?.querySelector<HTMLButtonElement>(":scope > .turn-notice-actions > button") ?? null;

const notice = (card: HTMLElement | undefined): string | null | undefined =>
  card?.querySelector(":scope > .turn-notice")?.textContent;

describe("the sign-in failure row", () => {
  beforeEach(() => {
    showLoginModal.mockClear();
  });

  it("says the sign-in cause and offers Sign in again on the newest turn", async () => {
    const [card] = await mount("c-signin", [turnEntries("t1", 1, signInFailure)]);
    expect(notice(card)).toBe(SIGN_IN);
    const btn = button(card);
    expect(btn?.textContent).toBe("Sign in again");
    btn?.click();
    expect(showLoginModal).toHaveBeenCalledTimes(1);
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("is not offered on an older turn", async () => {
    const [older] = await mount("c-signin-older", [
      turnEntries("t1", 1, signInFailure),
      turnEntries("t2", 2, { outcome: "completed" }),
    ]);
    expect(notice(older)).toBe(SIGN_IN);
    expect(button(older)).toBeNull();
  });

  it("is not offered for another failure", async () => {
    const [card] = await mount("c-signin-other", [turnEntries("t1", 1, otherFailure)]);
    expect(card?.querySelector(":scope > .turn-notice")).not.toBeNull();
    expect(button(card)).toBeNull();
  });
});
