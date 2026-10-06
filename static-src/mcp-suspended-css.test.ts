// The suspended tool list's TREATMENT, measured against the assembled cascade.
//
// The panel test beside this one pins the mark (`data-suspended`) and the notice;
// what it cannot see is whether the mark PAINTS anything. A rule whose selector
// loses, or whose spelling the shared stylelint config rejects, reads identically
// from the panel's side, and a reader is told a grant is in force when it is not.
// Every claim here is a computed value in a real browser over the real bundle.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLDivElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host?.remove();
});

/** A real chip inside a real MCP form section, suspended or not. */
function mountSection(suspended: boolean): { label: HTMLElement; notice: HTMLElement } {
  host?.remove();
  host = document.createElement("div");
  host.innerHTML = `
    <div class="mcp-form-section"${suspended ? ' data-suspended=""' : ""}>
      <div class="mcp-list-suspended">
        <span>Suspended</span>
      </div>
      <div class="chip-list">
        <span class="chip mono"><code class="chip-label">search_repos</code><button class="chip-remove" type="button"></button></span>
      </div>
    </div>`;
  document.body.appendChild(host);
  const label = host.querySelector<HTMLElement>(".chip-label");
  const notice = host.querySelector<HTMLElement>(".mcp-list-suspended");
  expect(label, "no .chip-label mounted").not.toBeNull();
  expect(notice, "no .mcp-list-suspended mounted").not.toBeNull();
  return { label: label as HTMLElement, notice: notice as HTMLElement };
}

describe("a suspended tool list", () => {
  it("strikes the NAME through and leaves the remove control alone", () => {
    // The strike is the whole non-hue channel: the names have to read as
    // not-in-force without relying on colour (WCAG 1.4.1). Scoped to the label so
    // the × does not read as a disabled button — the list is still editable.
    const { label } = mountSection(true);
    const remove = host.querySelector<HTMLElement>(".chip-remove");

    expect(getComputedStyle(label).textDecorationLine).toBe("line-through");
    expect(getComputedStyle(remove as HTMLElement).textDecorationLine).toBe("none");
  });

  it("leaves the name untouched with no mark, so the rule is the mark's and not the class's", () => {
    const { label } = mountSection(false);

    expect(getComputedStyle(label).textDecorationLine).toBe("none");
  });

  it("washes the notice in its own state rather than stepping a surface rung", () => {
    // A notice is a hue wash of the state over the container,
    // never a rung. A transparent background means the rule did not apply at all.
    const { notice } = mountSection(true);
    const bg = getComputedStyle(notice).backgroundColor;

    expect(bg).not.toBe("rgba(0, 0, 0, 0)");
    expect(bg).not.toBe("transparent");
  });
});
