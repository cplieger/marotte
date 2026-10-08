// The client half of the page-shape contract: internal/preview/testdata/page-shapes.json is the
// contract and TestGrant_PageShapesMatchTheFixture is the other reader.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import shapesRaw from "../internal/preview/testdata/page-shapes.json?raw";
import { isPreviewablePage } from "./preview-page.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

interface ShapeRow {
  path: string;
  previewable: boolean;
}

const rows = JSON.parse(shapesRaw) as ShapeRow[];

beforeEach(() => {
  setWorkspaceRoot("/workspace");
});

afterEach(() => {
  resetWorkspace();
});

describe("isPreviewablePage", () => {
  it("reads a fixture that carries both answers", () => {
    expect(rows.some((r) => r.previewable)).toBe(true);
    expect(rows.some((r) => !r.previewable)).toBe(true);
  });

  it.each(rows)("answers $previewable for $path", ({ path, previewable }) => {
    expect(isPreviewablePage(path)).toBe(previewable);
  });

  it("falls back to /workspace before the handshake names the root", () => {
    resetWorkspace();
    expect(isPreviewablePage("/workspace/demo/index.html")).toBe(true);
  });

  it("follows a root published with a trailing slash", () => {
    setWorkspaceRoot("/custom/work/");
    expect(isPreviewablePage("/custom/work/demo/index.html")).toBe(true);
    expect(isPreviewablePage("/custom/work-old/demo/index.html")).toBe(false);
  });

  it("takes a page in a folder under the filesystem root, never one in the root itself", () => {
    setWorkspaceRoot("/");
    expect(isPreviewablePage("/demo/index.html")).toBe(true);
    expect(isPreviewablePage("/index.html")).toBe(false);
  });
});
