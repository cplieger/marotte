// Tests for run-input.ts: the card a waiting workflow step renders as in the interaction dock.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

import {
  _resetRunAskDraftsForTest,
  buildRunAskCard,
  dropRunAskDrafts,
  openRunAskCard,
  keepRunAskDrafts,
  registerRunAskVerbs,
  updateRunAskCard,
  type RunAskCardInput,
} from "./run-input.js";
import type { RunAsk } from "./wire/types.gen.js";

function runAsk(over: Partial<RunAsk> = {}): RunAsk {
  return {
    ask_id: "a1",
    node_id: "review",
    node_path: ["wf_1", "review"],
    step_session_id: "sess_step",
    agent_name: "reviewer",
    text: "Which branch should I target?",
    text_source: "asked",
    can_continue: true,
    ...over,
  };
}

function input(over: Partial<RunAsk> = {}, run: Partial<RunAskCardInput> = {}): RunAskCardInput {
  return { workflowID: "wf_1", chatID: "run:wf_1", reachable: true, ask: runAsk(over), ...run };
}

function mount(d: RunAskCardInput): HTMLElement {
  const card = buildRunAskCard(d);
  document.body.replaceChildren(card);
  return card;
}

/** The answer row's visible buttons, in order. Scoped to `.dock-ask-actions` because the
 *  question's own clamp opener is a button too and belongs to the body. */
function actionLabels(card: HTMLElement): (string | null)[] {
  return [...card.querySelectorAll(".dock-ask-actions button")]
    .filter((b) => !b.classList.contains("hidden"))
    .map((b) => b.textContent);
}

function button(card: HTMLElement, label: string): HTMLButtonElement {
  const found = [...card.querySelectorAll("button")].find((b) => b.textContent === label);
  if (found === undefined) {
    throw new Error(`no button labelled ${label}`);
  }
  return found;
}

function box(card: HTMLElement): HTMLTextAreaElement {
  return card.querySelector("textarea") as HTMLTextAreaElement;
}

function type(card: HTMLElement, text: string): void {
  box(card).value = text;
  box(card).dispatchEvent(new Event("input", { bubbles: true }));
}

function note(card: HTMLElement): HTMLElement {
  return card.querySelector(".run-input-note") as HTMLElement;
}

const answer = vi.fn((_workflowID: string, _askID: string, _text: string) => Promise.resolve(true));
const cont = vi.fn((_workflowID: string, _nodeID: string, _askID: string) => Promise.resolve());
const defer = vi.fn((_chatID: string, _workflowID: string) => Promise.resolve());
registerRunAskVerbs({ answer, continueStep: cont, defer });

/** An answer whose reply the test settles: true is taken, false refused. */
function pending(): { resolve: (ok: boolean) => void } {
  let resolve: (ok: boolean) => void = () => undefined;
  answer.mockImplementationOnce(
    () =>
      new Promise<boolean>((r) => {
        resolve = r;
      }),
  );
  return { resolve: (ok) => resolve(ok) };
}

beforeEach(() => {
  document.body.replaceChildren();
  _resetRunAskDraftsForTest();
  answer.mockClear();
  cont.mockClear();
  defer.mockClear();
});

