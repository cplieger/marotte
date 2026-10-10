import { describe, it, expect, vi, beforeAll, beforeEach, afterAll, afterEach } from "vitest";
import fc from "fast-check";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";
import { makeToolCall } from "./__test-helpers__/model.js";
import type { EntryToolCall } from "./types.js";

// The root is module state, and every case but one asserts the pass-through form.
// Here rather than at the end of that case, which a failed assertion would skip.
afterEach(() => {
  resetWorkspace();
});

// Provide minimal DOM elements that transitive imports require at module level.
beforeAll(() => {
  for (const id of ["messages", "messages-wrap", "banner-stack"]) {
    if (!document.getElementById(id)) {
      const el = document.createElement("div");
      el.id = id;
      document.body.appendChild(el);
    }
  }
});

// Mock scroll.ts to avoid its eager DOM access ($.messages at module level).
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// Mock editor-openers.ts to avoid its transitive DOM dependencies.
const opened: string[] = [];
vi.mock("./editor-openers.js", () => ({
  openFile: (p: string) => {
    opened.push(`file:${p}`);
  },
  openFileDiff: (p: string) => {
    opened.push(`diff:${p}`);
  },
  openFileGitDiff: (p: string) => {
    opened.push(`gitdiff:${p}`);
  },
}));

// The bulk a previewed card fetches, plus one line per request so a case can assert ONE
// fetch serves every deferred piece. Hoisted above the mock factory.
const stubBulk = vi.hoisted(() => ({
  output: "",
  diffs: [] as { path: string; old_text?: string; new_text: string }[],
  calls: [] as string[],
}));
vi.mock("./tool-bulk.js", () => ({
  toolCallBulk: (chatID: string, toolCallID: string) => {
    stubBulk.calls.push(`${chatID}/${toolCallID}`);
    return Promise.resolve({ output: stubBulk.output, outputSpans: [], diffs: stubBulk.diffs });
  },
}));

const {
  extractSubtitle,
  mcpHue,
  buildToolCard,
  expandToolDetails,
  refreshToolDisclosure,
  insertDiffPreview,
  syncInteractionFact,
  syncOffloadLink,
} = await import("./tool-card.js");
const { toolCardOptsFor } = await import("./tool-card-opts.js");

describe("extractSubtitle", () => {
  const cases: {
    name: string;
    input: Record<string, unknown> | undefined;
    expected: string;
  }[] = [
    { name: "undefined input", input: undefined, expected: "" },
    { name: "empty object", input: {}, expected: "" },
    { name: "query key", input: { query: "find all files" }, expected: "find all files" },
    { name: "pattern key", input: { pattern: "*.ts" }, expected: "*.ts" },
    { name: "command key", input: { command: "ls -la" }, expected: "ls -la" },
    { name: "url key", input: { url: "https://example.com" }, expected: "https://example.com" },
    { name: "path key", input: { path: "/src/main.ts" }, expected: "/src/main.ts" },
    { name: "explanation key", input: { explanation: "doing stuff" }, expected: "doing stuff" },
    {
      name: "priority order: query wins over path",
      input: { path: "/a", query: "q" },
      expected: "q",
    },
    {
      name: "priority order: pattern wins over command",
      input: { command: "c", pattern: "p" },
      expected: "p",
    },
    { name: "empty string value skipped", input: { query: "", path: "/x" }, expected: "/x" },
    { name: "non-string value skipped", input: { query: 42, path: "/y" }, expected: "/y" },
    {
      name: "truncation at 121 chars",
      input: { query: "a".repeat(121) },
      expected: "a".repeat(117) + "\u2026",
    },
    {
      name: "exactly 120 chars not truncated",
      input: { query: "b".repeat(120) },
      expected: "b".repeat(120),
    },
    { name: "no matching keys", input: { foo: "bar", baz: "qux" }, expected: "" },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(extractSubtitle(input)).toBe(expected);
  });
});

describe("mcpHue", () => {
  const knownServers: { server: string; hue: number }[] = [
    { server: "github", hue: mcpHue("github") },
    { server: "s3", hue: mcpHue("s3") },
    { server: "postgres", hue: mcpHue("postgres") },
    { server: "filesystem", hue: mcpHue("filesystem") },
    { server: "brave-search", hue: mcpHue("brave-search") },
  ];

  it.each(knownServers)("deterministic for $server → $hue", ({ server, hue }) => {
    expect(mcpHue(server)).toBe(hue);
    expect(mcpHue(server)).toBe(hue);
  });

  it("returns value in [0, 360) for empty string", () => {
    const result = mcpHue("");
    expect(result).toBeGreaterThanOrEqual(0);
    expect(result).toBeLessThan(360);
  });

  it("different inputs produce different hues (for known distinct servers)", () => {
    const hues = new Set(knownServers.map((s) => s.hue));
    expect(hues.size).toBeGreaterThanOrEqual(3);
  });
});

describe("mcpHue properties", () => {
  it("output is always in [0, 360)", () => {
    fc.assert(
      fc.property(fc.string(), (s) => {
        const h = mcpHue(s);
        expect(h).toBeGreaterThanOrEqual(0);
        expect(h).toBeLessThan(360);
        expect(Number.isInteger(h)).toBe(true);
      }),
    );
  });

  it("deterministic: same input always yields same output", () => {
    fc.assert(
      fc.property(fc.string(), (s) => {
        expect(mcpHue(s)).toBe(mcpHue(s));
      }),
    );
  });

  it("distribution: 100 random strings cover at least 3 of 4 quadrants", () => {
    fc.assert(
      fc.property(
        fc.array(fc.string({ minLength: 1, maxLength: 50 }), { minLength: 100, maxLength: 100 }),
        (strings) => {
          const quadrants = new Set<number>();
          for (const s of strings) {
            quadrants.add(Math.floor(mcpHue(s) / 90));
          }
          expect(quadrants.size).toBeGreaterThanOrEqual(3);
        },
      ),
    );
  });
});

