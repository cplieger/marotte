// The delegated-work card: the header identity glyph (spinner while active, then the agent icon
// or a per-subagent glyph via setIcon), the head as the door to the delegate's page, and
// accessible names. The tail's content is subagent-tail.test.ts's.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { loadCSS, mountAppCSS } from "../__test-helpers__/css-rules.js";

// Partial mock: the card now imports turn-footer (whose navigate → tabs chain
// reads the whole icon table), so only the identity glyph is overridden.
vi.mock("../icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, string>>()),
  ICON_TAB_AGENT: '<svg data-icon="agent-hexagon"></svg>',
}));

import { buildSubagentCard } from "./subagent-block.js";
import { outcomeIcon } from "../icons.js";
import { iconEl } from "../icon-el.js";

const iconSlot = (root: HTMLElement): HTMLElement =>
  root.querySelector(".subagent-icon") as HTMLElement;
const headerOf = (root: HTMLElement): HTMLElement =>
  root.querySelector(".subagent-header") as HTMLElement;

const tailLines = (root: HTMLElement): (string | null)[] =>
  [...root.querySelectorAll(".subagent-tail-line")].map((n) => n.textContent);

const HREF = "/chat/c/subagent/u-1";

const opener = (open: () => void = () => undefined): { href: string; open: () => void } => ({
  href: HREF,
  open,
});

/** A click with modifiers or a non-primary button, which `HTMLElement.click()` cannot express;
 *  returns whether the head cancelled it. The trailing guard, registered after the head's
 *  listener, stops a not-cancelled click navigating the test page. */
function clickWith(head: HTMLElement, init: MouseEventInit): boolean {
  let prevented = false;
  const guard = (e: Event): void => {
    prevented = e.defaultPrevented;
    e.preventDefault();
  };
  head.addEventListener("click", guard);
  head.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, ...init }));
  head.removeEventListener("click", guard);
  return prevented;
}

describe("buildSubagentCard icon", () => {
  it("shows the spinner while active, the default hexagon once settled", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(false);
    expect(iconSlot(sa.root).querySelector('svg[data-icon="agent-hexagon"]')).not.toBeNull();
  });

  it("setIcon swaps the settled glyph (distinct icon per known subagent)", () => {
    const sa = buildSubagentCard("Introspect", "completed");
    sa.setIcon('<svg data-icon="introspect"></svg>');
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();

    // A FAILURE replaces the installed glyph with the shared failure silhouette:
    // one mark per row, and its SHAPE is what changes for a non-success state.
    sa.setStatus("failed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).toBeNull();
    expect(iconSlot(sa.root).querySelector("svg")?.outerHTML).toBe(
      (iconEl(outcomeIcon("fail")) as HTMLElement).outerHTML,
    );
    expect(iconSlot(sa.root).querySelectorAll("svg")).toHaveLength(1);

    // Back to a success and the installed identity glyph returns, so the card
    // cannot be stranded on a mark it borrowed.
    sa.setStatus("completed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
    expect(iconSlot(sa.root).querySelectorAll("svg")).toHaveLength(1);
  });

  it("setIcon while active defers the glyph until the subagent settles", () => {
    const sa = buildSubagentCard("Introspect", "in_progress");
    sa.setIcon('<svg data-icon="introspect"></svg>');
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(iconSlot(sa.root).querySelector("svg")).toBeNull();

    sa.setStatus("completed");
    expect(iconSlot(sa.root).querySelector('svg[data-icon="introspect"]')).not.toBeNull();
  });

  // A delegate the reader STOPPED is a third terminal state, not a slow success and
  // not a failure to debug.
  it("leaves the running state for the warn mark when the turn is cancelled", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(true);
    expect(sa.root.classList.contains("running")).toBe(true);

    sa.setStatus("aborted");

    expect(iconSlot(sa.root).classList.contains("subagent-spinner")).toBe(false);
    expect(sa.root.classList.contains("running")).toBe(false);
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
    // Yellow, and its own SHAPE: `is-ok` would claim a result this delegate never
    // produced, `is-fail` would send the reader to debug work nothing broke in.
    expect(iconSlot(sa.root).classList.contains("is-warn")).toBe(true);
    expect(iconSlot(sa.root).classList.contains("is-ok")).toBe(false);
    expect(iconSlot(sa.root).classList.contains("is-fail")).toBe(false);
    expect(iconSlot(sa.root).querySelector("svg")?.outerHTML).toBe(
      (iconEl(outcomeIcon("warn")) as HTMLElement).outerHTML,
    );
    expect(iconSlot(sa.root).querySelectorAll("svg")).toHaveLength(1);
  });
});