describe("buildRunAskCard", () => {
  it("renders the step's question and names where the text came from and who asks", () => {
    const card = mount(input());
    expect(card.querySelector("strong")?.textContent).toBe("Which branch should I target?");
    expect(card.querySelector(".run-input-step")?.textContent).toBe(
      "The step asks \u00b7 reviewer \u00b7 step review",
    );
  });

  it("labels the step's last words as not its question", () => {
    const card = mount(input({ text: "Done with part one.", text_source: "last_message" }));
    expect(card.querySelector(".run-input-step")?.textContent).toBe(
      "The step's last message \u00b7 reviewer \u00b7 step review",
    );
  });

  it("prints kiro-cli's reason for a step that waits without asking", () => {
    const reason =
      "Step 'review' sent a send_message success signal that could not be saved, so it is paused awaiting user input instead of finishing.";
    const card = mount(input({ text: "Done.", text_source: "last_message", reason }));
    expect(card.querySelector(".run-input-reason")?.textContent).toBe(reason);
    expect(actionLabels(card)).toContain("Continue without answering");
  });

  it("prints no reason line when the step asked", () => {
    expect(mount(input()).querySelector(".run-input-reason")).toBeNull();
  });

  it("says marotte saw nothing when no text came through", () => {
    const card = mount(input({ text: "", text_source: "unseen", agent_name: "", node_id: "" }));
    expect(card.querySelector("strong")?.textContent).toBe("A step is waiting for your answer");
    expect(card.querySelector(".run-input-step")?.textContent).toBe(
      "marotte did not see what this step said",
    );
  });

  it("sends the trimmed words under the card's ask id, inert until the reply", async () => {
    const card = mount(input());
    const reply = pending();
    type(card, "  the release branch  ");
    button(card, "Send answer").click();
    expect(answer).toHaveBeenCalledWith("wf_1", "a1", "the release branch");
    expect(card.inert).toBe(true);
    reply.resolve(true);
    await vi.waitFor(() => {
      expect(card.inert).toBe(false);
    });
    expect(box(card).value).toBe("");
  });

  it("keeps the words in the box when the answer is refused", async () => {
    const card = mount(input());
    const reply = pending();
    type(card, "the release branch");
    button(card, "Send answer").click();
    reply.resolve(false);
    await vi.waitFor(() => {
      expect(card.inert).toBe(false);
    });
    expect(box(card).value).toBe("the release branch");
    expect(actionLabels(card)).not.toContain("Restore last message");
  });

  it("offers the last words sent back once the box is empty, for a step that asks again", async () => {
    const first = mount(input());
    type(first, "the release branch");
    button(first, "Send answer").click();
    await vi.waitFor(() => {
      expect(actionLabels(first)).toContain("Restore last message");
    });
    // The step re-parks: a new ask id at the same wait position, so a new card.
    const again = mount(input({ ask_id: "a2" }));
    button(again, "Restore last message").click();
    expect(box(again).value).toBe("the release branch");
    expect(actionLabels(again)).not.toContain("Restore last message");
  });

  it("refuses an empty Send rather than waiving the question", () => {
    const card = mount(input());
    button(card, "Send answer").click();
    expect(answer).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(box(card));
  });

  it("disables Send with the blocker's reason, and re-enables it in place", () => {
    const card = mount(input({ blocked: "branch_running" }));
    type(card, "half an answer");
    expect(button(card, "Send answer").disabled).toBe(true);
    expect(note(card).textContent).toBe(
      "Another step of this run is still running. Answer once it stops.",
    );
    const keptBox = box(card);
    updateRunAskCard(card, input());
    expect(button(card, "Send answer").disabled).toBe(false);
    expect(note(card).classList.contains("hidden")).toBe(true);
    expect(box(card)).toBe(keptBox);
    expect(keptBox.value).toBe("half an answer");
  });

  it("keeps Send on when marotte could not read the run, and says so", () => {
    const card = mount(input({}, { reachable: false }));
    expect(button(card, "Send answer").disabled).toBe(false);
    expect(note(card).textContent).toBe("marotte could not read this run just now.");
  });

  it("keeps the typed words across a rebuilt card at the same wait", () => {
    const card = mount(input());
    type(card, "half an answer");
    expect(box(mount(input({ ask_id: "a2" }))).value).toBe("half an answer");
    expect(box(mount(input({ step_session_id: "sess_other" }))).value).toBe("");
  });

  it("forgets a run's words once the run has ended", () => {
    type(mount(input()), "half an answer");
    type(mount(input({}, { workflowID: "wf_2" })), "another run");
    dropRunAskDrafts("wf_1");
    expect(box(mount(input())).value).toBe("");
    expect(box(mount(input({}, { workflowID: "wf_2" }))).value).toBe("another run");
  });

  it("keeps the words of the runs a pending set names, and forgets the rest", () => {
    type(mount(input()), "half an answer");
    type(mount(input({}, { workflowID: "wf_2" })), "another run");
    keepRunAskDrafts(new Set(["wf_2"]));
    expect(box(mount(input())).value).toBe("");
    expect(box(mount(input({}, { workflowID: "wf_2" }))).value).toBe("another run");
  });

  describe("Continue without answering", () => {
    it("continues the card's own execution", () => {
      const card = mount(input());
      button(card, "Continue without answering").click();
      expect(cont).toHaveBeenCalledWith("wf_1", "review", "a1");
    });

    it("is offered only where the server says Continue reaches this execution", () => {
      expect(actionLabels(mount(input({ can_continue: false })))).toEqual(["Send answer"]);
    });

    it("is withdrawn while the ask is blocked, and offered again in place", () => {
      const card = mount(input({ blocked: "pending" }));
      expect(actionLabels(card)).toEqual(["Send answer"]);
      updateRunAskCard(card, input());
      expect(actionLabels(card)).toEqual(["Send answer", "Continue without answering"]);
    });

    it("says what it does, since the label cannot", () => {
      expect(
        button(mount(input()), "Continue without answering").getAttribute("data-tooltip"),
      ).toContain("carries on with no answer");
    });
  });

  describe("a CHAT-PARENTED ask", () => {
    const parented = (over: Partial<RunAsk> = {}): RunAskCardInput => input(over, { chatID: "c1" });

    it("offers the launching agent in place of the waive", () => {
      expect(actionLabels(mount(parented()))).toEqual(["Send answer", "Defer to parent agent"]);
    });

    it("asks the launching chat once, and answers nothing itself", async () => {
      const card = mount(parented());
      const b = button(card, "Defer to parent agent");
      b.click();
      b.click();
      expect(defer).toHaveBeenCalledTimes(1);
      expect(defer).toHaveBeenCalledWith("c1", "wf_1");
      expect(answer).not.toHaveBeenCalled();
      await vi.waitFor(() => {
        const asked = button(card, "Asked the agent");
        expect(asked.disabled).toBe(true);
        expect(asked.getAttribute("data-tooltip")).toContain("has been asked to answer");
      });
    });

    it("keeps the Defer button's asked state across a blocker change", async () => {
      const card = mount(parented());
      button(card, "Defer to parent agent").click();
      await vi.waitFor(() => {
        expect(button(card, "Asked the agent").disabled).toBe(true);
      });
      updateRunAskCard(card, parented({ blocked: "settling" }));
      expect(button(card, "Asked the agent").disabled).toBe(true);
    });

    it("hands the button back when the hand-off was refused", async () => {
      defer.mockRejectedValueOnce(new Error("refused"));
      const card = mount(parented());
      button(card, "Defer to parent agent").click();
      await vi.waitFor(() => {
        expect(button(card, "Defer to parent agent").disabled).toBe(false);
      });
    });

    it("keeps the question answerable after a deferral", async () => {
      const card = mount(parented());
      button(card, "Defer to parent agent").click();
      await vi.waitFor(() => {
        expect(button(card, "Asked the agent").disabled).toBe(true);
      });
      type(card, "the release branch");
      button(card, "Send answer").click();
      expect(answer).toHaveBeenCalledTimes(1);
    });
  });

  it("submits on Cmd/Ctrl+Enter and not on a bare Enter", () => {
    const card = mount(input());
    type(card, "the release branch");
    box(card).dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(answer).not.toHaveBeenCalled();
    box(card).dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true }),
    );
    expect(answer).toHaveBeenCalledTimes(1);
  });

  it("labels the touch keyboard's Return as a new line", () => {
    expect(box(mount(input())).enterKeyHint).toBe("enter");
  });

  describe("under a finger", () => {
    afterEach(() => {
      document.documentElement.removeAttribute("data-pointer");
    });

    it("continues a list from Return's line break", async () => {
      document.documentElement.setAttribute("data-pointer", "coarse");
      const b = box(mount(input()));
      b.focus();
      b.value = "- first";
      b.setSelectionRange(7, 7);
      await userEvent.keyboard("{Enter}");
      expect(b.value).toBe("- first\n- ");
    });
  });
});

