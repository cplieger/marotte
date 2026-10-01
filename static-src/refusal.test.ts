// ---------------------------------------------------------------------------
// Tests for refusal.ts — the model-refusal callout (kiro-cli 2.13 contract).
// syncRefusal(wrap, refusal, rewindTo) mounts/removes the callout off the RESOLVED
// refusal (messages.ts owns that precedence); the Rewind CTA routes the turn's own
// prompt through the injected handler and the switch CTA dispatches
// chat.switch_model. Store and the action are mocked; assertions are on the
// rendered DOM + dispatches.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

vi.mock("./store.js", () => ({ getActive: vi.fn() }));
vi.mock("./actions/chat.js", () => ({
  switchModel: { dispatch: vi.fn() },
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

import { getActive } from "./store.js";
import { switchModel } from "./actions/chat.js";
import { syncRefusal, setRefusalRewindHandler } from "./refusal.js";
import type { EntryPrompt, Session } from "./types.js";

const mockGetActive = vi.mocked(getActive);
const mockSwitch = vi.mocked(switchModel.dispatch);

/** The turn's own trigger — what the callout's Rewind addresses, so KAS drops the
 *  refused request along with everything after it. */
function prompt(id = "m1"): EntryPrompt {
  return { id, text: "write me a thing" };
}

beforeEach(() => {
  vi.clearAllMocks();
  setRefusalRewindHandler(() => undefined);
});

describe("syncRefusal", () => {
  it("mounts the callout with title, category chip, and rewind CTA", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { category: "safety" }, prompt());
    const callout = wrap.querySelector(".refusal-callout");
    expect(callout).not.toBeNull();
    expect(callout?.textContent).toContain("The model declined to continue");
    expect(callout?.querySelector(".refusal-chip")?.textContent).toBe("safety");
    const buttons = callout?.querySelectorAll(".refusal-btn") ?? [];
    expect([...buttons].map((b) => b.textContent)).toEqual(["Rewind"]);
  });

  it("omits the chip when no category and adds the switch CTA when a model is recommended", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { recommended_model: "model-x" }, prompt());
    const callout = wrap.querySelector(".refusal-callout");
    expect(callout?.querySelector(".refusal-chip")).toBeNull();
    const labels = [...(callout?.querySelectorAll(".refusal-btn") ?? [])].map((b) => b.textContent);
    expect(labels).toEqual(["Rewind", "Switch to model-x"]);
  });

  it("does nothing for a turn that was not refused and removes a stale callout", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { category: "safety" }, prompt());
    expect(wrap.querySelector(".refusal-callout")).not.toBeNull();
    syncRefusal(wrap, undefined, prompt());
    expect(wrap.querySelector(".refusal-callout")).toBeNull();
  });

  it("is idempotent — repeated syncs keep one callout", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { category: "safety" }, prompt());
    syncRefusal(wrap, { category: "safety" }, prompt());
    expect(wrap.querySelectorAll(".refusal-callout").length).toBe(1);
  });

  // Both CTAs are the app's SHARED small button, so the class pair is the whole
  // skin: `.refusal-btn` declares nothing of its own (13-messages.css carries the
  // reasoning), and dropping `btn-small` here would leave two unstyled buttons in
  // the callout with no rule anywhere to notice.
  it("gives every CTA the shared btn-small skin", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { recommended_model: "model-x" }, prompt());
    const buttons = [...wrap.querySelectorAll<HTMLButtonElement>(".refusal-btn")];
    expect(buttons.length).toBe(2);
    for (const b of buttons) {
      expect(b.classList.contains("btn-small"), b.textContent ?? "").toBe(true);
    }
  });

  it("rewind CTA routes the turn's own prompt through the injected handler", () => {
    const wrap = document.createElement("div");
    const target = prompt();
    const onRewind = vi.fn();
    setRefusalRewindHandler(onRewind);
    syncRefusal(wrap, {}, target);
    (wrap.querySelector(".refusal-rewind") as HTMLButtonElement).click();
    expect(onRewind).toHaveBeenCalledWith(target);
  });

  // A turn KAS opened on its own carries no user message, and a revert may only
  // address one — so there is no address to offer and the row keeps whatever else
  // the refusal earned.
  it("withholds the rewind CTA for a turn with no prompt of its own", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { recommended_model: "model-x" }, undefined);
    const labels = [...wrap.querySelectorAll(".refusal-btn")].map((b) => b.textContent);
    expect(labels).toEqual(["Switch to model-x"]);
  });

  // The target moves when a turn is added or removed, so a mounted callout has to
  // be rebound rather than left holding the first paint's closure — and the button
  // withdraws when the turn stops having a prompt at all.
  it("rebinds the rewind target on a later sync, and withdraws the CTA when it goes", () => {
    const wrap = document.createElement("div");
    const onRewind = vi.fn();
    setRefusalRewindHandler(onRewind);
    syncRefusal(wrap, {}, prompt("m1"));
    const next = prompt("m2");
    syncRefusal(wrap, {}, next);
    (wrap.querySelector(".refusal-rewind") as HTMLButtonElement).click();
    expect(onRewind).toHaveBeenCalledTimes(1);
    expect(onRewind).toHaveBeenCalledWith(next);
    syncRefusal(wrap, {}, undefined);
    expect(wrap.querySelector(".refusal-rewind")).toBeNull();
    expect(wrap.querySelectorAll(".refusal-callout").length).toBe(1);
  });

  it("switch CTA dispatches chat.switch_model for the active chat", () => {
    mockGetActive.mockReturnValue({ id: "c1" } as Session);
    mockSwitch.mockResolvedValue(true);
    const wrap = document.createElement("div");
    syncRefusal(wrap, { recommended_model: "model-x" }, prompt());
    const btn = [...wrap.querySelectorAll<HTMLButtonElement>(".refusal-btn")].find((b) =>
      b.textContent?.startsWith("Switch"),
    );
    btn?.click();
    expect(mockSwitch).toHaveBeenCalledWith({ chatID: "c1", model: "model-x" });
  });

  // The explanation is the SERVICE's sentence, and this callout is where it lands.
  // It used to be folded into the open assistant entry, so it rendered as the
  // model's own prose glued to the end of whatever it had been saying.
  it("renders the explanation inside the callout, between the header and the CTAs", () => {
    const wrap = document.createElement("div");
    syncRefusal(
      wrap,
      { category: "safety", explanation: "I can't help with that request." },
      prompt(),
    );
    const callout = wrap.querySelector(".refusal-callout");
    const body = callout?.querySelector(".refusal-explanation");
    expect(body?.textContent).toBe("I can't help with that request.");
    const kids = [...(callout?.children ?? [])].map((c) => c.className);
    expect(kids).toEqual(["refusal-header", "refusal-explanation", "refusal-actions"]);
  });

  // Point 4's old-record path: `explanation` is omitempty on the wire, so a refusal
  // recorded before it was carried decodes without one. The paragraph is WITHHELD
  // rather than placeheld, and the header's own wording still says what happened.
  it("withholds the explanation paragraph when the record carries none", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { category: "safety" }, prompt());
    const callout = wrap.querySelector(".refusal-callout");
    expect(callout?.querySelector(".refusal-explanation")).toBeNull();
    expect(callout?.textContent).toContain("The model declined to continue");
    expect([...(callout?.children ?? [])].map((c) => c.className)).toEqual([
      "refusal-header",
      "refusal-actions",
    ]);
  });

  // An empty string is the same absence: KAS omits the field and displayText can
  // reduce a whitespace-only sentence to "", so both have to read the same way.
  it("withholds the explanation paragraph for an empty explanation", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { explanation: "" }, prompt());
    expect(wrap.querySelector(".refusal-explanation")).toBeNull();
  });

  // Service-supplied prose, so it may never be parsed as markup. el() writes it
  // through textContent; the tags below have to survive as characters.
  it("renders the explanation as text, never as markup", () => {
    const wrap = document.createElement("div");
    const text = "Refused: <img src=x onerror=alert(1)> **not bold**";
    syncRefusal(wrap, { explanation: text }, prompt());
    const body = wrap.querySelector(".refusal-explanation");
    expect(body?.textContent).toBe(text);
    expect(body?.children.length).toBe(0);
  });

  it("keeps one explanation paragraph across repeated syncs", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, { explanation: "First." }, prompt());
    syncRefusal(wrap, { explanation: "First." }, prompt());
    expect(wrap.querySelectorAll(".refusal-explanation").length).toBe(1);
    expect(wrap.querySelector(".refusal-explanation")?.textContent).toBe("First.");
  });

  it("switch CTA is a no-op without an active session", () => {
    mockGetActive.mockReturnValue(undefined);
    const wrap = document.createElement("div");
    syncRefusal(wrap, { recommended_model: "model-x" }, prompt());
    const btn = [...wrap.querySelectorAll<HTMLButtonElement>(".refusal-btn")].find((b) =>
      b.textContent?.startsWith("Switch"),
    );
    btn?.click();
    expect(mockSwitch).not.toHaveBeenCalled();
  });
});
