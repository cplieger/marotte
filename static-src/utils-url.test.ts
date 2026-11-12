import { afterEach, describe, it, expect } from "vitest";
import { rewriteServedImageSrc, servedFileRoute } from "./utils-url.js";
import { setAdditionalDirectories, _resetForTest as resetWorkspace } from "./workspace.js";
describe("rewriteServedImageSrc", () => {
  // An agent writes ![shot](/workspace/out/shot.png); asked of the SPA, that path
  // falls back to index.html, a broken image.
  it("points a workspace image at the byte-serving route", () => {
    expect(rewriteServedImageSrc("/workspace/out/shot.png")).toBe(
      "/api/file/download?path=%2Fworkspace%2Fout%2Fshot.png",
    );
  });

  // `/uploads` is a granted browse mount, so an agent's link to a composer-dropped file must render.
  it("points an uploads image at the byte-serving route", () => {
    expect(rewriteServedImageSrc("/uploads/shot.png")).toBe(
      "/api/file/download?path=%2Fuploads%2Fshot.png",
    );
  });

  it("covers the image extensions an agent actually writes", () => {
    for (const ext of ["png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "PNG"]) {
      const got = rewriteServedImageSrc(`/workspace/a.${ext}`);
      expect(got.startsWith("/api/file/download?path=")).toBe(true);
    }
  });

  // The extension list is closed on purpose: without it, any `![](…)` the model
  // writes would become a file read of an arbitrary workspace path.
  it("leaves a non-image workspace path alone", () => {
    for (const p of ["/workspace/.env", "/workspace/id_rsa", "/workspace/notes.md"]) {
      expect(rewriteServedImageSrc(p)).toBe(p);
    }
  });

  // A markdown destination is a URL, so `%20` is a space in the filename.
  it("decodes a percent-escaped path once", () => {
    expect(rewriteServedImageSrc("/workspace/my%20shot.png")).toBe(
      "/api/file/download?path=%2Fworkspace%2Fmy%20shot.png",
    );
  });

  // `/workspace/../config/x` passes the prefix test and names a `/config` file.
  it("leaves a path with a dot segment untouched", () => {
    for (const p of ["/workspace/../config/x.png", "/workspace/%2e%2e/config/x.png"]) {
      expect(rewriteServedImageSrc(p)).toBe(p);
    }
  });

  it("leaves remote and relative sources untouched", () => {
    for (const p of [
      "https://example.com/a.png",
      "./local.png",
      "/config/secret.png",
      "workspace/a.png",
    ]) {
      expect(rewriteServedImageSrc(p)).toBe(p);
    }
  });
});

// An additional directory is a file-browser mount, so a markdown link into it opens the file.
describe("servedFileRoute: additional directories", () => {
  afterEach(() => {
    resetWorkspace();
  });

  it("routes a link under an additional directory to the editor", () => {
    setAdditionalDirectories(["/data"]);
    expect(servedFileRoute("/data/notes/plan.md#L3")).toEqual({
      kind: "file",
      path: "/data/notes/plan.md",
      line: 3,
    });
    expect(rewriteServedImageSrc("/data/shot.png")).toBe(
      "/api/file/download?path=%2Fdata%2Fshot.png",
    );
  });

  it("routes nothing under a removed directory, a lookalike sibling or a dot segment", () => {
    setAdditionalDirectories(["/data"]);
    expect(servedFileRoute("/database/plan.md")).toBeNull();
    expect(servedFileRoute("/data/../config/x.md")).toBeNull();
    setAdditionalDirectories([]);
    expect(servedFileRoute("/data/plan.md")).toBeNull();
  });
});
