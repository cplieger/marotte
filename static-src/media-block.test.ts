// File-role rendering for a workspace file presented with `![](…)`: native elements, so the assertions are about which
// element each role gets.
import { describe, it, expect } from "vitest";
import { mediaElementFor } from "./media-block.js";
import { renderMarkdownInto } from "./markdown.js";

describe("mediaElementFor", () => {
  it("gives audio a native player with controls", () => {
    const a = mediaElementFor("/workspace/out/clip.mp3", "the clip");
    expect(a?.tagName).toBe("AUDIO");
    expect((a as HTMLAudioElement).controls).toBe(true);
    expect(a?.getAttribute("src")).toBe("/api/file/download?path=%2Fworkspace%2Fout%2Fclip.mp3");
  });

  it("covers the codecs a browser ships a player for", () => {
    for (const ext of ["mp3", "wav", "ogg", "m4a", "flac", "aac", "opus"]) {
      expect(mediaElementFor(`/workspace/a.${ext}`, "")?.tagName, ext).toBe("AUDIO");
    }
  });

  // `metadata`, not `auto`: none of several clips was asked for, but the transport bar needs a duration.
  it("does not preload the audio itself", () => {
    const a = mediaElementFor("/workspace/out/clip.mp3", "") as HTMLAudioElement;
    expect(a.preload).toBe("metadata");
  });

  // As fallback content the label is both the codec-missing message and a readable name.
  it("carries the label as the player's fallback content", () => {
    expect(mediaElementFor("/workspace/out/clip.mp3", "the clip")?.textContent).toBe("the clip");
    expect(mediaElementFor("/workspace/out/clip.mp3", "")?.textContent).toBe("clip.mp3");
  });

  // The image half is utils-url.ts's src rewrite, so this module returns null.
  it("leaves an image to the <img> path", () => {
    for (const p of ["/workspace/a.png", "/workspace/a.webp", "/workspace/a.svg"]) {
      expect(mediaElementFor(p, ""), p).toBeNull();
    }
  });

  it("leaves a remote URL alone", () => {
    expect(mediaElementFor("https://example.com/a.mp3", "")).toBeNull();
    expect(mediaElementFor("/etc/passwd", "")).toBeNull();
  });

  // `/uploads` is a granted browse mount, so an uploaded file is served by the same route, not only `/workspace/`.
  it("serves a file under the uploads root", () => {
    const a = mediaElementFor("/uploads/clip.mp3", "");
    expect(a?.tagName).toBe("AUDIO");
    expect(a?.getAttribute("src")).toBe("/api/file/download?path=%2Fuploads%2Fclip.mp3");
  });

  it("decodes a percent-escaped path once", () => {
    const a = mediaElementFor("/workspace/my%20clip.mp3", "");
    expect(a?.getAttribute("src")).toBe("/api/file/download?path=%2Fworkspace%2Fmy%20clip.mp3");
    expect(a?.textContent).toBe("my clip.mp3");
  });

  it("leaves a path with a dot segment to the <img> path", () => {
    expect(mediaElementFor("/workspace/../config/x.zip", "")).toBeNull();
  });

  it("gives anything else a download affordance", () => {
    const link = mediaElementFor("/workspace/out/report.zip", "the report");
    expect(link?.tagName).toBe("A");
    expect(link?.getAttribute("href")).toBe(
      "/api/file/download?path=%2Fworkspace%2Fout%2Freport.zip",
    );
    expect(link?.hasAttribute("download")).toBe(true);
    expect(link?.textContent).toContain("the report");
  });

  // The download branch is the one anchor this module emits, and an SVG must never reach it: stored XSS otherwise.
  it("never emits an anchor for an .svg", () => {
    expect(mediaElementFor("/workspace/docs/arch.svg", "diagram")).toBeNull();
    expect(mediaElementFor("/workspace/docs/ARCH.SVG", "diagram")).toBeNull();
  });
});

describe("through the markdown renderer", () => {
  function render(md: string): HTMLElement {
    const host = document.createElement("div");
    renderMarkdownInto(host, md);
    return host;
  }

  // The tag is chosen when `![` opens but the path arrives when `)` closes, so the tag must be revisited.
  it("turns an audio reference into a player rather than a broken image", () => {
    const host = render("![clip](/workspace/out/clip.mp3)\n");
    expect(host.querySelector("audio")).not.toBeNull();
    expect(host.querySelector("img")).toBeNull();
  });

  it("leaves an image reference as an image, pointed at the file route", () => {
    const host = render("![shot](/workspace/out/shot.png)\n");
    expect(host.querySelector("img")?.getAttribute("src")).toBe(
      "/api/file/download?path=%2Fworkspace%2Fout%2Fshot.png",
    );
    expect(host.querySelector("audio")).toBeNull();
  });

  it("keeps the alt text on the swapped element", () => {
    const host = render("![the clip](/workspace/out/clip.mp3)\n");
    expect(host.querySelector("audio")?.textContent).toBe("the clip");
  });

  it("renders a download link for a presented file that is neither", () => {
    const host = render("![bundle](/workspace/out/bundle.zip)\n");
    const a = host.querySelector("a");
    expect(a?.getAttribute("href")).toBe("/api/file/download?path=%2Fworkspace%2Fout%2Fbundle.zip");
    expect(host.querySelector("img")).toBeNull();
  });

  it("does not disturb the surrounding block", () => {
    const host = render("before ![clip](/workspace/a.mp3) after\n");
    expect(host.querySelector("p")?.textContent).toContain("before");
    expect(host.querySelector("p")?.textContent).toContain("after");
    expect(host.querySelector("p > audio")).not.toBeNull();
  });
});
