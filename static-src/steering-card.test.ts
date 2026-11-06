// A steering inclusion draws as its own tool box: rendered through the production dispatcher, so the
// card, its glyph, its linked names and its group placement are the shipped ones.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Turn } from "./turns.js";
import toolsCSS from "./css/14-tools.css?raw";
import type * as EditorOpeners from "./editor-openers.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

const openFile = vi.fn();
vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorOpeners>()),
  openFile: (path: string, line?: number) => {
    openFile(path, line);
  },
}));

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
const { clearAllEntrySigs } = await import("./store-signals.js");
const { initToolCallbacks, applyToolCallUpdate } = await import("./messages-tools.js");
const { refreshGroupHeader } = await import("./tool-group.js");
const { setActive } = await import("./store.js");
const { toolIcon } = await import("./icons.js");
const { iconEl } = await import("./icon-el.js");
const { setWorkspaceRoot, _resetForTest: resetWorkspaceRoot } = await import("./workspace.js");

const CHAT_ID = "c-steering-card";
setActive(CHAT_ID);
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

const WORKSPACE_DOC = "file:///workspace/.kiro/steering/git%20commits.md";
const GLOBAL_DOC = "file:///config/home/.kiro/steering/global.md";
const BUILTIN_DOC = "builtin-guide";
const STEERING = { kind: "steering", toolName: "" } as const;
const READ = { kind: "read", toolName: "" } as const;
const TITLE = "Steering loaded: git commits.md, global.md, builtin-guide";

let turnSeq = 0;
let hosts: HTMLElement[] = [];