describe("outcome is the row's one mark, not a word and not a badge", () => {
  it("a finished card prints no status word anywhere in its text", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    for (const status of ["completed", "failed"] as const) {
      const card = buildToolCard({
        id: "t1",
        title: "strReplace",
        kind: "edit",
        status,
        input: { path: "src/a.ts", oldStr: "a", newStr: "b" },
        live: false,
      });
      // The literal wire enum must not appear as visible text.
      expect(card.textContent).not.toContain("completed");
      expect(card.textContent).not.toContain("failed");
      expect(card.querySelector(".tool-status")).toBeNull();
    }
  });

  it("keeps its identity glyph on success and REPLACES it on a failure", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const { toolIcon, outcomeIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const ok = buildToolCard({
      id: "t2",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    const okIcon = ok.querySelector(".tool-icon");
    expect(okIcon?.classList.contains("is-ok")).toBe(true);
    // Success is the ROW's OWN glyph, tinted — not a general success mark. One
    // mark, and no badge beside it.
    const identity = okIcon?.querySelector("svg")?.outerHTML ?? "";
    expect(identity).toBe((iconEl(toolIcon("execute", "executePwsh")) as HTMLElement).outerHTML);
    expect(identity).not.toBe((iconEl(outcomeIcon("ok")) as HTMLElement).outerHTML);
    expect(okIcon?.querySelectorAll("svg")).toHaveLength(1);

    const bad = buildToolCard({
      id: "t3",
      title: "executePwsh",
      kind: "execute",
      status: "failed",
      live: false,
    });
    const badIcon = bad.querySelector(".tool-icon");
    expect(badIcon?.classList.contains("is-fail")).toBe(true);
    // Still ONE mark, and it is a different SHAPE — which is what keeps hue from
    // being the only channel (WCAG 1.4.1).
    expect(badIcon?.querySelectorAll("svg")).toHaveLength(1);
    expect(badIcon?.querySelector("svg")?.outerHTML).not.toBe(identity);
  });

  it("gives the four outcome states four distinct shapes", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const { outcomeIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const identity = buildToolCard({
      id: "t2d",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    }).querySelector(".tool-icon svg")?.outerHTML;
    // The shape channel: if two states ever resolved to one glyph, or a
    // silhouette collided with a row's identity glyph, this is what fails.
    const marks = [
      identity ?? "",
      ...(["ok", "fail", "warn", "denied"] as const).map(
        (s) => (iconEl(outcomeIcon(s)) as HTMLElement).outerHTML,
      ),
    ];
    expect(new Set(marks).size).toBe(marks.length);
  });

  it("carries the outcome word in the accessible name instead", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t4",
      title: "strReplace",
      kind: "edit",
      status: "failed",
      input: { path: "src/auth.go", oldStr: "a", newStr: "b" },
      live: false,
    });
    expect(card.getAttribute("aria-label")).toContain("auth.go");
    expect(card.getAttribute("aria-label")).toContain("failed");
  });

  // `aborted` is a tool status as well as a run-level one, which is why
  // `OutcomeStatus` serves the History page's run verdict through this same writer.
  it("paints an aborted subject amber with the stop silhouette, not as a failure", async () => {
    const { buildToolCard, applyOutcome } = await import("./tool-card.js");
    const { outcomeIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const card = buildToolCard({
      id: "t4a",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    applyOutcome(card, "aborted", "Open feature-pipeline", {
      kind: "other",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    });
    const icon = card.querySelector(".tool-icon");
    expect(icon?.classList.contains("is-warn")).toBe(true);
    // Stopped is not broken: the failure tint must be gone, not merely joined.
    expect(icon?.classList.contains("is-fail")).toBe(false);
    expect(icon?.classList.contains("is-ok")).toBe(false);
    // Amber is shared with a policy refusal, so the SHAPE is what separates them,
    // and it comes from the shared set rather than from a copy here.
    expect(icon?.querySelector("svg")?.outerHTML).toBe(
      (iconEl(outcomeIcon("warn")) as HTMLElement).outerHTML,
    );
    // Rebuilt, not stacked: the identity glyph from the first paint is gone.
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
    expect(card.dataset["outcome"]).toBe("warn");
    expect(card.getAttribute("aria-label")).toBe("Open feature-pipeline, aborted");
  });

  it("leaves exactly one svg in the slot however often applyOutcome is called", async () => {
    const { buildToolCard, applyOutcome } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t4b",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    const icon = card.querySelector(".tool-icon");
    const info = {
      kind: "execute",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    } as const;
    // Same state twice, then a change, then the same state again: the writer REPLACES the
    // slot's child, so two marks in one glyph is unrepresentable.
    applyOutcome(card, "completed", "Run Command", info);
    applyOutcome(card, "completed", "Run Command", info);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
    applyOutcome(card, "failed", "Run Command", info);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
    applyOutcome(card, "failed", "Run Command", info);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
  });

  it("restores the identity glyph when a failed card re-renders as ok", async () => {
    const { buildToolCard, applyOutcome } = await import("./tool-card.js");
    const { toolIcon } = await import("./icons.js");
    const { iconEl } = await import("./icon-el.js");
    const card = buildToolCard({
      id: "t4c",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    const icon = card.querySelector(".tool-icon");
    // Named off the icon registry, not read back off the card, which has already been
    // through applyOutcome once.
    const identity = (iconEl(toolIcon("execute", "executePwsh")) as HTMLElement).outerHTML;
    expect(icon?.querySelector("svg")?.outerHTML).toBe(identity);
    const info = {
      kind: "execute",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    } as const;
    applyOutcome(card, "failed", "Run Command", info);
    expect(icon?.querySelector("svg")?.outerHTML).not.toBe(identity);
    // Back to ok: the glyph captured at build comes back, so the writer cannot
    // strand a surface on a silhouette it borrowed.
    applyOutcome(card, "completed", "Run Command", info);
    expect(icon?.querySelector("svg")?.outerHTML).toBe(identity);
    expect(icon?.querySelectorAll("svg")).toHaveLength(1);
  });

  it("does not strand a glyph-less slot on the silhouette it borrowed", async () => {
    const { applyOutcome } = await import("./tool-card.js");
    // A caller that mounts no identity glyph: the writer owns the silhouette it wrote, so a
    // return to `ok` must clear it.
    const node = document.createElement("div");
    const icon = document.createElement("span");
    icon.className = "tool-icon";
    node.appendChild(icon);
    const info = {
      kind: "execute",
      writesFile: false,
      filePath: "",
      fileBasename: "",
      diffSources: null,
      mcp: null,
      disclosed: null,
      denial: null,
    } as const;
    applyOutcome(node, "failed", "Run Command", info);
    expect(icon.querySelectorAll("svg")).toHaveLength(1);
    applyOutcome(node, "completed", "Run Command", info);
    expect(icon.querySelectorAll("svg")).toHaveLength(0);
    expect(icon.classList.contains("is-ok")).toBe(true);
    expect(node.getAttribute("aria-label")).toBe("Run Command, succeeded");
  });
});

describe("the depth ladder", () => {
  it("a claim-only kind gets no details region and no toggle", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t5",
      title: "read_files",
      kind: "read",
      status: "completed",
      input: { path: "src/a.ts" },
      live: false,
    });
    expect(card.querySelector(".tool-details")).toBeNull();
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    // The CSS affordance hook tracks the toggle: no toggle, no class.
    expect(card.querySelector(".tool-summary")?.classList.contains("has-disclosure")).toBe(false);
  });

  it("an edit gets a details region — the old tier axis gave it none", async () => {
    // The `output` makes the region non-empty (the chevron is gated on it); an edit's diff
    // preview is a SIBLING of the region.
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t6",
      output: "1 replacement made\n",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one\ntwo", newStr: "one\nTWO" },
      live: false,
    });
    expect(card.querySelector(".tool-details")).not.toBeNull();
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    // ...and a toggle brings the affordance class with it (14-tools.css keys
    // cursor/hover on the whole summary).
    expect(card.querySelector(".tool-summary")?.classList.contains("has-disclosure")).toBe(true);
  });

  it("has no second View diff button — the subject is the link", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t7",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one", newStr: "two" },
      live: false,
    });
    expect(card.querySelector(".tool-diff-view-btn")).toBeNull();
    expect(card.textContent).not.toContain("View diff");
  });

  it("the filename opens the DIFF on a change and the FILE on a read", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    opened.length = 0;
    const edit = buildToolCard({
      id: "t8",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one", newStr: "two" },
      live: false,
    });
    edit.querySelector<HTMLElement>(".tool-file-link")?.click();
    expect(opened).toEqual(["gitdiff:src/a.ts"]);

    opened.length = 0;
    const read = buildToolCard({
      id: "t9",
      title: "read_files",
      kind: "read",
      status: "completed",
      input: { path: "src/a.ts" },
      live: false,
    });
    read.querySelector<HTMLElement>(".tool-file-link")?.click();
    expect(opened).toEqual(["file:src/a.ts"]);
  });

  it("the `+N -M` stats open the call's own diff, at an ABSOLUTE path", async () => {
    // Absolute because /api/file resolves against the granted roots and denies a
    // relative path, so the tab this opens would render its 403 instead.
    const { buildToolCard } = await import("./tool-card.js");
    setWorkspaceRoot("/workspace");
    opened.length = 0;
    const edit = buildToolCard({
      id: "t8b",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: "one", newStr: "two" },
      live: false,
    });
    edit.querySelector<HTMLElement>(".tool-diff-stats")?.click();
    expect(opened).toEqual(["diff:/workspace/src/a.ts"]);
  });

  it("the `+N more hunks` count opens the same diff as the stats", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    setWorkspaceRoot("/workspace");
    opened.length = 0;
    // Ten changes ten lines apart: ten hunks, more than the 24-row window holds.
    const lines = Array.from({ length: 100 }, (_, i) => `line ${String(i)}`);
    const changed = lines.map((l, i) => (i % 10 === 0 ? `${l} changed` : l));
    const edit = buildToolCard({
      id: "t8c",
      title: "strReplace",
      kind: "edit",
      status: "completed",
      input: { path: "src/a.ts", oldStr: lines.join("\n"), newStr: changed.join("\n") },
      live: false,
    });
    const more = edit.querySelector<HTMLButtonElement>("button.tool-diff-more");
    expect(more?.textContent).toMatch(/^\+\d+ more hunks$/);
    more?.click();
    expect(opened).toEqual(["diff:/workspace/src/a.ts"]);
  });

  it("a move states from and to, which its claim line cannot carry", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t10",
      title: "smartRelocate",
      kind: "move",
      status: "completed",
      input: { sourcePath: "old/a.ts", destinationPath: "new/a.ts" },
      live: false,
    });
    const row = card.querySelector(".tool-move-row");
    expect(row?.textContent).toContain("old/a.ts");
    expect(row?.textContent).toContain("new/a.ts");
  });
});

