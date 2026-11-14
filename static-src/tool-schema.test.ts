// Unit tests for tool-schema.ts — pure functions, no DOM dependency.
import { describe, it, expect } from "vitest";
import {
  profileFor,
  toolDepth1,
  cardDepth1,
  formatMCPToolName,
  renderInfoFor,
  isToolActive,
  isToolDone,
  toolTitleText,
  commandTitle,
  loadedResources,
  loadedClaim,
} from "./tool-schema.js";
import type { ToolKind, ToolDepth1 as Depth1, LoadedResource } from "./tool-schema.js";
import type { ToolStatus } from "./types.js";

// ---------------------------------------------------------------------------
// profileFor — table-driven
// ---------------------------------------------------------------------------

const PROBE = { server_name: "probe", tool_name: "echo_text" };

describe("profileFor", () => {
  const kindCases: {
    kind: string;
    expectedKind: ToolKind;
    expectedWrites: boolean;
  }[] = [
    { kind: "read", expectedKind: "read", expectedWrites: false },
    { kind: "edit", expectedKind: "edit", expectedWrites: true },
    { kind: "write", expectedKind: "write", expectedWrites: true },
    { kind: "delete", expectedKind: "delete", expectedWrites: false },
    { kind: "move", expectedKind: "move", expectedWrites: false },
    { kind: "search", expectedKind: "search", expectedWrites: false },
    { kind: "execute", expectedKind: "execute", expectedWrites: false },
    { kind: "fetch", expectedKind: "fetch", expectedWrites: false },
    { kind: "think", expectedKind: "think", expectedWrites: false },
    { kind: "switch_mode", expectedKind: "switch_mode", expectedWrites: false },
    { kind: "hook", expectedKind: "hook", expectedWrites: false },
    { kind: "steering", expectedKind: "steering", expectedWrites: false },
  ];

  it.each(kindCases)(
    "no tool name + kind=$kind → $expectedKind",
    ({ kind, expectedKind, expectedWrites }) => {
      const p = profileFor(kind, null);
      expect(p.kind).toBe(expectedKind);
      expect(p.writesFile).toBe(expectedWrites);
    },
  );

  it.each(["unknownKind", "constructor", "toString"])("kind=%s → other", (kind) => {
    const p = profileFor(kind, null);
    expect(p.kind).toBe("other");
    expect(p.writesFile).toBe(false);
  });

  it("an MCP identity is an integration call whatever the kind", () => {
    const p = profileFor("execute", PROBE);
    expect(p.kind).toBe("mcp");
    expect(p.writesFile).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// toolDepth1 / cardDepth1 — what a card reveals, per kind
// ---------------------------------------------------------------------------

describe("toolDepth1", () => {
  const cases: { kind: ToolKind; expected: Depth1 }[] = [
    // Claim-only, and each for a stated reason: reads arrive in floods (p99 75
    // per turn), and delete_file takes ONE path so a list would restate the claim.
    { kind: "read", expected: "none" },
    { kind: "delete", expected: "none" },
    { kind: "hook", expected: "none" },
    { kind: "steering", expected: "none" },
    { kind: "think", expected: "none" },
    { kind: "switch_mode", expected: "none" },
    // The two that carry real information.
    { kind: "edit", expected: "diff" },
    { kind: "write", expected: "diff" },
    { kind: "execute", expected: "output" },
    { kind: "shell", expected: "output" },
    { kind: "command", expected: "output" },
    { kind: "move", expected: "move" },
    { kind: "search", expected: "search" },
    { kind: "fetch", expected: "fetch" },
    { kind: "mcp", expected: "mcp" },
    { kind: "browser", expected: "generic" },
    { kind: "other", expected: "generic" },
  ];

  it.each(cases)("$kind → $expected", ({ kind, expected }) => {
    expect(toolDepth1(kind)).toBe(expected);
  });

  it("cardDepth1 is the kind's for any call that is not a load", () => {
    for (const { kind, expected } of cases) {
      expect(cardDepth1({ kind, loaded: null })).toBe(expected);
    }
  });

  it("an edit CAN be expanded, which the old tier axis prevented", () => {
    // The tier this replaced gave `simple` (which included edit) no toggle at
    // all, so the diff had to live in the resting state to be visible.
    expect(cardDepth1({ kind: "edit", loaded: null })).not.toBe("none");
  });

  it("a load reveals what it loaded, whatever its kind", () => {
    const loaded = [{ id: "skill::land", type: "skill", name: "land", server: "" }] as const;
    expect(cardDepth1({ kind: "other", loaded })).toBe("load");
    expect(cardDepth1({ kind: "read", loaded })).toBe("load");
  });
});

// ---------------------------------------------------------------------------
// formatMCPToolName
// ---------------------------------------------------------------------------

describe("formatMCPToolName", () => {
  const cases: { input: string; expected: string }[] = [
    { input: "create_issue", expected: "create issue" },
    { input: "list_buckets", expected: "list buckets" },
    { input: "tool", expected: "tool" },
    { input: "", expected: "" },
    { input: "a_b_c", expected: "a b c" },
    { input: "_leading", expected: " leading" },
    { input: "trailing_", expected: "trailing " },
  ];

  it.each(cases)("$input → $expected", ({ input, expected }) => {
    expect(formatMCPToolName(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// renderInfoFor — integration-level table-driven
// ---------------------------------------------------------------------------

describe("renderInfoFor", () => {
  it("an edit with oldStr/newStr input", () => {
    const info = renderInfoFor("edit", {
      path: "src/main.ts",
      oldStr: "a",
      newStr: "b",
    });
    expect(info.kind).toBe("edit");
    expect(info.writesFile).toBe(true);
    expect(info.filePath).toBe("src/main.ts");
    expect(info.fileBasename).toBe("main.ts");
    expect(info.diffSources).toEqual({ oldText: "a", newText: "b" });
    expect(info.mcp).toBeNull();
    expect(info.toolName).toBe("");
  });

  it("a write with text input", () => {
    const info = renderInfoFor("write", { path: "out.txt", text: "hello" });
    expect(info.kind).toBe("write");
    expect(info.writesFile).toBe(true);
    expect(info.diffSources).toEqual({ oldText: "", newText: "hello" });
  });

  it("read tool — no diff, no mcp", () => {
    const info = renderInfoFor("read", { path: "foo.ts" });
    expect(info.kind).toBe("read");
    expect(info.writesFile).toBe(false);
    expect(info.diffSources).toBeNull();
    expect(info.mcp).toBeNull();
  });

  it("an MCP call carries the identity the server named", () => {
    const info = renderInfoFor(
      "other",
      { tool_id: "tool::probe::echo_text", arguments: {} },
      { mcp: PROBE },
    );
    expect(info.kind).toBe("mcp");
    expect(info.mcp).toEqual(PROBE);
  });

  it("an envelope input names nothing on its own", () => {
    const info = renderInfoFor("other", { tool_id: "tool::probe::echo_text", arguments: {} });
    expect(info.kind).toBe("other");
    expect(info.mcp).toBeNull();
    expect(info.toolName).toBe("");
  });

  it("a server-named built-in carries its name", () => {
    const info = renderInfoFor("other", {}, { toolName: "update_workflow" });
    expect(info.toolName).toBe("update_workflow");
    expect(info.mcp).toBeNull();
  });

  it("unknown tool with no input", () => {
    const info = renderInfoFor("unknown", undefined);
    expect(info.kind).toBe("other");
    expect(info.writesFile).toBe(false);
    expect(info.filePath).toBe("");
    expect(info.fileBasename).toBe("");
    expect(info.diffSources).toBeNull();
    expect(info.mcp).toBeNull();
  });

  it("picks targetFile when path is absent", () => {
    const info = renderInfoFor("write", { targetFile: "dest.ts", text: "x" });
    expect(info.filePath).toBe("dest.ts");
    expect(info.fileBasename).toBe("dest.ts");
  });

  it("a hook card's file is the hook definition", () => {
    const info = renderInfoFor("hook", undefined, { sourcePath: ".kiro/hooks/lint.json" });
    expect(info.filePath).toBe(".kiro/hooks/lint.json");
    expect(info.fileBasename).toBe("lint.json");
  });
});

// ---------------------------------------------------------------------------
// isToolActive / isToolDone — the two predicates every settled-card rule reads
// ---------------------------------------------------------------------------

describe("the status predicates partition the wire enum", () => {
  /** Keyed over the GENERATED union, so the table is complete in both directions at compile time.
   *  `active` and `done` stay complements: `messages-tools.ts` settles a card on `done`, so
   *  `aborted` (settled at turn close) is terminal, not a third state. */
  const PREDICATES: Readonly<Record<ToolStatus, { active: boolean; done: boolean }>> = {
    pending: { active: true, done: false },
    in_progress: { active: true, done: false },
    completed: { active: false, done: true },
    failed: { active: false, done: true },
    aborted: { active: false, done: true },
  };

  for (const [status, want] of Object.entries(PREDICATES) as (readonly [
    ToolStatus,
    { active: boolean; done: boolean },
  ])[]) {
    it(`reads ${status} as active=${String(want.active)} done=${String(want.done)}`, () => {
      expect(isToolActive(status)).toBe(want.active);
      expect(isToolDone(status)).toBe(want.done);
    });
  }
});

describe("toolTitleText", () => {
  const cases: readonly (readonly [string, string])[] = [
    ["web_search", "web search"],
    ["remote_web_search", "remote web search"],
    ["Running: read_files", "read files"],
    ["Run tests in ./internal/foo_bar", "Run tests in ./internal/foo_bar"],
    ["Run go test with --dry-run", "Run go test with --dry-run"],
    ["Check marotte-acp.md", "Check marotte-acp.md"],
    ["foo_bar.go", "foo_bar.go"],
    ["Run Command", "Run Command"],
    ["Hook fired: lint-on-save", "Hook fired: lint-on-save"],
  ];
  for (const [raw, want] of cases) {
    it(`renders ${JSON.stringify(raw)} as ${JSON.stringify(want)}`, () => {
      expect(toolTitleText(raw)).toBe(want);
    });
  }
});

describe("commandTitle", () => {
  it("answers the command for KAS's placeholder on an execute call", () => {
    expect(commandTitle("Run Command", "execute", { command: "go test ./..." })).toBe(
      "go test ./...",
    );
  });

  it("collapses whitespace and bounds the result", () => {
    expect(commandTitle("Run Command", "execute", { command: "  make\n\tlint  " })).toBe(
      "make lint",
    );
    expect(commandTitle("Run Command", "execute", { command: "a".repeat(200) })).toBe(
      "a".repeat(119) + "\u2026",
    );
  });

  it("leaves a model-written description to the caller", () => {
    expect(commandTitle("Check vet output", "execute", { command: "go vet ./..." })).toBeNull();
  });

  // An MCP or custom tool titled "Run Command" with a command input is representable;
  // the literal is KAS's execute_bash fallback and nothing else's.
  it("answers nothing for a non-execute kind", () => {
    expect(commandTitle("Run Command", "other", { command: "go vet ./..." })).toBeNull();
  });

  it("answers nothing when the input carries no usable command", () => {
    expect(commandTitle("Run Command", "execute", undefined)).toBeNull();
    expect(commandTitle("Run Command", "execute", null)).toBeNull();
    expect(commandTitle("Run Command", "execute", "go vet")).toBeNull();
    expect(commandTitle("Run Command", "execute", { command: 7 })).toBeNull();
    expect(commandTitle("Run Command", "execute", { command: " \n " })).toBeNull();
  });
});

// KAS's resource-id grammar, one case per prefix the load tools accept, plus ids outside it.
describe("loadedResources", () => {
  const cases: { id: string; want: Omit<LoadedResource, "id"> }[] = [
    {
      id: "tool::agent-mail::fetch_inbox",
      want: { type: "mcp", name: "fetch_inbox", server: "agent-mail" },
    },
    { id: "tool::inspect_workflow", want: { type: "tool", name: "inspect_workflow", server: "" } },
    {
      id: "builtin::inspect_workflow",
      want: { type: "tool", name: "inspect_workflow", server: "" },
    },
    { id: "skill::land", want: { type: "skill", name: "land", server: "" } },
    { id: "steering::code-style", want: { type: "steering", name: "code-style", server: "" } },
    { id: "power::aws::overview", want: { type: "overview", name: "aws", server: "" } },
    { id: "power::aws::tool::docs::search", want: { type: "mcp", name: "search", server: "docs" } },
    { id: "power::aws::steering::guide", want: { type: "steering", name: "guide", server: "" } },
    { id: "plugin::kit::skill::release", want: { type: "skill", name: "release", server: "" } },
    { id: "plugin::kit::tool::srv::do", want: { type: "mcp", name: "do", server: "srv" } },
    { id: "tool::mailbox::", want: { type: "unknown", name: "tool::mailbox::", server: "" } },
    { id: "tool::::x", want: { type: "unknown", name: "tool::::x", server: "" } },
    { id: "power::aws", want: { type: "unknown", name: "power::aws", server: "" } },
    { id: "plugin::::skill::x", want: { type: "unknown", name: "plugin::::skill::x", server: "" } },
    { id: "memory::x", want: { type: "unknown", name: "memory::x", server: "" } },
    { id: "skill::team::land", want: { type: "skill", name: "team::land", server: "" } },
    { id: "steering::web::css", want: { type: "steering", name: "web::css", server: "" } },
    { id: "skill::", want: { type: "unknown", name: "skill::", server: "" } },
    { id: "power::aws::skill::a::b", want: { type: "skill", name: "a::b", server: "" } },
    { id: "plugin::kit::steering::a::b", want: { type: "steering", name: "a::b", server: "" } },
    {
      id: "power::a::b::skill::x",
      want: { type: "unknown", name: "power::a::b::skill::x", server: "" },
    },
    {
      id: "plugin::a::b::steering::x",
      want: { type: "unknown", name: "plugin::a::b::steering::x", server: "" },
    },
  ];
  it.each(cases)("$id", ({ id, want }) => {
    expect(loadedResources("load_resource", { id })).toEqual([{ id, ...want }]);
  });

  it("reads an older tool_load's tool_ids, skipping non-strings and empties", () => {
    const got = loadedResources("tool_load", { tool_ids: ["skill::a", 7, "", "tool::b"] });
    expect(got?.map((r) => r.id)).toEqual(["skill::a", "tool::b"]);
  });

  it("is null for a call that is not a load, or names no id yet", () => {
    expect(loadedResources("read_file", { id: "skill::a" })).toBeNull();
    expect(loadedResources("load_resource", undefined)).toBeNull();
    expect(loadedResources("load_resource", {})).toBeNull();
    expect(loadedResources("tool_load", { tool_ids: [] })).toBeNull();
  });

  it("names what loaded on the claim line", () => {
    const loaded = loadedResources("load_resource", { id: "tool::agent-mail::fetch_inbox" });
    expect(loaded === null ? "" : loadedClaim(loaded)).toBe("Loaded agent-mail fetch_inbox");
  });
});
