// Every tier 2 browser view is held by a read-budget file, and read-budgets.json holds an entry for
// each of them and for nothing else, so a view added to views.ts cannot go uncounted.
import { describe, expect, it } from "vitest";

import budgetsText from "./read-budgets.json?raw";
import { VIEWS } from "./views.ts";

const files = Object.keys(import.meta.glob("./read-budget/*.test.ts"));
const views = Object.keys(VIEWS).sort();

describe("the read budgets", () => {
  it("run one file per view", () => {
    expect(files.map((f) => f.slice("./read-budget/".length, -".test.ts".length)).sort()).toEqual(
      views,
    );
  });

  it("hold an entry for each view and no other", () => {
    const budgets = JSON.parse(budgetsText) as Record<string, unknown>;
    expect(Object.keys(budgets).sort()).toEqual(views);
  });
});
