import { describe, it, expect, afterEach } from "vitest";

import { makeSession, makeTurn } from "./__test-helpers__/model.js";
import { bumpMessages, setActive, setSessions } from "./store.js";
import { initTaskListPill } from "./task-list.js";

function mountPill(): { pill: HTMLButtonElement; popover: HTMLElement } {
  const slot = document.createElement("span");
  slot.className = "pill-slot";
  slot.innerHTML = `
    <button id="task-list-pill" class="pill pill-task-list pill-expandable hidden" type="button">
      <span id="task-list-badge" class="pill-badge hidden">0</span>
    </button>
    <span id="task-list-popover" class="pill-expand-content hidden">
      <span id="task-list-items" class="task-list-items"></span>
    </span>`;
  document.body.append(slot);
  const pill = slot.querySelector<HTMLButtonElement>("#task-list-pill");
  const popover = slot.querySelector<HTMLElement>("#task-list-popover");
  if (pill === null || popover === null) {
    throw new Error("task-list fixture did not mount");
  }
  return { pill, popover };
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("task list pill", () => {
  it("opens the plan card when the pill is clicked", () => {
    const { pill, popover } = mountPill();
    setSessions([
      makeSession({
        turns: [
          makeTurn({
            body: [
              {
                kind: "plan",
                payload: {
                  entries: [
                    { content: "Read the steering docs", priority: "high", status: "pending" },
                  ],
                },
              },
            ],
          }),
        ],
      }),
    ]);
    setActive("c-1");
    initTaskListPill();
    expect(pill.classList.contains("hidden"), "a filed plan shows the pill").toBe(false);
    expect(popover.textContent).toContain("Read the steering docs");

    pill.click();

    expect(popover.hidden).toBe(false);
    expect(popover.classList.contains("hidden")).toBe(false);
    expect(pill.getAttribute("aria-expanded")).toBe("true");
  });

  it("names its open items in its accessible name and tooltip, which keep the count when the row drops it", () => {
    const { pill } = mountPill();
    const plan = (statuses: readonly ("pending" | "in_progress" | "completed")[]): void => {
      setSessions([
        makeSession({
          turns: [
            makeTurn({
              body: [
                {
                  kind: "plan",
                  payload: {
                    entries: statuses.map((status, i) => ({
                      content: `Step ${String(i)}`,
                      priority: "medium",
                      status,
                    })),
                  },
                },
              ],
            }),
          ],
        }),
      ]);
      setActive("c-1");
      bumpMessages("c-1");
    };
    plan(["completed", "in_progress", "pending"]);
    initTaskListPill();
    expect(pill.getAttribute("aria-label")).toBe("Task list, 2 open");
    expect(pill.getAttribute("data-tooltip")).toBe("Task list, 2 open");

    plan(["completed", "completed"]);
    expect(pill.getAttribute("aria-label")).toBe("Task list");
    expect(pill.getAttribute("data-tooltip")).toBe("Task list");
  });
});
