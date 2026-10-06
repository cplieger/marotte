// A linkified path chip must not break its line's leading. `linkify.ts` emits a `<button>`, and the hit-target floor in
// `61-mcp-tools.css` sizes every button, at zero specificity, while its inline exception covers only `a[href]`. Bounded
// by the floor, never a delta against the chipless line, whose `line-height: normal` varies by font stack.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** Read off an ordinary button: `--hit-floor` is a rem token, and the subject is the height the cascade gives. */
function hitFloorPx(): number {
  const probe = document.createElement("button");
  host.append(probe);
  const h = probe.getBoundingClientRect().height;
  probe.remove();
  return h;
}

/** The plain paragraph carries the premise the bound depends on. */
function prose(): { withChip: HTMLElement; plain: HTMLElement; chip: HTMLElement } {
  const plain = document.createElement("p");
  plain.className = "msg-body";
  plain.textContent = "a plain line of prose with no chip in it at all";

  const withChip = document.createElement("p");
  withChip.className = "msg-body";
  withChip.append(document.createTextNode("a line of prose naming "));
  const chip = document.createElement("button");
  chip.className = "inline-file-link";
  chip.append(document.createTextNode("internal/agent/auth.go"));
  withChip.append(chip);
  withChip.append(document.createTextNode(" in the middle of a sentence"));

  host.append(plain, withChip);
  return { withChip, plain, chip };
}

describe("a path chip inside prose", () => {
  it.each(["fine", "coarse"] as const)(
    "leaves the line at the leading of a chipless line on a %s pointer",
    (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const floor = hitFloorPx();
      const { withChip, plain, chip } = prose();

      // The premise: a font whose `normal` leading exceeded the floor fails here, not as a chip regression.
      expect(plain.getBoundingClientRect().height, "prose sits under the floor").toBeLessThan(
        floor,
      );
      // The leading is the chip's own, not the hit target's.
      expect(withChip.getBoundingClientRect().height, "the line").toBeLessThan(floor);
      // The mechanism: the chip's box is its content's, so no floor pushes the line apart.
      expect(chip.getBoundingClientRect().height, "the chip").toBeLessThan(floor);
    },
  );

  it("is still reachable by the sentence around it, which is what the exception trades for", () => {
    // WCAG 2.5.8's inline exception applies because the line constrains the target, so the chip must still be a real target.
    document.documentElement.dataset["pointer"] = "coarse";
    const { chip } = prose();
    const r = chip.getBoundingClientRect();
    expect(r.height, "a real box").toBeGreaterThan(12);
    expect(r.width, "wide enough to hit along the line").toBeGreaterThan(44);
  });
});
