// Input made while the panel's Restart is in flight and the restart succeeds, through the real
// terminal library and the shipped stylesheet, against a fake shell server on the WebSocket seam.
// The server swaps in a handler with a new boot epoch, kills the old PTY with whatever it was sent,
// then answers the POST.

import { afterAll, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import {
  FakeShellSocket,
  buildShellPanel,
  inputAt,
  mountShellCSS,
  resumedSocket,
} from "./__test-helpers__/shell-socket.js";
import type * as ShellActions from "./actions/shell.js";
import type * as CodeBlocks from "./code-blocks.js";
import type * as Confirm from "./confirm.js";
import type * as Dom from "./dom.js";
import type * as Messages from "./messages.js";
import type * as Scroll from "./scroll.js";

const live = vi.hoisted(() => ({
  els: {} as Record<string, HTMLElement>,
  runCb: null as ((cmd: string) => void) | null,
  scrollEl: null as HTMLElement | null,
  answer: null as ((value: { ok: boolean } | null) => void) | null,
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
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: (): Promise<boolean> => Promise.resolve(true),
}));
vi.mock("./actions/shell.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ShellActions>()),
  restartShell: {
    dispatch: (): Promise<{ ok: boolean } | null> =>
      new Promise((resolve) => {
        live.answer = resolve;
      }),
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

describe("input made while a restart that succeeds is in flight", () => {
  it("sends no key to either shell, and the Run once to the replacement", async () => {
    vi.stubGlobal("WebSocket", FakeShellSocket);
    live.els = buildShellPanel();
    const shell = await import("./shell.js");
    shell.initShellPanel();
    live.els["shellBtn"]?.click();
    const first = await resumedSocket();
    await vi.waitUntil(() => document.activeElement?.classList.contains("term-input") === true, {
      timeout: 2000,
    });
    await userEvent.keyboard("a");
    await inputAt(first, "a");

    live.els["shellRestartBtn"]?.click();
    await vi.waitUntil(() => live.answer !== null, { timeout: 2000 });
    await userEvent.keyboard("rm");
    live.runCb?.("echo hi");
    await frame();
    await frame();
    FakeShellSocket.epoch = 2;
    first.end();
    live.answer?.({ ok: true });
    const next = await resumedSocket(first);
    await inputAt(next, "echo hi");
    await userEvent.keyboard("z");
    await inputAt(next, "z");

    expect(first.input(), "input on the killed shell").toBe("a");
    expect(next.input(), "input on the replacement").toBe("echo hi\rz");
  });
});
