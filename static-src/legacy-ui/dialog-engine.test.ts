import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { createRoot } from "@cplieger/reactive";

import { closeDialog, createDialog, openDialog } from "./dialog.js";
import { existingEngine } from "./dialog-engine.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

function makeDialog(): HTMLDialogElement {
  const d = document.createElement("dialog");
  document.body.append(d);
  return d;
}

describe("dialog engine: created only once a binding is admitted", () => {
  it("createDialog under a closing owner leaves no engine on a dialog no one touched", () => {
    const d = makeDialog();
    let thrown: unknown = null;
    const [dispose, owner] = createRoot((disp, o) => [disp, o] as const);
    owner.onDispose(() => {
      try {
        createDialog(d);
      } catch (error) {
        thrown = error;
      }
    });
    dispose();

    expect(thrown).toBeInstanceOf(Error);
    expect(existingEngine(d)).toBeUndefined();
  });
});

describe("dialog engine: the closeDialog outbox", () => {
  it("is empty after each closeDialog on a closed dialog, throwing callbacks included", () => {
    const d = makeDialog();
    const calls: number[] = [];
    for (let i = 0; i < 8; i++) {
      const cb = (): void => {
        calls.push(i);
        if (i % 3 === 0) {
          throw new Error(`cb ${i.toString()}`);
        }
      };
      if (i % 3 === 0) {
        expect(() => {
          closeDialog(d, cb);
        }).toThrow(`cb ${i.toString()}`);
      } else {
        closeDialog(d, cb);
      }
      expect(existingEngine(d)?.outboxSize).toBe(0);
    }
    expect(calls).toEqual([0, 1, 2, 3, 4, 5, 6, 7]);
  });

  it("drops the callbacks of a fade that a reopen cancelled, and holds nothing once closed", () => {
    const d = makeDialog();
    openDialog(d);
    const dropped = vi.fn();
    closeDialog(d, dropped);
    openDialog(d);
    closeDialog(d);
    vi.advanceTimersByTime(400);

    expect(dropped).not.toHaveBeenCalled();
    expect(d.open).toBe(false);
    expect(existingEngine(d)?.outboxSize).toBe(0);
  });

  it("a dispose during the fade drops its pending callbacks, throwing or not, and removes the class", () => {
    const d = makeDialog();
    const ctrl = createDialog(d);
    ctrl.open();
    const quiet = vi.fn();
    const loud = vi.fn(() => {
      throw new Error("loud");
    });
    closeDialog(d, quiet);
    closeDialog(d, loud);
    expect(() => {
      ctrl.dispose();
    }).not.toThrow();
    vi.runAllTimers();

    expect(quiet).not.toHaveBeenCalled();
    expect(loud).not.toHaveBeenCalled();
    expect(d.classList.contains("uip-dialog")).toBe(false);
    expect(existingEngine(d)?.outboxSize).toBe(0);
  });
});
