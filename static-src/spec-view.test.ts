// ---------------------------------------------------------------------------
// Tests for spec-view.ts: the spec tab's page.
//
// The render helpers transplanted from the deleted specs board (the tree, the
// toggle, the optional badge) are asserted against real DOM; the fetch, the tab
// store and the send primitive are mocked so the lifecycle rules (eligibility,
// the prompts, the result mapping, the state map's lifetime, the poll cadence,
// 304 and 404) are deterministic.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { signal } from "@cplieger/reactive";
import type { Spec, SpecDoc, SpecTaskNode } from "./wire/types.gen.js";
import type * as StoreModule from "./store.js";
import type * as ApiModule from "./api-client.js";
import type * as ChatCommandsModule from "./chat-commands.js";
import type * as OpenersModule from "./editor-openers.js";

const m = vi.hoisted(() => ({
  /** The next replies `apiGetConditional` hands back, oldest first; the last one
   *  repeats once the queue is drained. */
  replies: [] as { status: number; data: unknown; etag: string }[],
  requests: [] as { path: string; etag: string }[],
  sent: [] as { chat: string; text: string }[],
  sendResult: { current: "sent" as string },
  opened: [] as string[],
  reparented: [] as { id: string; parent: string }[],
  /** Which spec refs have a tab, which chat each spec tab's parent is, and which
   *  chats have a tab. Three plain records the cases drive. */
  specTabs: new Map<string, string>(),
  chatTabs: new Set<string>(),
  thinking: new Map<string, boolean>(),
  names: new Map<string, string>(),
}));

/** Reactive versions the mocked tracked reads bump, so the page's one effect
 *  re-runs the way the real store would make it. */
const tabsVersion = signal(0);
const thinkVersion = signal(0);

vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  openSpecRefs: () => {
    void tabsVersion.value;
    return [...m.specTabs.keys()];
  },
  openChatRefs: () => {
    void tabsVersion.value;
    return [...m.chatTabs];
  },
  tabIdFor: (kind: string, ref: string) => {
    if (kind === "spec") {
      return m.specTabs.has(ref) ? `spec:${ref}` : "";
    }
    if (kind === "chat") {
      return m.chatTabs.has(ref) ? `chat:${ref}` : "";
    }
    return "";
  },
  parentChatRef: (id: string) => m.specTabs.get(id.replace(/^spec:/, "")) ?? "",
  setTabParent: vi.fn(async (id: string, parent: string) => {
    m.reparented.push({ id, parent });
    m.specTabs.set(id.replace(/^spec:/, ""), parent.replace(/^chat:/, ""));
    return true;
  }),
  activateTab: vi.fn(),
  openTab: vi.fn(async (args: { kind: string; ref?: string }) => {
    m.opened.push(`${args.kind}:${args.ref ?? ""}`);
    return "opened";
  }),
}));

vi.mock("./api-client.js", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiModule>();
  return {
    ...actual,
    apiGetConditional: vi.fn(async (path: string, etag: string) => {
      m.requests.push({ path, etag });
      const next = m.replies.length > 1 ? m.replies.shift() : m.replies[0];
      return next ?? { status: 0, data: null, etag: "" };
    }),
  };
});

vi.mock("./chat-commands.js", async (importOriginal) => {
  const actual = await importOriginal<typeof ChatCommandsModule>();
  return {
    ...actual,
    sendPromptTo: vi.fn(async (chat: string, text: string) => {
      m.sent.push({ chat, text });
      return m.sendResult.current;
    }),
  };
});

vi.mock("./store.js", async (importOriginal) => {
  const actual = await importOriginal<typeof StoreModule>();
  return {
    ...actual,
    get: (id: string) => (m.names.has(id) ? { id, name: m.names.get(id) } : undefined),
    isThinking: (id: string) => m.thinking.get(id) ?? false,
    watchSession: (id: string) => {
      void thinkVersion.value;
      return { id, thinking: m.thinking.get(id) ?? false };
    },
  };
});

vi.mock("./editor-openers.js", async (importOriginal) => {
  const actual = await importOriginal<typeof OpenersModule>();
  return { ...actual, openFile: vi.fn() };
});

const view = await import("./spec-view.js");
const bus = await import("./bus.js");
const dock = await import("./decision-dock.js");
const openers = await import("./editor-openers.js");
const navigate = await import("./navigate.js");

// --- Fixtures ---

const DIR = ".kiro/specs/demo";

function node(id: string, text: string, extra: Partial<SpecTaskNode> = {}): SpecTaskNode {
  return {
    id,
    number: "",
    text,
    status: "pending",
    hash: `h-${id}`,
    detail: "",
    children: [],
    line: Number(id.slice(1)),
    indent: 0,
    queued: false,
    optional: false,
    ...extra,
  };
}

