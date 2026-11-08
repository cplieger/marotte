// A Run made while the shell reconnects to a server that restarted meanwhile, through the real
// terminal library and the shipped stylesheet, against a fake shell server on the WebSocket seam:
// the library drops input queued for the old process when the resume reports the new epoch.

import { afterAll, describe, expect, it, vi } from "vitest";
import {
  FakeShellSocket,
  buildShellPanel,
  inputAt,
  isResize,
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

describe("a Run made while the shell reconnects to a restarted server", () => {
  it("reaches the new shell once", { timeout: 15_000 }, async () => {
    FakeShellSocket.reset();
    vi.stubGlobal("WebSocket", FakeShellSocket);
    live.els = buildShellPanel();
    const shell = await import("./shell.js");
    shell.initShellPanel();
    live.els["shellBtn"]?.click();
    const first = await resumedSocket();
    await vi.waitUntil(() => first.drawn && first.frames.some(isResize), { timeout: 5000 });

    FakeShellSocket.refuse = true;
    first.drop();
    await vi.waitUntil(
      () => document.querySelector('.wt-conn-banner[data-state="reconnecting"]') !== null,
      { timeout: 5000 },
    );
    live.runCb?.("echo hello");
    FakeShellSocket.epoch = 2;
    FakeShellSocket.refuse = false;
    const next = await resumedSocket(first, 10_000);
    await inputAt(next, "echo hello");
    await frame();
    await frame();

    expect(first.hasInput(), "input on the socket that dropped").toBe(false);
    expect(next.input(), "input on the restarted server's socket").toBe("echo hello\r");
  });
});
