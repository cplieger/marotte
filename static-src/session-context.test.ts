import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock persist.js so we observe patchSettings calls without touching the network. Mirrors
// persist.test.ts's pattern.
vi.mock("./persist.js", () => ({
  patchSettings: vi.fn(),
}));

import {
  setLastModel,
  getLastModel,
  restoreLastModel,
  getLastEffortFor,
  restoreLastEffort,
} from "./session-context.js";
import { patchSettings } from "./persist.js";

describe("the last model a new chat opens with", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Reset module-level cache by restoring to a known sentinel.
    restoreLastModel("__reset__");
  });

  it("remembers a pick and hands it to the settings writer", () => {
    setLastModel("claude-opus-4.6");
    expect(patchSettings).toHaveBeenCalledTimes(1);
    expect(patchSettings).toHaveBeenCalledWith({ last_model: "claude-opus-4.6" });
    expect(getLastModel()).toBe("claude-opus-4.6");
  });

  it("hands every pick to the settings writer, which alone decides one changes nothing", () => {
    setLastModel("claude-opus-4.6");
    restoreLastModel("gpt-5");
    setLastModel("gpt-5");

    expect(patchSettings).toHaveBeenNthCalledWith(1, { last_model: "claude-opus-4.6" });
    expect(patchSettings).toHaveBeenNthCalledWith(2, { last_model: "gpt-5" });
    expect(getLastModel()).toBe("gpt-5");
  });

  it("restoreLastModel updates the cache without patching", () => {
    restoreLastModel("kiro-default");
    expect(patchSettings).not.toHaveBeenCalled();
    expect(getLastModel()).toBe("kiro-default");
  });
});

describe("the effort seed — the level a new chat opens on, per model", () => {
  // The seed is WRITTEN by the server, inside the set_effort command that justifies it, so this
  // module only ever adopts and reads it. There is no setter to test: a level the session refused
  // must not be remembered, and only the command knows whether it took.
  beforeEach(() => {
    vi.clearAllMocks();
    restoreLastEffort({});
  });

  it("answers the level adopted for that model", () => {
    restoreLastEffort({ "claude-opus-5": "max" });
    expect(getLastEffortFor("claude-opus-5")).toBe("max");
  });

  it("keeps one level per model, so a pick on one retracts none of the others", () => {
    // The whole reason the seed is a map: one level for the app meant the only level remembered
    // anywhere was the one chosen most recently, so every other model silently reopened on its own
    // default tier.
    restoreLastEffort({ m1: "max", m2: "low" });

    expect(getLastEffortFor("m1")).toBe("max");
    expect(getLastEffortFor("m2")).toBe("low");
  });

  it("answers only for a model that has an entry", () => {
    restoreLastEffort({ "claude-opus-5": "max" });
    expect(getLastEffortFor("gpt-luna")).toBe("");
    expect(getLastEffortFor("")).toBe("");
  });

  it("an inherited member answers like a model with no entry", () => {
    // A model id is arbitrary text, and a bare record read hands back Object's own member for
    // `constructor` — which would reach the picker as a level.
    expect(getLastEffortFor("constructor")).toBe("");
    expect(getLastEffortFor("toString")).toBe("");
  });

  it("adopting a payload patches nothing", () => {
    // The write side is the server's. A patch from here would push a server-confirmed value
    // straight back and loop at debounce speed.
    restoreLastEffort({ m1: "xhigh" });

    expect(patchSettings).not.toHaveBeenCalled();
    expect(getLastEffortFor("m1")).toBe("xhigh");
  });

  it("an absent payload leaves the cache alone", () => {
    // A settings document that says nothing about the seed is not a document saying nobody has
    // picked: nothing is remembered, so nothing is retracted.
    restoreLastEffort({ m1: "high" });
    restoreLastEffort(undefined);
    expect(getLastEffortFor("m1")).toBe("high");
  });
});