// A CARD HAS NO BODY: the transcript renders
// none of a delegate's output, so there is nothing here to disclose and a toggle
// that toggles nothing is worse than no toggle.
describe("a delegate's card is not a disclosure", () => {
  it("has no body, no disclosure toggle and no aria-expanded", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(sa.root.querySelector(".subagent-body")).toBeNull();
    expect(sa.root.querySelector(".subagent-toggle")).toBeNull();
    const header = sa.root.querySelector<HTMLElement>(".subagent-header");
    expect(header?.getAttribute("role")).toBeNull();
    expect(header?.getAttribute("tabindex")).toBeNull();
    expect(header?.getAttribute("aria-expanded")).toBeNull();

    // A card WITH an opener is a plain anchor: nothing expands, so it takes none of
    // the disclosure's attributes and its `href` is the whole state it carries.
    const door = buildSubagentCard("Subagent", "in_progress", { open: opener() });
    const head = headerOf(door.root);
    expect(head.tagName).toBe("A");
    expect(head.getAttribute("href")).toBe(HREF);
    expect(head.getAttribute("role")).toBeNull();
    expect(head.getAttribute("aria-expanded")).toBeNull();
    expect(door.root.querySelector(".subagent-toggle")).toBeNull();
  });

  it("opens the delegate's page when its header is activated, and folds nothing", () => {
    const opened: number[] = [];
    const sa = buildSubagentCard("Subagent", "in_progress", {
      open: opener(() => opened.push(1)),
    });
    // Through `clickWith` rather than `.click()`, so a head that stops cancelling the
    // event fails the case below by name instead of navigating the test page.
    clickWith(headerOf(sa.root), {});
    expect(opened).toHaveLength(1);
    expect(sa.root.classList.contains("collapsed")).toBe(false);
    // Nor on a failure: the reason is on the delegate's page, and the head is the way to it.
    sa.setStatus("failed");
    expect(sa.root.classList.contains("collapsed")).toBe(false);
  });

  it("marks the root running for the tail's visibility gate", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    expect(sa.root.classList.contains("running")).toBe(true);
    sa.setStatus("completed");
    expect(sa.root.classList.contains("running")).toBe(false);
  });
});