function tasksDoc(tasks: SpecTaskNode[], extra: Partial<SpecDoc> = {}): SpecDoc {
  const leaves = (ns: SpecTaskNode[]): SpecTaskNode[] =>
    ns.flatMap((n) => (n.children.length === 0 ? [n] : leaves(n.children)));
  const required = leaves(tasks).filter((n) => !n.optional);
  const count = (s: string): number => required.filter((n) => n.status === s).length;
  return {
    file: "tasks.md",
    role: "tasks",
    hash: `tasks-${String(tasks.length)}`,
    content: "",
    tasks,
    progress: {
      pending: count("pending"),
      in_progress: count("in_progress"),
      completed: count("completed"),
      queued: required.filter((n) => n.queued).length,
      total: required.length,
    },
    unreadable_lines: 0,
    ...extra,
  };
}

function prose(file: string, role: SpecDoc["role"], content = `# ${file}`): SpecDoc {
  return { file, role, hash: `hash-${file}`, content };
}

function spec(docs: SpecDoc[], dir = DIR): Spec {
  return { dir, name: dir.split("/").pop() ?? dir, updated_at: "2026-09-17T10:00:00Z", docs };
}

function reply(s: Spec, etag = '"e1"'): { status: number; data: unknown; etag: string } {
  return { status: 200, data: s, etag };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await new Promise((r) => setTimeout(r, 0));
  }
}

/** Drain the microtask queue. `settle`'s twin for the one case that runs on the
 *  fake clock, where a `setTimeout` would never resolve. */
async function flush(): Promise<void> {
  for (let i = 0; i < 12; i++) {
    await Promise.resolve();
  }
}

function body(): HTMLElement {
  const el = document.getElementById("spec-body");
  if (el === null) {
    throw new Error("no #spec-body");
  }
  return el;
}

function openParented(chat = "c1"): void {
  m.specTabs.set(DIR, chat);
  m.chatTabs.add(chat);
  m.names.set(chat, "Chat one");
  tabsVersion.value++;
}

beforeEach(() => {
  vi.useRealTimers();
  m.replies = [];
  m.requests = [];
  m.sent = [];
  m.opened = [];
  m.reparented = [];
  m.sendResult.current = "sent";
  m.specTabs.clear();
  m.chatTabs.clear();
  m.thinking.clear();
  m.names.clear();
  document.body.replaceChildren();
  const viewEl = document.createElement("div");
  viewEl.id = "spec-view";
  const bodyEl = document.createElement("div");
  bodyEl.id = "spec-body";
  viewEl.appendChild(bodyEl);
  document.body.appendChild(viewEl);
});

afterEach(() => {
  view._resetForTest();
  vi.useRealTimers();
});

// --- The tree (transplanted from specs.test.ts@f776c166^) ---

describe("the task tree", () => {
  it("renders the status glyph and the task text, with a spacer for a leaf", async () => {
    openParented();
    m.replies = [
      reply(spec([tasksDoc([node("L3", "2.1 Do a thing", { status: "in_progress" })])])),
    ];
    view.showSpec(DIR);
    await settle();
    const row = body().querySelector(".spec-node");
    expect(row?.getAttribute("data-task-id")).toBe("L3");
    expect(row?.querySelector(".spec-status")?.classList.contains("work-status-in-progress")).toBe(
      true,
    );
    expect(row?.querySelector(".spec-task-text")?.textContent).toBe("2.1 Do a thing");
    expect(row?.querySelector(".spec-toggle")).toBeNull();
    expect(row?.querySelector(".spec-toggle-spacer")).not.toBeNull();
    expect(row?.querySelector(".spec-run")).not.toBeNull();
  });

  it("gives a parent a collapse toggle and nested children, open by default", async () => {
    openParented();
    m.replies = [
      reply(
        spec([
          tasksDoc([
            node("L1", "1 Parent", {
              number: "1",
              children: [node("L2", "1.1 Child", { number: "1.1" })],
            }),
          ]),
        ]),
      ),
    ];
    view.showSpec(DIR);
    await settle();
    const parent = body().querySelector(".spec-node");
    expect(parent?.querySelector(".spec-toggle")).not.toBeNull();
    expect(parent?.querySelector(".spec-node-children .spec-node")).not.toBeNull();
    expect(parent?.classList.contains("collapsed")).toBe(false);
    expect(parent?.querySelector(":scope > .spec-node-row .spec-run")).toBeNull();
  });

  it("marks an optional task, tints a queued one, and renders detail as markdown", async () => {
    openParented();
    m.replies = [
      reply(
        spec([
          tasksDoc([
            node("L1", "Optional one", {
              optional: true,
              queued: true,
              detail: "Some **bold** detail",
            }),
          ]),
        ]),
      ),
    ];
    view.showSpec(DIR);
    await settle();
    const row = body().querySelector(".spec-node");
    expect(row?.querySelector(".spec-badge-optional")?.textContent).toBe("Optional");
    expect(row?.querySelector(".spec-status")?.classList.contains("work-status-queued")).toBe(true);
    expect(row?.querySelector(".spec-node-detail strong")?.textContent).toBe("bold");
    expect(row?.querySelector(".spec-run")).not.toBeNull();
  });

  it("names the wave a task runs in, and says nothing for a task with none", async () => {
    openParented();
    m.replies = [
      reply(
        spec([
          tasksDoc([
            node("L1", "1. first", { number: "1", wave: 0 }),
            node("L2", "2. second", { number: "2" }),
          ]),
        ]),
      ),
    ];
    view.showSpec(DIR);
    await settle();
    const rows = body().querySelectorAll(".spec-node");
    expect(rows[0]?.querySelector(".spec-badge-wave")?.textContent).toBe("Wave 0");
    expect(rows[1]?.querySelector(".spec-badge-wave")).toBeNull();
  });

  it("withholds Run from a node whose children were cut", async () => {
    openParented();
    m.replies = [
      reply(
        spec([
          tasksDoc([node("L1", "Cut parent", { truncated_children: true })], {
            truncated: { returned: 1, total: 1200 },
          }),
        ]),
      ),
    ];
    view.showSpec(DIR);
    await settle();
    expect(body().querySelector(".spec-run")).toBeNull();
    expect(body().querySelector(".spec-truncated")?.textContent).toBe("Showing 1 of 1200 tasks");
  });
});

