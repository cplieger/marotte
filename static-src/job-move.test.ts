// Move to background on a running command's card: shown only while kiro-cli could move the call, one press posts the
// call, and any answer ends the control, an error as its toast.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ToolCall } from "./types.js";
import type { BackgroundJobMoveRequest, EffectiveSettings } from "./wire/types.gen.js";

// The real chrome reaches `scroll.ts`, which throws on a missing id at module load.
for (const id of ["messages", "messages-wrap", "messages-wrap-outer", "chat-view"]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}
if (document.getElementById("scroll-bottom") === null) {
  const scrollBottom = document.createElement("div");
  scrollBottom.id = "scroll-bottom";
  scrollBottom.appendChild(document.createElement("span"));
  document.body.appendChild(scrollBottom);
}

vi.mock("./notice-subject.js", { spy: true });

const { initActions } = await import("./actions/boot.js");
const { actionNotice } = await import("./notice-subject.js");
const { shownSettingsSignal } = await import("./persist.js");
const { buildToolCard } = await import("./tool-card.js");
const { ensureToolCallSig } = await import("./store-signals.js");
const { mountToolCallCard, disposeAllToolEffects } = await import("./messages-tools.js");
const { syncMoveAction, _resetForTest } = await import("./job-move.js");

initActions();

function running(over: Partial<ToolCall> = {}): ToolCall {
  return {
    id: "tc-1",
    title: "Running: sleep 60",
    kind: "execute",
    status: "in_progress",
    ts: 0,
    ...over,
  } as ToolCall;
}

function card(tc: ToolCall): HTMLDivElement {
  const c = buildToolCard({
    id: tc.id,
    title: tc.title,
    kind: tc.kind,
    status: tc.status,
    live: true,
    input: { command: "sleep 60" },
    output: "waiting",
  });
  document.body.appendChild(c);
  return c;
}

const control = (c: HTMLElement): HTMLButtonElement | null =>
  c.querySelector<HTMLButtonElement>(".tool-header > .tool-move-bg");

function jobsOn(on: boolean): void {
  shownSettingsSignal.value = { background_jobs: on } as EffectiveSettings;
}

const posts: { path: string; body: unknown }[] = [];
let answer: () => Response;

beforeEach(() => {
  posts.length = 0;
  answer = () => Response.json({ outcome: "moved", job_id: "7" });
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(input instanceof Request ? input.url : String(input), location.href)
        .pathname;
      posts.push({
        path,
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      });
      return Promise.resolve(answer());
    }),
  );
  vi.mocked(actionNotice).mockClear();
  jobsOn(true);
});

afterEach(() => {
  disposeAllToolEffects();
  vi.unstubAllGlobals();
  shownSettingsSignal.value = null;
  _resetForTest();
  for (const c of document.querySelectorAll(".tool-call")) {
    c.remove();
  }
});

describe("the gate", () => {
  it("shows on a running shell call with Background jobs on", () => {
    const tc = running();
    const c = card(tc);

    syncMoveAction(c, tc, "c1");

    const btn = control(c);
    expect(btn?.textContent).toBe("Move to background");
    expect(btn?.getAttribute("aria-label")).toBe("Move to background: Running: sleep 60");
    expect(btn?.type).toBe("button");
  });

  const hidden: [string, () => void, Partial<ToolCall>, string][] = [
    [
      "Background jobs off",
      () => {
        jobsOn(false);
      },
      {},
      "c1",
    ],
    [
      "no settings read yet",
      () => {
        shownSettingsSignal.value = null;
      },
      {},
      "c1",
    ],
    ["waiting for approval", () => undefined, { status: "pending" }, "c1"],
    ["completed", () => undefined, { status: "completed" }, "c1"],
    ["already a job", () => undefined, { job: { id: "7" } }, "c1"],
    ["a subagent's or step's call", () => undefined, { agent_subtask_id: "sub-1" }, "c1"],
    ["a command in marotte's terminal", () => undefined, { terminal_id: "term-1" }, "c1"],
    ["another kind of call", () => undefined, { kind: "other" }, "c1"],
    ["no chat", () => undefined, {}, ""],
  ];
  for (const [name, arrange, over, chatID] of hidden) {
    it(`is hidden: ${name}`, () => {
      const tc = running(over);
      const c = card(running());
      syncMoveAction(c, running(), "c1");
      expect(control(c)).not.toBeNull();
      arrange();

      syncMoveAction(c, tc, chatID);

      expect(control(c)).toBeNull();
    });
  }
});