// The card's HEAD is the door to the delegate's page; one `it` per claim.
describe("the card's head is the door to the delegate's page", () => {
  /** A card with an opener, plus the calls its head made. */
  function door(): { head: HTMLElement; opened: number[]; root: HTMLElement } {
    const opened: number[] = [];
    const sa = buildSubagentCard("Subagent", "in_progress", {
      open: opener(() => opened.push(1)),
    });
    return { head: headerOf(sa.root), opened, root: sa.root };
  }

  it("routes a plain click through the app instead of the href", () => {
    const { head, opened } = door();
    expect(clickWith(head, { button: 0 })).toBe(true);
    expect(opened).toHaveLength(1);
  });

  it("lets a modified or non-primary click fall through to the browser", () => {
    // The deliberate escape from routing: a new tab or window is the reader asking
    // the platform rather than the app.
    for (const init of [
      { metaKey: true },
      { ctrlKey: true },
      { shiftKey: true },
      { altKey: true },
      { button: 1 },
    ]) {
      const { head, opened } = door();
      expect(clickWith(head, init), JSON.stringify(init)).toBe(false);
      expect(opened, JSON.stringify(init)).toHaveLength(0);
    }
  });

  it("is keyboard-reachable and natively activated", () => {
    // A real `a[href]` gets its tab stop and its Enter activation from the platform,
    // which is why the head carries no `role`, no `tabindex` and no keydown handler.
    const { head } = door();
    expect(head.tagName).toBe("A");
    expect(head.getAttribute("href")).toBe(HREF);
    expect(head.tabIndex).toBe(0);
    // Both lines are wanted: `tabIndex === 0` is the platform's answer and an explicit
    // `tabindex="0"` satisfies it too, so it cannot say the head takes its tab stop
    // from being an anchor.
    expect(head.hasAttribute("tabindex")).toBe(false);
  });

  it("holds no interactive descendant", () => {
    // The binding constraint: anything interactive inside the control is axe's
    // `nested-interactive`, which is why the foot is OUTSIDE it — turn-footer builds a
    // real button plus one per changed file.
    const { head } = door();
    expect(
      head.querySelector(
        "a, button, input, select, textarea, summary, [role='button'], [tabindex]",
      ),
    ).toBeNull();
  });

  it("carries the shared chevron, and a detached render carries none", () => {
    const { head } = door();
    expect(head.querySelector(".subagent-head-chevron > .disclosure-chevron")).not.toBeNull();

    const inert = buildSubagentCard("Subagent", "in_progress");
    expect(inert.root.querySelector(".subagent-head-chevron")).toBeNull();
    expect(inert.root.querySelector(".disclosure-chevron")).toBeNull();
    expect(headerOf(inert.root).tagName).toBe("DIV");
  });

  it("has no separate open link, in either shape or in the stylesheet", () => {
    // The source guard is what catches a rule left behind for an element nothing builds.
    const { root } = door();
    expect(root.querySelector(".subagent-open")).toBeNull();
    expect(buildSubagentCard("Subagent", "completed").root.querySelector(".subagent-open")).toBe(
      null,
    );
    expect(loadCSS("14-tools.css")).not.toContain("subagent-open");
  });
});

describe("the tail is a sink", () => {
  it("renders one line element per line, in the order given", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setTail(["three", "four", "five"]);
    expect(tailLines(sa.root)).toEqual(["three", "four", "five"]);
    // Replaced, never appended: the tail is the last N lines, not a log.
    sa.setTail(["four", "five", "six"]);
    expect(tailLines(sa.root)).toEqual(["four", "five", "six"]);
  });

  it("exists while running and is REMOVED on settle — the footer takes over", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setTail(["working"]);
    expect(sa.root.querySelector(".subagent-tail")).not.toBeNull();
    sa.setStatus("completed");
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
  });

  it("ignores a write that arrives after the settle", () => {
    // The binding is disposed on settle, but a delta already in flight would
    // otherwise re-attach nothing and paint into a detached node.
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setStatus("completed");
    sa.setTail(["late line"]);
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
    expect(tailLines(sa.root)).toEqual([]);
  });

  it("has no tail at all when built already settled", () => {
    const sa = buildSubagentCard("Subagent", "completed");
    expect(sa.root.querySelector(".subagent-tail")).toBeNull();
  });
});