// --- Eligibility (design 4.3) ---

describe("eligibility", () => {
  const tree = [
    node("L1", "Pending one"),
    node("L2", "Done one", { status: "completed" }),
    node("L3", "Half one", { status: "in_progress" }),
    node("L4", "Parent", { children: [node("L5", "Child")] }),
  ];

  it("refuses a stale hash", () => {
    const v = view.eligibility(tree, "L1", "other-hash", false);
    expect(v).toEqual({ ok: false, reason: "the list changed, pick again" });
  });

  it("refuses a node that left the list", () => {
    expect(view.eligibility(tree, "L9", "h-L9", false).ok).toBe(false);
  });

  it("refuses a ticked task", () => {
    expect(view.eligibility(tree, "L2", "h-L2", false)).toEqual({
      ok: false,
      reason: "already checked off",
    });
  });

  it("runs an in-progress task while the target is idle", () => {
    const v = view.eligibility(tree, "L3", "h-L3", false);
    expect(v.ok).toBe(true);
  });

  it("refuses an in-progress task while the target is thinking", () => {
    expect(view.eligibility(tree, "L3", "h-L3", true).ok).toBe(false);
  });

  it("refuses a node with children", () => {
    expect(view.eligibility(tree, "L4", "h-L4", false).ok).toBe(false);
  });

  it("accepts a pending leaf", () => {
    const v = view.eligibility(tree, "L1", "h-L1", true);
    expect(v.ok).toBe(true);
  });

  it("re-checks against a fresh fetch before dispatching and shows the refusal", async () => {
    openParented();
    const first = spec([tasksDoc([node("L1", "Pending one")])]);
    const second = spec([tasksDoc([node("L1", "Pending one", { status: "completed" })])]);
    m.replies = [reply(first, '"a"'), reply(second, '"b"')];
    view.showSpec(DIR);
    await settle();
    body().querySelector<HTMLButtonElement>(".spec-run")?.click();
    await settle();
    expect(m.sent).toEqual([]);
    expect(body().querySelector(".spec-failure")?.textContent).toBe("already checked off");
  });
});

// --- Prompts (design 4.3, verbatim) ---

