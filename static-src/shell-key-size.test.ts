// A key typed as the panel first opens, through the real terminal library and the shipped
// stylesheet, against a fake shell server on the WebSocket seam: the server starts the PTY on the
// first frame it gets, so a key ahead of the size announce starts it at the server's default size.

import { afterAll, describe, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
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
  setShellRunCallback: (): void => undefined,
}));

const styles = mountShellCSS();

afterAll(() => {
  for (const s of styles) {
    s.remove();
  }
  document.body.replaceChildren();
});

describe("a key typed as the shell panel first opens", () => {
  it("reaches the PTY after a resize to the panel's own grid", async () => {
    vi.stubGlobal("WebSocket", FakeShellSocket);
    const { fontLands } = holdWebFont();
    live.els = buildShellPanel();
    const shell = await import("./shell.js");
    shell.initShellPanel();

    live.els["shellBtn"]?.click();
    const sock = await resumedSocket();
    await vi.waitUntil(() => document.activeElement?.classList.contains("term-input") === true, {
      timeout: 2000,
    });
    await userEvent.keyboard("k");
    styles.push(fontLands());
    const keyAt = await inputAt(sock, "k");

    await expectSizedBefore(live.els["shellTerminal"] as HTMLElement, sock, keyAt, "the typed key");
  });
});
