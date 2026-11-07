import { describe, it, expect } from "vitest";
import { buildExecDetail } from "./detail.js";
import type { ExecNode } from "./model.js";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";

const node = (path: string): ExecNode => ({
  path,
  label: path,
  kind: "step",
  state: "waiting",
  children: [],
});

describe("the detail header's verbs", () => {
  it("seats the consumer's element in the header and leaves the same one in place", () => {
    const verbs = document.createElement("span");
    const detail = buildExecDetail(
      () => "",
      undefined,
      (n) => (n.path === "a" ? verbs : null),
    );
    detail.render(node("a"));
    const slot = detail.root.querySelector<HTMLElement>(".ev-d-head .ev-d-head-action");
    expect(slot?.firstElementChild).toBe(verbs);
    expect(slot?.hidden).toBe(false);

    const seats = new MutationObserver(() => undefined);
    seats.observe(slot!, { childList: true });
    detail.render(node("a"));
    expect(seats.takeRecords()).toEqual([]);
    seats.disconnect();
    expect(slot?.firstElementChild).toBe(verbs);
  });

  it("empties and hides the slot for a node with no verbs and for no node", () => {
    const verbs = document.createElement("span");
    const detail = buildExecDetail(
      () => "",
      undefined,
      (n) => (n.path === "a" ? verbs : null),
    );
    const slot = (): HTMLElement | null => detail.root.querySelector(".ev-d-head-action");
    detail.render(node("a"));
    detail.render(node("b"));
    expect(slot()?.childElementCount).toBe(0);
    expect(slot()?.hidden).toBe(true);
    detail.render(node("a"));
    detail.render(undefined);
    expect(slot()?.childElementCount).toBe(0);
    expect(slot()?.hidden).toBe(true);
  });
});

describe("the detail header's state", () => {
  it("shows a recorded end's words in place of the state word", () => {
    const detail = buildExecDetail(() => "");
    const state = (): string | null | undefined =>
      detail.root.querySelector(".ev-d-head .ev-d-state")?.textContent;
    detail.render(node("a"));
    expect(state()).toBe("waiting");
    detail.render({
      ...node("a"),
      pendingEnd: { status: "completed", text: "Done, applies when the run's other steps finish" },
    });
    expect(state()).toBe("Done, applies when the run's other steps finish");
  });

  /** A run page's detail at phone width (322 px inside the card), with the two 32 px end verbs. */
  function narrowDetail(): { detail: ReturnType<typeof buildExecDetail>; done: () => void } {
    const style = mountAppCSS();
    const host = document.createElement("div");
    host.style.inlineSize = "322px";
    const verbs = document.createElement("span");
    verbs.style.cssText = "display: inline-block; inline-size: 72px; block-size: 32px";
    const detail = buildExecDetail(
      () => "",
      undefined,
      () => verbs,
    );
    host.append(detail.root);
    document.body.append(host);
    return {
      detail,
      done: () => {
        host.remove();
        style.remove();
      },
    };
  }

  const q = (root: HTMLElement, sel: string): HTMLElement => root.querySelector<HTMLElement>(sel)!;
  const fiveHoursAgo = (): string => new Date(Date.now() - 5 * 3_600_000).toISOString();

  it.each([
    "Done, applies when the run resumes",
    "Done, applies when the run's other steps finish",
  ])("keeps %j in sentence case and wraps it before the title or the verbs give way", (text) => {
    const { detail, done } = narrowDetail();
    try {
      detail.render({
        ...node("gated"),
        start: fiveHoursAgo(),
        pendingEnd: { status: "completed", text },
      });
      expect(getComputedStyle(q(detail.root, ".ev-d-state")).textTransform).toBe("none");
      const title = q(detail.root, ".ev-d-title");
      expect(title.scrollWidth, "title not cut").toBeLessThanOrEqual(title.clientWidth);
      expect(q(detail.root, ".ev-d-head-action").getBoundingClientRect().right).toBeLessThanOrEqual(
        q(detail.root, ".ev-d-head").getBoundingClientRect().right + 0.5,
      );

      detail.render(node("gated"));
      expect(getComputedStyle(q(detail.root, ".ev-d-state")).textTransform).toBe("capitalize");
    } finally {
      done();
    }
  });

  it("cuts a long title before it squeezes a recorded end's sentence below its floor", () => {
    const { detail, done } = narrowDetail();
    try {
      detail.render({
        ...node("review_gpt_seat_for_the_release_gate"),
        start: fiveHoursAgo(),
        pendingEnd: { status: "failed", text: "Failed, applies when the run resumes" },
      });
      const title = q(detail.root, ".ev-d-title");
      expect(title.scrollWidth).toBeGreaterThan(title.clientWidth);
      const state = q(detail.root, ".ev-d-state");
      const em = parseFloat(getComputedStyle(state).fontSize);
      expect(state.getBoundingClientRect().width).toBeGreaterThanOrEqual(8 * em - 0.5);
      expect(q(detail.root, ".ev-d-head-action").getBoundingClientRect().right).toBeLessThanOrEqual(
        q(detail.root, ".ev-d-head").getBoundingClientRect().right + 0.5,
      );
    } finally {
      done();
    }
  });
});

describe("the detail's hand end", () => {
  it("says a step ended by hand, and says nothing for one that ended itself", () => {
    const style = mountAppCSS();
    const detail = buildExecDetail(() => "");
    document.body.append(detail.root);
    const shown = (): string[] =>
      [...detail.root.querySelectorAll<HTMLElement>("[role=status]")]
        .filter((n) => getComputedStyle(n).display !== "none")
        .map((n) => n.textContent ?? "");
    try {
      detail.render({ ...node("a"), state: "ok", endedByHand: true });
      expect(shown()).toEqual(["Ended by hand"]);
      detail.render({ ...node("a"), state: "ok" });
      expect(shown()).toEqual([]);
      detail.render(undefined);
      expect(shown()).toEqual([]);
    } finally {
      detail.root.remove();
      style.remove();
    }
  });
});