describe("a tool's output renders verbatim", () => {
  it("linkifies nothing, and keeps the path text as written", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "s1",
      title: "grepSearch",
      kind: "search",
      status: "completed",
      input: { query: "needle" },
      output: "src/a.ts:42: some match\n",
      live: false,
    });
    // The output is painted on first open, so the assertions need the region open.
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    expect(card.querySelector(".tool-output .inline-file-link")).toBeNull();
    expect(card.querySelectorAll(".tool-output button.inline-file-link")).toHaveLength(0);
    // Verbatim: the path token is still one run of text, not a chip's label.
    expect(card.querySelector(".tool-output")?.textContent).toContain("src/a.ts:42: some match");
    card.remove();
  });
});

describe("disclose_context and policy denials", () => {
  // The card names the DOCUMENT rather than the tool that fetched it.
  it("names the skill a disclose_context call loaded", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t1",
      title: "disclose_context",
      kind: "other",
      status: "completed",
      live: false,
      disclosed: { type: "skill", display_name: "code-review", uri: "file:///x/code-review" },
    });
    expect(card.dataset["title"]).toBe("Loaded skill: code-review");
    expect(card.dataset["disclosed"]).toBe("skill");
  });

  it("distinguishes a steering document from a skill", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t2",
      title: "disclose_context",
      kind: "other",
      status: "completed",
      live: false,
      disclosed: { type: "steering", display_name: "marotte", uri: "file:///x/marotte.md" },
    });
    expect(card.dataset["title"]).toBe("Loaded steering: marotte");
  });

  // A refusal is not a failure: the state, the badge shape and the accessible name all
  // have to say policy.
  it("renders a policy denial as its own state, not a failure", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t3",
      title: "executeBash",
      kind: "execute",
      status: "failed",
      live: false,
      denial: {
        capability: "shell",
        resource: "rm -rf /",
        scope: "workspace",
        source: "/workspace/.kiro/permissions.yaml",
        rule: { capability: "shell", effect: "deny", match: ["rm *"] },
      },
    });
    expect(card.dataset["outcome"]).toBe("denied");
    expect(card.dataset["denied"]).toBe("1");
    expect(card.querySelector(".tool-icon")?.classList.contains("is-denied")).toBe(true);
    expect(card.getAttribute("aria-label")).toContain("blocked by security policy");
  });

  // The rule and its file are the actionable half: the user owns the policy, so a
  // refusal that names what fired is one step from changing it.
  it("shows the matched rule and where it lives", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t4",
      title: "executeBash",
      kind: "execute",
      status: "failed",
      live: false,
      denial: {
        capability: "shell",
        resource: "curl evil.example",
        scope: "user",
        source: "/root/.kiro/permissions.yaml",
        rule: {
          capability: "shell",
          effect: "deny",
          match: ["curl *"],
          exclude: ["curl localhost*"],
        },
      },
    });
    // Behind the disclosure, built on first open: the claim line already says "blocked by
    // security policy", and the rule that fired is depth 2.
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    const text = card.querySelector(".tool-denial")?.textContent ?? "";
    card.remove();
    expect(text).toContain("shell");
    expect(text).toContain("curl *");
    expect(text).toContain("!curl localhost*");
    expect(text).toContain("/root/.kiro/permissions.yaml");
  });

  // An ordinary failure must keep reading as a failure; the denial state is
  // additive, not a reclassification of every red card.
  it("leaves an ordinary failure alone", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "t5",
      title: "executeBash",
      kind: "execute",
      status: "failed",
      live: false,
    });
    expect(card.dataset["outcome"]).toBe("fail");
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    expect(card.querySelector(".tool-denial")).toBeNull();
    card.remove();
  });
});

// The header IS the disclosure's hit target (disclosure-row.ts); the controls inside it
// keep their own click, and a claim-only card stays inert.

