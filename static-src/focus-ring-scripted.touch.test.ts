// The pointer suite's rules for a finger: a tap into the transcript or on a dialog's opener draws no ring, and a
// hardware keyboard's Tab inside the dialog does (an iPad with a keyboard attached is both at once).
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  RING,
  focusedIn,
  mountModalButton,
  mountRewindButton,
  mountTranscript,
  openConfirm,
  openModal,
  outlineOf,
  unmountScenes,
} from "./__test-helpers__/focus-ring.js";
import { tap } from "./__test-helpers__/tap.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  unmountScenes();
});

describe("a tap", () => {
  it("into the transcript, then a scroll key, draws no ring on it", async () => {
    const pane = mountTranscript();
    await tap("#messages-wrap");
    await userEvent.keyboard("{ArrowDown}");

    expect(document.activeElement).toBe(pane);
    expect(outlineOf(pane)).toBe("none");
  });

  it("on Rewind draws no ring on the confirm's first focus", async () => {
    mountRewindButton();
    await tap("#focus-ring-rewind");

    const cancel = focusedIn(openConfirm());
    expect(
      cancel.classList.contains("uip-ask-cancel"),
      "the destructive confirm focuses Cancel",
    ).toBe(true);
    expect(outlineOf(cancel)).toBe("none");
  });

  it("on a modal's opener draws no ring on its first focus", async () => {
    mountModalButton();
    await tap("#focus-ring-modal");

    expect(outlineOf(focusedIn(openModal()))).toBe("none");
  });

  it("on Rewind, then Tab, rings the next control in the confirm", async () => {
    mountRewindButton();
    await tap("#focus-ring-rewind");
    const dialog = openConfirm();
    const first = focusedIn(dialog);

    await userEvent.tab();

    const next = focusedIn(dialog);
    expect(next).not.toBe(first);
    expect(outlineOf(next)).toBe(RING);
  });
});