describe("prompts", () => {
  const s = { name: "demo", dir: DIR };

  it("composes the numbered single-task prompt", () => {
    const n = node("L4", "2.1 Do a thing", { number: "2.1", detail: "More about it" });
    expect(view.taskPrompt(s, n)).toBe(
      "Execute task 2.1 from spec 'demo' (.kiro/specs/demo/tasks.md):\n\n" +
        "2.1 Do a thing\nMore about it\n\n" +
        "Before implementing, read requirements.md (or bugfix.md) and design.md in that directory.\n" +
        "Work only on this task. Mark its checkbox [x] in tasks.md when it is genuinely done, run\n" +
        "the relevant build or tests to verify, then end the turn with a summary. Do not continue\n" +
        "to other tasks.",
    );
  });

  it("uses the unnumbered stem for a task with no number", () => {
    const n = node("L4", "Do a thing");
    expect(
      view
        .taskPrompt(s, n)
        .startsWith(
          "Execute the following task from spec 'demo' (.kiro/specs/demo/tasks.md):\n\nDo a thing\n\n",
        ),
    ).toBe(true);
  });

  it("adds the interrupted-run sentence for a [-] task", () => {
    const n = node("L4", "Do a thing", { status: "in_progress" });
    expect(
      view
        .taskPrompt(s, n)
        .endsWith(
          "\n\nIts box is `[-]` from an interrupted run, so treat the work as not started and redo it.",
        ),
    ).toBe(true);
    expect(view.taskPrompt(s, node("L4", "Do a thing"))).not.toContain("interrupted run");
  });

  it("composes Run all verbatim", () => {
    expect(view.runAllPrompt(s)).toBe(
      "Run all tasks in spec 'demo' (.kiro/specs/demo/tasks.md). Before implementing, read requirements.md\n" +
        "(or bugfix.md) and design.md. Work through every unchecked task that is not marked optional\n" +
        "(a `*` after the checkbox), respecting the ## Task Dependency Graph section if the file has\n" +
        "one. After each task mark its checkbox [x], run the relevant build or tests to verify, then\n" +
        "continue. Finish when every required task is checked or you hit a blocker that needs me,\n" +
        "and end the turn with a summary of what was done and what remains.",
    );
  });

  it("drops the optional clause and the required finish word for the all scope", () => {
    const all = view.runAllPrompt(s, "all");
    // Same stem, so KAS's own classifier still matches it.
    expect(all.startsWith("Run all tasks in spec 'demo' (.kiro/specs/demo/tasks.md).")).toBe(true);
    expect(all).toContain("the ones marked optional\n(a `*` after the checkbox) included");
    expect(all).not.toContain("that is not marked optional");
    // Left standing, "required" would tell the agent to include the optional
    // tasks and then stop once the required ones were checked.
    expect(all).toContain("Finish when every task is checked");
    expect(all).not.toContain("every required task is checked");
  });

  it("measures the byte cap in UTF-8 bytes", () => {
    expect(view.promptFits("a".repeat(view.MAX_PROMPT_BYTES))).toBe(true);
    expect(view.promptFits("a".repeat(view.MAX_PROMPT_BYTES + 1))).toBe(false);
    expect(view.promptFits("\u00e9".repeat(view.MAX_PROMPT_BYTES / 2 + 1))).toBe(false);
  });

  it("withholds Run from a task whose prompt exceeds the cap, with the reason in its tooltip", async () => {
    openParented();
    m.replies = [
      reply(spec([tasksDoc([node("L1", "Huge", { detail: "x".repeat(view.MAX_PROMPT_BYTES) })])])),
    ];
    view.showSpec(DIR);
    await settle();
    const run = body().querySelector<HTMLButtonElement>(".spec-run");
    expect(run?.disabled).toBe(true);
    expect(run?.getAttribute("data-tooltip")).toContain("too large");
  });
});

// --- Dispatch and the result mapping ---

describe("dispatch", () => {
  it("sends the task prompt to the tab's parent chat and pulses the row on sent", async () => {
    openParented("c1");
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
    body().querySelector<HTMLButtonElement>(".spec-run")?.click();
    await settle();
    expect(m.sent).toHaveLength(1);
    expect(m.sent[0]?.chat).toBe("c1");
    expect(m.sent[0]?.text.startsWith("Execute the following task from spec 'demo'")).toBe(true);
    expect(body().querySelector(".spec-node")?.classList.contains("spec-dispatched")).toBe(true);
    expect(view._stateOf(DIR)?.fastUntil).toBeGreaterThan(Date.now());
  });

  it("renders queued and starting as busy on the row", async () => {
    openParented();
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
    for (const r of ["queued", "starting"] as const) {
      view.applyResult(DIR, "L1", r);
      const run = body().querySelector<HTMLButtonElement>(".spec-run");
      expect(run?.classList.contains("spec-busy")).toBe(true);
      expect(run?.disabled).toBe(true);
      view._stateOf(DIR)?.busy.clear();
    }
  });

  it("renders gone as the empty state and failed as a failure line", async () => {
    openParented();
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
    view.applyResult(DIR, "L1", "failed");
    expect(body().querySelector(".spec-failure")?.textContent).toBe("The prompt could not be sent");
    view.applyResult(DIR, "L1", "gone");
    expect(body().querySelector(".spec-empty")?.textContent).toContain(DIR);
    expect(body().querySelector(".spec-run")).toBeNull();
  });

  it("sends Run all to the parent chat", async () => {
    openParented("c1");
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
    body().querySelector<HTMLButtonElement>(".spec-run-all")?.click();
    await settle();
    expect(m.sent[0]?.text.startsWith("Run all tasks in spec 'demo'")).toBe(true);
  });

  it("disables Run all while the tree is truncated and while the target is thinking", async () => {
    openParented("c1");
    m.replies = [
      reply(
        spec([tasksDoc([node("L1", "Pending one")], { truncated: { returned: 1, total: 1200 } })]),
      ),
    ];
    view.showSpec(DIR);
    await settle();
    const all = body().querySelector<HTMLButtonElement>(".spec-run-all");
    expect(all?.disabled).toBe(true);
    expect(all?.getAttribute("data-tooltip")).toContain("cut at the node cap");

    m.thinking.set("c1", true);
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]), '"t"')];
    view.refreshSpec(DIR);
    await settle();
    expect(body().querySelector<HTMLButtonElement>(".spec-run-all")?.disabled).toBe(true);
    expect(body().querySelector<HTMLButtonElement>(".spec-run")?.disabled).toBe(true);
  });

  it("offers a Run-in picker over the open chats on a parentless page and re-parents on pick", async () => {
    m.specTabs.set(DIR, "");
    m.chatTabs.add("c1");
    m.chatTabs.add("c2");
    m.names.set("c1", "First chat");
    m.names.set("c2", "Second chat");
    tabsVersion.value++;
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
    expect(body().querySelector(".spec-run-all")).toBeNull();
    const select = body().querySelector<HTMLSelectElement>(".spec-run-in-select");
    expect([...(select?.options ?? [])].map((o) => o.textContent)).toEqual([
      "First chat",
      "Second chat",
    ]);
    expect(body().querySelector<HTMLButtonElement>(".spec-run")?.disabled).toBe(true);
    if (select !== null) {
      select.value = "c2";
    }
    body().querySelector<HTMLButtonElement>(".spec-run-in")?.click();
    await settle();
    expect(m.reparented).toEqual([{ id: `spec:${DIR}`, parent: "chat:c2" }]);
    expect(body().querySelector(".spec-run-all")).not.toBeNull();
  });
});

