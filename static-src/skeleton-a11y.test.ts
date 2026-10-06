import { KEY_ATTR } from "@cplieger/reactive";
import { describe, it, expect, beforeEach } from "vitest";

import {
  chatSkeleton,
  editorDocSkeleton,
  fileRowsSkeleton,
  gitRepoSkeleton,
  loadMoreSkeleton,
  paintPlaceholder,
  skeletonField,
  skeletonRows,
} from "./skeleton.js";

// The accessibility contract: the placeholder is `aria-hidden` and the HOST carries the busy state.
// Nothing here mocks `./dom.js`, so `setBusy` is the real one — the literal `"true"` and the
// attribute REMOVAL are its own contract, and a fake would re-state it rather than test it.

function host(): HTMLElement {
  const h = document.createElement("div");
  document.body.appendChild(h);
  return h;
}

function keyedRow(): HTMLElement {
  const row = document.createElement("div");
  row.setAttribute(KEY_ATTR, "row-1");
  return row;
}

function placeholder(): HTMLElement {
  const p = document.createElement("div");
  p.className = "shimmer";
  return p;
}

/** The shape of both git mounts (`static/index.html` `#git-changes-mount` and `#git-prs-mount`),
 *  which is the one host family where `aria-busy` changes when the region's announcements reach
 *  the reader. */
function liveRegionHost(): HTMLElement {
  const h = document.createElement("div");
  h.className = "git-multirepo-mount";
  h.setAttribute("aria-live", "polite");
  document.body.appendChild(h);
  return h;
}

describe("paintPlaceholder marks the host busy", () => {
  beforeEach(() => {
    document.body.replaceChildren();
  });

  it("arms `aria-busy=true` on an admitted host and clears it on teardown", () => {
    const h = host();
    const teardown = paintPlaceholder(h, placeholder);
    expect(h.getAttribute("aria-busy")).toBe("true");
    teardown();
    expect(h.hasAttribute("aria-busy")).toBe(false);
  });

  it("marks NOTHING busy on a host already holding content", () => {
    const h = host();
    h.appendChild(keyedRow());
    const teardown = paintPlaceholder(h, placeholder);
    expect(h.hasAttribute("aria-busy")).toBe(false);
    teardown();
    expect(h.hasAttribute("aria-busy")).toBe(false);
  });

  it("survives an absent host, so the refusal reaches no writer", () => {
    const teardown = paintPlaceholder(null, placeholder);
    expect(() => {
      teardown();
    }).not.toThrow();
  });

  it("arms and RELEASES busy on an `aria-live` host, leaving no attribute behind", () => {
    const h = liveRegionHost();
    const teardown = paintPlaceholder(h, placeholder);
    expect(h.getAttribute("aria-busy")).toBe("true");
    teardown();
    // `hasAttribute` rather than a value comparison: the empty string is not a busy state, so
    // ABSENT is the claim — an attribute left behind at any value silences this region for the rest
    // of the session.
    expect(h.hasAttribute("aria-busy")).toBe(false);
    expect(h.getAttribute("aria-live")).toBe("polite");
  });
});

describe("every painter's root is hidden from the accessibility tree", () => {
  // ENUMERATED rather than swept: the marker is per-painter, so a new painter that forgets it is
  // the failure this catches.
  const roots: readonly [string, () => Element][] = [
    ["chatSkeleton", () => chatSkeleton()],
    ["loadMoreSkeleton", () => loadMoreSkeleton()],
    ["editorDocSkeleton", () => editorDocSkeleton()],
    ["fileRowsSkeleton", () => fileRowsSkeleton()],
    ["gitRepoSkeleton", () => gitRepoSkeleton({ widths: ["40%"] })],
    ["skeletonRows", () => skeletonRows("list-row", [[{ w: "50%" }]])],
    ["skeletonField", () => skeletonField()],
  ];

  for (const [name, build] of roots) {
    it(`${name} carries aria-hidden="true" on its root`, () => {
      expect(build().getAttribute("aria-hidden")).toBe("true");
    });
  }
});
