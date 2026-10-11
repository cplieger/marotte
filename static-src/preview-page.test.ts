// The client half of the page-shape contract: internal/preview/testdata/page-shapes.json is the
// contract and TestGrant_PageShapesMatchTheFixture is the other reader.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import shapesRaw from "../internal/preview/testdata/page-shapes.json?raw";
import type * as ApiClient from "./api-client.js";
import { isPreviewablePage } from "./preview-page.js";
import { _resetVersionsForTest, loadVersions } from "./versions.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

const api = vi.hoisted(() => ({ apiGet: vi.fn() }));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: api.apiGet,
}));

interface ShapeRow {
  path: string;
  previewable: boolean;
}

const shapes = JSON.parse(shapesRaw) as { config_dir: string; pages: ShapeRow[] };
const rows = shapes.pages;

async function serveConfigDir(dir: string): Promise<void> {
  api.apiGet.mockResolvedValue({ config_dir: dir });
  await loadVersions();
}

beforeEach(async () => {
  setWorkspaceRoot("/workspace");
  await serveConfigDir(shapes.config_dir);
});

afterEach(() => {
  resetWorkspace();
  _resetVersionsForTest();
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

  it("refuses the config directory and its ancestors under a filesystem-root workspace", async () => {
    setWorkspaceRoot("/");
    _resetVersionsForTest();
    await serveConfigDir("/data/marotte/");
    expect(isPreviewablePage("/data/marotte/index.html")).toBe(false);
    expect(isPreviewablePage("/data/index.html")).toBe(false);
    expect(isPreviewablePage("/data/marotte-old/index.html")).toBe(true);
  });

  it("defers a page in the config directory to the server while the directory is unknown", () => {
    _resetVersionsForTest();
    expect(isPreviewablePage("/workspace/state/config/index.html")).toBe(true);
  });
});