// --- Phase checkpoints (design 7.2) ---

describe("phase checkpoints", () => {
  /** The three answers KAS's after-tasks.md checkpoint offers, verbatim off the
   *  pinned bundle. `user-input.ts` sends an option's title exactly. */
  const RUN_REQUIRED = "Run required tasks";
  const RUN_ALL = "Run required and optional tasks";
  const NOT_NOW = "Not now";

  async function openIdle(chat = "c1"): Promise<void> {
    openParented(chat);
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
  }

  it("fires a Run required answer at once while the target is idle", async () => {
    await openIdle("c1");
    view.carryCheckpointAnswer("c1", RUN_REQUIRED);
    await settle();
    expect(m.sent).toHaveLength(1);
    expect(m.sent[0]?.chat).toBe("c1");
    expect(m.sent[0]?.text).toBe(view.runAllPrompt({ name: "demo", dir: DIR }, "required"));
    expect(view._stateOf(DIR)?.armedRunAll).toBeUndefined();
  });

  it("holds a Run required and optional answer until the target's turn ends", async () => {
    await openIdle("c1");
    m.thinking.set("c1", true);
    thinkVersion.value++;
    // The rising edge proves the page is subscribed to this chat's session, so a
    // silent arm below cannot be told from a page that never saw the answer.
    expect(view._stateOf(DIR)?.lastThinking).toBe(true);

    view.carryCheckpointAnswer("c1", RUN_ALL);
    await settle();
    // The agent answers the checkpoint by ENDING its turn, so the turn is still
    // open at the click: a prompt sent now takes the server's 409.
    expect(m.sent).toHaveLength(0);
    expect(view._stateOf(DIR)?.armedRunAll).toBe("all");

    m.thinking.set("c1", false);
    thinkVersion.value++;
    await settle();
    expect(m.sent).toHaveLength(1);
    expect(m.sent[0]?.text).toBe(view.runAllPrompt({ name: "demo", dir: DIR }, "all"));
    expect(view._stateOf(DIR)?.armedRunAll).toBeUndefined();
  });

  it("carries nothing for Not now or for an answer that is not ours", async () => {
    await openIdle("c1");
    view.carryCheckpointAnswer("c1", NOT_NOW);
    view.carryCheckpointAnswer("c1", "Yes");
    view.carryCheckpointAnswer("c1", "constructor");
    await settle();
    expect(m.sent).toEqual([]);
    expect(view._stateOf(DIR)?.armedRunAll).toBeUndefined();
  });

  it("carries nothing for a chat no open spec page is parented to", async () => {
    await openIdle("c1");
    view.carryCheckpointAnswer("c2", RUN_REQUIRED);
    await settle();
    expect(m.sent).toEqual([]);
  });

  it("reaches the carry through the bus event the dock emits", async () => {
    await openIdle("c1");
    bus.emitBus(bus.BUS_USER_INPUT_ANSWERED, { chatID: "c1", answer: RUN_REQUIRED });
    await settle();
    expect(m.sent).toHaveLength(1);
    expect(m.sent[0]?.chat).toBe("c1");
  });

  it("picks the page on screen when two spec pages share the chat", async () => {
    const OTHER = ".kiro/specs/other";
    m.specTabs.set(DIR, "c1");
    m.specTabs.set(OTHER, "c1");
    m.chatTabs.add("c1");
    m.names.set("c1", "Chat one");
    tabsVersion.value++;
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])], OTHER), '"o1"')];
    view.showSpec(OTHER);
    await settle();

    view.carryCheckpointAnswer("c1", RUN_REQUIRED);
    await settle();
    expect(m.sent).toHaveLength(1);
    expect(m.sent[0]?.text).toContain(OTHER);
    expect(view._stateOf(DIR)?.armedRunAll).toBeUndefined();
  });

  it("fetches before it composes when the answer beats the page's first fetch", async () => {
    openParented("c1");
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    // No settle: the page exists with `spec === null`, which is the window a
    // checkpoint answered right after the tab opened lands in.
    expect(view._stateOf(DIR)?.spec).toBeNull();
    view.carryCheckpointAnswer("c1", RUN_REQUIRED);
    await settle();
    expect(m.sent).toHaveLength(1);
    expect(m.sent[0]?.text).toContain("spec 'demo'");
  });

  it("mounts a decision-dock host on the page and unmounts it on release", async () => {
    await openIdle("c1");
    const host = body().querySelector(".spec-page > .decision-dock");
    expect(host).not.toBeNull();
    expect(host?.getAttribute("role")).toBe("group");
    expect(host?.getAttribute("aria-label")).toBe("Waiting for your decision");
    expect(host?.classList.contains("hidden")).toBe(true);
    expect(dock._hostCount()).toBe(1);

    m.specTabs.delete(DIR);
    tabsVersion.value++;
    await settle();
    expect(body().childElementCount).toBe(0);
    expect(dock._hostCount()).toBe(0);
  });
});

