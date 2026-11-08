// A step's hooks in the run pane: the hook fold's rules through the pane's own production path
// (`createRunStepStream` over the real dispatcher), so a rule the chat applies and the run tab skips fails here.

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";
import type { Turn } from "./turns.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";
import type { RunStepStream } from "./run-chat-steps.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}

const { resetBlockRenders, initBlockRenderer } = await import("./messages-blocks.js");
const { clearAllEntrySigs, toolCallSigs } = await import("./store-signals.js");
const { initToolCallbacks } = await import("./messages-tools.js");
const { refreshGroupHeader } = await import("./tool-group.js");
const { setHookStatusShown } = await import("./hook-fold.js");
const { createRunStepStream } = await import("./run-chat-steps.js");

initToolCallbacks({ refreshGroupHeader });
initBlockRenderer({
  pushEntryEffect: (): void => {
    /* noop */
  },
  disposeEntryEffects: (): void => {
    /* noop */
  },
  liveAnchorChanged: () => undefined,
  makeRow: () => {
    const row = document.createElement("div");
    row.className = "msg-row";
    return row;
  },
  makeEvent: (e: Entry) => {
    const row = document.createElement("div");
    row.className = `event event-${e.kind}`;
    return row;
  },
});

const NODE = "review";

function call(turnID: string, seq: number, over: Partial<EntryToolCall>): Entry {
  const payload: EntryToolCall = {
    id: `tool-${String(seq)}`,
    title: "Run Command",
    status: "completed",
    kind: "execute",
    ts: 1,
    ...over,
  };
  return {
    id: `${turnID}-c${String(seq)}`,
    turn: turnID,
    kind: "tool_call",
    seq,
    ts: seq + 1,
    payload,
  };
}

function hook(turnID: string, seq: number, name: string, over: Partial<EntryToolCall> = {}): Entry {
  return call(turnID, seq, {
    id: `hook-op-${turnID}-${String(seq)}`,
    title: `Hook fired: ${name}`,
    kind: "hook",
    source_path: `.kiro/hooks/${name.replaceAll(" ", "-")}.json`,
    hook_trigger: "PreToolUse",
    ...over,
  } as Partial<EntryToolCall>);
}

function stepTurn(turnID: string, body: Entry[]): Turn {
  return {
    id: turnID,
    n: 1,
    trigger: undefined,
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
}

let seq = 0;
let host: HTMLElement;
let stream: RunStepStream;

/** Paint one settled step turn into the pane and return its host. */
function paintStep(build: (turnID: string) => Entry[]): HTMLElement {
  seq += 1;
  const turnID = `t-run-hooks-${String(seq)}`;
  stream.apply(new Map([[NODE, [{ turn: stepTurn(turnID, build(turnID)), live: false }]]]));
  return host;
}

function shownHookTitles(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".tool-call")]
    .filter((c) => c.dataset["kind"] === "hook" && !c.hidden && c.closest("[hidden]") === null)
    .map((c) => c.dataset["title"] ?? "");
}

function badgeText(root: HTMLElement): string | null {
  const b = root.querySelector<HTMLElement>(".tool-group-hooks");
  return b === null || b.hidden ? null : (b.textContent ?? "");
}

beforeEach(() => {
  resetBlockRenders();
  clearAllEntrySigs();
  toolCallSigs.clearAll();
  setHookStatusShown(true);
  host = document.createElement("div");
  host.className = "run-step-body";
  document.body.appendChild(host);
  stream = createRunStepStream(() => host);
});

afterEach(() => {
  stream.dispose();
  host.remove();
  resetBlockRenders();
  setHookStatusShown(true);
});

describe("a step's hooks in the run pane", () => {
  it("folds a passing tool-trigger hook into its command's badge, hidden from find and listed by name", async () => {
    const root = paintStep((t) => [call(t, 1, { id: "cmd-a" }), hook(t, 2, "seat guard")]);
    expect(shownHookTitles(root)).toEqual([]);
    expect(badgeText(root)).toContain("1");
    const folded = root.querySelector<HTMLElement>('.tool-call[data-kind="hook"]');
    expect(folded?.hidden).toBe(true);
    expect(folded?.hasAttribute("data-hook-folded")).toBe(true);
    const badge = root.querySelector<HTMLButtonElement>(".tool-group .tool-group-hooks");
    expect(badge).not.toBeNull();
    await userEvent.click(badge as HTMLButtonElement);
    await expect
      .poll(() =>
        [...document.querySelectorAll(".tool-hook-popover .tool-hook-item .tool-title")].map(
          (t) => t.textContent,
        ),
      )
      .toEqual(["seat guard"]);
  });

  it("keeps a failed hook and a hook that printed as rows", () => {
    const root = paintStep((t) => [
      call(t, 1, { id: "cmd-a" }),
      hook(t, 2, "broken", { status: "failed", output: "exit 1" }),
      hook(t, 3, "printer", { output: "lint: 2 warnings" }),
      hook(t, 4, "seat guard"),
    ]);
    expect(shownHookTitles(root)).toEqual(["Hook fired: broken", "Hook fired: printer"]);
    expect(badgeText(root)).toContain("1");
  });

  it("unfolds the hooks of a denied command", () => {
    const root = paintStep((t) => [
      call(t, 1, {
        id: "cmd-d",
        title: "rm -rf /",
        status: "failed",
        denial: { capability: "shell", resource: "rm -rf /", scope: "workspace", source: "policy" },
      } as Partial<EntryToolCall>),
      hook(t, 2, "seat guard"),
    ]);
    expect(shownHookTitles(root)).toEqual(["Hook fired: seat guard"]);
    expect(badgeText(root)).toBeNull();
  });

  it("hides the badge and every quiet hook with Show hook status off, keeping the failed one", () => {
    setHookStatusShown(false);
    const root = paintStep((t) => [
      call(t, 1, { id: "cmd-a" }),
      hook(t, 2, "seat guard"),
      hook(t, 3, "broken", { status: "failed" }),
      hook(t, 4, "stop check", { hook_trigger: "Stop" }),
    ]);
    expect(shownHookTitles(root)).toEqual(["Hook fired: broken"]);
    expect(badgeText(root)).toBeNull();
    expect(root.querySelector('.tool-call[data-title="Hook fired: stop check"]')).toBeNull();
  });

  it("follows a flip of Show hook status on a mounted step", () => {
    const root = paintStep((t) => [
      call(t, 1, { id: "cmd-a" }),
      hook(t, 2, "seat guard"),
      hook(t, 3, "stop check", { hook_trigger: "Stop" }),
    ]);
    expect(shownHookTitles(root)).toEqual([]);
    expect(badgeText(root)).toContain("2");

    setHookStatusShown(false);
    expect(shownHookTitles(root)).toEqual([]);
    expect(badgeText(root)).toBeNull();

    setHookStatusShown(true);
    expect(shownHookTitles(root)).toEqual([]);
    expect(badgeText(root)).toContain("2");
  });

  it("folds a quiet stop hook into the badge once Show hook status turns on after the step mounted without it", () => {
    setHookStatusShown(false);
    const root = paintStep((t) => [
      call(t, 1, { id: "cmd-a" }),
      hook(t, 2, "stop check", { hook_trigger: "Stop" }),
    ]);
    expect(shownHookTitles(root)).toEqual([]);
    expect(badgeText(root)).toBeNull();
    setHookStatusShown(true);
    expect(shownHookTitles(root)).toEqual([]);
    expect(badgeText(root)).toContain("1");
  });
});