// Every fixture carries an `output`, because a card with nothing to reveal has no toggle
// for a header click to reach.
describe("tool card: whole-header disclosure", () => {
  it("a click on the header toggles the card's details", () => {
    const card = buildToolCard({
      id: "hdr1",
      output: "total 0\n",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      input: { command: "ls -la" },
      live: false,
    });
    document.body.appendChild(card);

    const header = card.querySelector<HTMLElement>(".tool-header")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    header.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    header.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    card.remove();
  });

  it("a click on the title inside the summary toggles it too", () => {
    const card = buildToolCard({
      id: "hdr2",
      output: "total 0\n",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      input: { command: "ls -la" },
      live: false,
    });
    document.body.appendChild(card);

    const title = card.querySelector<HTMLElement>(".tool-title")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;

    title.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    card.remove();
  });

  it("a click on the description below the title toggles the card", () => {
    // Search/fetch/generic cards put their first input fact under the title, and that line
    // is part of the one disclosure target.
    const card = buildToolCard({
      id: "hdr-subtitle",
      output: "3 results\n",
      title: "remote_web_search",
      kind: "fetch",
      status: "completed",
      input: { query: "marotte fold animation" },
      live: false,
    });
    document.body.appendChild(card);

    const subtitle = card.querySelector<HTMLElement>(".tool-subtitle")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(subtitle.textContent).toBe("marotte fold animation");

    subtitle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    card.remove();
  });

  it("shows non-MCP tool names without underscores", () => {
    const card = buildToolCard({
      id: "hdr-human-title",
      title: "remote_web_search",
      kind: "fetch",
      status: "completed",
      input: { query: "marotte" },
      live: false,
    });
    const title = card.querySelector<HTMLElement>(".tool-title")!;
    expect(title.textContent).toBe("remote web search");
    expect(card.dataset["title"]).toBe("remote web search");
  });

  it("the chevron still toggles exactly once", () => {
    // The forwarded click bubbles back into the header's listener, so a chevron
    // click could have counted twice and landed back where it started.
    const card = buildToolCard({
      id: "hdr3",
      output: "total 0\n",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      input: { command: "ls -la" },
      live: false,
    });
    document.body.appendChild(card);

    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    card.remove();
  });

  it("the filename link opens the change and does NOT toggle the card", () => {
    const card = buildToolCard({
      id: "hdr4",
      output: "wrote 3 lines\n",
      title: "fsAppend",
      kind: "write",
      status: "completed",
      input: { path: "src/main.ts" },
      live: false,
    });
    document.body.appendChild(card);

    const link = card.querySelector<HTMLElement>(".tool-file-link")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(link).not.toBeNull();

    const before = opened.length;
    link.click();
    expect(opened.length).toBe(before + 1);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    card.remove();
  });

  it("puts the PATH in the hover text and the action in the accessible name", () => {
    // Two channels: the chip shows the basename, so the tooltip carries the path alone (a
    // leading action line would clip long paths under the two-line cap, `tooltip-size.test.ts`)
    // and the action lives in the NAME.
    const card = buildToolCard({
      id: "hdr5",
      output: "wrote 3 lines\n",
      title: "fsWrite",
      kind: "write",
      status: "completed",
      input: { path: "/workspace/marotte/static-src/fundamentals/turn-footer.ts" },
      live: false,
    });
    const link = card.querySelector<HTMLElement>(".tool-file-link")!;

    expect(link.getAttribute("data-tooltip")).toBe(
      "/workspace/marotte/static-src/fundamentals/turn-footer.ts",
    );
    expect(link.getAttribute("aria-label")).toBe("Open the diff for turn-footer.ts");
  });

  it("names a read card's chip for the file it opens, not a diff", () => {
    const card = buildToolCard({
      id: "hdr5-read",
      title: "Read File",
      kind: "read",
      status: "completed",
      input: { path: "/workspace/a.txt" },
      live: false,
    });
    expect(card.querySelector(".tool-file-link")?.getAttribute("aria-label")).toBe("Open a.txt");
  });

  it("a claim-only card has no toggle and its header stays inert", () => {
    // `readFile` resolves to kind `read`, depth 1 "none": no details region, so the header
    // must not become a control.
    const card = buildToolCard({
      id: "hdr5",
      title: "readFile",
      kind: "read",
      status: "completed",
      input: { path: "src/main.ts" },
      live: false,
    });
    document.body.appendChild(card);

    const header = card.querySelector<HTMLElement>(".tool-header")!;
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    expect(card.querySelector(".tool-details")).toBeNull();

    header.click();
    expect(card.querySelector("[aria-expanded]")).toBeNull();

    card.remove();
  });
});

// The card's BODY is built on first open, because a transcript mounts dozens of collapsed
// cards. `.tool-output` is in the shell: the live update path streams into it unopened.

describe("the details body is deferred to first open", () => {
  it("mounts an EMPTY details region with the output slot in it", () => {
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "completed",
      live: false,
      output: "hello from the build\n",
    });
    const details = card.querySelector(".tool-details");
    expect(details).not.toBeNull();
    // The slot exists so a streamed chunk has somewhere to land...
    expect(details?.querySelector(".tool-output")).not.toBeNull();
    // ...and nothing has been painted into it.
    expect(details?.querySelector("pre")).toBeNull();
  });

  it("paints the output when the disclosure is activated", () => {
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "completed",
      live: false,
      output: "hello from the build\n",
    });
    document.body.appendChild(card);
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    expect(card.querySelector(".tool-output pre")?.textContent).toContain("hello from the build");
    card.remove();
  });

  it("builds once, however many times the disclosure is toggled", () => {
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "completed",
      live: false,
      output: "one line\n",
    });
    document.body.appendChild(card);
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
    toggle?.click();
    toggle?.click();
    toggle?.click();
    expect(card.querySelectorAll(".tool-output pre")).toHaveLength(1);
    card.remove();
  });

  it("makes the output readable straight after expandToolDetails, which the failure path needs", async () => {
    // `messages-tools.ts` force-opens a failed card and then reads `.tool-output` to offer
    // "Explain this error", so the body is built by the time the call returns.
    const { expandToolDetails } = await import("./tool-card.js");
    const card = buildToolCard({
      id: "tc1",
      title: "Execute",
      kind: "execute",
      status: "failed",
      live: false,
      output: "cc: fatal error\n",
    });
    document.body.appendChild(card);
    expandToolDetails(card);
    expect(card.querySelector(".tool-output")?.textContent).toContain("cc: fatal error");
    card.remove();
  });
});

// A card with nothing to disclose loses its disclosure. Emptiness is a property of
// `.tool-details`; the diff preview and the Explain button sit outside it and stay. The
// region cannot simply be read before first open, so the predicate combines build-time
// facts with the live output region.

/** A settled tool call that produced nothing at all, in replay mode. `other` resolves to
 *  depth 1 `generic`, which gives it a details region: the shape of a subagent that
 *  failed to START. */
function bareCard(id = "bare1"): HTMLDivElement {
  return buildToolCard({
    id,
    title: "invoke_sub_agent",
    kind: "other",
    status: "failed",
    live: false,
  });
}

