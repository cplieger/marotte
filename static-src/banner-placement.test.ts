// The banner band's placement must keep it CLICKABLE. Inside `#messages-wrap` (absolute,
// inset 0) it would paint under the scroller and answer no pointer; out of flow and before
// the transcript in DOM order, it needs an explicit `z-index`. Read as RENDERED GEOMETRY,
// with `mountAppCSS` assembling `css/MANIFEST` in `cmd/bundle`'s order, because
// equal-specificity ties are decided by that order.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import indexHtml from "../static/index.html?raw";
import messagesCss from "./css/13-messages.css?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

describe("banner band placement (static/index.html)", () => {
  it("puts the banner stack outside the transcript scroller's wrapper", () => {
    const host = document.createElement("div");
    const start = indexHtml.indexOf('<div id="chat-view"');
    const end = indexHtml.indexOf('<form id="prompt-form"', start);
    expect(start, "#chat-view not found").toBeGreaterThan(-1);
    expect(end, "#prompt-form not found").toBeGreaterThan(start);
    host.innerHTML = indexHtml.slice(start, end);

    const stack = host.querySelector<HTMLElement>("#banner-stack");
    expect(stack, "#banner-stack must exist").not.toBeNull();
    expect(
      stack?.closest("#messages-wrap-outer"),
      "#banner-stack must not sit inside #messages-wrap-outer — the scroller is inset:0 over it",
    ).toBeNull();
    expect(stack?.parentElement?.id).toBe("chat-view");
    // Before the wrapper, so the band paints above the transcript rather than under it.
    expect(stack?.nextElementSibling?.id).toBe("messages-wrap-outer");
  });
});

describe("the banner band sits under the title bar", () => {
  let styleEl: HTMLStyleElement;
  let app: HTMLElement;
  let toolbar: HTMLElement;
  let band: HTMLElement;
  let wrapOuter: HTMLElement;
  let banner: HTMLElement;

  /**
   * The real ancestor chain: the band's containing block is `#chat-area`, and
   * `[data-tab-view]` makes `#chat-view` a flex column.
   */
  beforeAll(() => {
    styleEl = mountAppCSS();

    app = document.createElement("div");
    app.id = "app";
    app.innerHTML = `
      <main id="chat-area">
        <div class="chat-toolbar">
          <button type="button" id="find-btn" class="icon-btn" aria-label="Search"></button>
          <button type="button" id="files-btn" class="icon-btn" aria-label="Files"></button>
          <button type="button" id="git-btn" class="icon-btn" aria-label="Git"></button>
        </div>
        <div id="chat-view" data-tab-view>
          <div id="banner-stack" class="banner-stack"></div>
          <div id="messages-wrap-outer"><div id="messages-wrap"></div></div>
        </div>
      </main>`;
    document.body.appendChild(app);

    toolbar = app.querySelector<HTMLElement>(".chat-toolbar")!;
    band = app.querySelector<HTMLElement>("#banner-stack")!;
    wrapOuter = app.querySelector<HTMLElement>("#messages-wrap-outer")!;

    banner = document.createElement("div");
    banner.className = "banner banner-error";
    banner.innerHTML = `<span class="banner-glyph" aria-hidden="true">\u2717</span><span class="banner-msg">A message.</span>`;
    band.appendChild(banner);
  });

  afterAll(() => {
    styleEl?.remove();
    app?.remove();
  });

  it("starts at or below the bar's bottom edge, so neither can cover the other", () => {
    // The bar is a flex child of the band's containing block, so an overlap means it left
    // flow. Below the bar's bottom, horizontal position cannot overlap at all.
    expect(band.getBoundingClientRect().top).toBeGreaterThanOrEqual(
      toolbar.getBoundingClientRect().bottom - 0.5,
    );
  });

  it("gives a banner row at least the toolbar's full box height", () => {
    expect(banner.getBoundingClientRect().height).toBeGreaterThanOrEqual(
      toolbar.getBoundingClientRect().height,
    );
  });

  it("reserves no flow space, so a banner reveals no canvas above the transcript", () => {
    // An overlay moves nothing; in flow, the band's margin shrank the transcript and exposed
    // the `body` canvas.
    const outerTop = wrapOuter.getBoundingClientRect().top;
    const viewTop = app.querySelector<HTMLElement>("#chat-view")!.getBoundingClientRect().top;
    expect(outerTop).toBeCloseTo(viewTop, 1);
  });

  it("declares no background of its own, on the band or on the stack", () => {
    // Nothing painted the reported box, so a `background` appearing here would be
    // a new one rather than a fix.
    expect(getComputedStyle(band).backgroundColor).toBe("rgba(0, 0, 0, 0)");
  });

  it("stacks above the transcript", () => {
    // Out of flow before `#messages-wrap-outer`, two `z-index: auto` boxes paint in DOM order,
    // so an explicit z-index keeps the transcript off it.
    const bandZ = Number(getComputedStyle(band).zIndex);
    expect(Number.isNaN(bandZ), "the band needs an explicit z-index, not `auto`").toBe(false);
    expect(bandZ).toBeGreaterThan(0);
  });
});

describe("--chat-toolbar-h is the toolbar's real box height", () => {
  let styleEl: HTMLStyleElement;
  let app: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    app = document.createElement("div");
    app.id = "app";
    app.innerHTML = `
      <main id="chat-area">
        <div class="chat-toolbar">
          <button type="button" class="icon-btn" aria-label="Search"></button>
        </div>
      </main>`;
    document.body.appendChild(app);
  });

  afterAll(() => {
    styleEl?.remove();
    app?.remove();
  });

  it("resolves to what the toolbar actually measures", () => {
    // Measured, not string-matched: the offset must account for the toolbar's 1px borders.
    const probe = document.createElement("div");
    probe.style.blockSize = "var(--chat-toolbar-h)";
    app.appendChild(probe);
    const toolbar = app.querySelector<HTMLElement>(".chat-toolbar")!;
    expect(probe.getBoundingClientRect().height).toBeCloseTo(
      toolbar.getBoundingClientRect().height,
      1,
    );
    probe.remove();
  });
});

describe("the banner band on a phone", () => {
  it("returns to the flow", () => {
    // Read off the CSSOM: the 1280px test viewport cannot reach it. On a short screen an
    // overlay would cover the live turn, so the band takes flow space.
    const media = messagesCss.match(/@media \(width <= 48rem\) \{\s*\.banner-stack \{([^}]*)\}/);
    expect(media, "13-messages.css must carry the band's own <=48rem block").not.toBeNull();
    expect(media?.[1]).toContain("position: static");
  });
});
