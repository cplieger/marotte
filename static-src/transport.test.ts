import { describe, it, expect, afterEach, vi } from "vitest";
import { BUS_COMMAND_FAILED, onBus } from "./bus.js";
import { defaultUsage, setSessions } from "./store.js";
import { send } from "./transport.js";
import type { Session } from "./types.js";

function session(id: string, name: string): Session {
  return {
    id,
    name,
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

afterEach(() => {
  vi.unstubAllGlobals();
  setSessions([]);
});

describe("send's failure event", () => {
  it("names the chat as it was when the command went out, after its row is gone", async () => {
    setSessions([session("c1", "Fix the parser")]);
    vi.stubGlobal("fetch", () => {
      setSessions([]);
      return Promise.resolve(
        new Response(JSON.stringify({ error: "at capacity" }), { status: 503 }),
      );
    });
    const failed: { chatID: string; chatName: string; message: string }[] = [];
    const off = onBus(BUS_COMMAND_FAILED, (p) => failed.push(p));

    await send({ type: "cancel", chat_id: "c1" });
    off();

    expect(failed).toEqual([{ chatID: "c1", chatName: "Fix the parser", message: "at capacity" }]);
  });
});