describe("a card with nothing to disclose", () => {
  it("has no chevron", () => {
    expect(bareCard().querySelector(".tool-disclosure")).toBeNull();
  });

  it("exposes no aria-expanded anywhere on the card", () => {
    // The bar a claim-only card already meets: the primitive keeps writing `aria-expanded`,
    // so the attribute goes only when the button does.
    expect(bareCard().querySelector("[aria-expanded]")).toBeNull();
  });

  it("drops the summary's pointer affordance", () => {
    // `has-disclosure` is what 14-tools.css keys the cursor and the hover wash on.
    // The chevron GUTTER is keyed on the region instead, so it does not move.
    const summary = bareCard().querySelector(".tool-summary");
    expect(summary?.classList.contains("has-disclosure")).toBe(false);
  });

  it("keeps its details region, which is what the update path streams into", () => {
    expect(bareCard().querySelector(".tool-details")).not.toBeNull();
  });

  it("opens nothing when its header is clicked", () => {
    // Read off the REGION: a detached button's listeners ride along with it, so an unrefused
    // forward would open the region with no `aria-expanded` anywhere.
    const card = bareCard();
    document.body.appendChild(card);

    card.querySelector<HTMLElement>(".tool-header")!.click();

    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    card.remove();
  });

  it("refuses a FORCE-open, which is the door a persisted flag comes back through", () => {
    // `messages-blocks.ts` re-opens a card from a `tool:<id>` flag that outlives the chevron,
    // so the refusal is `expandToolDetails`'s own; opened here, the region would be stranded
    // with no chevron to close it.
    const card = bareCard("bare-force");
    document.body.appendChild(card);

    expandToolDetails(card);

    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    card.remove();
  });

  it("keeps the chevron when the call DID produce output", () => {
    const card = buildToolCard({
      id: "bare-out",
      title: "invoke_sub_agent",
      kind: "other",
      status: "failed",
      live: false,
      output: "the delegate refused\n",
    });
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
  });

  it("counts output the reader cannot see as nothing", () => {
    // One newline is the same defect from a narrower input. The Explain gate reads the same
    // region with the same trim, so a blank-output card offers no button either.
    const card = buildToolCard({
      id: "bare-blank",
      title: "invoke_sub_agent",
      kind: "other",
      status: "failed",
      live: false,
      output: "\n",
    });
    expect(card.querySelector(".tool-disclosure")).toBeNull();
  });

  it("has no chevron on a call still in flight either", () => {
    // The wire status is not evidence about the region, and both production call
    // sites pass `live: true`, so a replayed in-progress call is this exact shape.
    const card = buildToolCard({
      id: "bare-pending",
      title: "invoke_sub_agent",
      kind: "other",
      status: "pending",
      live: true,
    });
    expect(card.querySelector(".tool-disclosure")).toBeNull();
  });

  it("keeps the chevron on an in-flight call that carries its INPUT", () => {
    // The latch, not the status, covers the common case: nearly every toggle-bearing live
    // call carries input, which `detailsBody` will write.
    const card = buildToolCard({
      id: "bare-pending-input",
      title: "executePwsh",
      kind: "execute",
      status: "in_progress",
      live: true,
      input: { command: "ls -la" },
    });
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
  });

  it("keeps the chevron on a settled LIVE call, whose input dump is the reveal", () => {
    // `detailsBody` writes the input `<pre>` only in live mode, and this call has settled,
    // so nothing else says the region will fill.
    const card = buildToolCard({
      id: "bare-live-input",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: true,
      input: { command: "ls -la" },
    });
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
  });

  it("keeps the chevron on a REFUSED call, whose rule is the whole point", () => {
    const card = buildToolCard({
      id: "bare-denied",
      title: "executePwsh",
      kind: "execute",
      status: "failed",
      live: false,
      denial: {
        capability: "shell",
        resource: "rm -rf /",
        scope: "workspace",
        source: ".kiro/permissions.yaml",
      },
    });
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
  });

  it("keeps the chevron on a previewed call whose OUTPUT bytes are still on the server", () => {
    // `outputBytes` is what says the bulk holds output: the store stamps it only in
    // the branch that cut the output, so it is the exact signal rather than a guess.
    const card = buildToolCard({
      id: "bare-full",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
      hasFull: true,
      outputBytes: 48_000,
      chatID: "c1",
    });
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
  });

  it("goes bare when an OUTPUT-cut previewed call carries no chat id either", () => {
    // The other member behind the same conjunct: one chat-id condition over the whole table,
    // so the output arm cannot drift from the diff arm.
    const card = buildToolCard({
      id: "bare-full-out-nochat",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
      hasFull: true,
      outputBytes: 48_000,
    });
    expect(card.querySelector(".tool-disclosure")).toBeNull();
  });

  it("keeps the chevron on a previewed call whose cut was DIFFS only", () => {
    // `hasFull` with a DIFFS-only cut: `outputBytes` is absent, so the diff member's own arm
    // is what keeps this card openable.
    const card = buildToolCard({
      id: "bare-full-diffs",
      title: "fsWrite",
      kind: "edit",
      status: "completed",
      live: false,
      hasFull: true,
      chatID: "c1",
    });
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
  });

  it("goes bare when the previewed call carries NO CHAT ID, so nothing can be fetched", () => {
    // The diffs-only cut with no chat id: `detailsBody` returns before the request, so a
    // chevron would open onto a region that can never fill. `toolCardOptsFor` defaults
    // `chatID` to `""`, which makes this reachable.
    const card = buildToolCard({
      id: "bare-full-nochat",
      title: "fsWrite",
      kind: "edit",
      status: "completed",
      live: false,
      hasFull: true,
    });
    expect(card.querySelector(".tool-disclosure")).toBeNull();
  });

  it("goes bare when the previewed call is not an EDIT, so no piece is deferred", () => {
    // The control: `hasFull` with no `outputBytes` on a non-diff kind leaves both members
    // false, so the card is bare.
    const card = buildToolCard({
      id: "bare-full-search",
      title: "grepSearch",
      kind: "search",
      status: "completed",
      live: false,
      hasFull: true,
      chatID: "c1",
    });
    expect(card.querySelector(".tool-disclosure")).toBeNull();
  });

  it("gets its chevron BACK when output lands, and it toggles", () => {
    const card = bareCard("bare-revive");
    document.body.appendChild(card);
    expect(card.querySelector(".tool-disclosure")).toBeNull();

    const out = card.querySelector(".tool-output")!;
    out.appendChild(document.createElement("pre")).textContent = "late output\n";
    refreshToolDisclosure(card);

    const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
    expect(toggle).not.toBeNull();
    // The RE-ATTACHED button, not a second one: its controller's listeners rode
    // along, so it still drives the region.
    toggle!.click();
    expect(toggle!.getAttribute("aria-expanded")).toBe("true");
    card.remove();
  });

  it("stays bare when a chunk paints a blank line into the region", () => {
    // The live half of the case above: a settled card can still receive a chunk (a
    // terminal outlives the call), and a `<pre>` on its own is not content.
    const card = bareCard("bare-blank-live");
    card.querySelector(".tool-output")!.appendChild(document.createElement("pre"));
    refreshToolDisclosure(card);
    expect(card.querySelector(".tool-disclosure")).toBeNull();
  });

  it("collapses an OPEN card whose region is emptied, rather than stranding it", () => {
    const card = buildToolCard({
      id: "bare-empty-open",
      title: "invoke_sub_agent",
      kind: "other",
      status: "failed",
      live: false,
      output: "transient\n",
    });
    document.body.appendChild(card);
    expandToolDetails(card);
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    // The build-time guarantee goes with the content it described.
    delete card.dataset["disclosable"];
    card.querySelector(".tool-output")!.replaceChildren();
    refreshToolDisclosure(card);

    expect(card.querySelector(".tool-disclosure")).toBeNull();
    expect(card.querySelector("[aria-expanded]")).toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    card.remove();
  });

  it("paints no output <pre> on a latched card whose output is blank", () => {
    // The build path reads the same output string, reachable with blank output: the INPUT
    // arm latches this card, and an untrimmed gate would put an empty `<pre>` in its region.
    const card = buildToolCard({
      id: "bare-blank-body",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: true,
      input: { command: "ls -la" },
      output: "   \n  \n",
    });
    document.body.appendChild(card);
    expandToolDetails(card);
    expect(card.querySelector(".tool-output pre")).toBeNull();
    card.remove();
  });

  it("paints no output <pre> when the FETCHED bulk is blank either", async () => {
    // The same, one fetch out: a `\r`-and-spaces progress animation persists whole and cuts
    // on serve, so the store stamps `outputBytes` and the bulk it opens is blank.
    stubBulk.output = "\r      ".repeat(1757);
    const card = buildToolCard({
      id: "bare-blank-bulk",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
      hasFull: true,
      outputBytes: 12_292,
      chatID: "c1",
    });
    document.body.appendChild(card);
    expandToolDetails(card);
    // A macrotask, so the fetch's resolved `.then` has certainly run: asserting on an
    // absent node would otherwise pass before the paint it is meant to refuse.
    await new Promise((r) => setTimeout(r, 0));
    expect(card.querySelector(".tool-output pre")).toBeNull();
    card.remove();
  });
});