// --- Fetch: 304, 404, SSE, the state map ---

describe("fetch and state", () => {
  it("sends If-None-Match on the second fetch and keeps the last reply on a 304", async () => {
    openParented();
    m.replies = [
      reply(spec([prose("requirements.md", "requirements", "# Reqs body")]), '"v1"'),
      { status: 304, data: null, etag: '"v1"' },
    ];
    view.showSpec(DIR);
    await settle();
    expect(body().querySelector(".spec-prose h1")?.textContent).toBe("Reqs body");
    view.refreshSpec(DIR);
    await settle();
    expect(m.requests.map((r) => r.etag)).toEqual(["", '"v1"']);
    expect(body().querySelector(".spec-prose h1")?.textContent).toBe("Reqs body");
    expect(view._stateOf(DIR)?.spec).not.toBeNull();
  });

  it("renders the empty state naming the directory on a 404, with every control disabled", async () => {
    openParented();
    m.replies = [{ status: 404, data: null, etag: "" }];
    view.showSpec(DIR);
    await settle();
    expect(body().querySelector(".spec-empty")?.textContent).toBe(`Nothing is at ${DIR} any more`);
    expect(body().querySelector(".spec-run")).toBeNull();
    expect(body().querySelector(".spec-run-all")).toBeNull();
    expect(body().querySelector(".spec-bar")).toBeNull();
  });

  it("refetches on a spec_changed frame for its own ref only", async () => {
    openParented();
    m.replies = [reply(spec([prose("design.md", "design")]))];
    view.showSpec(DIR);
    await settle();
    const before = m.requests.length;
    bus.dispatch({ type: "spec_changed", chat_id: "", payload: { dir: ".kiro/specs/other" } });
    await settle();
    expect(m.requests.length).toBe(before);
    bus.dispatch({ type: "spec_changed", chat_id: "", payload: { dir: DIR } });
    await settle();
    expect(m.requests.length).toBe(before + 1);
  });

  it("releases a ref's state when its tab leaves the open set and keeps it across a re-parent", async () => {
    openParented("c1");
    m.replies = [reply(spec([prose("design.md", "design")]))];
    view.showSpec(DIR);
    await settle();
    expect(view._openStates()).toEqual([DIR]);
    m.specTabs.set(DIR, "c2");
    tabsVersion.value++;
    await settle();
    expect(view._openStates()).toEqual([DIR]);
    m.specTabs.delete(DIR);
    tabsVersion.value++;
    await settle();
    expect(view._openStates()).toEqual([]);
    expect(body().childElementCount).toBe(0);
  });

  it("refetches at the target's turn end after a dispatch from this page", async () => {
    openParented("c1");
    m.replies = [reply(spec([tasksDoc([node("L1", "Pending one")])]))];
    view.showSpec(DIR);
    await settle();
    m.thinking.set("c1", true);
    thinkVersion.value++;
    // The RISING edge is what proves the page is subscribed to this chat's session
    // at all: without it the falling edge below reads as unchanged and the refetch
    // it triggers cannot be told from one this case never asked for.
    expect(view._stateOf(DIR)?.lastThinking).toBe(true);
    view.applyResult(DIR, "L1", "sent");
    const before = m.requests.length;
    m.thinking.set("c1", false);
    thinkVersion.value++;
    await settle();
    expect(m.requests.length).toBe(before + 1);
    expect(view._stateOf(DIR)?.dispatched.size).toBe(0);
  });

  it("says so on the row when the turn ends without ticking the box", async () => {
    openParented("c1");
    const pending = tasksDoc([node("L1", "Pending one")]);
    m.replies = [reply(spec([pending])), reply(spec([pending]), '"e2"')];
    view.showSpec(DIR);
    await settle();
    m.thinking.set("c1", true);
    thinkVersion.value++;
    view.applyResult(DIR, "L1", "sent");
    m.thinking.set("c1", false);
    thinkVersion.value++;
    await settle();

    expect([...(view._stateOf(DIR)?.unmoved ?? [])]).toEqual(["L1"]);
    const note = body().querySelector(".spec-node-note");
    expect(note?.textContent ?? "").toContain("without ticking this box");

    // Running it again withdraws the note in the same gesture.
    view.applyResult(DIR, "L1", "sent");
    expect(view._stateOf(DIR)?.unmoved.size).toBe(0);
    expect(body().querySelector(".spec-node-note")).toBeNull();
  });

  it("says nothing when the box moved in the fetch the turn end triggered", async () => {
    openParented("c1");
    m.replies = [
      reply(spec([tasksDoc([node("L1", "Pending one")])])),
      reply(spec([tasksDoc([node("L1", "Pending one", { status: "completed" })])]), '"e2"'),
    ];
    view.showSpec(DIR);
    await settle();
    m.thinking.set("c1", true);
    thinkVersion.value++;
    view.applyResult(DIR, "L1", "sent");
    m.thinking.set("c1", false);
    thinkVersion.value++;
    // The falling edge itself paints, so a mark taken there rather than from the
    // fetch it triggers reaches the DOM for one frame before being retracted.
    expect(body().querySelector(".spec-node-note")).toBeNull();
    await settle();

    expect(view._stateOf(DIR)?.unmoved.size).toBe(0);
    expect(body().querySelector(".spec-node-note")).toBeNull();
  });

  it("collapses one in-flight fetch and one trailing refetch", async () => {
    openParented();
    // A real initializer rather than `null`: the executor runs synchronously, and
    // the compiler cannot see that, so a nullable binding narrows to `never` at the
    // call below and `typecheck:tests` refuses it.
    let release = (): void => undefined;
    const held = new Promise<void>((r) => {
      release = r;
    });
    const api = await import("./api-client.js");
    vi.mocked(api.apiGetConditional).mockImplementationOnce(async (path: string, etag: string) => {
      m.requests.push({ path, etag });
      await held;
      return reply(spec([prose("design.md", "design")]));
    });
    m.replies = [reply(spec([prose("design.md", "design")]))];
    view.showSpec(DIR);
    view.refreshSpec(DIR);
    view.refreshSpec(DIR);
    await settle();
    expect(m.requests.length).toBe(1);
    release();
    await settle();
    expect(m.requests.length).toBe(2);
  });
});

