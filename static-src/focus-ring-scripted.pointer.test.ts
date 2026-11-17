// A ring marks keyboard navigation only. The transcript scroller is not a control and never draws one, whatever
// focused it; a dialog opened by a pointer draws none on the control it focuses first, and a dialog opened or
// walked by keyboard does. WebKit matches `:focus-visible` for the focus `showModal()` places after a click.
import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  RING,
  focusedIn,
  mountComposer,
  mountModalButton,
  mountRewindButton,
  mountTranscript,
  openConfirm,
  openModal,
  outlineOf,
  unmountScenes,
} from "./__test-helpers__/focus-ring.js";
import { confirm } from "./confirm.js";

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

describe("the transcript scroller", () => {
  it("draws no ring when a script focuses it after typing in the composer", async () => {
    const pane = mountTranscript();
    const composer = mountComposer();
    // By script, as a tab switch or a rewind places it: Firefox keeps a later scripted focus quiet after a click.
    composer.focus();
    await userEvent.keyboard("hello");

    // turn-rail.ts's Escape and its reconcile fallback focus the scroller this way.
    pane.focus();

    expect(document.activeElement).toBe(pane);
    expect(
      pane.matches(":focus-visible"),
      "precondition: the engine flags the focus as visible",
    ).toBe(true);
    expect(outlineOf(pane)).toBe("none");
  });

  it("draws no ring after a click into it and a scroll key", async () => {
    const pane = mountTranscript();
    await userEvent.click(pane);
    await userEvent.keyboard("{ArrowDown}");

    expect(document.activeElement).toBe(pane);
    expect(outlineOf(pane)).toBe("none");
  });

  it("draws no ring when a dialog closed by Escape hands focus back to it", async () => {
    const pane = mountTranscript();
    pane.focus();
    void confirm({ title: "Discard this draft?", confirmLabel: "Discard" });
    openConfirm();

    await userEvent.keyboard("{Escape}");

    await vi.waitFor(() => {
      expect(document.activeElement).toBe(pane);
    });
    expect(outlineOf(pane)).toBe("none");
  });
});

describe("a dialog opened by a click", () => {
  it("draws no ring on the Rewind confirm's first focus", async () => {
    await userEvent.click(mountRewindButton());

    const cancel = focusedIn(openConfirm());
    expect(
      cancel.classList.contains("uip-ask-cancel"),
      "the destructive confirm focuses Cancel",
    ).toBe(true);
    expect(outlineOf(cancel)).toBe("none");
  });

  it("draws no ring on a modal's first focus", async () => {
    await userEvent.click(mountModalButton());

    expect(outlineOf(focusedIn(openModal()))).toBe("none");
  });

  it("rings the next control on Tab in the Rewind confirm", async () => {
    await userEvent.click(mountRewindButton());
    const dialog = openConfirm();
    const first = focusedIn(dialog);

    await userEvent.tab();

    const next = focusedIn(dialog);
    expect(next).not.toBe(first);
    expect(next.matches(":focus-visible")).toBe(true);
    expect(outlineOf(next)).toBe(RING);
  });

  it("rings the first control again on Shift+Tab in the Rewind confirm", async () => {
    await userEvent.click(mountRewindButton());
    const dialog = openConfirm();
    const first = focusedIn(dialog);

    await userEvent.tab();
    await userEvent.tab({ shift: true });

    expect(document.activeElement).toBe(first);
    expect(outlineOf(first)).toBe(RING);
  });

  it("rings the next control on Tab in a modal", async () => {
    await userEvent.click(mountModalButton());
    const dialog = openModal();
    const first = focusedIn(dialog);

    await userEvent.tab();

    const next = focusedIn(dialog);
    expect(next).not.toBe(first);
    expect(outlineOf(next)).toBe(RING);
  });
});

describe("a dialog's focus return", () => {
  it("draws no ring on the opener when a click closes a confirm a click opened", async () => {
    const rewind = mountRewindButton();
    await userEvent.click(rewind);
    const cancel = openConfirm().querySelector<HTMLButtonElement>(".uip-ask-cancel");
    if (cancel === null) {
      throw new Error("no Cancel");
    }

    await userEvent.click(cancel);

    await vi.waitFor(() => {
      expect(document.activeElement).toBe(rewind);
    });
    expect(outlineOf(rewind)).toBe("none");
  });

  it("rings the opener when Escape closes a confirm the keyboard opened", async () => {
    const rewind = mountRewindButton();
    rewind.focus();
    await userEvent.keyboard("{Enter}");
    openConfirm();

    await userEvent.keyboard("{Escape}");

    await vi.waitFor(() => {
      expect(document.activeElement).toBe(rewind);
    });
    expect(outlineOf(rewind)).toBe(RING);
  });
});

describe("a dialog opened by keyboard", () => {
  it("rings the Rewind confirm's first focus", async () => {
    const rewind = mountRewindButton();
    rewind.focus();

    await userEvent.keyboard("{Enter}");

    const cancel = focusedIn(openConfirm());
    expect(cancel.classList.contains("uip-ask-cancel")).toBe(true);
    expect(outlineOf(cancel)).toBe(RING);
  });

  it("rings a modal's first focus", async () => {
    const opener = mountModalButton();
    opener.focus();

    await userEvent.keyboard("{Enter}");

    expect(outlineOf(focusedIn(openModal()))).toBe(RING);
  });
});
