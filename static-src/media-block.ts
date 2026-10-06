// File-role rendering for a workspace file in an image position (`![label](/workspace/out/clip.mp3)`), native
// elements only. An image keeps the parser's `<img>` (utils-url.ts `rewriteServedImageSrc`), audio gets
// `<audio controls>`, anything else a download link. `![](…)` is the one place the agent chose to present a file.

import { el } from "@cplieger/reactive";
import { isPlayableAudio, isViewableImage } from "./file-extensions.js";
import { fileDownloadURL, servedPath } from "./utils-url.js";

/**
 * The element a served file in an image position needs, or null when the parser's `<img>` is right (a remote URL or
 * an image). `alt` is complete by now: the label lands when `]` closes, the URL when `)` does.
 */
export function mediaElementFor(src: string, alt: string): HTMLElement | null {
  const path = servedPath(src);
  if (path === null || isViewableImage(path)) {
    return null;
  }
  const url = fileDownloadURL(path);
  const label = alt !== "" ? alt : (path.split("/").pop() ?? path);

  if (isPlayableAudio(path)) {
    const audio = el("audio", { className: "media-audio" }) as HTMLAudioElement;
    audio.controls = true;
    // `metadata`, not `auto`: none of several clips was asked for, but the transport bar needs a duration.
    audio.preload = "metadata";
    audio.src = url;
    // Fallback for a missing codec, and a place for the label a void `<img>` never had.
    audio.appendChild(document.createTextNode(label));
    return audio;
  }

  // An anchor is safe only here: `/api/file/download` sends `Content-Disposition: attachment`, and no image extension
  // (`.svg` included) reaches this branch, keeping `image/svg+xml` off a same-origin link.
  const link = el("a", { className: "media-download", href: url }, `⬇ ${label}`);
  link.setAttribute("download", "");
  return link;
}
