// Show in file browser: on the editor's toolbar for every file in every view but a KAS tool output,
// which the file browser cannot show, and a press asks the browser to show the file open in the editor.
import { describe, it, expect, vi } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import type * as EditorActions from "./actions/editor.js";
import type * as ConfirmModule from "./confirm.js";
import type * as Git from "./git.js";
import type * as RouterModule from "./router.js";
import type * as TabsModule from "./tabs.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.apiClient(),
}));
vi.mock("./actions/editor.js", async (importOriginal) => {
  const real = await importOriginal<typeof EditorActions>();
  const { mocks } = await import("./__test-helpers__/editor-transitions-harness.js");
  return { ...real, ...mocks.editorActions(real) };
});
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ConfirmModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.confirm(),
}));
vi.mock("./git.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Git>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.git(),
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.tabs(),
}));
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RouterModule>()),
  ...(await import("./__test-helpers__/editor-transitions-harness.js")).mocks.router(),
}));

import {
  A,
  BIG,
  CONFLICT,
  CONFLICTED,
  FIRST,
  NEW,
  byId,
  drain,
  idOf,
  readURL,
  server,
  showURL,
  shown,
  stage,
  stageLarge,
  statURL,
} from "./__test-helpers__/editor-transitions-harness.js";
import {
  installTransitionHarness,
  opened,
  typeInto,
} from "./__test-helpers__/editor-transitions-driver.js";
import { BUS_FILES_REVEAL, onBus } from "./bus.js";
import { openFileGitDiff } from "./editor-openers.js";

installTransitionHarness();

const PROMPT = "/workspace/.kiro/prompts/Remember.md";
const IMAGE = "/workspace/out/shot.png";
const TOOL_OUTPUT = "/workspace/.kiro/tool-output/run.txt";

const revealBtn = (): HTMLButtonElement => byId<HTMLButtonElement>("editor-reveal-btn");

function stageImage(path: string): void {
  server.routes.set(statURL(path), {
    status: 200,
    body: {
      path,
      file_id: idOf("png bytes"),
      modified: "2026-10-01T00:00:00Z",
      size: 9,
      large: false,
      binary: true,
      utf8: false,
      read_only: false,
      renamable: true,
    },
  });
}

/** A KAS tool output: read-only, and the rename always refuses it. */
function stageToolOutput(path: string): void {
  const content = "tool said this\n";
  const meta = {
    path,
    file_id: idOf(content),
    modified: "2026-10-01T00:00:00Z",
    size: content.length,
    utf8: true,
    read_only: true,
    renamable: false,
  };
  server.routes.set(statURL(path), {
    status: 200,
    body: { ...meta, large: false, binary: false },
  });
  server.routes.set(readURL(path), { status: 200, body: { ...meta, content } });
}

describe("Show in file browser", () => {
  it("is a labelled glyph button", async () => {
    stage(A, FIRST);
    await opened(A);
    expect(revealBtn().getAttribute("aria-label")).toBe("Show in file browser");
    expect(revealBtn().dataset["tooltip"]).toBe("Show in file browser");
    expect(revealBtn().querySelector("svg")).not.toBeNull();
  });

  it("is on the toolbar in every view, and not on a tool output", async () => {
    const seen: Record<string, boolean> = {};

    stage(A, FIRST);
    await opened(A);
    seen["text"] = shown("editor-reveal-btn");
    byId("editor-edit-btn").click();
    typeInto("changed\n");
    seen["editing"] = shown("editor-reveal-btn");
    byId("editor-diff-btn").click();
    await drain();
    seen["diff vs saved"] = shown("editor-reveal-btn");

    stage(PROMPT, "# Remember\n");
    await opened(PROMPT);
    seen["markdown"] = shown("editor-reveal-btn");

    stageImage(IMAGE);
    await opened(IMAGE);
    seen["image"] = shown("editor-reveal-btn");

    stageLarge(BIG);
    await opened(BIG);
    seen["large"] = shown("editor-reveal-btn");

    stage(CONFLICTED, CONFLICT);
    await opened(CONFLICTED);
    seen["conflict"] = shown("editor-conflict-overlay") && shown("editor-reveal-btn");

    stageToolOutput(TOOL_OUTPUT);
    await opened(TOOL_OUTPUT);
    seen["read-only tool output"] =
      byId("editor-readonly-label").textContent === "KAS tool output" && shown("editor-reveal-btn");
    await opened(A);
    seen["text after a tool output"] = shown("editor-reveal-btn");

    const diffed = "/workspace/diffed.txt";
    stage(diffed, NEW);
    server.routes.set(showURL("diffed.txt"), { status: 200, body: { content: FIRST } });
    openFileGitDiff(diffed);
    await vi.waitFor(() => {
      expect(byId("editor-readonly-label").textContent).toBe("Git revision");
    });
    seen["diff vs HEAD"] = shown("editor-reveal-btn");

    expect(seen).toEqual({
      text: true,
      editing: true,
      "diff vs saved": true,
      markdown: true,
      image: true,
      large: true,
      conflict: true,
      "read-only tool output": false,
      "text after a tool output": true,
      "diff vs HEAD": true,
    });
  });

  it("asks the file browser to show the file open in the editor", async () => {
    const asked: string[] = [];
    const off = onBus(BUS_FILES_REVEAL, ({ path }) => {
      asked.push(path);
    });
    stage(A, FIRST);
    await opened(A);
    stage(PROMPT, "# Remember\n");
    await opened(PROMPT);
    revealBtn().click();
    off();
    expect(asked).toEqual([PROMPT]);
  });
});