// After the drop the delegate's output is reachable only through the foot's link, so the card
// names itself and where that link goes.
describe("accessible names", () => {
  it("announces the state as a word, not as the glyph's colour", () => {
    const sa = buildSubagentCard("introspect", "in_progress");
    const state = (): string | null =>
      sa.root.querySelector<HTMLElement>(".subagent-header > .sr-only")?.textContent ?? null;
    expect(state()).toBe("running");
    sa.setStatus("failed");
    expect(state()).toBe("failed");
    sa.setStatus("completed");
    expect(state()).toBe("succeeded");
    // The DISPLAYED word is not the wire enum: the wire spells it `aborted` and the
    // reader is told `cancelled`, which is what the enclosing turn card's own footer
    // says about the same stop.
    sa.setStatus("aborted");
    expect(state()).toBe("cancelled");
  });

  it("names the head for its delegate and its state, and follows both", () => {
    const sa = buildSubagentCard("Subagent", "in_progress", { open: opener() });
    const head = headerOf(sa.root);
    // `<thing>, <state word>` and nothing else: the anchor already says it opens
    // something, so a second sentence would restate the role.
    expect(head.getAttribute("aria-label")).toBe("Subagent, running");
    sa.setStatus("failed");
    expect(head.getAttribute("aria-label")).toBe("Subagent, failed");
    sa.setName("context-gatherer");
    expect(head.getAttribute("aria-label")).toBe("context-gatherer, failed");
    // ONE owner: the `.sr-only` span is not built at all here, so the state word
    // cannot be announced twice.
    expect(head.querySelector(".sr-only")).toBeNull();
  });

  it("shows the requested category beside the name and in the head's name", () => {
    const sa = buildSubagentCard("x", "in_progress", { open: opener() });
    const head = headerOf(sa.root);
    sa.setDetail("Category: auto-smart");
    expect(head.querySelector(".subagent-detail")?.textContent).toBe("Category: auto-smart");
    expect(head.getAttribute("aria-label")).toBe("x, Category: auto-smart, running");
    sa.setDetail("");
    expect(head.getAttribute("aria-label")).toBe("x, running");
  });
});

describe("the footer", () => {
  it("does not exist until the summary has something worth a row", () => {
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setSummary({ changedFiles: {} });
    expect(sa.root.querySelector(".subagent-footer")).toBeNull();
  });

  it("is turn-footer reused, updated in place, and the card's last region", () => {
    // `kindCounts` BESIDE the aggregates, the only shape `subagentSummary` emits; `turnFacts` counts
    // from the kind map, so aggregates alone earn no footer.
    const summary = { kindCounts: { execute: 3, read: 2 } } as const;
    const sa = buildSubagentCard("Subagent", "in_progress");
    sa.setSummary({ ...summary, changedFiles: {} });
    const footer = sa.root.querySelector<HTMLElement>(".subagent-footer");
    expect(footer).not.toBeNull();
    expect(footer?.classList.contains("turn-footer")).toBe(true);
    expect(sa.root.lastElementChild?.classList.contains("subagent-foot")).toBe(true);

    sa.setSummary({
      ...summary,
      changedFiles: { "a.go": { lines_added: 4, lines_removed: 1 } },
      outcome: "completed",
      elapsedMs: 1200,
    });
    // Still ONE footer, updated rather than stacked.
    expect(sa.root.querySelectorAll(".subagent-footer").length).toBe(1);
    expect(footer?.dataset["outcome"]).toBe("completed");
  });
});

// The delegate card's footer info panel: no ACP field carries credits, a model id or a stop reason
// per delegate, so Cost, Model and Diagnostics always withhold (`subagentSummary` shapes the data).