// A card whose region the reader had open is BUILT open, not opened afterwards: creating
// it closed and opening it in the same task animates the reveal. The stylesheet is
// mounted because the height transition lives on `.uip-disclosure-region`; the second
// case is the control that proves it is in force.

describe("a card built with its details open", () => {
  let style: HTMLStyleElement;
  beforeAll(async () => {
    const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
    style = mountAppCSS();
  });
  afterAll(() => {
    style.remove();
  });

  const opts = {
    id: "tc-open",
    title: "Execute",
    kind: "execute",
    status: "completed" as const,
    live: false,
    output: "hello from the build\n",
  };

  it("paints its body and animates nothing", () => {
    const card = buildToolCard({ ...opts, detailsOpen: true });
    document.body.appendChild(card);
    const details = card.querySelector<HTMLElement>(".tool-details");

    // The deferred builder ran at construction, so the region has content before any
    // click — which is what makes `open: true` honest rather than an empty reveal.
    expect(card.querySelector(".tool-output pre")?.textContent).toContain("hello from the build");
    expect(details?.getAttribute("aria-hidden")).toBe("false");
    // `applyHeight(true, false)` clears the height instead of tweening to it.
    expect(details?.style.height).toBe("");
    expect(card.querySelector(".tool-disclosure")?.getAttribute("aria-expanded")).toBe("true");
    expect(details?.getAnimations()).toHaveLength(0);
    card.remove();
  });

  it.each([true, false])("leaves a failed call's details CLOSED, live=%s", (live) => {
    // A failure is not a reason to be born open, live or replay: expand-on-fail is the live
    // FLIP's (`expandToolDetails`), and live-ness does not enter this decision.
    const card = buildToolCard({ ...opts, live, status: "failed" });
    document.body.appendChild(card);

    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    expect(card.querySelector(".tool-disclosure")?.getAttribute("aria-expanded")).toBe("false");
    // The deferred body never ran, which is what makes the close real rather than a
    // hidden region already holding a built output dump.
    expect(card.querySelector(".tool-output pre")).toBeNull();
    card.remove();
  });

  it("still ANIMATES a region opened by an EVENT after the mount", () => {
    // `expandToolDetails` stays the path for a call that fails or is refused
    // mid-stream, and that reveal must keep its animation.
    const card = buildToolCard({ ...opts });
    document.body.appendChild(card);
    const details = card.querySelector<HTMLElement>(".tool-details");
    expect(details?.getAnimations()).toHaveLength(0);

    expandToolDetails(card);
    // `.tool-details` transitions height AND opacity; asserted non-empty rather than as a
    // count.
    expect(details?.getAnimations().length).toBeGreaterThan(0);
    card.remove();
  });
});

// Deferred content: what the transcript dropped, loaded when the reader OPENS the card,
// in ONE bulk request.

describe("deferred content a previewed card loads on open", () => {
  const DIFF = { path: "src/auth.go", old_text: "before\n", new_text: "after\n" };

  // The stub is module state and one case above leaves an output in it, so every
  // field is restored rather than only the ones a case sets.
  beforeEach(() => {
    stubBulk.output = "";
    stubBulk.diffs = [];
    stubBulk.calls.length = 0;
  });

  /** A previewed EDIT card whose diffs the transcript dropped: `hasFull` with no
   *  resting diff of its own, which is the shape the diff member answers for. */
  const diffsOnly = {
    title: "fsWrite",
    kind: "edit",
    status: "completed" as const,
    live: false,
    hasFull: true,
    chatID: "c1",
  };

  /** A macrotask, so the bulk's resolved `.then` has certainly run: asserting on
   *  an absent node would otherwise pass before the paint it is meant to see. */
  const settle = (): Promise<unknown> => new Promise((r) => setTimeout(r, 0));

  it("issues NO request for a card nobody opens", async () => {
    const card = buildToolCard({ ...diffsOnly, id: "def-no-open" });
    document.body.appendChild(card);
    await settle();
    expect(stubBulk.calls).toEqual([]);
    expect(card.querySelector(".tool-diff-preview")).toBeNull();
    card.remove();
  });

  it("inserts the diff BEFORE the details region on open, and it survives a close", async () => {
    stubBulk.diffs = [DIFF];
    const card = buildToolCard({ ...diffsOnly, id: "def-open-diff" });
    document.body.appendChild(card);
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;

    toggle.click();
    await settle();

    const preview = card.querySelector(".tool-diff-preview");
    expect(preview).not.toBeNull();
    // An edit's diff is its depth-1 claim, so it lands where every other edit
    // card's does: in the card's resting state, above the region.
    expect(preview?.nextElementSibling?.classList.contains("tool-details")).toBe(true);

    // Closing the box is not un-loading the diff: it was never inside the region.
    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    card.remove();
  });

  it("fetches ONE bulk for a card whose output AND diff were both dropped", async () => {
    stubBulk.output = "the whole of it\n";
    stubBulk.diffs = [DIFF];
    const card = buildToolCard({ ...diffsOnly, id: "def-both", outputBytes: 48_000 });
    document.body.appendChild(card);

    card.querySelector<HTMLElement>(".tool-disclosure")!.click();
    await settle();

    // One request, not one per piece: the bulk carries both, and a second would be
    // a second megabyte on the wire for an answer already in hand.
    expect(stubBulk.calls).toEqual(["c1/def-both"]);
    expect(card.querySelector(".tool-output pre")?.textContent).toContain("the whole of it");
    expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    card.remove();
  });

  it("leaves exactly ONE preview when an update lands its diff mid-flight", async () => {
    // After the await, a `tool_call_update` may already have inserted the preview through
    // this same exported function (`applyDiffUpdate`); without the table's presence check
    // the card holds two mini-diffs.
    stubBulk.diffs = [DIFF];
    const card = buildToolCard({ ...diffsOnly, id: "def-double-insert" });
    document.body.appendChild(card);

    card.querySelector<HTMLElement>(".tool-disclosure")!.click();
    insertDiffPreview(card, DIFF.path, { oldText: DIFF.old_text, newText: DIFF.new_text });
    expect(card.querySelectorAll(".tool-diff-preview")).toHaveLength(1);

    await settle();

    expect(card.querySelectorAll(".tool-diff-preview")).toHaveLength(1);
    card.remove();
  });

  it("adopts on a card built ALREADY open, which never gets a click", async () => {
    // `detailsOpen` is the transcript's window drop and re-mount, and the run tab's
    // per-frame rebuild: the reader had this region open, so the load is theirs.
    stubBulk.diffs = [DIFF];
    const card = buildToolCard({ ...diffsOnly, id: "def-born-open", detailsOpen: true });
    document.body.appendChild(card);
    // The request went out at CONSTRUCTION, with no click anywhere: how many the
    // card issues is the case above's claim, not this one's.
    expect(stubBulk.calls[0]).toBe("c1/def-born-open");

    await settle();
    expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    card.remove();
  });
});

