// A session change kiro-cli has not answered yet reads as still applying, never as failed: the command
// endpoint's 504 with reason still_applying (internal/command/engine_change.go), or the transport's timeout.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Transport from "../transport.js";
import type * as NoticeSubject from "../notice-subject.js";
import type * as Toast from "../toast.js";

vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("../transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  send: vi.fn(),
}));
vi.mock("../notice-subject.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NoticeSubject>()),
  actionNotice: vi.fn(),
}));

import { send as transportSend, type SendResult } from "../transport.js";
import { actionNotice } from "../notice-subject.js";
import * as toast from "../toast.js";
import { get, setSessions } from "../store.js";
import { makeSession } from "../__test-helpers__/model.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { initActions } from "./boot.js";
import { compactChat, forkChat, setMode, setSupervised, switchModel } from "./chat.js";
import { rewindChat } from "./rewind.js";
import { setEffortAction, setThinkingOffAction } from "../model-switcher.js";

const mockSend = vi.mocked(transportSend);

// What transport.send answers for the server's still_applying envelope: the sentence plus its reason.
const stillApplying: SendResult = {
  ok: false,
  status: 504,
  error:
    "kiro-cli has not answered yet and may still apply the change; the chat updates when it does",
  reason: "still_applying",
};
const timedOut: SendResult = { ok: false, status: 0, error: "Request timed out", code: "timeout" };
const refused: SendResult = { ok: false, status: 502, error: "kiro-cli refused" };

beforeEach(() => {
  resetActionFramework();
  vi.clearAllMocks();
  setSessions([makeSession({ id: "c1", current_mode_id: "kiro_default", effort: "high" })]);
});

function errors(): unknown[] {
  return vi.mocked(toast.error).mock.calls.map((c) => c[0]);
}

const cases: { what: string; run: () => Promise<unknown>; still: string; failed: string }[] = [
  {
    what: "a mode switch",
    run: () => setMode.dispatch({ chatID: "c1", modeID: "plan" }),
    still: "Still switching mode; the chat updates when it lands",
    failed: "Could not switch mode: kiro-cli refused",
  },
  {
    what: "an effort pick",
    run: () => setEffortAction.dispatch({ chatID: "c1", level: "max" }),
    still: "Still setting reasoning effort; the chat updates when it lands",
    failed: "Could not set reasoning effort: kiro-cli refused",
  },
  {
    what: "turning thinking off",
    run: () => setThinkingOffAction.dispatch({ chatID: "c1" }),
    still: "Still turning thinking off; the chat updates when it lands",
    failed: "Couldn't turn thinking off: kiro-cli refused",
  },
  {
    what: "a supervised-mode toggle",
    run: () => setSupervised.dispatch({ chatID: "c1", enabled: true }),
    still: "Still updating supervised mode; the chat updates when it lands",
    failed: "Could not update supervised mode: kiro-cli refused",
  },
  {
    what: "a model switch",
    run: () => switchModel.dispatch({ chatID: "c1", model: "model-a-5" }),
    still: "Still switching model; the chat updates when it lands",
    failed: "Could not switch model: kiro-cli refused",
  },
  {
    what: "a tangent",
    run: () => forkChat.dispatch({ opID: "op-1", parentChatID: "c1" }),
    still: "Still starting the tangent; its tab appears when kiro-cli answers",
    failed: "Could not start a tangent: kiro-cli refused",
  },
  {
    what: "a rewind",
    run: () => rewindChat.dispatch({ chatID: "c1", messageID: "m-1" }),
    still: "Still rewinding the chat; the chat updates when it lands",
    failed: "Could not rewind chat: kiro-cli refused",
  },
  {
    what: "a compact",
    run: () => compactChat.dispatch({ chatID: "c1" }),
    still: "Still starting the compaction; the chat updates when it lands",
    failed: "Compact failed: kiro-cli refused",
  },
];

describe.each(cases)("$what", ({ run, still, failed }) => {
  it("says it is still applying when the server answers still_applying, and sends it once", async () => {
    mockSend.mockResolvedValue(stillApplying);
    await run();
    expect(errors()).toEqual([still]);
    // A 5xx frees the Idempotency-Key slot, so a retry would apply the change a second time.
    expect(mockSend).toHaveBeenCalledTimes(1);
  });

  it("says it is still applying when the transport gives up waiting", async () => {
    mockSend.mockResolvedValue(timedOut);
    await run();
    expect(errors()).toEqual([still]);
  });

  it("still says it failed for a refusal", async () => {
    mockSend.mockResolvedValue(refused);
    await run();
    expect(errors()).toEqual([failed]);
  });
});

describe("a mode switch kiro-cli has not answered", () => {
  it("shows the chat's confirmed mode until the late change's broadcast arrives", async () => {
    mockSend.mockResolvedValue(stillApplying);
    await setMode.dispatch({ chatID: "c1", modeID: "plan" });
    expect(get("c1")?.current_mode_id).toBe("kiro_default");
  });
});

describe("the app's own action wiring", () => {
  it("reads the server's still_applying reason", async () => {
    initActions();
    mockSend.mockResolvedValue(stillApplying);
    await setMode.dispatch({ chatID: "c1", modeID: "plan" });
    expect(vi.mocked(actionNotice).mock.calls.map((c) => [c[1], c[2]])).toEqual([
      ["Still switching mode; the chat updates when it lands", "error"],
    ]);
  });
});
