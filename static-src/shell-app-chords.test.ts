// The app's chords typed in the shell's focused input, through the real terminal library, keys.ts's
// document listener and a fake shell server on the WebSocket seam. The library sends a key's byte
// from its own listener on the input, before keys.ts sees the event bubble.

import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
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

const actions = {
  newChat: vi.fn(),
  toggleShell: vi.fn(),
  toggleFiles: vi.fn(),
  toggleGit: vi.fn(),
  toggleSettings: vi.fn(),
  sendMessage: vi.fn(),
  showShortcuts: vi.fn(),
};

const styles = mountShellCSS();
let sock: FakeShellSocket;

beforeAll(async () => {
  vi.stubGlobal("WebSocket", FakeShellSocket);
  live.els = buildShellPanel();
  const keys = await import("./keys.js");
  keys.initKeyboardShortcuts(actions);
  const shell = await import("./shell.js");
  shell.initShellPanel();
  live.els["shellBtn"]?.click();
  sock = await resumedSocket();
  await vi.waitUntil(() => document.activeElement?.classList.contains("term-input") === true, {
    timeout: 2000,
  });
});

beforeEach(() => {
  for (const fn of Object.values(actions)) {
    fn.mockClear();
  }
});

afterAll(() => {
  for (const s of styles) {
    s.remove();
  }
  document.body.replaceChildren();
  FakeShellSocket.reset();
});

/** The input `typed` puts on the wire, between two marker keys that show the run is over. */
async function wireFor(typed: string, marker: string): Promise<string> {
  const from = sock.frames.length;
  await userEvent.keyboard(typed + marker);
  await inputAt(sock, marker);
  const sent = sock.frames
    .slice(from)
    .flatMap((f) => (f.kind === "input" ? [f.text] : []))
    .join("");
  return sent.slice(0, sent.lastIndexOf(marker));
}

describe("the app's chords typed in the shell's focused input", () => {
  it("opens a chat on Ctrl+N and sends no 0x0e to the shell", async () => {
    expect(await wireFor("{Control>}n{/Control}", "1")).toBe("");
    expect(actions.newChat).toHaveBeenCalledTimes(1);
  });

  it("sends the message on Ctrl+Enter and no CR to the shell", async () => {
    expect(await wireFor("{Control>}{Enter}{/Control}", "2")).toBe("");
    expect(actions.sendMessage).toHaveBeenCalledTimes(1);
  });

  it("opens a chat on Cmd+K and sends nothing to the shell", async () => {
    expect(await wireFor("{Meta>}k{/Meta}", "3")).toBe("");
    expect(actions.newChat).toHaveBeenCalledTimes(1);
  });

  it("sends Ctrl+K to the shell as 0x0b and opens no chat", async () => {
    expect(await wireFor("{Control>}k{/Control}", "4")).toBe("\x0b");
    expect(actions.newChat).not.toHaveBeenCalled();
  });
});
