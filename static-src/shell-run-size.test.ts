// Run from a closed panel, through the real terminal library and the shipped stylesheet, against a
// fake shell server on the WebSocket seam: the PTY must hold the panel's columns when the command
// reaches it.

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import wtuiManifest from "./node_modules/@cplieger/web-terminal-ui/css/MANIFEST.touch?raw";
import type * as CodeBlocks from "./code-blocks.js";
import type * as Dom from "./dom.js";
import type * as Messages from "./messages.js";
import type * as Scroll from "./scroll.js";

const live = vi.hoisted(() => ({
  els: {} as Record<string, HTMLElement>,
  runCb: null as ((cmd: string) => void) | null,
  scrollEl: null as HTMLElement | null,
}));

vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
  // The scroll controller builds on import and reads app elements this panel never shows.
  $: new Proxy(
    {},
    { get: (_t, prop: string) => (live.els[prop] ??= document.createElement("div")) },
  ),
}));
vi.mock("./messages.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Messages>()),
  getScrollEl: (): HTMLElement => {
    live.scrollEl ??= document.createElement("div");
    return live.scrollEl;
  },
}));
vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  shiftScroll: (): void => undefined,
}));
vi.mock("./code-blocks.js", async (importOriginal) => ({
  ...(await importOriginal<typeof CodeBlocks>()),
  setShellRunCallback: (cb: (cmd: string) => void): void => {
    live.runCb = cb;
  },
}));

const wtuiSheets = import.meta.glob<string>("./node_modules/@cplieger/web-terminal-ui/css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

type ClientFrame =
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

/** The shell server's side of one socket: it answers the resume as the engine's server does, with
 *  the ack and a default-sized screen, and records every client frame in order. */
class FakeShellSocket extends EventTarget {
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

  private deliver(data: ArrayBuffer): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }
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

const isResize = (f: ClientFrame): f is { kind: "control"; msg: Record<string, unknown> } =>
  f.kind === "control" && f.msg["type"] === "resize";

let styles: HTMLStyleElement[] = [];

beforeAll(() => {
  const library = document.createElement("style");
  library.textContent = wtuiManifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"))
    .map((name) => wtuiSheets[`./node_modules/@cplieger/web-terminal-ui/css/${name}`] ?? "")
    .join("\n");
  document.head.appendChild(library);
  styles = [library, mountAppCSS()];
});

afterAll(() => {
  for (const s of styles) {
    s.remove();
  }
  document.body.replaceChildren();
});

function buildPanel(): void {
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
  live.els = {
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

/** The columns the panel's grid holds, from the box the library measures and the cell width it
 *  publishes on it. */
function panelCols(): number {
  const term = live.els["shellTerminal"]?.querySelector<HTMLElement>(".term");
  if (!term) {
    throw new Error("no terminal surface");
  }
  const cs = getComputedStyle(term);
  const contentW = term.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
  return Math.floor(contentW / parseFloat(term.style.getPropertyValue("--char-w")));
}

describe("Run from a closed shell panel", () => {
  it("reaches the PTY after a resize to the panel's own columns", async () => {
    vi.stubGlobal("WebSocket", FakeShellSocket);
    buildPanel();
    const shell = await import("./shell.js");
    shell.initShellPanel();

    live.runCb?.("echo hello");

    const sock = await vi.waitUntil(() => FakeShellSocket.last, { timeout: 2000 });
    const runAt = await vi.waitUntil(
      () => {
        const i = sock.frames.findIndex((f) => f.kind === "input" && f.text.includes("echo hello"));
        return i >= 0 ? i : false;
      },
      { timeout: 5000 },
    );
    // Settled: the library has announced the grid the panel now holds.
    await vi.waitUntil(() => sock.frames.filter(isResize).at(-1)?.msg["cols"] === panelCols(), {
      timeout: 5000,
    });

    const sizedBefore = sock.frames.slice(0, runAt).filter(isResize);
    expect(sizedBefore.length, "a resize precedes the Run bytes").toBeGreaterThan(0);
    expect(sizedBefore.at(-1)?.msg["cols"]).toBe(panelCols());
  });
});
