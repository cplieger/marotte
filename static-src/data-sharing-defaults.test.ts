// The Data sharing switches as static/index.html ships them, which is what stays on screen until
// the kiro-cli settings read lands, so each must start at its key's default.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

const doc = new DOMParser().parseFromString(indexHtml, "text/html");

function shippedSwitch(id: string): HTMLInputElement {
  const box = doc.getElementById(id);
  if (!(box instanceof HTMLInputElement)) {
    throw new Error(`static/index.html has no input #${id}`);
  }
  return box;
}

describe("kiro-cli telemetry switch before settings load", () => {
  it("paints on, kiro-cli's own default", () => {
    expect(shippedSwitch("flag-telemetry").checked).toBe(true);
  });

  it("says it covers the toolkit client too and is on by default", () => {
    const hint = shippedSwitch("flag-telemetry")
      .closest(".section-option")
      ?.querySelector(".section-hint");
    expect(hint?.textContent).toContain("its usage telemetry");
    expect(hint?.textContent).toContain("its legacy toolkit telemetry");
    expect(hint?.textContent).toContain("On by default, as in kiro-cli.");
  });
});
