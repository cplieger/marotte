// A passing tool-trigger hook folds into its tool group's header badge; every other hook keeps its row. Rendered
// through the production dispatcher, so the fold, the header and the window's predicates are the shipped ones.

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

const { buildAssistantBody, resetBlockRenders, initBlockRenderer } =
  await import("./messages-blocks.js");
const { rendersNothing } = await import("./block-window.js");
const { clearAllEntrySigs, ensureToolCallSig, toolCallSigs } = await import("./store-signals.js");
const { initToolCallbacks } = await import("./messages-tools.js");
const { refreshGroupHeader } = await import("./tool-group.js");
const { setActive } = await import("./store.js");
const { setHookStatusShown } = await import("./hook-fold.js");

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

/** The text an assistive technology reads off an element: aria-hidden subtrees skipped. */
function spokenText(node: Node): string {
  if (node instanceof HTMLElement && node.getAttribute("aria-hidden") === "true") {
    return "";
  }
  if (node.nodeType === Node.TEXT_NODE) {
    return node.textContent ?? "";
  }
  return [...node.childNodes].map(spokenText).join("");
}

beforeEach(() => {
  resetBlockRenders();
  clearAllEntrySigs();
  toolCallSigs.clearAll();
  setHookStatusShown(true);
});

afterEach(() => {
  resetBlockRenders();
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
    expect(spokenText(header(r.body)).trimEnd().endsWith(", 8 hooks ran")).toBe(true);
  });

  it("keeps a lone command with its two guards a group: Ran 1 command with a 2 badge", () => {
    const r = render((t) => guardedCommands(t, 1));
    const group = r.body.querySelector<HTMLElement>(".tool-group");
    expect(group?.classList.contains("tool-group-bare")).toBe(false);
    expect(header(r.body).querySelector(".tool-group-count")?.textContent).toMatch(
      /^Ran 1 command/,
    );
    expect(badge(r.body)?.textContent).toContain("2");
    expect(spokenText(header(r.body)).trimEnd().endsWith(", 2 hooks ran")).toBe(true);
  });

  it("lists the folded hooks with their file links when the group is open, each reachable by Tab", async () => {
    const r = render((t) => guardedCommands(t, 1));
    const list = r.body.querySelector<HTMLElement>(".tool-hook-list");
    expect(list).not.toBeNull();
    const items = [...(list?.querySelectorAll<HTMLElement>("li") ?? [])];
    expect(items.map((li) => li.textContent.trim())).toEqual(
      GUARDS.map((g) => expect.stringContaining(`Hook fired: ${g.name}`) as unknown as string),
    );
    const links = [...(list?.querySelectorAll<HTMLElement>(".tool-file-link") ?? [])];
    expect(links.map((l) => l.getAttribute("aria-label"))).toEqual(
      GUARDS.map((g) => `Open the hook file ${g.file.split("/").pop() ?? ""}`),
    );
    // The command card has nothing to disclose, so the header's next tab stop is the list's first link.
    header(r.body).focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(links[0]);
    await userEvent.tab();
    expect(document.activeElement).toBe(links[1]);
  });

  it("shows only the badge while the group is collapsed", () => {
    const r = render((t) => [
      ...guardedCommands(t, 2),
      entry(t, 7, "text", { text: "done" }, `${t}-say#7`),
    ]);
    const group = r.body.querySelector<HTMLElement>(".tool-group");
    expect(group?.classList.contains("tool-group-collapsed")).toBe(true);
    expect(badge(r.body)?.textContent).toContain("4");
    const list = r.body.querySelector<HTMLElement>(".tool-hook-list");
    // Inside the closed body region, so nothing of it is in view.
    expect(list === null || list.closest(".tool-group-body") !== null).toBe(true);
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

  it("keeps a prompt-submit hook and an unresolved-trigger hook as rows", () => {
    const r = render((t) => [
      hook(t, 1, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      untriggered(hook(t, 2, "mystery")),
      call(t, 3, { id: "cmd-a" }),
      hook(t, 4, GUARDS[0].name),
    ]);
    const shown = shownHookRows(r.body).map((c) => c.dataset["title"]);
    expect(shown).toEqual(["Hook fired: prompt check", "Hook fired: mystery"]);
    expect(badge(r.body)?.textContent).toContain("1");
  });

  it("counts only rows in the header", () => {
    const r = render((t) => [
      hook(t, 1, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      ...guardedCommands(t, 2, 2),
    ]);
    const count = header(r.body).querySelector(".tool-group-count")?.textContent ?? "";
    expect(count).toMatch(/^3 operations: 2 commands, 1 hook/);
  });

  it("draws a group of only quiet tool-trigger hooks as rows: there is no card to fold into", () => {
    const r = render((t) => [hook(t, 1, GUARDS[0].name), hook(t, 2, GUARDS[1].name)]);
    expect(shownHookRows(r.body)).toHaveLength(2);
    expect(badge(r.body)).toBeNull();
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