// --- Poll cadence ---

describe("poll cadence", () => {
  it("runs at 2.5 s inside the fast window and 15 s outside it", () => {
    expect(view.pollInterval({ fastUntil: 1000 }, 999)).toBe(view.POLL_FAST_MS);
    expect(view.pollInterval({ fastUntil: 1000 }, 1000)).toBe(view.POLL_SLOW_MS);
    expect(view.POLL_FAST_MS).toBe(2500);
    expect(view.POLL_SLOW_MS).toBe(15000);
    expect(view.FAST_WINDOW_MS).toBe(20000);
  });

  it("opens the fast window when the tasks document's hash moves", async () => {
    openParented();
    m.replies = [
      reply(spec([tasksDoc([node("L1", "One")], { hash: "t1" })]), '"a"'),
      reply(spec([tasksDoc([node("L1", "One", { status: "completed" })], { hash: "t2" })]), '"b"'),
    ];
    view.showSpec(DIR);
    await settle();
    expect(view._stateOf(DIR)?.fastUntil).toBe(0);
    view.refreshSpec(DIR);
    await settle();
    expect(view._stateOf(DIR)?.fastUntil).toBeGreaterThan(Date.now());
  });

  it("does not fetch on a tick while the view is hidden, and fetches once it is shown", async () => {
    openParented();
    m.replies = [reply(spec([prose("design.md", "design")]))];
    // BEFORE the first fetch, because the poll is armed at the end of it: with the
    // fake clock installed afterwards that timer is a REAL one, `advanceTimersByTime`
    // moves nothing, and both halves of this case pass while asserting nothing.
    vi.useFakeTimers();
    view.showSpec(DIR);
    // Microtasks only: the whole fetch chain is promises, and `settle`'s setTimeout
    // would never resolve under the fake clock.
    await flush();
    const before = m.requests.length;
    const viewEl = document.getElementById("spec-view");
    if (viewEl === null) {
      throw new Error("no #spec-view");
    }
    // `offsetParent` is what the page reads, so the inline style is the whole of
    // hiding it here: no stylesheet is loaded, so a class would paint nothing.
    viewEl.style.display = "none";
    vi.advanceTimersByTime(view.POLL_SLOW_MS + 10);
    await flush();
    expect(m.requests.length).toBe(before);
    expect(view._stateOf(DIR)?.pollTimer).not.toBeNull();
    viewEl.style.display = "";
    vi.advanceTimersByTime(view.POLL_SLOW_MS + 10);
    await flush();
    expect(m.requests.length).toBe(before + 1);
  });
});

// --- Header ---