function entry(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(seq)}`, turn: turnID, kind, seq, ts: seq + 1, payload };
}

function call(turnID: string, seq: number, over: Partial<EntryToolCall>): Entry {
  const base: EntryToolCall = {
    id: `tool-${String(seq)}`,
    title: "Read File",
    status: "completed",
    kind: "read",
    ts: 1,
  };
  return entry(turnID, seq, "tool_call", Object.assign(base, over));
}

function steering(turnID: string, seq: number, docs: string[], title: string): Entry {
  return call(turnID, seq, {
    id: `steering-${String(seq)}`,
    title,
    kind: "steering",
    steering_docs: docs,
  });
}

function text(turnID: string, seq: number, t: string): Entry {
  return entry(turnID, seq, "text", { text: t });
}

function render(build: (turnID: string) => Entry[], live: boolean): HTMLElement {
  turnSeq += 1;
  const turnID = `t-sc-${String(turnSeq)}`;
  const host = document.createElement("div");
  host.className = "turn-body";
  document.body.appendChild(host);
  hosts.push(host);
  const turn: Turn = {
    id: turnID,
    n: 1,
    trigger: { id: `${turnID}-p`, text: "go" },
    body: build(turnID),
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
  buildAssistantBody(host, turn, CHAT_ID, live);
  return host;
}

function boxes(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>('.tool-call[data-kind="steering"]')];
}

function onlyBox(root: HTMLElement): HTMLElement {
  const all = boxes(root);
  expect(all).toHaveLength(1);
  return all[0] as HTMLElement;
}

/** The box's title as segments: a link's name with its path, a plain name with null. */
function nameParts(box: HTMLElement): [string, string | null][] {
  return [...box.querySelectorAll<HTMLElement>(".tool-title .steering-doc")].map((n) => [
    n.textContent,
    n.tagName === "BUTTON" ? (n.dataset["path"] ?? "") : null,
  ]);
}

const threeDocs = (turnID: string): Entry[] => [
  text(turnID, 1, "before"),
  steering(turnID, 2, [WORKSPACE_DOC, GLOBAL_DOC, BUILTIN_DOC], TITLE),
  text(turnID, 3, "after"),
];

beforeEach(() => {
  setWorkspaceRoot("/workspace");
  openFile.mockClear();
});

afterEach(() => {
  for (const h of hosts) {
    h.remove();
  }
  hosts = [];
  resetBlockRenders();
  clearAllEntrySigs();
  resetWorkspaceRoot();
});

describe("a steering box", () => {
  it("draws one box between the prose either side of it, with the steering glyph", () => {
    const body = render(threeDocs, true);
    const box = onlyBox(body);
    const at = (seq: number): HTMLElement | null =>
      body.querySelector<HTMLElement>(`.msg-row[data-entry-seq="${String(seq)}"]`);
    expect(box.dataset["entrySeq"]).toBe("2");
    const before = at(1);
    const after = at(3);
    expect(before).not.toBeNull();
    expect(after).not.toBeNull();
    expect(before?.compareDocumentPosition(box)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(after?.compareDocumentPosition(box)).toBe(Node.DOCUMENT_POSITION_PRECEDING);
    expect(box.querySelector(".tool-icon > svg")?.isEqualNode(iconEl(toolIcon(STEERING)))).toBe(
      true,
    );
    expect(box.querySelector(".tool-icon > svg")?.isEqualNode(iconEl(toolIcon(READ)))).toBe(false);
  });

  it("titles the box with the server's words, so a find offset lands where the server counted", () => {
    const box = onlyBox(render(threeDocs, true));
    expect(box.querySelector(".tool-title")?.textContent).toBe(TITLE);
    expect(box.dataset["title"]).toBe(TITLE);
  });

  it("links a workspace document into the editor and names a global or non-file id as text", () => {
    const box = onlyBox(render(threeDocs, true));
    expect(nameParts(box)).toEqual([
      ["git commits.md", "/workspace/.kiro/steering/git commits.md"],
      ["global.md", null],
      ["builtin-guide", null],
    ]);
    const link = box.querySelector<HTMLButtonElement>("button.steering-doc");
    expect(link?.classList).toContain("tool-file-link");
    expect(link?.getAttribute("aria-label")).toBe("Open git commits.md");
    link?.click();
    expect(openFile).toHaveBeenCalledExactlyOnceWith(
      "/workspace/.kiro/steering/git commits.md",
      undefined,
    );
  });

  it("names every document as text before the workspace root is known", () => {
    resetWorkspaceRoot();
    const box = onlyBox(render(threeDocs, true));
    expect(nameParts(box).map(([, path]) => path)).toEqual([null, null, null]);
  });

  it("draws the same box from history as live", () => {
    const live = onlyBox(render(threeDocs, true));
    const replay = onlyBox(render(threeDocs, false));
    expect(replay.querySelector(".tool-header")?.innerHTML).toBe(
      live.querySelector(".tool-header")?.innerHTML,
    );
  });

  it("keeps its links through a republished call carrying the same title", () => {
    const body = render(threeDocs, true);
    const box = onlyBox(body);
    const payload = threeDocs("x")[1]?.payload as EntryToolCall;
    applyToolCallUpdate(box as HTMLDivElement, payload, CHAT_ID);
    expect(box.querySelectorAll("button.steering-doc")).toHaveLength(1);
    expect(box.querySelector(".tool-title")?.textContent).toBe(TITLE);
  });

  it("offers no disclosure, because it has nothing to show beyond its title", () => {
    const box = onlyBox(render(threeDocs, true));
    expect(box.querySelector(".tool-disclosure")).toBeNull();
    expect(box.dataset["depth1"]).toBe("none");
  });

  it("sits in the tool group of the read that triggered it", () => {
    const body = render(
      (turnID) => [
        call(turnID, 1, { id: "r1", input: { path: "/workspace/main.go" } }),
        steering(turnID, 2, [WORKSPACE_DOC], "Steering loaded: git commits.md"),
      ],
      true,
    );
    const box = onlyBox(body);
    const read = body.querySelector<HTMLElement>('.tool-call[data-kind="read"]');
    expect(read?.closest(".tool-group")).not.toBeNull();
    expect(box.closest(".tool-group")).toBe(read?.closest(".tool-group"));
  });

  it("shows every name on a phone-width card, where one line cannot hold them", () => {
    const style = document.createElement("style");
    style.textContent = toolsCSS;
    document.head.appendChild(style);
    try {
      const body = render(threeDocs, true);
      body.style.inlineSize = "240px";
      const title = onlyBox(body).querySelector<HTMLElement>(".tool-title");
      const names = [...(title?.querySelectorAll<HTMLElement>(".steering-doc") ?? [])];
      expect(names).toHaveLength(3);
      const edge = title?.getBoundingClientRect().right ?? 0;
      for (const n of names) {
        expect(n.getBoundingClientRect().right, n.textContent).toBeLessThanOrEqual(edge + 0.5);
      }
      expect(title?.scrollWidth).toBeLessThanOrEqual(title?.clientWidth ?? 0);
    } finally {
      style.remove();
    }
  });
});
