import { describe, it, expect } from "vitest";
import { buildRuleInputs } from "./rule-inputs.js";

describe("buildRuleInputs value", () => {
  it("drops a stored input the recipe no longer declares", () => {
    const block = buildRuleInputs({
      declared: { tag: "string" },
      setByEvent: null,
      initial: { tag: "v1", gone: "x" },
    });
    expect(block.value()).toEqual({ tag: "v1" });
  });

  it("keeps the stored inputs while the recipe's inputs are unknown", () => {
    const block = buildRuleInputs({
      declared: undefined,
      setByEvent: null,
      initial: { tag: "v1", gone: "x" },
    });
    expect(block.value()).toEqual({ tag: "v1", gone: "x" });
  });

  it("sends nothing once every stored input is retired", () => {
    const block = buildRuleInputs({ declared: {}, setByEvent: null, initial: { gone: "x" } });
    expect(block.value()).toBeUndefined();
  });
});
