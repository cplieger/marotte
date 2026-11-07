// The shell panel over the real terminal library, against a fake shell server on the WebSocket
// seam: the page markup the panel needs, the library's shipped stylesheet, and a socket that
// answers as the engine's server does and records every client frame.

import { expect, vi } from "vitest";
import { mountAppCSS } from "./css-rules.js";
import wtuiManifest from "../node_modules/@cplieger/web-terminal-ui/css/MANIFEST.touch?raw";

const wtuiSheets = import.meta.glob<string>("../node_modules/@cplieger/web-terminal-ui/css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

export type ClientFrame =
  { kind: "input"; text: string } | { kind: "control"; msg: Record<string, unknown> };

const decoder = new TextDecoder();
const encoder = new TextEncoder();

function u64(view: DataView, off: number, v: number): void {
  view.setBigUint64(off, BigInt(v), true);
}

/** A resumeAck carrying the received count and a server epoch (the engine's binary layout). */
function resumeAck(): ArrayBuffer {
  const buf = new ArrayBuffer(17);
  const view = new DataView(buf);
  view.setUint8(0, 2);
  u64(view, 1, 0);
  u64(view, 9, 1);
  return buf;
}

/** A full screen frame of `height` rows with a prompt on the first, as an attach sends at the
 *  server's default size before any resize. */
function screen(height: number): ArrayBuffer {
  const text = encoder.encode("$ ");
  const parts: number[] = [];
  const push16 = (v: number): void => {
    parts.push(v & 0xff, v >> 8);
  };
  const push32 = (v: number): void => {
    for (let i = 0; i < 4; i++) {
      parts.push((v >>> (8 * i)) & 0xff);
    }
  };
  parts.push(0);
  for (let i = 0; i < 16; i++) {
    parts.push(0); // inputAck, base
  }
  push16(0); // cursor row
  push16(2); // cursor col
  push16(height);
  push16(1); // one changed row
  parts.push(0, 0); // cursor style, flags
  push16(0); // row index
  push16(1); // one run
  push16(text.length);
  parts.push(...text);
  push32(-1); // fg default
  push32(-1); // bg default
  push16(0); // attrs
  push32(-1); // underline colour
  push16(0); // no hyperlink
  return new Uint8Array(parts).buffer;
}

function classify(text: boolean, bytes: Uint8Array): ClientFrame {
  const json = text
    ? decoder.decode(bytes)
    : bytes[0] === 0
      ? decoder.decode(bytes.subarray(1))
      : null;
  if (json !== null && json !== "") {
    try {
      const msg: unknown = JSON.parse(json);
      if (typeof msg === "object" && msg !== null && "type" in msg) {
        return { kind: "control", msg: msg as Record<string, unknown> };
      }
    } catch {
      // literal input
    }
  }
  return { kind: "input", text: decoder.decode(bytes) };
}

export const isResize = (f: ClientFrame): f is { kind: "control"; msg: Record<string, unknown> } =>
  f.kind === "control" && f.msg["type"] === "resize";

/** The shell server's side of one socket: it answers the resume as the engine's server does, with
 *  the ack and a default-sized screen, and records every client frame in order. */
export class FakeShellSocket extends EventTarget {
  static last: FakeShellSocket | null = null;
  readyState = 0;
  binaryType = "blob";
  readonly frames: ClientFrame[] = [];

  constructor(readonly url: string) {
    super();
    FakeShellSocket.last = this;
    setTimeout(() => {
      this.readyState = 1;
      this.dispatchEvent(new Event("open"));
    }, 0);
  }

  send(data: string | ArrayBuffer | ArrayBufferView): void {
    const bytes =
      typeof data === "string"
        ? encoder.encode(data)
        : data instanceof ArrayBuffer
          ? new Uint8Array(data)
          : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    const frame = classify(typeof data === "string", bytes);
    this.frames.push(frame);
    if (frame.kind === "control" && frame.msg["type"] === "resume") {
      setTimeout(() => {
        this.deliver(resumeAck());
        this.deliver(screen(30));
      }, 0);
    }
  }

  close(): void {
    this.readyState = 3;
  }

  /** Whether the input frames this socket received, in order, spell `text`. */
  inputIncludes(text: string): boolean {
    return this.frames
      .flatMap((f) => (f.kind === "input" ? [f.text] : []))
      .join("")
      .includes(text);
  }

  private deliver(data: ArrayBuffer): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }
}

/** Mount the library's shipped stylesheet and the app's; returns the elements to remove. */
export function mountShellCSS(): HTMLStyleElement[] {
  const library = document.createElement("style");
  library.textContent = wtuiManifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"))
    .map((name) => wtuiSheets[`../node_modules/@cplieger/web-terminal-ui/css/${name}`] ?? "")
    .join("\n");
  document.head.appendChild(library);
  return [library, mountAppCSS()];
}

