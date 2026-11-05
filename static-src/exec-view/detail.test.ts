import { describe, it, expect } from "vitest";
import { buildExecDetail } from "./detail.js";
import type { ExecNode } from "./model.js";

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
