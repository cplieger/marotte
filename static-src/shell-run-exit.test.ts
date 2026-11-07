// A Run held while the font loads, across a session that ends before it settles, through the real
// terminal library and the shipped stylesheet, against a fake shell server on the WebSocket seam:
// the library lowers its loading state on the exit, but its size announce still waits for the font.

import { afterAll, describe, expect, it, vi } from "vitest";
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

const frame = (): Promise<void> =>
  new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });

describe("a Run held across a session that ends before the font settles", () => {
  it("reaches the replacement PTY only after a resize to the panel's own grid", async () => {
    vi.stubGlobal("WebSocket", FakeShellSocket);
    const { fontLands } = holdWebFont();
    live.els = buildShellPanel();
    const shell = await import("./shell.js");
    shell.initShellPanel();
    const panel = live.els["shellPanel"] as HTMLElement;

    live.runCb?.("echo hello");
    const first = await resumedSocket();
    await vi.waitUntil(() => first.drawn, { timeout: 2000 });
    first.end();
    const next = await resumedSocket(first);
    await vi.waitUntil(() => next.drawn && panel.getAnimations().length === 0, { timeout: 2000 });
    await frame();
    await frame();
    expect(first.hasInput(), "input on the ended session").toBe(false);
    expect(next.hasInput(), "input before the font settled").toBe(false);

    styles.push(fontLands());
    const runAt = await inputAt(next, "echo hello");

    await expectSizedBefore(live.els["shellTerminal"] as HTMLElement, next, runAt, "the Run bytes");
  });
});
