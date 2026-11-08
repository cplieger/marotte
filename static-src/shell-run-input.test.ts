// The bytes a Run sends, through the real terminal library and the shipped stylesheet, against a
// fake shell server whose modes frame reports DEC mode 2004 on (xterm ctlseqs "Bracketed Paste
// Mode"). Enter on a PTY is CR, so no LF reaches the shell.

import { afterAll, describe, expect, it, vi } from "vitest";
import {
  FakeShellSocket,
  buildShellPanel,
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
  FakeShellSocket.reset();
});

const frame = (): Promise<void> =>
  new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });

describe("Run under DEC 2004", () => {
  it("sends a line as typed text and one CR, and a block as one bracketed paste and one CR", async () => {
    vi.stubGlobal("WebSocket", FakeShellSocket);
    FakeShellSocket.bracketedPaste = true;
    live.els = buildShellPanel();
    const shell = await import("./shell.js");
    shell.initShellPanel();

    live.runCb?.("echo hi");
    const sock = await resumedSocket();
    await inputAt(sock, "echo hi");
    live.runCb?.("cd /tmp\nls -la");
    await inputAt(sock, "ls -la");
    await frame();
    await frame();

    expect(sock.input()).toBe("echo hi\r\x1b[200~cd /tmp\rls -la\x1b[201~\r");
  });
});