// The spec door: only for a path a spec directory holds.

describe("the spec door on a file chip", () => {
  const card = (path: string): HTMLElement =>
    buildToolCard({
      id: `spec-door-${path}`,
      title: "fsWrite",
      kind: "edit",
      status: "completed",
      input: { path },
      live: false,
    });

  it("offers it for a path a spec directory holds, carrying that directory", () => {
    const door = card(".kiro/specs/demo/tasks.md").querySelector(".tool-spec-link");
    expect(door).not.toBeNull();
    // The DIRECTORY, not the file: it is the spec tab's ref, and the page reads
    // every document of the directory rather than the one this card wrote.
    expect(door?.getAttribute("data-spec-dir")).toBe(".kiro/specs/demo");
    // Chrome-marked, so find-in-chat prunes it rather than matching UI copy.
    expect(door?.hasAttribute("data-vk-chrome")).toBe(true);
  });

  it("resolves a spec directory under a nested repo root", () => {
    expect(
      card("myrepo/.kiro/specs/demo/design.md")
        .querySelector(".tool-spec-link")
        ?.getAttribute("data-spec-dir"),
    ).toBe("myrepo/.kiro/specs/demo");
  });

  it("withholds it from every other path, the .kiro tree included", () => {
    for (const path of [
      "src/main.ts",
      ".kiro/steering/marotte.md",
      ".kiro/specs",
      "notes/.kiro/specs.md",
      "a/b/.kiro/specs/demo/tasks.md",
    ]) {
      expect(card(path).querySelector(".tool-spec-link"), path).toBeNull();
    }
  });

  it("offers it for a FILE sitting directly in the specs root, which is the shared rule", () => {
    // `specDirOf` is lexical, the twin of Go's `spec.DirOf` (whose `the_directory` case marks
    // a DELETE of a spec directory), so one segment under `specs/` reads as a spec directory
    // and the door opens a page answering 404 with its empty state.
    expect(
      card(".kiro/specs/tasks.md").querySelector(".tool-spec-link")?.getAttribute("data-spec-dir"),
    ).toBe(".kiro/specs/tasks.md");
  });

  it("withholds it from a card that names no file at all", () => {
    const bare = buildToolCard({
      id: "spec-door-bare",
      title: "executePwsh",
      kind: "execute",
      status: "completed",
      live: false,
    });
    expect(bare.querySelector(".tool-file-link")).toBeNull();
    expect(bare.querySelector(".tool-spec-link")).toBeNull();
  });
});

describe("a card's title", () => {
  function channels(card: HTMLElement): string[] {
    const header = card.querySelector<HTMLElement>(".tool-header")!;
    return [
      card.querySelector(".tool-title")?.textContent ?? "",
      header.title,
      card.dataset["title"] ?? "",
    ];
  }

  // KAS sends the static "Run Command" whenever the model wrote no description,
  // which is most shell calls, so the placeholder told a reader nothing.
  it("titles a description-less shell card with its command and drops the repeat row", () => {
    const card = buildToolCard({
      id: "title-shell",
      title: "Run Command",
      kind: "execute",
      status: "completed",
      input: { command: "go test ./..." },
      live: false,
    });
    expect(channels(card)).toEqual(["go test ./...", "go test ./...", "go test ./..."]);
    expect(card.querySelector(".tool-subtitle")).toBeNull();
    expect(card.getAttribute("aria-label")).toBe("go test ./..., succeeded");
  });

  it("keeps the command's hyphens and underscores byte for byte", () => {
    const card = buildToolCard({
      id: "title-shell-flags",
      title: "Run Command",
      kind: "execute",
      status: "completed",
      input: { command: "go test -run Test_parse ./internal/x-y" },
      live: false,
    });
    expect(card.querySelector(".tool-title")?.textContent).toBe(
      "go test -run Test_parse ./internal/x-y",
    );
  });

  it("lets a model-written description outrank the command, which stays below it", () => {
    const card = buildToolCard({
      id: "title-described",
      title: "Check vet output",
      kind: "execute",
      status: "completed",
      input: { command: "go vet ./..." },
      live: false,
    });
    expect(card.querySelector(".tool-title")?.textContent).toBe("Check vet output");
    expect(card.querySelector(".tool-subtitle")?.textContent).toBe("go vet ./...");
  });

  it("collapses a multi-line command to one bounded line", () => {
    const card = buildToolCard({
      id: "title-heredoc",
      title: "Run Command",
      kind: "execute",
      status: "completed",
      input: { command: "cat <<'EOF' > out.txt\n" + "x".repeat(300) + "\nEOF\n" },
      live: false,
    });
    expect(card.dataset["title"]).toBe("cat <<'EOF' > out.txt " + "x".repeat(95) + "\u2026");
  });

  it("keeps the placeholder when the call carries no command", () => {
    const card = buildToolCard({
      id: "title-no-input",
      title: "Run Command",
      kind: "execute",
      status: "completed",
      live: false,
    });
    expect(channels(card)).toEqual(["Run Command", "Run Command", "Run Command"]);
  });

  // KAS titles a described shell call with the model's own sentence, and that
  // sentence is full of paths and flags a name humanizer would turn into spaces.
  it("renders a described title verbatim on all three channels", () => {
    const card = buildToolCard({
      id: "title-prose",
      title: "Run go test with --dry-run in ./internal/foo_bar",
      kind: "execute",
      status: "completed",
      input: { command: "go test --dry-run ./internal/foo_bar" },
      live: false,
    });
    expect(channels(card)).toEqual([
      "Run go test with --dry-run in ./internal/foo_bar",
      "Run go test with --dry-run in ./internal/foo_bar",
      "Run go test with --dry-run in ./internal/foo_bar",
    ]);
  });

  it("keeps a hook card's own hook name", () => {
    const card = buildToolCard({
      id: "title-hook",
      title: "Hook fired: lint-on-save",
      kind: "hook",
      status: "completed",
      live: false,
    });
    expect(card.querySelector(".tool-title")?.textContent).toBe("Hook fired: lint-on-save");
  });

  it("samples the commands, not the placeholder, in a group of shell calls", async () => {
    const { buildToolGroupShell, groupBody, refreshGroupHeader } = await import("./tool-group.js");
    const group = buildToolGroupShell();
    for (const [id, command] of [
      ["group-a", "git status"],
      ["group-b", "go test ./..."],
    ] as const) {
      groupBody(group).appendChild(
        buildToolCard({
          id,
          title: "Run Command",
          kind: "execute",
          status: "completed",
          input: { command },
          live: false,
        }),
      );
    }
    document.body.appendChild(group);
    refreshGroupHeader(group);
    expect(group.querySelector(".tool-group-count")?.textContent).toBe(
      "Ran 2 commands: git status, go test ./...",
    );
    group.remove();
  });
});

