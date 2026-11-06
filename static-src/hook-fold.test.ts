// A passing hook folds into its tool group's header badge, whatever its trigger; one that printed or failed keeps its
// row. Rendered through the production dispatcher, so the fold, the header and the window's predicates are the
// shipped ones.

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";
import type { Turn } from "./turns.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

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

const { buildAssistantBody, resetBlockRenders, initBlockRenderer, dropHead, mountHeadRange } =
  await import("./messages-blocks.js");
const { rendersNothing } = await import("./block-window.js");
const { clearAllEntrySigs, ensureToolCallSig, toolCallSigs } = await import("./store-signals.js");
const { initToolCallbacks } = await import("./messages-tools.js");
const { refreshGroupHeader } = await import("./tool-group.js");
const { setActive } = await import("./store.js");
const { setHookStatusShown } = await import("./hook-fold.js");
const { hookTriggerLabel } = await import("./hook-badge.js");

const CHAT_ID = "c-hook-fold";
setActive(CHAT_ID);
// As messages.ts wires it: a card's settle refreshes its group.
initToolCallbacks({ refreshGroupHeader });

initBlockRenderer({
  pushEntryEffect: (): void => {
    /* noop */
  },
  disposeEntryEffects: (): void => {
    /* noop */
  },
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

let turnSeq = 0;
let hosts: HTMLElement[] = [];

function entry(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id: string,
): Entry {
  return { id, turn: turnID, kind, seq, ts: seq + 1, payload };
}

function call(turnID: string, seq: number, over: Partial<EntryToolCall>): Entry {
  const base: EntryToolCall = {
    id: `tool-${String(seq)}`,
    title: "Run Command",
    status: "completed",
    kind: "execute",
    ts: 1,
  };
  return entry(turnID, seq, "tool_call", Object.assign(base, over), `${turnID}-c${String(seq)}`);
}

const GUARDS = [
  { name: "animap seat guard", file: ".kiro/hooks/animap-seat-guard.json" },
  { name: "Destructive command guard", file: ".kiro/hooks/destructive-guard.json" },
] as const;

function hook(turnID: string, seq: number, name: string, over: Partial<EntryToolCall> = {}): Entry {
  return call(turnID, seq, {
    id: `hook-op-${String(seq)}`,
    title: `Hook fired: ${name}`,
    kind: "hook",
    source_path: `.kiro/hooks/${name.replaceAll(" ", "-")}.json`,
    hook_trigger: "PreToolUse",
    ...over,
  } as Partial<EntryToolCall>);
}

/** A hook the trigger cache could not name: the server stamps no trigger. */
function untriggered(e: Entry): Entry {
  const { hook_trigger: _dropped, ...call } = e.payload as EntryToolCall;
  return { ...e, payload: call };
}

/** The user's case: each command followed by its two passing guards. */
function guardedCommands(turnID: string, n: number, from = 1): Entry[] {
  const out: Entry[] = [];
  let seq = from;
  for (let i = 0; i < n; i++) {
    out.push(call(turnID, seq, { id: `cmd-${String(i)}`, title: `cmd ${String(i)}` }));
    seq++;
    for (const g of GUARDS) {
      out.push(hook(turnID, seq, g.name, { source_path: g.file }));
      seq++;
    }
  }
  return out;
}

function turnOf(turnID: string, body: Entry[]): Turn {
  return {
    id: turnID,
    n: 1,
    trigger: { id: `${turnID}-p`, text: "go" },
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
}

interface Render {
  readonly turnID: string;
  readonly body: HTMLElement;
}

function render(build: (turnID: string) => Entry[]): Render {
  turnSeq += 1;
  const turnID = `t-hf-${String(turnSeq)}`;
  const host = document.createElement("div");
  host.className = "turn-body";
  document.body.appendChild(host);
  hosts.push(host);
  buildAssistantBody(host, turnOf(turnID, build(turnID)), CHAT_ID, false);
  return { turnID, body: host };
}

/** Cards a reader sees as rows: mounted and not hidden. */
function shownRows(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(".tool-call")].filter(
    (c) => !c.hidden && c.closest("[hidden]") === null,
  );
}

function shownHookRows(root: HTMLElement): HTMLElement[] {
  return shownRows(root).filter((c) => c.dataset["kind"] === "hook");
}

function header(root: HTMLElement): HTMLElement {
  const h = root.querySelector<HTMLElement>(".tool-group-header");
  if (h === null) {
    throw new Error("no group header");
  }
  return h;
}

function badge(root: HTMLElement): HTMLElement | null {
  const b = root.querySelector<HTMLElement>(".tool-group-hooks");
  return b === null || b.hidden ? null : b;
}

function hooksButton(root: HTMLElement): HTMLButtonElement {
  const b = root.querySelector<HTMLButtonElement>(".tool-group > .tool-group-hooks");
  if (b === null) {
    throw new Error("no hooks badge button");
  }
  return b;
}

function popoverPanel(): HTMLElement | null {
  return document.querySelector<HTMLElement>(".tool-hook-popover");
}

/** The popover's entries as [count, name], top to bottom. */
function popoverEntries(): string[][] {
  return [...(popoverPanel()?.querySelectorAll<HTMLElement>(".tool-hook-item") ?? [])].map((li) => [
    li.querySelector(".tool-hook-count")?.textContent ?? "",
    li.querySelector(".tool-title")?.textContent ?? "",
  ]);
}

/** The popover's entries' trigger lines, top to bottom. */
function popoverTriggers(): string[] {
  return [...(popoverPanel()?.querySelectorAll(".tool-hook-trigger") ?? [])].map(
    (t) => t.textContent,
  );
}

/** One command followed by a passing run of each named hook, in order. */
function commandWithHooks(...names: string[]): (turnID: string) => Entry[] {
  return (t) => [
    call(t, 1, { id: "cmd-a", title: "go vet" }),
    ...names.map((n, i) => hook(t, 2 + i, n)),
  ];
}

beforeEach(() => {
  resetBlockRenders();
  clearAllEntrySigs();
  toolCallSigs.clearAll();
  setHookStatusShown(true);
});

afterEach(() => {
  resetBlockRenders();
  for (const panel of document.querySelectorAll(".tool-hook-popover")) {
    panel.remove();
  }
  for (const host of hosts) {
    host.remove();
  }
  hosts = [];
  setHookStatusShown(true);
});

describe("passing tool-trigger hooks fold into the group header", () => {
  it("draws four commands with an 8 badge and no hook rows", () => {
    const r = render((t) => guardedCommands(t, 4));
    expect(shownHookRows(r.body)).toHaveLength(0);
    expect(shownRows(r.body)).toHaveLength(4);
    const count = header(r.body).querySelector(".tool-group-count")?.textContent ?? "";
    expect(count.startsWith("Ran 4 commands")).toBe(true);
    expect(count).not.toContain("hook");
    expect(badge(r.body)?.textContent).toContain("8");
    expect(hooksButton(r.body).getAttribute("aria-label")).toBe("8 hooks ran");
  });

  it("keeps a lone command with its two guards a group: Ran 1 command with a 2 badge", () => {
    const r = render((t) => guardedCommands(t, 1));
    const group = r.body.querySelector<HTMLElement>(".tool-group");
    expect(group?.classList.contains("tool-group-bare")).toBe(false);
    expect(header(r.body).querySelector(".tool-group-count")?.textContent).toMatch(
      /^Ran 1 command/,
    );
    expect(badge(r.body)?.textContent).toContain("2");
    expect(hooksButton(r.body).getAttribute("aria-label")).toBe("2 hooks ran");
  });

  it("shows only the badge while the group is collapsed", () => {
    const r = render((t) => [
      ...guardedCommands(t, 2),
      entry(t, 7, "text", { text: "done" }, `${t}-say#7`),
    ]);
    const group = r.body.querySelector<HTMLElement>(".tool-group");
    expect(group?.classList.contains("tool-group-collapsed")).toBe(true);
    expect(badge(r.body)?.textContent).toContain("4");
    expect(r.body.querySelector(".tool-hook-list")).toBeNull();
  });
});

describe("passing turn hooks fold into the group they sit in", () => {
  it("folds a prompt-submit hook and an unresolved-trigger hook, counting them in the badge", async () => {
    const r = render((t) => [
      hook(t, 1, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      untriggered(hook(t, 2, "mystery")),
      call(t, 3, { id: "cmd-a" }),
      hook(t, 4, GUARDS[0].name),
    ]);
    expect(shownHookRows(r.body)).toHaveLength(0);
    expect(hooksButton(r.body).getAttribute("aria-label")).toBe("3 hooks ran");
    await userEvent.click(hooksButton(r.body));
    await expect
      .poll(() => popoverTriggers())
      .toEqual(["at turn start (prompt sent)", "trigger unknown", "before a tool runs"]);
  });

  it("folds a stop hook after the group's last command", async () => {
    const r = render((t) => [
      call(t, 1, { id: "cmd-a" }),
      hook(t, 2, "stop check", { hook_trigger: "Stop" }),
    ]);
    expect(shownHookRows(r.body)).toHaveLength(0);
    expect(hooksButton(r.body).getAttribute("aria-label")).toBe("1 hook ran");
    await userEvent.click(hooksButton(r.body));
    await expect.poll(() => popoverTriggers()).toEqual(["at turn end (agent stops)"]);
  });

  it("folds a turn hook in a refused group, where only the tool-trigger hooks unfold", () => {
    const r = render((t) => [
      hook(t, 1, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      call(t, 2, { id: "cmd-x", title: "rm -rf /", status: "failed" }),
      hook(t, 3, GUARDS[0].name),
      untriggered(hook(t, 4, "mystery")),
    ]);
    const shown = shownHookRows(r.body).map((c) => c.dataset["title"]);
    expect(shown).toEqual([`Hook fired: ${GUARDS[0].name}`]);
    expect(hooksButton(r.body).getAttribute("aria-label")).toBe("2 hooks ran");
  });
});

describe("a hook that does something keeps its row", () => {
  it("unfolds every hook of a group whose command is refused, live", () => {
    const r = render((t) => guardedCommands(t, 4));
    expect(shownHookRows(r.body)).toHaveLength(0);
    // The first command, refused by its guard: KAS fails the call with the hook's intercept text, and the store
    // republishes the card's settled value.
    const first = guardedCommands(r.turnID, 1)[0];
    const call = first?.payload as EntryToolCall;
    ensureToolCallSig(CHAT_ID, call.id, call).value = {
      ...call,
      status: "failed",
      output: "blocked",
    };
    expect(shownHookRows(r.body)).toHaveLength(8);
    expect(badge(r.body)).toBeNull();
  });

  it("unfolds the hooks of a policy-denied command", () => {
    const r = render((t) => [
      call(t, 1, {
        id: "cmd-d",
        title: "rm -rf /",
        status: "failed",
        denial: { capability: "shell", resource: "rm -rf /", scope: "workspace", source: "policy" },
      } as Partial<EntryToolCall>),
      hook(t, 2, GUARDS[0].name),
      hook(t, 3, GUARDS[1].name),
    ]);
    expect(shownHookRows(r.body)).toHaveLength(2);
    expect(badge(r.body)).toBeNull();
  });

  it("keeps a hook that printed output and a failed hook as rows, folding the quiet ones", () => {
    const r = render((t) => [
      call(t, 1, { id: "cmd-a" }),
      hook(t, 2, "printer", { output: "lint: 2 warnings" }),
      hook(t, 3, "broken", { status: "failed" }),
      hook(t, 4, GUARDS[1].name),
    ]);
    const shown = shownHookRows(r.body).map((c) => c.dataset["title"]);
    expect(shown).toEqual(["Hook fired: printer", "Hook fired: broken"]);
    expect(badge(r.body)?.textContent).toContain("1");
  });

  it("keeps a printing and a failed turn hook as rows, folding the quiet one", () => {
    const r = render((t) => [
      hook(t, 1, "prompt printer", { hook_trigger: "UserPromptSubmit", output: "3 unread" }),
      hook(t, 2, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      call(t, 3, { id: "cmd-a" }),
      hook(t, 4, "stop broken", { hook_trigger: "Stop", status: "failed" }),
    ]);
    const shown = shownHookRows(r.body).map((c) => c.dataset["title"]);
    expect(shown).toEqual(["Hook fired: prompt printer", "Hook fired: stop broken"]);
    expect(hooksButton(r.body).getAttribute("aria-label")).toBe("1 hook ran");
  });

  it("keeps a quiet turn hook as a row when its group holds no other card", () => {
    const r = render((t) => [
      hook(t, 1, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      entry(t, 2, "text", { text: "on it" }, `${t}-say#2`),
      call(t, 3, { id: "cmd-a" }),
      entry(t, 4, "text", { text: "done" }, `${t}-say#4`),
      hook(t, 5, "stop check", { hook_trigger: "Stop" }),
    ]);
    const shown = shownHookRows(r.body).map((c) => c.dataset["title"]);
    expect(shown).toEqual(["Hook fired: prompt check", "Hook fired: stop check"]);
    expect(r.body.querySelector(".tool-group-hooks")).toBeNull();
  });

  it("leaves hooks out of the header summary: a hook row is no operation", () => {
    const r = render((t) => [
      call(t, 1, { id: "cmd-a", title: "go vet" }),
      call(t, 2, { id: "edit-a", title: "Edit a.ts", kind: "edit" }),
      hook(t, 3, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      hook(t, 4, GUARDS[0].name),
      hook(t, 5, GUARDS[1].name),
    ]);
    const count = header(r.body).querySelector(".tool-group-count")?.textContent ?? "";
    expect(count).toBe("2 operations: 1 command, 1 edit");
    expect(badge(r.body)?.textContent).toContain("3");
  });

  it("still names a failed hook row in the header, without counting it as an operation", () => {
    const r = render((t) => [
      call(t, 1, { id: "cmd-a", title: "go vet" }),
      call(t, 2, { id: "cmd-b", title: "go test" }),
      hook(t, 3, "broken", { status: "failed" }),
    ]);
    const count = header(r.body).querySelector(".tool-group-count")?.textContent ?? "";
    expect(count).toMatch(/^Ran 2 commands: go vet, go test/);
    expect(count).toContain("1 failed");
    expect(count).not.toContain("hook");
  });

  it("draws a group of only quiet tool-trigger hooks as rows: there is no card to fold into", () => {
    const r = render((t) => [hook(t, 1, GUARDS[0].name), hook(t, 2, GUARDS[1].name)]);
    expect(shownHookRows(r.body)).toHaveLength(2);
    expect(badge(r.body)).toBeNull();
  });
});

describe("the hooks badge opens a popover of the folded hooks", () => {
  const GUARD_A = GUARDS[0].name;
  const GUARD_B = GUARDS[1].name;

  it("is a button outside the header's own button, counting runs, with no list in the group", () => {
    const r = render(commandWithHooks(GUARD_A, GUARD_A, GUARD_A, GUARD_B, GUARD_B));
    const b = hooksButton(r.body);
    expect(b.tagName).toBe("BUTTON");
    expect(b.closest(".tool-group-header")).toBeNull();
    expect(b.textContent).toContain("5");
    expect(b.getAttribute("aria-label")).toBe("5 hooks ran");
    expect(r.body.querySelector(".tool-hook-list")).toBeNull();
    expect(popoverPanel()).toBeNull();
  });

  it("lists each distinct hook once with its run count, in order of first appearance", async () => {
    const r = render(commandWithHooks(GUARD_B, GUARD_A, GUARD_B, GUARD_B, GUARD_A));
    await userEvent.click(hooksButton(r.body));
    await expect
      .poll(() => popoverEntries())
      .toEqual([
        ["3×", GUARD_B],
        ["2×", GUARD_A],
      ]);
    expect(hooksButton(r.body).getAttribute("aria-expanded")).toBe("true");
  });

  it("states each entry's trigger in plain words", async () => {
    const r = render((t) => [
      call(t, 1, { id: "cmd-a", title: "go vet" }),
      hook(t, 2, GUARD_A, { hook_trigger: "PreToolUse" }),
      hook(t, 3, GUARD_B, { hook_trigger: "PostToolUse" }),
    ]);
    await userEvent.click(hooksButton(r.body));
    await expect.poll(() => popoverTriggers()).toEqual(["before a tool runs", "after a tool runs"]);
  });

  it("keeps one hook on two triggers as two entries", async () => {
    const r = render((t) => [
      call(t, 1, { id: "cmd-a", title: "go vet" }),
      hook(t, 2, GUARD_A, { hook_trigger: "PreToolUse" }),
      hook(t, 3, GUARD_A, { hook_trigger: "PostToolUse" }),
      hook(t, 4, GUARD_A, { hook_trigger: "PreToolUse" }),
    ]);
    expect(hooksButton(r.body).textContent).toContain("3");
    await userEvent.click(hooksButton(r.body));
    await expect
      .poll(() => popoverEntries())
      .toEqual([
        ["2×", GUARD_A],
        ["1×", GUARD_A],
      ]);
    expect(popoverTriggers()).toEqual(["before a tool runs", "after a tool runs"]);
  });

  it("gives each entry its hook file's chip", async () => {
    const r = render(commandWithHooks(GUARD_A, GUARD_B));
    await userEvent.click(hooksButton(r.body));
    await expect.poll(() => popoverEntries()).toHaveLength(2);
    const labels = [...(popoverPanel()?.querySelectorAll(".tool-file-link") ?? [])].map((l) =>
      l.getAttribute("aria-label"),
    );
    expect(labels).toEqual([
      "Open the hook file animap-seat-guard.json",
      "Open the hook file Destructive-command-guard.json",
    ]);
  });

  it("keeps a failed run of a hook in place and out of the passing count", async () => {
    const r = render((t) => [
      call(t, 1, { id: "cmd-a", title: "go vet" }),
      hook(t, 2, GUARD_A),
      hook(t, 3, GUARD_A),
      hook(t, 4, GUARD_A),
      hook(t, 5, GUARD_A, { status: "failed" }),
    ]);
    const rows = shownHookRows(r.body);
    expect(rows.map((c) => c.dataset["outcome"])).toEqual(["fail"]);
    expect(hooksButton(r.body).textContent).toContain("3");
    await userEvent.click(hooksButton(r.body));
    await expect.poll(() => popoverEntries()).toEqual([["3×", GUARD_A]]);
  });

  it("opens from the keyboard, moves focus inside, and returns it on Escape", async () => {
    const r = render(commandWithHooks(GUARD_A, GUARD_A));
    const b = hooksButton(r.body);
    b.focus();
    await userEvent.keyboard("{Enter}");
    await expect.poll(() => popoverPanel()?.contains(document.activeElement)).toBe(true);
    await userEvent.keyboard("{Escape}");
    await expect.poll(() => popoverPanel()).toBeNull();
    expect(document.activeElement).toBe(b);
    expect(b.getAttribute("aria-haspopup")).toBe("dialog");
    expect(b.getAttribute("aria-expanded")).toBe("false");
    await userEvent.keyboard(" ");
    await expect.poll(() => popoverEntries()).toEqual([["2×", GUARD_A]]);
  });

  it("leaves the group's own fold alone when the badge is pressed", async () => {
    const r = render(commandWithHooks(GUARD_A));
    const head = header(r.body);
    expect(head.getAttribute("aria-expanded")).toBe("true");
    await userEvent.click(hooksButton(r.body));
    expect(head.getAttribute("aria-expanded")).toBe("true");
  });

  it("follows a hook arriving while it is open", async () => {
    const r = render(commandWithHooks(GUARD_A));
    await userEvent.click(hooksButton(r.body));
    await expect.poll(() => popoverEntries()).toEqual([["1×", GUARD_A]]);
    const card = r.body.querySelector<HTMLElement>(
      `.tool-call[data-title="Hook fired: ${GUARD_A}"]`,
    );
    expect(card).not.toBeNull();
    const twin = card?.cloneNode(true) as HTMLElement;
    card?.after(twin);
    refreshGroupHeader(r.body.querySelector<HTMLElement>(".tool-group") as HTMLElement);
    await expect.poll(() => popoverEntries()).toEqual([["2×", GUARD_A]]);
  });
});

describe("hook trigger wording", () => {
  it.each<[string, string]>([
    ["SessionStart", "at agent spawn (session start)"],
    ["SessionEnd", "at session end"],
    ["UserPromptSubmit", "at turn start (prompt sent)"],
    ["Stop", "at turn end (agent stops)"],
    ["PreToolUse", "before a tool runs"],
    ["PostToolUse", "after a tool runs"],
    ["PreTaskExec", "before a spec task"],
    ["PostTaskExec", "after a spec task"],
    ["PostFileCreate", "after a file is created"],
    ["PostFileSave", "after a file is saved"],
    ["PostFileDelete", "after a file is deleted"],
    ["Manual", "run by hand"],
  ])("words %s as %s", (trigger, want) => {
    expect(hookTriggerLabel(trigger)).toBe(want);
  });

  it("shows a trigger it has no wording for as it came, and an unnamed one as unknown", () => {
    expect(hookTriggerLabel("FutureTrigger")).toBe("FutureTrigger");
    expect(hookTriggerLabel("constructor")).toBe("constructor");
    expect(hookTriggerLabel("")).toBe("trigger unknown");
  });
});

describe("Show hook status gates the badge and its popover", () => {
  const A = GUARDS[0].name;

  it("draws the badge with the switch on and none with it off", () => {
    const r = render(commandWithHooks(A, A));
    expect(hooksButton(r.body).textContent).toContain("2");
    setHookStatusShown(false);
    expect(r.body.querySelector(".tool-group-hooks")).toBeNull();
    expect(popoverPanel()).toBeNull();
  });

  it("closes an open popover when the switch turns off, and offers it again when it turns on", async () => {
    const r = render(commandWithHooks(A, A));
    await userEvent.click(hooksButton(r.body));
    await expect.poll(() => popoverEntries()).toEqual([["2×", A]]);
    setHookStatusShown(false);
    await expect.poll(() => popoverPanel()).toBeNull();
    expect(r.body.querySelector(".tool-group-hooks")).toBeNull();
    setHookStatusShown(true);
    await userEvent.click(hooksButton(r.body));
    await expect.poll(() => popoverEntries()).toEqual([["2×", A]]);
  });

  it("keeps a failed hook's row and leaves it out of the badge with the switch off", () => {
    setHookStatusShown(false);
    const r = render((t) => [
      call(t, 1, { id: "cmd-a", title: "go vet" }),
      hook(t, 2, A),
      hook(t, 3, A, { status: "failed" }),
    ]);
    expect(r.body.querySelector(".tool-group-hooks")).toBeNull();
    expect(shownHookRows(r.body).map((c) => c.dataset["outcome"])).toEqual(["fail"]);
  });
});

describe("Show hook status off", () => {
  it("hides the badge and every quiet hook, and keeps the refused group's hooks, the failed hook and the refused card", () => {
    setHookStatusShown(false);
    const r = render((t) => [
      ...guardedCommands(t, 1),
      call(t, 4, { id: "cmd-x", title: "rm -rf /", status: "failed" }),
      hook(t, 5, GUARDS[0].name),
      hook(t, 6, "broken", { status: "failed" }),
      entry(t, 7, "text", { text: "between" }, `${t}-say#7`),
      hook(t, 8, "prompt check", { hook_trigger: "Stop" }),
      ...guardedCommands(t, 1, 9),
    ]);
    const groups = [...r.body.querySelectorAll<HTMLElement>(".tool-group")].filter(
      (g) => !g.hidden,
    );
    expect(groups.map((g) => badge(g))).toEqual([null, null]);
    expect(shownHookRows(r.body).map((c) => c.dataset["title"])).toEqual([
      `Hook fired: ${GUARDS[0].name}`,
      `Hook fired: ${GUARDS[1].name}`,
      `Hook fired: ${GUARDS[0].name}`,
      "Hook fired: broken",
    ]);
    expect(shownRows(r.body).some((c) => c.dataset["title"] === "rm -rf /")).toBe(true);
  });

  it("hides every quiet turn hook, beside a command or a refused one, and draws no badge for them", () => {
    setHookStatusShown(false);
    const r = render((t) => [
      hook(t, 1, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      untriggered(hook(t, 2, "mystery")),
      call(t, 3, { id: "cmd-a" }),
      hook(t, 4, "stop check", { hook_trigger: "Stop" }),
      entry(t, 5, "text", { text: "between" }, `${t}-say#5`),
      hook(t, 6, "spawn check", { hook_trigger: "SessionStart" }),
      call(t, 7, { id: "cmd-x", title: "rm -rf /", status: "failed" }),
    ]);
    expect(shownHookRows(r.body)).toHaveLength(0);
    expect(r.body.querySelector(".tool-group-hooks")).toBeNull();
  });

  it("draws nothing for a wake turn holding only quiet stop hooks", () => {
    setHookStatusShown(false);
    const t = "t-wake";
    const turn = turnOf(t, [
      hook(t, 1, "stop a", { hook_trigger: "Stop" }),
      hook(t, 2, "stop b", { hook_trigger: "Stop" }),
    ]);
    expect(rendersNothing(turn)).toBe(true);
    setHookStatusShown(true);
    expect(rendersNothing(turn)).toBe(false);
  });

  it("brings the badge back when the switch turns on again", () => {
    setHookStatusShown(false);
    const r = render((t) => guardedCommands(t, 2));
    expect(badge(r.body)).toBeNull();
    setHookStatusShown(true);
    expect(badge(r.body)?.textContent).toContain("4");
  });
});

describe("a group the window drops", () => {
  /** Two guarded runs split by prose: seqs 1-6 and 8-13, each command followed by its two guards. */
  function twoRuns(t: string): Entry[] {
    return [
      ...guardedCommands(t, 2, 1),
      entry(t, 7, "text", { text: "between" }, `${t}-say#7`),
      ...guardedCommands(t, 2, 8),
    ];
  }

  function mount(t: string): { body: HTMLElement; turn: Turn } {
    const turn = turnOf(t, twoRuns(t));
    const body = document.createElement("div");
    body.className = "turn-body";
    document.body.appendChild(body);
    hosts.push(body);
    buildAssistantBody(body, turn, CHAT_ID, false);
    return { body, turn };
  }

  it("leaves no empty shell when every card of a group goes", () => {
    const { body, turn } = mount("t-drop-whole");
    dropHead(turn, { from: 7, to: 14 });
    const groups = [...body.querySelectorAll<HTMLElement>(".tool-group")];
    expect(groups).toHaveLength(1);
    expect(groups[0]?.querySelectorAll(".tool-call")).toHaveLength(6);
  });

  it("mounts the dropped group again when the window grows back over it", () => {
    const { body, turn } = mount("t-drop-regrow");
    dropHead(turn, { from: 7, to: 14 });
    mountHeadRange(turn, { from: 1, to: 14 }, false);
    expect(body.querySelector(".entry-fallback")).toBeNull();
    const seqs = [...body.querySelectorAll<HTMLElement>(".tool-call")].map((c) =>
      Number(c.dataset["entrySeq"]),
    );
    expect(seqs).toEqual([1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 12, 13]);
    expect(badge(body)?.textContent).toContain("4");
  });

  it("recounts a group that keeps some of its cards", async () => {
    const { body, turn } = mount("t-drop-part");
    dropHead(turn, { from: 4, to: 14 });
    const first = body.querySelector<HTMLElement>(".tool-group")!;
    expect(first.querySelectorAll(".tool-call")).toHaveLength(3);
    expect(badge(first)?.textContent).toContain("2");
    await userEvent.click(hooksButton(first));
    await expect.poll(() => popoverEntries().map(([n]) => n)).toEqual(["1\u00d7", "1\u00d7"]);
  });
});
