import { describe, expect, it } from "vitest";
import paneSource from "./node_modules/@cplieger/web-terminal-ui/src/kernel/pane.ts?raw";
import { LIBRARY_FONT_DEADLINE_MS } from "./shell-font.js";

describe("shell-font.ts's copy of web-terminal-ui's font deadline", () => {
  it("equals FONT_READY_TIMEOUT_MS in the installed library", () => {
    const declared = /const FONT_READY_TIMEOUT_MS = ([\d_]+);/.exec(paneSource)?.[1];
    expect(declared, "web-terminal-ui's pane.ts declares FONT_READY_TIMEOUT_MS").toBeDefined();
    expect(
      Number(declared?.replaceAll("_", "")),
      "LIBRARY_FONT_DEADLINE_MS in shell-font.ts against the library's FONT_READY_TIMEOUT_MS",
    ).toBe(LIBRARY_FONT_DEADLINE_MS);
  });
});
