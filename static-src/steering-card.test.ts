// A steering inclusion draws as its own tool box: rendered through the production dispatcher, so the
// card, its glyph, its linked names and its group placement are the shipped ones.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Turn } from "./turns.js";
import toolsCSS from "./css/14-tools.css?raw";
import indexHtml from "../static/index.html?raw";
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
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { createRunStepStream } = await import("./run-chat-steps.js");

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

  it("links the names a capped title shows and reads the rest as a count", () => {
    const docs = [WORKSPACE_DOC, GLOBAL_DOC, BUILTIN_DOC];
    const capped = "Steering loaded: git commits.md +2 more";
    const box = onlyBox(render((turnID) => [steering(turnID, 1, docs, capped)], true));
    expect(box.querySelector(".tool-title")?.textContent).toBe(capped);
    expect(nameParts(box)).toEqual([
      ["git commits.md", "/workspace/.kiro/steering/git commits.md"],
    ]);
  });

  it("draws a title whose count disagrees with its documents as plain text", () => {
    const docs = [WORKSPACE_DOC, GLOBAL_DOC, BUILTIN_DOC];
    const wrong = "Steering loaded: git commits.md +5 more";
    const box = onlyBox(render((turnID) => [steering(turnID, 1, docs, wrong)], true));
    expect(box.querySelector(".tool-title")?.textContent).toBe(wrong);
    expect(nameParts(box)).toEqual([]);
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

// KAS names a document the client injected (marotte's own environment doc) `client:<session>:<name>`.
const CLIENT_DOC = "client:sess_79643ff5-74a8-49d5-a321-6a3698d6d9ad:marotte";
const SECOND_DOC = "file:///workspace/.kiro/steering/mandatory-instructions.md";
const CLEAN_TITLE =
  "Steering loaded: marotte, mandatory-instructions.md, git commits.md, global.md";
const clientSet = (turnID: string): Entry[] => [
  steering(turnID, 1, [CLIENT_DOC, SECOND_DOC, WORKSPACE_DOC, GLOBAL_DOC], CLEAN_TITLE),
];

/** The inline glyph a docs-page tab draws in static/index.html, children only. */
function docsTabGlyph(tab: string): string {
  const at = indexHtml.indexOf(`data-docs-tab="${tab}"`);
  expect(at, `static/index.html has no ${tab} docs tab`).toBeGreaterThan(-1);
  const tpl = document.createElement("template");
  tpl.innerHTML = /<svg\b[\s\S]*?<\/svg>/.exec(indexHtml.slice(at))?.[0] ?? "";
  // Serialized through the DOM like the card's icon, so `<circle/>` and `<circle></circle>` compare equal.
  return (tpl.content.querySelector("svg")?.innerHTML ?? "").replace(/\s+/g, " ").trim();
}

function frame(): Promise<void> {
  return new Promise((r) => requestAnimationFrame(() => r()));
}

describe("a steering box's words", () => {
  it("names a client-injected document by its name, with no session id or source key", () => {
    const box = onlyBox(render(clientSet, true));
    const header = box.querySelector<HTMLElement>(".tool-header");
    for (const text of [
      header?.textContent ?? "",
      header?.title ?? "",
      box.dataset["title"] ?? "",
    ]) {
      expect(text).not.toContain("client:");
      expect(text).not.toContain("sess_");
    }
    expect(nameParts(box)).toEqual([
      ["marotte", null],
      ["mandatory-instructions.md", "/workspace/.kiro/steering/mandatory-instructions.md"],
      ["git commits.md", "/workspace/.kiro/steering/git commits.md"],
      ["global.md", null],
    ]);
  });

  it("counts documents in its group's header, singular and plural, with no source key", () => {
    const body = render(
      (turnID) => [
        steering(turnID, 1, [CLIENT_DOC], "Steering loaded: marotte"),
        steering(
          turnID,
          2,
          [SECOND_DOC, WORKSPACE_DOC],
          "Steering loaded: mandatory-instructions.md, git commits.md",
        ),
      ],
      true,
    );
    const group = boxes(body)[0]?.closest<HTMLElement>(".tool-group");
    expect(group).not.toBeNull();
    refreshGroupHeader(group as HTMLDivElement);
    const count = group?.querySelector(".tool-group-count")?.textContent ?? "";
    expect(count).toBe("Loaded 3 steering docs: marotte, mandatory-instructions.md, +1 more");
  });

  it("counts a document whose name holds a comma once, from the box's documents", () => {
    const body = render(
      (turnID) => [
        steering(
          turnID,
          1,
          ["file:///workspace/.kiro/steering/a,%20b.md"],
          "Steering loaded: a, b.md",
        ),
        steering(turnID, 2, ["file:///workspace/.kiro/steering/go.md"], "Steering loaded: go.md"),
      ],
      true,
    );
    const group = boxes(body)[0]?.closest<HTMLElement>(".tool-group");
    expect(group).not.toBeNull();
    refreshGroupHeader(group as HTMLDivElement);
    const count = group?.querySelector(".tool-group-count")?.textContent ?? "";
    expect(count).toBe("Loaded 2 steering docs: a, b.md, go.md");
  });
});

describe("a steering box's links", () => {
  let style: HTMLStyleElement | null = null;
  afterEach(() => {
    style?.remove();
    style = null;
  });

  it("draws every openable document as the standard file pill and every other name as plain text", () => {
    const box = onlyBox(render(clientSet, true));
    const links = [...box.querySelectorAll<HTMLButtonElement>(".tool-title button")];
    expect(links.map((b) => b.dataset["path"])).toEqual([
      "/workspace/.kiro/steering/mandatory-instructions.md",
      "/workspace/.kiro/steering/git commits.md",
    ]);
    for (const b of links) {
      expect(b.classList).toContain("tool-file-link");
      expect(b.querySelector(".tool-file-icon > svg"), "the pill's file glyph").not.toBeNull();
      expect(b.querySelector(".tool-file-name")?.textContent).toBe(b.textContent);
    }
    const plain = [...box.querySelectorAll<HTMLElement>(".tool-title .steering-doc")].filter(
      (n) => n.tagName !== "BUTTON",
    );
    expect(plain.map((n) => n.textContent)).toEqual(["marotte", "global.md"]);
    for (const n of plain) {
      expect(n.closest(".tool-file-link")).toBeNull();
    }
  });

  it("paints the title's words in the default text colour and only the links in the accent", () => {
    style = mountAppCSS();
    const box = onlyBox(render(clientSet, true));
    const probe = document.createElement("span");
    probe.style.color = "var(--c-text-secondary)";
    box.appendChild(probe);
    const accent = document.createElement("span");
    accent.style.color = "var(--c-accent)";
    box.appendChild(accent);
    const title = box.querySelector<HTMLElement>(".tool-title");
    const plain = box.querySelector<HTMLElement>(".tool-title span.steering-doc");
    expect(getComputedStyle(title as HTMLElement).color).toBe(getComputedStyle(probe).color);
    expect(getComputedStyle(plain as HTMLElement).color).toBe(getComputedStyle(probe).color);
    for (const b of box.querySelectorAll<HTMLElement>(".tool-title .tool-file-link")) {
      expect(getComputedStyle(b).color).toBe(getComputedStyle(accent).color);
    }
  });
});

describe("a steering box's icon", () => {
  let style: HTMLStyleElement | null = null;
  afterEach(() => {
    style?.remove();
    style = null;
  });

  it("is the docs page's Steering glyph", () => {
    const box = onlyBox(render(threeDocs, true));
    const svg = box.querySelector(".tool-icon > svg")?.innerHTML.replace(/\s+/g, " ").trim();
    expect(svg).toBe(docsTabGlyph("steering"));
  });

  for (const width of [760, 360]) {
    it(`sits in its neighbours' icon column and row centre at ${String(width)}px`, async () => {
      style = mountAppCSS();
      const body = render(
        (turnID) => [
          call(turnID, 1, { id: "r1", input: { path: "/workspace/main.go" } }),
          steering(turnID, 2, [CLIENT_DOC, SECOND_DOC, WORKSPACE_DOC, GLOBAL_DOC], CLEAN_TITLE),
          call(turnID, 3, { id: "r3", input: { path: "/workspace/other.go" } }),
        ],
        true,
      );
      body.style.cssText = `position:fixed;top:0;left:0;inline-size:${String(width)}px;`;
      const group = body.querySelector<HTMLElement>(".tool-group");
      if (group?.classList.contains("tool-group-collapsed") === true) {
        group.querySelector<HTMLElement>(".tool-group-header")?.click();
      }
      await frame();
      await Promise.all(document.getAnimations().map((a) => a.finished));
      await frame();
      const cards = [...body.querySelectorAll<HTMLElement>(".tool-call")];
      expect(cards.map((c) => c.dataset["kind"])).toEqual(["read", "steering", "read"]);
      const at = (card: HTMLElement): { left: number; mid: number } => {
        const icon = card.querySelector(".tool-header > .tool-icon")?.getBoundingClientRect();
        const top = card.getBoundingClientRect().top;
        return {
          left: icon?.left ?? NaN,
          mid: (icon?.top ?? NaN) + (icon?.height ?? NaN) / 2 - top,
        };
      };
      const [above, box, below] = cards.map(at);
      for (const n of [above, below]) {
        expect(
          Math.abs((box?.left ?? NaN) - (n?.left ?? NaN)),
          "icon left edge",
        ).toBeLessThanOrEqual(1);
        expect(
          Math.abs((box?.mid ?? NaN) - (n?.mid ?? NaN)),
          "icon vertical centre",
        ).toBeLessThanOrEqual(1);
      }
    });
  }

  it("is as tall as a one-line read card when a file pill sits on its line", async () => {
    style = mountAppCSS();
    const body = render(
      (turnID) => [
        call(turnID, 1, { id: "r1", input: { path: "/workspace/main.go" } }),
        steering(turnID, 2, [SECOND_DOC], "Steering loaded: mandatory-instructions.md"),
        call(turnID, 3, { id: "r3", input: { path: "/workspace/other.go" } }),
      ],
      true,
    );
    body.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
    const group = body.querySelector<HTMLElement>(".tool-group");
    if (group?.classList.contains("tool-group-collapsed") === true) {
      group.querySelector<HTMLElement>(".tool-group-header")?.click();
    }
    await frame();
    await Promise.all(document.getAnimations().map((a) => a.finished));
    await frame();
    const headers = [...body.querySelectorAll<HTMLElement>(".tool-call .tool-header")];
    expect(headers).toHaveLength(3);
    const [read, box] = headers.map((h) => h.getBoundingClientRect());
    expect(box?.height, "steering header height").toBeCloseTo(read?.height ?? NaN, 0);
    const icon = headers[1]?.querySelector(".tool-icon")?.getBoundingClientRect();
    const pill = headers[1]?.querySelector(".tool-file-name")?.getBoundingClientRect();
    expect(
      Math.abs(
        (pill?.top ?? NaN) +
          (pill?.height ?? NaN) / 2 -
          ((icon?.top ?? NaN) + (icon?.height ?? NaN) / 2),
      ),
      "pill text centre against the icon centre",
    ).toBeLessThanOrEqual(1);
  });

  it("draws the same box in the chat and in a run step's transcript", async () => {
    style = mountAppCSS();
    const build = (turnID: string): Entry[] => [
      call(turnID, 1, { id: "r1", input: { path: "/workspace/main.go" } }),
      steering(turnID, 2, [CLIENT_DOC, SECOND_DOC, WORKSPACE_DOC, GLOBAL_DOC], CLEAN_TITLE),
    ];
    const chat = render(build, false);
    const pane = document.createElement("div");
    pane.className = "run-step-body";
    document.body.appendChild(pane);
    hosts.push(pane);
    const stream = createRunStepStream(() => pane);
    turnSeq += 1;
    const turnID = `t-sc-run-${String(turnSeq)}`;
    const turn: Turn = {
      id: turnID,
      n: 1,
      trigger: undefined,
      body: build(turnID),
      openEntries: new Map(),
      ts: 1,
      outcome: "completed",
      rewindTo: undefined,
      branchFrom: undefined,
    };
    stream.apply(new Map([["step", [{ turn, live: false }]]]));
    chat.style.cssText = "position:fixed;top:0;left:0;inline-size:360px;";
    pane.style.cssText = "position:fixed;top:0;left:400px;inline-size:360px;";
    for (const root of [chat, pane]) {
      const group = root.querySelector<HTMLElement>(".tool-group");
      if (group?.classList.contains("tool-group-collapsed") === true) {
        group.querySelector<HTMLElement>(".tool-group-header")?.click();
      }
    }
    await frame();
    await Promise.all(document.getAnimations().map((a) => a.finished));
    await frame();
    const look = (root: HTMLElement): unknown => {
      const box = onlyBox(root);
      const card = box.getBoundingClientRect();
      const icon = box.querySelector(".tool-header > .tool-icon")?.getBoundingClientRect();
      return {
        left: Math.round((icon?.left ?? NaN) - card.left),
        mid: Math.round((icon?.top ?? NaN) + (icon?.height ?? NaN) / 2 - card.top),
        colour: getComputedStyle(box.querySelector(".tool-title") as HTMLElement).color,
        names: nameParts(box),
      };
    };
    stream.dispose();
    expect(look(pane)).toEqual(look(chat));
  });
});