describe("the compact card on a phone", () => {
  async function phone(): Promise<HTMLStyleElement> {
    await page.viewport(390, 844);
    return mountAppCSS();
  }

  async function restore(style: HTMLStyleElement): Promise<void> {
    style.remove();
    await page.viewport(1280, 720);
  }

  const shown = (node: Element | null): boolean => node?.checkVisibility() ?? false;

  it("rests as one row with Answer, its box and buttons behind it", async () => {
    const style = await phone();
    try {
      const card = mount(input());
      expect(shown(button(card, "Answer"))).toBe(true);
      expect(shown(box(card))).toBe(false);
      expect(shown(card.querySelector(".dock-ask-actions"))).toBe(false);
      const row = button(card, "Answer").getBoundingClientRect();
      expect(card.getBoundingClientRect().height).toBeLessThan(row.height * 3);
      button(card, "Answer").click();
      expect(card.hasAttribute("data-open")).toBe(true);
      expect(shown(box(card))).toBe(true);
      expect(shown(button(card, "Send answer"))).toBe(true);
      expect(shown(button(card, "Answer"))).toBe(false);
      expect(document.activeElement).toBe(box(card));
    } finally {
      await restore(style);
    }
  });

  it("opens at once for a card holding a draft", async () => {
    const style = await phone();
    try {
      type(mount(input()), "main");
      const card = mount(input());
      expect(box(card).value).toBe("main");
      expect(card.hasAttribute("data-open")).toBe(true);
      expect(shown(box(card))).toBe(true);
    } finally {
      await restore(style);
    }
  });

  it("keeps the box in view and Answer out of it on a wider screen", () => {
    const style = mountAppCSS();
    try {
      const card = mount(input());
      expect(shown(box(card))).toBe(true);
      expect(shown(button(card, "Answer"))).toBe(false);
      openRunAskCard(card);
      expect(document.activeElement).toBe(box(card));
    } finally {
      style.remove();
    }
  });
});