describe("a tool output KAS offloaded to a file", () => {
  const offload = {
    path: "/config/.kiro/sessions/cli/sess_1/tool-outputs/shell-0a1b2c3d.txt",
    total_chars: 812345,
  };
  const card = (): HTMLDivElement =>
    buildToolCard({
      id: "tc-off",
      title: "Run Command",
      kind: "execute",
      status: "completed",
      input: { command: "cat big.log" },
      output: "head of the log\n",
      live: false,
      offload,
    });

  it("links the full file from the card's details region", async () => {
    const { parseRoute } = await import("./route-path.js");
    const link = card().querySelector<HTMLAnchorElement>(".tool-details .tool-offload-link");
    expect(link?.textContent).toBe("Open full output (812,345 chars)");
    const href = link?.getAttribute("href") ?? "";
    expect(parseRoute(href, "")).toEqual({ kind: "file", path: offload.path });
  });

  it("opens the absolute path in the editor on a plain click", () => {
    opened.length = 0;
    const link = card().querySelector<HTMLAnchorElement>(".tool-offload-link");
    link?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
    expect(opened).toEqual([`file:${offload.path}`]);
  });

  it("leaves a modified click to the browser", () => {
    opened.length = 0;
    const link = card().querySelector<HTMLAnchorElement>(".tool-offload-link");
    const e = new MouseEvent("click", {
      bubbles: true,
      cancelable: true,
      button: 0,
      ctrlKey: true,
    });
    link?.dispatchEvent(e);
    expect(opened).toEqual([]);
    expect(e.defaultPrevented).toBe(false);
  });

  it("adds one link however often the field arrives", () => {
    const c = card();
    syncOffloadLink(c, offload);
    syncOffloadLink(c, offload);
    expect(c.querySelectorAll(".tool-offload-link")).toHaveLength(1);
  });

  it("links a claim-only read card from its claim row", () => {
    const read = buildToolCard({
      id: "tc-ls",
      title: "List Directory",
      kind: "read",
      status: "completed",
      input: { path: "/workspace", recursive: true },
      live: false,
      offload,
    });
    expect(read.querySelector(".tool-details")).toBeNull();
    const link = read.querySelector<HTMLAnchorElement>(".tool-summary > .tool-offload-link");
    expect(link?.textContent).toBe("Open full output (812,345 chars)");
    opened.length = 0;
    link?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
    expect(opened).toEqual([`file:${offload.path}`]);
  });

  it("adds none without an offload", () => {
    const plain = buildToolCard({
      id: "tc-plain",
      title: "Run Command",
      kind: "execute",
      status: "completed",
      input: { command: "ls" },
      output: "a\n",
      live: false,
    });
    expect(plain.querySelector(".tool-offload-link")).toBeNull();
  });
});

describe("a hook card", () => {
  const hookCall = (over: Partial<EntryToolCall> = {}): HTMLDivElement =>
    buildToolCard(
      toolCardOptsFor(
        makeToolCall({
          id: "hook-1",
          title: "Hook fired: lint",
          kind: "hook",
          status: "completed",
          ...over,
        }),
        false,
        "c-1",
      ),
    );

  const click = (target: Element | null | undefined): void => {
    target?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }));
  };

  it("makes the file chip the one control that opens the hook's file", () => {
    const card = hookCall({ source_path: ".kiro/hooks/lint.kiro.hook" });
    const chip = card.querySelector<HTMLElement>(".tool-file-link");
    expect(chip?.tagName).toBe("BUTTON");
    expect(chip?.getAttribute("aria-label")).toBe("Open the hook file lint.kiro.hook");
    expect(chip?.dataset["tooltip"]).toBe(".kiro/hooks/lint.kiro.hook");
    expect(card.querySelectorAll("button, a, [role='button'], [tabindex]")).toHaveLength(1);
  });

  it("opens the hook's file from the chip and nowhere else on the row", () => {
    setWorkspaceRoot("/workspace");
    const card = hookCall({ source_path: ".kiro/hooks/lint.kiro.hook" });
    opened.length = 0;
    click(card.querySelector(".tool-title"));
    click(card.querySelector(".tool-summary"));
    expect(opened, "the title and the row are inert").toEqual([]);
    click(card.querySelector(".tool-file-link"));
    expect(opened).toEqual(["file:/workspace/.kiro/hooks/lint.kiro.hook"]);
  });

  it("offers no control when KAS named no file", () => {
    const card = hookCall();
    expect(card.querySelector("button, a, [role='button'], [tabindex]")).toBeNull();
    opened.length = 0;
    click(card.querySelector(".tool-summary"));
    expect(opened).toEqual([]);
  });
});

describe("a tool call whose ask was answered", () => {
  const build = (interaction?: {
    type: string;
    outcome: string;
    choice?: string;
  }): HTMLDivElement =>
    buildToolCard({
      id: "tc-ask",
      title: "Read File",
      kind: "read",
      status: "completed",
      input: { path: "/workspace/a.txt" },
      live: false,
      ...(interaction !== undefined && { interaction }),
    });

  it("states the answer on the claim row, which a claim-only card has", () => {
    const c = build({ type: "tool_approval", outcome: "selected", choice: "allow_always" });
    expect(c.querySelector(".tool-details")).toBeNull();
    expect(c.querySelector(".tool-summary > .tool-fact")?.textContent).toBe("Always allowed");
  });

  it("rewrites the one line in place when the field arrives on an update", () => {
    const c = build();
    expect(c.querySelector(".tool-fact")).toBeNull();
    syncInteractionFact(c, { type: "user_input", outcome: "answered", choice: "Use Go" });
    syncInteractionFact(c, { type: "user_input", outcome: "answered", choice: "Use Rust" });
    expect([...c.querySelectorAll(".tool-fact")].map((e) => e.textContent)).toEqual([
      "Answered: Use Rust",
    ]);
  });

  it("leaves a stated answer standing when a later frame carries none", () => {
    const c = build({ type: "tool_approval", outcome: "selected", choice: "reject_once" });
    syncInteractionFact(c, undefined);
    expect(c.querySelector(".tool-fact")?.textContent).toBe("Rejected");
  });
});
