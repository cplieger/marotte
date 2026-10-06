// The licence-attribution link restates the pointer target floor in `.code-refs-link`: as an `<li>` descendant it
// falls under 61-mcp-tools.css's WCAG 2.5.8 inline exception, which zeroes the app-wide floor (it measured 20px).
// Real layout, because the exemption arrives from another sheet at zero specificity. Two controls: a bare anchor
// outside prose (gets the floor) and one inside an `<li>` (exempt), so the target is the link's own declaration.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { syncCodeReferences } from "./code-refs.js";

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

/**
 * A bare anchor's height, directly in the host or inside an `<li>`. `display: inline-flex` because `min-height` does
 * not apply to a non-replaced inline box; the two calls differ only in position, which the exemption keys on.
 */
function bareAnchorPx(inList: boolean): number {
  const probe = document.createElement("a");
  probe.href = "https://example.com/";
  probe.textContent = "x";
  probe.style.display = "inline-flex";
  const mount = document.createElement(inList ? "li" : "div");
  mount.append(probe);
  host.append(mount);
  const h = probe.getBoundingClientRect().height;
  mount.remove();
  return h;
}

function attributionLink(): HTMLAnchorElement {
  const turn = document.createElement("div");
  turn.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  const row = document.createElement("div");
  row.className = "msg-row";
  body.append(row);
  turn.append(body);
  host.append(turn);

  syncCodeReferences(row, [
    {
      license_name: "MIT",
      repository: "github.com/foo/bar",
      url: "https://github.com/foo/bar",
    },
  ]);
  const link = row.querySelector<HTMLAnchorElement>(".code-refs-link");
  expect(link, "the footnote built its link").not.toBeNull();
  return link as HTMLAnchorElement;
}

describe("the licensed-code attribution link", () => {
  it.each(["fine", "coarse"] as const)(
    "keeps the %s tier's hit-target floor, which the inline exception withholds",
    (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const floor = bareAnchorPx(false);
      const exempt = bareAnchorPx(true);
      const link = attributionLink();

      // Premise: the exception still reaches an anchor in an `<li>`, so the assertion below is about the link's own rule.
      expect(exempt, "a bare anchor in an <li> is exempt").toBeLessThan(floor);
      expect(
        link.getBoundingClientRect().height,
        "the attribution link's box",
      ).toBeGreaterThanOrEqual(floor);
    },
  );

  it("is a 44px target under a finger, which is the number the exception cost", () => {
    // The coarse tier stated absolutely (the WCAG 2.5.5 / Apple HIG 44px figure): a floor probe alone would pass at any
    // shared value.
    document.documentElement.dataset["pointer"] = "coarse";
    expect(attributionLink().getBoundingClientRect().height).toBe(44);
  });
});
