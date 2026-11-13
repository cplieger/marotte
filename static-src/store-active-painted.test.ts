// The store's two chat pointers: the ACTIVE chat (the one the reader is on, moved by a switch's
// gesture) and the PAINTED chat (the one whose transcript is mounted, moved by its activation).
// Readers that act for the reader follow the active chat; readers of the transcript's DOM follow
// the painted chat, so a switch's gap never sends to one chat while showing another.
import { describe, it, expect, beforeEach } from "vitest";
import { effect } from "@cplieger/reactive";
import * as store from "./store.js";
import { defaultUsage } from "./store.js";
import type { Session } from "./types.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: chatID,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

beforeEach(() => {
  store.setSessions([makeSession("a"), makeSession("b"), makeSession("c")]);
  store.setActive("a");
  store.setPainted("a");
});

describe("the active and painted chat pointers", () => {
  it("moves only the active chat on setActive, and only the painted chat on setPainted", () => {
    store.setActive("b");
    expect(store.getActiveId()).toBe("b");
    expect(store.getPaintedId()).toBe("a");
    expect(store.getPainted()?.id).toBe("a");
    store.setPainted("b");
    expect(store.getPaintedId()).toBe("b");
    expect(store.getPainted()?.id).toBe("b");
  });

  it("re-runs a watchPaintedId effect on setPainted and not on setActive", () => {
    const seen: string[] = [];
    const stop = effect(() => {
      seen.push(store.watchPaintedId());
    });
    try {
      store.setActive("b");
      expect(seen, "an effect on the painted chat re-ran on setActive").toEqual(["a"]);
      store.setPainted("b");
      expect(seen).toEqual(["a", "b"]);
    } finally {
      stop();
    }
  });

  it("moves the painted chat to the active chat when removeChat drops the painted chat", () => {
    store.setActive("b"); // a switch's gesture: B is active, A still painted
    store.removeChat("a");
    expect(store.getActiveId()).toBe("b");
    expect(store.getPaintedId(), "the painted pointer names a removed row").toBe("b");
  });

  it("keeps the painted chat when removeChat drops another chat", () => {
    store.removeChat("c");
    expect(store.getPaintedId()).toBe("a");
  });
});
