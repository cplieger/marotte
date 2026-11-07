// Run from a closed panel, through the real terminal library and the shipped stylesheet, against a
// fake shell server on the WebSocket seam: the PTY must hold the panel's grid when the command
// reaches it.

import { afterAll, describe, it, vi } from "vitest";
import {
  FakeShellSocket,
  buildShellPanel,
  expectSizedBefore,
  holdWebFont,
  inputAt,
  mountShellCSS,
  resumedSocket,
} from "./__test-helpers__/shell-socket.js";
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

const styles = mountShellCSS();

afterAll(() => {
  for (const s of styles) {
    s.remove();
  }
  document.body.replaceChildren();
});

describe("Run from a closed shell panel", () => {
  it("reaches the PTY after a resize to the panel's own grid", async () => {
    vi.stubGlobal("WebSocket", FakeShellSocket);
    const { fontLands } = holdWebFont();
    live.els = buildShellPanel();
    const shell = await import("./shell.js");
    shell.initShellPanel();

    live.runCb?.("echo hello");
    const sock = await resumedSocket();
    styles.push(fontLands());
    const runAt = await inputAt(sock, "echo hello");

    await expectSizedBefore(live.els["shellTerminal"] as HTMLElement, sock, runAt, "the Run bytes");
  });
});
