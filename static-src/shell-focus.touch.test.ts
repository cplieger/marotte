// Typing into the shell on a touch device with a hardware keyboard (an iPad with a Magic
// Keyboard): once the library has seen a hardware key, a tap in the terminal releases focus to the
// body and the library takes the next typed key back from the document. The chat's type-to-compose
// redirect listens on the same document and must leave that key to the terminal.

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import {
  FakeShellSocket,
  buildShellPanel,
  isResize,
  mountShellCSS,
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

const { dispatchTouch, frames } = await import("./__test-helpers__/turn-rail-dom.js");
const { focusComposerOnTyping } = await import("./composer-type-ahead.js");
const shell = await import("./shell.js");

let styles: HTMLStyleElement[] = [];
let sock: FakeShellSocket;
let composer: HTMLTextAreaElement;

function middle(el: Element): { x: number; y: number } {
  const r = el.getBoundingClientRect();
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
}

/** A finger's press and release on `el`, with the pointer events a touch device sends. */
async function touch(el: Element): Promise<void> {
  await dispatchTouch("touchStart", middle(el));
  await dispatchTouch("touchEnd");
  await frames();
}

/** A tap on content that takes no focus. WebKit's compatibility mousedown then moves focus from
 *  wherever it sat to the body; Chromium sends no compatibility events for CDP touch input, so the
 *  move is made here. */
async function tapContent(el: Element): Promise<void> {
  await touch(el);
  (document.activeElement as HTMLElement | null)?.blur();
}

/** The terminal's visible box: its output element is as tall as its scrollback. */
function terminal(): HTMLElement {
  const term = live.els["shellTerminal"]?.querySelector<HTMLElement>(".term");
  if (!term) {
    throw new Error("no terminal surface");
  }
  return term;
}

const inShell = (): boolean => live.els["shellTerminal"]?.contains(document.activeElement) === true;

beforeAll(async () => {
  styles = mountShellCSS();
  vi.stubGlobal("WebSocket", FakeShellSocket);
  vi.spyOn(document.fonts, "load").mockResolvedValue([]);
  live.els = buildShellPanel();
  // The chat view and its composer, above the docked panel.
  const chatView = document.createElement("div");
  chatView.id = "chat-view";
  composer = document.createElement("textarea");
  composer.style.cssText = "display:block;width:400px;height:60px";
  chatView.append(composer);
  live.els["shellPanel"]?.before(chatView);
  live.els["promptInput"] = composer;
  // Registered at boot, before the terminal exists, as app.ts does.
  document.addEventListener("keydown", focusComposerOnTyping);
  shell.initShellPanel();
  live.els["shellBtn"]?.click();
  sock = await vi.waitUntil(() => FakeShellSocket.last, { timeout: 2000 });
  // The panel has opened and sized its grid, so input reaches the socket at once.
  await vi.waitUntil(() => sock.frames.some(isResize), { timeout: 5000 });
  await vi.waitUntil(() => live.els["shellPanel"]?.getAnimations().length === 0, { timeout: 2000 });
  await vi.waitUntil(() => inShell(), { timeout: 2000 });
  // One hardware key seen: from here a tap in the terminal is the platform's, and releases focus.
  await userEvent.keyboard("{ArrowLeft}");
});

afterAll(() => {
  document.removeEventListener("keydown", focusComposerOnTyping);
  for (const s of styles) {
    s.remove();
  }
  document.body.replaceChildren();
});

describe("a hardware keyboard on a touch device, after the composer had focus", () => {
  it("types the key after a tap in the terminal into the shell", async () => {
    composer.focus();
    await tapContent(terminal());
    expect(document.activeElement, "the tap released focus to the page").toBe(document.body);

    await userEvent.keyboard("w");

    expect(composer.value, "the composer took the key").toBe("");
    expect(inShell(), "focus is in the terminal").toBe(true);
    await vi.waitUntil(() => sock.inputIncludes("w"), { timeout: 2000 });
  });

  it("keeps the next keys in the shell, and leaves keys after a press outside it to the composer", async () => {
    await userEvent.keyboard("y");
    expect(composer.value, "the composer took focus back").toBe("");
    expect(inShell(), "focus is in the terminal").toBe(true);
    await vi.waitUntil(() => sock.inputIncludes("y"), { timeout: 2000 });

    await tapContent(document.getElementById("chat-view") as HTMLElement);
    await userEvent.keyboard("q");

    expect(document.activeElement, "a key after a press on the chat goes to the composer").toBe(
      composer,
    );
    expect(composer.value).toBe("q");
  });
});
