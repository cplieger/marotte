// The Agent capabilities switches and selects as static/index.html ships them, which is also what
// stays on screen when settings cannot be applied, so each must start at
// settings.EffectiveDefaults() for its key.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

function shippedSwitch(id: string): HTMLInputElement {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  const box = doc.getElementById(id);
  if (!(box instanceof HTMLInputElement)) {
    throw new Error(`static/index.html has no input #${id}`);
  }
  return box;
}

describe("Agent capabilities switches before settings load", () => {
  it.each([
    ["flag-knowledge", true],
    ["flag-model-fallback", true],
    ["flag-spec-ask-first", false],
    ["flag-steering-reminders", false],
    ["flag-workflows", true],
    ["flag-chat-messaging", false],
    ["flag-chat-awareness", false],
  ] as const)("paints #%s at its default (%s)", (id, on) => {
    expect(shippedSwitch(id).checked).toBe(on);
  });

  // A select paints the last option marked selected, so a second marked
  // option shows the wrong default without any parse error.
  it.each([
    ["memory-mode", "off"],
    ["work-validation", ""],
    ["auto-routing", ""],
    ["auto-delegation", ""],
    ["cloudformation-safety", ""],
    ["workflow-creator-effort", ""],
    ["chat-awareness-mode", ""],
    ["knowledge-index-type", ""],
  ] as const)("paints #%s at its default (%j)", (id, value) => {
    const doc = new DOMParser().parseFromString(indexHtml, "text/html");
    const select = doc.getElementById(id);
    if (!(select instanceof HTMLSelectElement)) {
      throw new Error(`static/index.html has no select #${id}`);
    }
    expect(select.value).toBe(value);
    expect(select.querySelectorAll("option[selected]")).toHaveLength(1);
  });

  // kiro-cli 3.2.0's vocabularies: workflowsCreatorEffort's gce list, the infraSafety enum
  // (off, monitor, enforce) behind settings.ValidCloudFormationChoice's "", on, off and enforce, and
  // knowledgeIndexType's best and fast (its "accurate" normalizes to best).
  it.each([
    ["workflow-creator-effort", ["", "low", "medium", "high", "xhigh", "max"]],
    ["chat-awareness-mode", ["", "direct", "consult"]],
    ["cloudformation-safety", ["", "on", "enforce", "off"]],
    ["knowledge-index-type", ["", "best", "fast"]],
  ] as const)("#%s offers exactly the values the server accepts", (id, values) => {
    const doc = new DOMParser().parseFromString(indexHtml, "text/html");
    const options = doc.querySelectorAll<HTMLOptionElement>(`select[id="${id}"] option`);
    expect([...options].map((o) => o.value)).toEqual(values);
  });

  it("#flag-workflows hint names what each position does to delegation", () => {
    const doc = new DOMParser().parseFromString(indexHtml, "text/html");
    expect(doc.getElementById("flag-workflows-hint")?.textContent).toContain(
      "On: the agent can start workflows that run in the background while you keep chatting, and " +
        "Workflow use says when. Off: it uses subagents instead and waits for each to finish before it continues.",
    );
  });
});
