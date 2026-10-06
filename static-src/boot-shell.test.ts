// The shell paints in the first frame, guarded over static/index.html and
// static/manifest.json: no opaque loading overlay or hidden `#app` released only at the
// end of the boot chain, and no `navigate-existing` launch handler re-navigating the live
// page. Asserted on the shipped files, because these are properties of the ARTIFACT.

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import manifestRaw from "../static/manifest.json?raw";
import a11yCss from "./css/40-a11y.css?raw";

/** Parse a slice of the shell rather than the whole document: a full-document
 *  parse makes the runner chase the <link rel=stylesheet> over the network. */
function slice(html: string, from: string, to: string): HTMLElement {
  const start = html.indexOf(from);
  const end = html.indexOf(to, start + 1);
  expect(start, `marker not found: ${from}`).toBeGreaterThan(-1);
  expect(end, `marker not found: ${to}`).toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = html.slice(start, end);
  return host;
}

describe("the shell has nothing covering it", () => {
  it("ships no splash overlay and no hidden app root", () => {
    expect(indexHtml).not.toContain("app-loading");
    expect(indexHtml).not.toContain("app-hidden");
    // No rule may make either of them opaque, so a reintroduced element cannot
    // inherit a working overlay.
    expect(a11yCss).not.toContain("app-loading");
    expect(a11yCss).not.toContain("app-hidden");
  });

  it("opens #app with no class at all", () => {
    const m = /<div id="app"([^>]*)>/.exec(indexHtml);
    expect(m, "#app must be declared in index.html").not.toBeNull();
    expect(m?.[1]?.trim()).toBe("");
  });
});

describe("every region whose content is pending says so", () => {
  it("gives the tab strip authored skeleton rows", () => {
    const strip = slice(indexHtml, '<div id="tab-list"', '<div class="sidebar-footer">');
    const skeleton = strip.querySelector("#tab-strip-skeleton");
    expect(skeleton, "#tab-strip-skeleton is the strip's pending state").not.toBeNull();
    // Announced to nobody: it stands in for content, so a screen reader must not
    // read three empty rows.
    expect(skeleton?.getAttribute("aria-hidden")).toBe("true");
    // Every row carries `.skeleton`, which is what supplies the shimmer.
    const rows = skeleton?.querySelectorAll(".skeleton.skeleton-tab-row") ?? [];
    expect(rows.length).toBeGreaterThan(0);
    // No `data-tab-id` on any of them: tabs.ts reconciles the strip's children by
    // that attribute, so the placeholder and the real rows cannot contend.
    for (const row of rows) {
      expect((row as HTMLElement).dataset["tabId"]).toBeUndefined();
    }
  });

  it("gives the sidebar identity row a pending shimmer", () => {
    // Both markers matter: with only the opening one changed the fragment would run to
    // `#st-account`'s `</a>` and pass vacuously.
    const footer = slice(indexHtml, '<span id="user-email"', "</span>");
    const pending = footer.querySelector(".skeleton.sidebar-email-skeleton");
    expect(pending, "the identity row is pending until /api/whoami answers").not.toBeNull();
    expect(pending?.getAttribute("aria-hidden")).toBe("true");
  });
});

describe("a relaunch focuses the running app", () => {
  it("declares focus-existing, so a taskbar restore is not a navigation", () => {
    const manifest: unknown = JSON.parse(manifestRaw);
    expect(manifest).toMatchObject({ launch_handler: { client_mode: "focus-existing" } });
  });
});
