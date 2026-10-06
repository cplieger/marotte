import { describe, it, expect } from "vitest";
import { exfilShaped, rewriteServedImageSrc } from "./utils-url.js";
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

describe("exfilShaped", () => {
  const prose = "word ".repeat(44); // 220 chars of plain prose, the issue-prefill body
  it.each([
    ["a query of 200 characters", `https://e.example/?q=${"a.".repeat(99)}`],
    ["21 consecutive percent escapes", `https://e.example/?d=${"%41".repeat(21)}`],
    ["a 40-character base64 run", `https://e.example/?d=${"Q".repeat(40)}`],
    ["an AWS access key id", `https://e.example/?k=${"AKIA"}ABCDEFGHIJKLMNOP`],
    ["an ssh key marker", "https://e.example/?k=ssh-ed25519%20AAAA"],
    ["a private key header", "https://e.example/?k=BEGIN+RSA+PRIVATE+KEY"],
    ["a Slack token", "https://e.example/?t=xoxb-123-abc"],
    ["a classic GitHub token", `https://e.example/?t=ghp_${"a1B2c3".repeat(6)}`],
    ["a fine-grained GitHub token", `https://e.example/?t=github_pat_${"a1_B2".repeat(5)}`],
    ["an issue-prefill link", `https://github.com/o/r/issues/new?title=x&body=${prose}`],
    ["a payload in a fragment with no query", `https://e.example/page#${"%41".repeat(21)}`],
    ["a token in a fragment with no query", `https://e.example/#ghp_${"a1B2c3".repeat(6)}`],
    ["a protocol-relative link", `//e.example/?d=${"Q".repeat(40)}`],
    ["a payload behind C0 controls", `\x01https://e.example/?d=${"Q".repeat(40)}`],
  ])("flags %s", (_name, url) => {
    expect(exfilShaped(url)).toBe(true);
  });

  it.each([
    ["no query", "https://github.com/o/r"],
    ["a short query", "https://e.example/search?q=marotte&page=2"],
    ["a 199-character query", `https://e.example/?q=${"a.".repeat(98)}a`],
    ["a long path and no query", `https://e.example/${"segment/".repeat(40)}`],
    [
      "a commit SHA in the path",
      `https://github.com/o/r/commit/${"0123456789abcdef0123".repeat(2)}`,
    ],
    ["20 percent escapes", `https://e.example/?d=${"%41".repeat(20)}`],
    ["a 39-character run with no padding", `https://e.example/?d=${"Q".repeat(39)}`],
    ["a ghp_ prefix one character short", `https://e.example/?t=ghp_${"a".repeat(35)}`],
    ["a short fragment", "https://e.example/docs#section-2"],
    ["a mailto with a long body", `mailto:a@b.example?body=${"Q".repeat(60)}`],
    ["a relative path", `/api/file?path=${"Q".repeat(60)}`],
  ])("passes %s", (_name, url) => {
    expect(exfilShaped(url)).toBe(false);
  });
});