describe("a press", () => {
  for (const [name, res] of [
    ["moved", () => Response.json({ outcome: "moved", job_id: "7" })],
    ["ended", () => Response.json({ outcome: "ended" })],
    ["not movable", () => Response.json({ outcome: "not_movable" })],
  ] as const) {
    it(`posts the call and the control goes on ${name}, with no notice`, async () => {
      answer = res;
      const tc = running();
      const c = card(tc);
      syncMoveAction(c, tc, "c1");

      control(c)?.click();

      expect(control(c)?.disabled).toBe(true);
      expect(control(c)?.getAttribute("aria-busy")).toBe("true");
      await vi.waitFor(() => {
        expect(control(c)).toBeNull();
      });
      const want: BackgroundJobMoveRequest = { tool_call_id: "tc-1" };
      expect(posts).toEqual([{ path: "/api/chats/c1/jobs", body: want }]);
      expect(actionNotice).not.toHaveBeenCalled();
      // Answered: the next snapshot of the still running call does not bring it back.
      syncMoveAction(c, tc, "c1");
      expect(control(c)).toBeNull();
    });
  }

  for (const [name, status, message] of [
    ["kiro-cli could not move it", 502, "kiro-cli could not move the command to the background."],
    ["kiro-cli refused it", 409, "Background jobs are not enabled for this session."],
  ] as const) {
    it(`raises the server's words when ${name}, and the control goes`, async () => {
      answer = () => Response.json({ error: message }, { status });
      const tc = running();
      const c = card(tc);
      syncMoveAction(c, tc, "c1");

      control(c)?.click();

      await vi.waitFor(() => {
        expect(control(c)).toBeNull();
      });
      expect(vi.mocked(actionNotice).mock.calls.map((call) => [call[1], call[2]])).toEqual([
        [message, "error"],
      ]);
    });
  }

  it("keeps the control for another press when the post never reached the server", async () => {
    let fail = true;
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        fail ? Promise.reject(new TypeError("Failed to fetch")) : Promise.resolve(answer()),
      ),
    );
    const tc = running();
    const c = card(tc);
    syncMoveAction(c, tc, "c1");

    control(c)?.click();

    await vi.waitFor(() => {
      expect(control(c)?.disabled).toBe(false);
    });
    expect(control(c)?.hasAttribute("aria-busy")).toBe(false);
    syncMoveAction(c, tc, "c1");
    expect(control(c)).not.toBeNull();

    fail = false;
    control(c)?.click();

    await vi.waitFor(() => {
      expect(control(c)).toBeNull();
    });
  });

  it("does not toggle the card's details", () => {
    const tc = running();
    const c = card(tc);
    syncMoveAction(c, tc, "c1");
    const toggle = c.querySelector(".tool-disclosure");
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");

    control(c)?.click();

    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("on a mounted card", () => {
  it("appears when the setting turns on with no tool frame", () => {
    jobsOn(false);
    const c = mountToolCallCard("c-on", running());
    document.body.appendChild(c);
    expect(control(c)).toBeNull();

    jobsOn(true);

    expect(control(c)).not.toBeNull();
  });

  it("goes when the job mark lands", () => {
    const c = mountToolCallCard("c-job", running());
    document.body.appendChild(c);
    expect(control(c)).not.toBeNull();

    ensureToolCallSig("c-job", "tc-1", running()).value = running({ job: { id: "7" } });

    expect(control(c)).toBeNull();
    expect(c.querySelector(".tool-job")).not.toBeNull();
  });

  it("goes when the call settles", () => {
    const c = mountToolCallCard("c-done", running());
    document.body.appendChild(c);
    expect(control(c)).not.toBeNull();

    ensureToolCallSig("c-done", "tc-1", running()).value = running({ status: "completed" });

    expect(control(c)).toBeNull();
  });
});