describe("header", () => {
  it("names the phase from the known roles, counts done over required leaves, and carries the progressbar", async () => {
    openParented();
    m.replies = [
      reply(
        spec([
          prose("requirements.md", "requirements"),
          prose("analysis.md", "other"),
          tasksDoc([
            node("L1", "One", { status: "completed" }),
            node("L2", "Two", { status: "in_progress" }),
            node("L3", "Three", { queued: true }),
            node("L4", "Opt", { optional: true }),
          ]),
        ]),
      ),
    ];
    view.showSpec(DIR);
    await settle();
    expect(body().querySelector(".spec-phase")?.textContent).toBe("Tasks");
    expect(body().querySelector(".spec-progress-count")?.textContent).toBe("1 of 3 done");
    const bar = body().querySelector('[role="progressbar"]');
    expect(bar?.getAttribute("aria-valuenow")).toBe("1");
    expect(bar?.getAttribute("aria-valuemin")).toBe("0");
    expect(bar?.getAttribute("aria-valuemax")).toBe("3");
    expect(bar?.getAttribute("aria-valuetext")).toBe("1 of 3 done, 1 in progress, 1 queued");
    const labels = [...body().querySelectorAll(".spec-seg .seg-label")].map((e) => e.textContent);
    expect(labels).toEqual(["Requirements", "Design", "Tasks", "analysis"]);
    expect(
      body()
        .querySelector('[data-spec-doc="design.md"] .spec-seg-dot')
        ?.classList.contains("spec-seg-missing"),
    ).toBe(true);
  });

  it("renders a segment for a document whose name would break an attribute selector", async () => {
    // A segment's id is a filename off the directory listing, and a `"` in one is
    // legal on every filesystem this runs on. Unescaped it made both selectors
    // (this page's dot painter and the shared bar's own) throw a SyntaxError, so
    // the whole paint went down over one badly named file.
    openParented();
    m.replies = [reply(spec([prose('we"re.md', "other"), prose("design.md", "design")]))];
    view.showSpec(DIR);
    await settle();
    const labels = [...body().querySelectorAll(".spec-seg .seg-label")].map((e) => e.textContent);
    expect(labels).toEqual(["Requirements", "Design", "Tasks", 'we"re']);
    const dot = body().querySelector(`[data-spec-doc="${CSS.escape('we"re.md')}"] .spec-seg-dot`);
    expect(dot?.classList.contains("spec-seg-present")).toBe(true);
  });

  it("renders no progressbar at a total of zero and no phase for other documents alone", async () => {
    openParented();
    m.replies = [reply(spec([prose("analysis.md", "other"), tasksDoc([])]))];
    view.showSpec(DIR);
    await settle();
    expect(body().querySelector('[role="progressbar"]')).toBeNull();
    expect(body().querySelector(".spec-phase")?.textContent).toBe("Tasks");
    m.replies = [reply(spec([prose("analysis.md", "other")]), '"o"')];
    view.refreshSpec(DIR);
    await settle();
    expect(body().querySelector(".spec-phase")).toBeNull();
  });

  it("states the unreadable-lines count", async () => {
    expect(view.unreadableLine(0)).toBe("");
    expect(view.unreadableLine(1)).toBe("1 line looks like a task but Kiro cannot read it");
    expect(view.unreadableLine(3)).toBe("3 lines look like tasks but Kiro cannot read them");
    openParented();
    m.replies = [reply(spec([tasksDoc([node("L1", "One")], { unreadable_lines: 3 })]))];
    view.showSpec(DIR);
    await settle();
    expect(body().querySelector(".spec-unreadable")?.textContent).toBe(
      "3 lines look like tasks but Kiro cannot read them",
    );
  });

  it("shows a skeleton for a missing expected document and Edit for a present one", async () => {
    openParented();
    m.replies = [reply(spec([prose("requirements.md", "requirements")]))];
    view.showSpec(DIR);
    await settle();
    body().querySelector<HTMLButtonElement>(".spec-edit")?.click();
    expect(vi.mocked(openers.openFile)).toHaveBeenCalledWith(`${DIR}/requirements.md`);
    body().querySelector<HTMLButtonElement>('[data-spec-doc="design.md"]')?.click();
    await settle();
    expect(body().querySelector(".editor-skeleton")).not.toBeNull();
  });
});

// --- The doors ---

describe("openSpec", () => {
  it("opens a parented spec tab from a chat and a parentless one without", async () => {
    m.chatTabs.add("c1");
    await navigate.openSpec(DIR, "c1");
    await navigate.openSpec(".kiro/specs/other");
    expect(m.opened).toEqual([`spec:${DIR}`, "spec:.kiro/specs/other"]);
  });

  it("re-parents an open parentless tab instead of opening a second", async () => {
    m.specTabs.set(DIR, "");
    m.chatTabs.add("c1");
    await navigate.openSpec(DIR, "c1");
    expect(m.opened).toEqual([]);
    expect(m.reparented).toEqual([{ id: `spec:${DIR}`, parent: "chat:c1" }]);
  });
});