describe("the delegate footer's info panel", () => {
  /** A settled delegate as `subagentSummary` produces one; no credits, model or stop reason. */
  const DELEGATE = {
    outcome: "completed",
    changedFiles: { "a.go": { lines_added: 4, lines_removed: 1 } },
    elapsedMs: 92000,
    toolMs: 30000,
    kindCounts: { read: 3, execute: 2, edit: 1 },
    delegateCount: 1,
    delegateMs: 9000,
    startedAt: Date.UTC(2026, 0, 2, 9, 5, 0),
    endedAt: Date.UTC(2026, 0, 2, 9, 5, 0) + 92000,
  } as const;

  function panelSections(root: HTMLElement): string[] {
    return [...root.querySelectorAll(".subagent-footer .turn-info-title")].map(
      (h) => h.textContent ?? "",
    );
  }

  function settled(): HTMLElement {
    const sa = buildSubagentCard("context-gatherer", "completed");
    sa.setSummary(DELEGATE);
    return sa.root;
  }

  it("renders the three sections a delegate can fill", () => {
    const root = settled();
    expect(panelSections(root)).toEqual(["Timings", "Work", "Delegates"]);
  });

  it("carries the delegate's own timings, its work and the delegates it dispatched", () => {
    const root = settled();
    const rows = [...root.querySelectorAll(".subagent-footer .turn-info-row")].map((r) => [
      r.querySelector(".turn-info-label")?.textContent ?? "",
      r.querySelector(".turn-info-value")?.textContent ?? "",
    ]);
    expect(rows).toEqual([
      ["Wall clock", "1m 32s"],
      ["Tool time", "30.0s"],
      ["Model time", "1m 2s"],
      // The stamp text is the reader's own locale, so the LABELS are what is pinned
      // here; `fundamentals/turn-footer.test.ts` owns the `datetime` pair.
      ["Started", rows[3]?.[1] ?? ""],
      ["Ended", rows[4]?.[1] ?? ""],
      ["reads", "3"],
      ["commands", "2"],
      // Singular at one, from the shared noun table rather than an `s` appended here.
      ["edit", "1"],
      ["Dispatched", "1"],
      ["Time", "9.0s"],
    ]);
    // The delegate's changed file still gets its row, inside Work.
    expect(root.querySelectorAll(".subagent-footer .turn-file-row")).toHaveLength(1);
  });

  it("withholds Cost, Model and Diagnostics, which the wire cannot carry per delegate", () => {
    // On an UNCLEAN delegate `aborted` opens the Diagnostics gate and the section is STILL absent, so
    // the withholding rests on the absent fields, not the outcome.
    const sa = buildSubagentCard("context-gatherer", "aborted");
    sa.setSummary({ ...DELEGATE, outcome: "cancelled" });
    expect(panelSections(sa.root)).toEqual(["Timings", "Work", "Delegates"]);
    expect(sa.root.querySelector(".subagent-footer .turn-info-panel")).not.toBeNull();
  });

  it("opens the panel from the delegate card's own trigger", () => {
    // The trigger is never disabled now, so a delegate whose footer exists at all can
    // always be opened — including one whose only content is its tool time.
    const root = settled();
    const footer = root.querySelector<HTMLElement>(".subagent-footer");
    const trigger = root.querySelector<HTMLButtonElement>(".turn-ledger-summary");
    expect(trigger?.disabled).toBe(false);
    expect(trigger?.getAttribute("aria-expanded")).toBe("false");
    trigger?.click();
    expect(footer?.dataset["info"]).toBe("open");
    expect(trigger?.getAttribute("aria-expanded")).toBe("true");
  });
});

// The closed angle lives in 10-shell-app.css and the card's rules in 14-tools.css, so only the
// assembled bundle (`mountAppCSS()`) answers the cascade.
describe("the leaf's navigation chevron never turns", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement;

  beforeEach(() => {
    style = mountAppCSS();
    host = document.createElement("div");
    document.body.appendChild(host);
  });

  afterEach(() => {
    style.remove();
    host.remove();
  });

  it("resolves the closed angle and keeps it through every settle", () => {
    const sa = buildSubagentCard("Subagent", "in_progress", { open: opener() });
    host.appendChild(sa.root);
    const chev = sa.root.querySelector<HTMLElement>(".subagent-head-chevron > .disclosure-chevron");
    if (chev === null) {
      throw new Error("the card's head has no chevron");
    }
    const turn = (): string => getComputedStyle(chev).getPropertyValue("--chev-turn").trim();
    expect(turn()).toBe("-90deg");
    sa.setStatus("completed");
    expect(turn()).toBe("-90deg");
    sa.setStatus("failed");
    expect(turn()).toBe("-90deg");
  });
});
