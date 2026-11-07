// The reveal chain on fixture containers: what it opens for a hit, what a step closes, what a hand toggle keeps.

import { describe, it, expect, afterEach, vi } from "vitest";
import { createDisclosure } from "@cplieger/ui/disclosure";
import type { Hit } from "./wire/types.gen.js";
import type * as FoldState from "./fold-state.js";
import type * as ChatSearch from "./chat-search.js";

const { closeForSearch, refoldAfterReveal } = vi.hoisted(() => ({
  closeForSearch: vi.fn(() => true),
  refoldAfterReveal: vi.fn(),
}));
vi.mock("./fold-state.js", async (importOriginal) => ({
  ...(await importOriginal<typeof FoldState>()),
  closeForSearch,
}));
vi.mock("./chat-search.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatSearch>()),
  refoldAfterReveal,
}));

const { revealChain, releaseChain } = await import("./search-reveal.js");

afterEach(() => {
  releaseChain();
  document.body.replaceChildren();
  closeForSearch.mockClear();
  refoldAfterReveal.mockClear();
});

function hit(over: Partial<Hit> = {}): Hit {
  return {
    turn_id: "t1",
    entry_id: "e1",
    kind: "message",
    excerpt: "",
    segments: ["content"],
    turn: 1,
    ...over,
  };
}

function mount(html: string): HTMLElement {
  const host = document.createElement("div");
  host.className = "turn-body";
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

/** A reasoning block inside a collapsed steer-style disclosure: two containers, each with its own control. */
function nested(): { details: HTMLDetailsElement; head: HTMLElement; item: HTMLElement } {
  const host = mount(`
    <div class="steer-group">
      <div class="steer-group-header"></div>
      <div class="steer-group-body">
        <details class="reasoning-block"><summary>Reasoning</summary><p data-find-unit="reasoning">words</p></details>
      </div>
    </div>`);
  const head = host.querySelector<HTMLElement>(".steer-group-header") as HTMLElement;
  createDisclosure(head, host.querySelector<HTMLElement>(".steer-group-body") as HTMLElement, {
    open: false,
  });
  return {
    details: host.querySelector("details") as HTMLDetailsElement,
    head,
    item: host.querySelector("[data-find-unit]") as HTMLElement,
  };
}

describe("revealChain", () => {
  it("opens every collapsed container between the item and its turn body", () => {
    const { details, head, item } = nested();
    revealChain("c1", hit({ kind: "reasoning" }), item);
    expect(details.open).toBe(true);
    expect(head.getAttribute("aria-expanded")).toBe("true");
  });

  it("closes them again when the chain moves to an item outside them", () => {
    const { details, head, item } = nested();
    const other = mount(`<p data-find-unit="message">elsewhere</p>`)
      .firstElementChild as HTMLElement;
    revealChain("c1", hit({ kind: "reasoning" }), item);
    revealChain("c1", hit({ entry_id: "e2" }), other);
    expect(details.open).toBe(false);
    expect(head.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps a container two chains share open", () => {
    const { details, item } = nested();
    const sibling = document.createElement("p");
    sibling.dataset["findUnit"] = "reasoning";
    details.appendChild(sibling);
    revealChain("c1", hit({ kind: "reasoning" }), item);
    revealChain("c1", hit({ kind: "reasoning", entry_id: "e2" }), sibling);
    expect(details.open).toBe(true);
  });

  it("leaves a container the reader toggled by hand as they left it", () => {
    const { details, item } = nested();
    revealChain("c1", hit({ kind: "reasoning" }), item);
    const summary = details.querySelector("summary") as HTMLElement;
    summary.click();
    summary.click();
    expect(details.open).toBe(true);
    releaseChain();
    expect(details.open).toBe(true);
  });

  it("opens a tool card's details only for words that render inside them", () => {
    const host = mount(`
      <div class="tool-call" data-find-unit="tool">
        <button type="button" class="tool-disclosure"></button>
        <div class="tool-details"><pre class="tool-output">out</pre></div>
      </div>`);
    const card = host.firstElementChild as HTMLElement;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure") as HTMLElement;
    createDisclosure(toggle, card.querySelector<HTMLElement>(".tool-details") as HTMLElement, {
      open: false,
    });
    revealChain("c1", hit({ kind: "tool", segments: ["tool_title"] }), card);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    revealChain("c1", hit({ kind: "tool", segments: ["tool_title", "tool_output"] }), card);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    releaseChain();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });

  it("releases and folds the previous hit's turn, and keeps a turn the next hit shares", () => {
    const item = mount(`<p data-find-unit="message">x</p>`).firstElementChild as HTMLElement;
    revealChain("c1", hit({ turn_id: "t1" }), item);
    revealChain("c1", hit({ turn_id: "t1", entry_id: "e2" }), item);
    expect(closeForSearch).not.toHaveBeenCalled();
    revealChain("c1", hit({ turn_id: "t2" }), item);
    expect(closeForSearch).toHaveBeenCalledExactlyOnceWith("c1", "t1");
    expect(refoldAfterReveal).toHaveBeenCalledExactlyOnceWith("c1", "t1");
  });

  it("folds nothing when the released turn was not search's to fold", () => {
    const item = mount(`<p data-find-unit="message">x</p>`).firstElementChild as HTMLElement;
    closeForSearch.mockReturnValueOnce(false);
    revealChain("c1", hit({ turn_id: "t1" }), item);
    revealChain("c1", hit({ turn_id: "t2" }), item);
    expect(refoldAfterReveal).not.toHaveBeenCalled();
  });

  it("skips a container that left the page", () => {
    const { details, item } = nested();
    revealChain("c1", hit({ kind: "reasoning" }), item);
    details.closest(".turn-body")?.remove();
    expect(() => {
      releaseChain();
    }).not.toThrow();
    expect(details.open).toBe(true);
  });
});
