// The `tabs.switch` action's framework contract: the time bound, the verdicts and the refusal
// sentences. The bound only aborts the signal `settle` is handed, so a `settle` that ignores its
// signal never settles and never rolls back; every `settle` here observes it.

import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  ActionError,
  configure,
  type ActionErrorLike,
  type NotifierRetry,
} from "@cplieger/ui/actions";
import { resetActionFramework } from "@cplieger/ui/actions/testing";

import { defineSwitchCommand, switchRefusal, type SwitchArgs, type SwitchHooks } from "./tabs.js";

const ARGS: SwitchArgs = { to: "tb_b", subject: "b" };

interface Notified {
  readonly message: string;
  readonly retry: NotifierRetry | undefined;
}

let notified: Notified[];

beforeEach(() => {
  resetActionFramework();
  notified = [];
  configure({
    error: (message, retry) => {
      notified.push({ message, retry });
    },
  });
});

/** A `settle` that waits for its signal and words a timeout the way `tabs.ts` does. */
function waitsForBound(): SwitchHooks["settle"] {
  return (_args, signal) =>
    new Promise((_resolve, reject) => {
      signal.addEventListener("abort", () => {
        const reason: unknown = signal.reason;
        reject(
          reason instanceof DOMException && reason.name === "TimeoutError"
            ? new ActionError("timeout", { code: "timeout" })
            : reason,
        );
      });
    });
}

function hooks(settle: SwitchHooks["settle"]): {
  readonly begin: ReturnType<typeof vi.fn<SwitchHooks["begin"]>>;
  readonly settle: ReturnType<typeof vi.fn<SwitchHooks["settle"]>>;
  readonly revert: ReturnType<typeof vi.fn<SwitchHooks["revert"]>>;
} {
  return {
    begin: vi.fn<SwitchHooks["begin"]>(() => ({ gen: 1, from: "tb_a" })),
    settle: vi.fn<SwitchHooks["settle"]>(settle),
    revert: vi.fn<SwitchHooks["revert"]>(),
  };
}

describe("the time bound", () => {
  it("reverts once, toasts the timeout sentence with Retry, and runs settle once", async () => {
    const h = hooks(waitsForBound());
    const cmd = defineSwitchCommand(h, 30);

    await cmd.dispatch(ARGS).catch(() => undefined);

    expect(h.revert).toHaveBeenCalledTimes(1);
    expect(h.settle).toHaveBeenCalledTimes(1);
    expect(notified).toHaveLength(1);
    expect(notified[0]?.message).toBe(
      "This conversation did not load in time, so you are back on the previous tab.",
    );
    expect(notified[0]?.retry).toBeDefined();
  });
});

describe("verdicts that are not failures", () => {
  it("a superseded switch neither reverts nor toasts", async () => {
    const h = hooks(() => Promise.resolve("superseded"));
    const cmd = defineSwitchCommand(h, 30);

    await cmd.dispatch(ARGS);

    expect(h.revert).not.toHaveBeenCalled();
    expect(notified).toEqual([]);
  });

  it("a cancelled dispatch does not revert", async () => {
    const h = hooks(waitsForBound());
    const cmd = defineSwitchCommand(h, 10_000);
    const ac = new AbortController();

    const done = cmd.dispatch(ARGS, { signal: ac.signal }).catch(() => undefined);
    ac.abort();
    await done;

    expect(h.revert).not.toHaveBeenCalled();
    expect(notified).toEqual([]);
  });
});

describe("switchRefusal", () => {
  const sentence = (err: ActionErrorLike): string => switchRefusal(ARGS, err);

  it("words the framework's own timeout error as a timeout, not a network loss", () => {
    expect(sentence({ message: "timed out", code: "timeout", status: 0 })).toBe(
      "This conversation did not load in time, so you are back on the previous tab.",
    );
  });

  it("words a network loss, a server error, a gone chat and a view defect", () => {
    expect(sentence({ message: "network", code: "network", status: 0 })).toBe(
      "Could not reach marotte, so you are back on the previous tab.",
    );
    expect(sentence({ message: "load failed", status: 503 })).toBe(
      "Could not load this conversation, so you are back on the previous tab.",
    );
    expect(sentence({ message: "gone", status: 404 })).toBe("This conversation no longer exists.");
    expect(sentence({ message: "show", code: "show_failed" })).toBe("Could not show that tab.");
  });
});
