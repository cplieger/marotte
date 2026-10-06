import { describe, it, expect, beforeEach, vi } from "vitest";
import type * as Tabs from "./tabs.js";
import type * as RunStore from "./run-store.js";
import type * as Transport from "./transport.js";
import type * as Toast from "./toast.js";

const m = vi.hoisted(() => ({
  notice: vi.fn(),
  error: vi.fn(),
  success: vi.fn(),
  activateTab: vi.fn(),
  send: vi.fn(),
}));

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
  notice: m.notice,
  error: m.error,
  success: m.success,
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  activateTab: m.activateTab,
  getActiveTabId: () => "c1",
  tabIdFor: (_kind: string, ref = "") => ref,
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  send: m.send,
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  runLabelOf: (id: string) => (id === "wf-7" ? "Nightly build" : ""),
}));

import { configureApi } from "@cplieger/actions";
import { initActions } from "./actions/boot.js";
import { renameChat } from "./actions/chat.js";
import { cancelRun } from "./actions/runs.js";
import { actionNotice } from "./notice-subject.js";
import { defaultUsage, setSessions } from "./store.js";
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

beforeEach(() => {
  vi.clearAllMocks();
  initActions();
  setSessions([session("c1", "Fix the parser"), session("c2", "Write the docs")]);
});

describe("an action's failure toast", () => {
  it("names an off-screen chat and offers Open onto it", async () => {
    m.send.mockResolvedValue({ ok: false, status: 409, error: "that name is taken" });

    await renameChat.dispatch({ chatID: "c2", name: "Docs" });

    expect(m.notice).toHaveBeenCalledTimes(1);
    const [text, level, open] = m.notice.mock.calls[0] as [
      string,
      string,
      { label: string; onClick: () => void },
    ];
    expect(text).toBe("Write the docs: Couldn't rename the chat: that name is taken");
    expect(level).toBe("error");
    expect(open.label).toBe("Open");
    open.onClick();
    expect(m.activateTab).toHaveBeenCalledWith("c2");
    expect(m.error).not.toHaveBeenCalled();
  });

  it("names the chat by the name it had when the action began", async () => {
    m.send.mockImplementation(() => {
      setSessions([session("c1", "Fix the parser"), session("c2", "Renamed elsewhere")]);
      return Promise.resolve({ ok: false, status: 409, error: "that name is taken" });
    });

    await renameChat.dispatch({ chatID: "c2", name: "Docs" });

    const [text] = m.notice.mock.calls[0] as [string];
    expect(text).toBe("Write the docs: Couldn't rename the chat: that name is taken");
  });

  it("names a chat whose row was removed before the failure landed", async () => {
    m.send.mockImplementation(() => {
      setSessions([session("c1", "Fix the parser")]);
      return Promise.resolve({ ok: false, status: 404, error: "no such chat" });
    });

    await renameChat.dispatch({ chatID: "c2", name: "Docs" });

    const [text] = m.notice.mock.calls[0] as [string];
    expect(text).toBe("Write the docs: Couldn't rename the chat: no such chat");
  });

  it("names an off-screen chat when its retry takes the button from Open", () => {
    const retry = { label: "Retry", onClick: vi.fn() };

    actionNotice("c2", "Couldn't rename the chat: network error", "error", retry);

    expect(m.error).toHaveBeenCalledWith(
      "Write the docs: Couldn't rename the chat: network error",
      retry,
    );
    expect(m.notice).not.toHaveBeenCalled();
  });

  it("names the run a run action was about", async () => {
    configureApi({
      fetchFn: () =>
        Promise.resolve(
          new Response(JSON.stringify({ error: "the run already ended" }), {
            status: 409,
            headers: { "content-type": "application/json" },
          }),
        ),
    });

    await cancelRun.dispatch("wf-7");

    expect(m.error).toHaveBeenCalledWith(
      "Nightly build: Could not cancel the run: the run already ended",
      undefined,
    );
    expect(m.notice).not.toHaveBeenCalled();
  });
});