/** A closed shell panel docked under a spacer, and the `$` elements shell.ts reads. */
export function buildShellPanel(): Record<string, HTMLElement> {
  const page = document.createElement("div");
  page.style.cssText = "display:flex;flex-direction:column;width:1200px;height:700px";
  const spacer = document.createElement("div");
  spacer.style.flex = "1";
  const shellPanel = document.createElement("div");
  shellPanel.id = "shell-panel";
  shellPanel.className = "shell-panel shell-closed";
  const shellResize = document.createElement("div");
  shellResize.className = "shell-resize";
  const shellTerminal = document.createElement("div");
  shellTerminal.className = "shell-terminal";
  shellPanel.append(shellResize, shellTerminal);
  page.append(spacer, shellPanel);
  const button = (): HTMLButtonElement => document.createElement("button");
  const shellBtn = button();
  document.body.replaceChildren(page, shellBtn);
  return {
    shellBtn,
    shellToggleBtn: button(),
    shellRestartBtn: button(),
    shellKeysBtn: button(),
    shellFullscreenBtn: button(),
    shellPanel,
    shellTerminal,
    shellResize,
  };
}

function surface(root: HTMLElement): HTMLElement {
  const term = root.querySelector<HTMLElement>(".term");
  if (!term) {
    throw new Error("no terminal surface");
  }
  return term;
}

/** The columns the panel's grid holds: the `.term` content box over the cell its font draws now,
 *  measured here rather than read from the `--char-w` the library publishes, which can be stale. */
function panelCols(root: HTMLElement): number {
  const term = surface(root);
  const cs = getComputedStyle(term);
  const contentW = term.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
  const span = document.createElement("span");
  span.style.cssText = "position:absolute;visibility:hidden;white-space:pre";
  span.textContent = "MMMMMMMMMM";
  term.append(span);
  const cell = Math.round(span.getBoundingClientRect().width / 10);
  span.remove();
  return Math.floor(contentW / cell);
}

/** The rows the panel's grid holds once open: the `.term` content box over its line height. */
function panelRows(root: HTMLElement): number {
  const term = surface(root);
  const cs = getComputedStyle(term);
  const contentH = term.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
  return Math.floor(contentH / parseFloat(cs.lineHeight));
}

/** Hold the web font until the returned call: it lands after the socket has drawn, with a wider
 *  cell than the fallback the terminal measured at build, as Monaspace does over the browser's
 *  default monospace. Returns the style it adds when it lands, for the caller to remove. */
export function holdWebFont(): { fontLands: () => HTMLStyleElement } {
  let resolveFont: () => void = () => undefined;
  const landed = new Promise<FontFace[]>((resolve) => {
    resolveFont = () => {
      resolve([]);
    };
  });
  vi.spyOn(document.fonts, "load").mockReturnValue(landed);
  return {
    fontLands: () => {
      const wideFont = document.createElement("style");
      wideFont.textContent = ".shell-terminal .term { font-size: 18px !important; }";
      document.head.appendChild(wideFont);
      resolveFont();
      return wideFont;
    },
  };
}

/** The socket the terminal opened, once it has sent its resume. */
export async function resumedSocket(): Promise<FakeShellSocket> {
  const sock = await vi.waitUntil(() => FakeShellSocket.last, { timeout: 2000 });
  await vi.waitUntil(
    () => sock.frames.some((f) => f.kind === "control" && f.msg["type"] === "resume"),
    { timeout: 2000 },
  );
  return sock;
}

/** Index of the first input frame carrying `text`. */
export async function inputAt(sock: FakeShellSocket, text: string): Promise<number> {
  return vi.waitUntil(
    () => {
      const i = sock.frames.findIndex((f) => f.kind === "input" && f.text.includes(text));
      return i >= 0 ? i : false;
    },
    { timeout: 5000 },
  );
}

/** Wait until the library has announced the grid the panel now holds, then assert that the last
 *  resize before frame `at` already carried that grid. */
export async function expectSizedBefore(
  root: HTMLElement,
  sock: FakeShellSocket,
  at: number,
  what: string,
): Promise<void> {
  await vi.waitUntil(
    () => {
      const last = sock.frames.filter(isResize).at(-1);
      return last?.msg["cols"] === panelCols(root) && last.msg["rows"] === panelRows(root);
    },
    { timeout: 5000 },
  );
  const sizedBefore = sock.frames.slice(0, at).filter(isResize);
  expect(sizedBefore.length, `a resize precedes ${what}`).toBeGreaterThan(0);
  expect(sizedBefore.at(-1)?.msg).toMatchObject({ cols: panelCols(root), rows: panelRows(root) });
}
