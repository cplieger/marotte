// Run from a closed panel while the web font never lands, through the real terminal library and the
// shipped stylesheet, against a fake shell server on the WebSocket seam: the Run waits out the
// library's font deadline and still reaches the PTY after a resize to the panel's grid.

import { afterAll, describe, expect, it, vi } from "vitest";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
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

// web-terminal-ui docs/configuration.md "Waiting for the font": the wait ends after 3 seconds.
const LIBRARY_FONT_DEADLINE_MS = 3000;
// performance.now() and the deadline's timer clamp to different granularities.
const CLOCK_SLACK_MS = 50;

const styles = mountShellCSS();

afterAll(() => {
  for (const s of styles) {
    s.remove();
  }
  document.body.replaceChildren();
});

describe("Run from a closed shell panel when the web font never lands", () => {
  it(
    "reaches the PTY at the font deadline, after a resize to the panel's own grid",
    { timeout: LIBRARY_FONT_DEADLINE_MS + testTimeoutFor(FRAME_BUDGET_MS) },
    async () => {
      vi.stubGlobal("WebSocket", FakeShellSocket);
      holdWebFont();
      live.els = buildShellPanel();
      const shell = await import("./shell.js");
      shell.initShellPanel();

      const runClicked = performance.now();
      live.runCb?.("echo hello");
      const sock = await resumedSocket();
      const runAt = await inputAt(sock, "echo hello", LIBRARY_FONT_DEADLINE_MS + FRAME_BUDGET_MS);

      expect(performance.now() - runClicked, "the Run waited for the font").toBeGreaterThanOrEqual(
        LIBRARY_FONT_DEADLINE_MS - CLOCK_SLACK_MS,
      );

      await expectSizedBefore(
        live.els["shellTerminal"] as HTMLElement,
        sock,
        runAt,
        "the Run bytes",
      );
    },
  );
});
