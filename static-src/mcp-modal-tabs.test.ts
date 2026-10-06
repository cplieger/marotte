// One tab per mode, and switching mode never hides a tab. `data-mcp-mode` marks both panels and tab buttons, so a
// selector over the bare attribute reaches both; hiding the bar strands the Remote URL and npm forms.

import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// Stubbed: this file is about which elements carry `hidden`.
vi.mock("./actions/tools.js", () => ({ getToolsStatus: { dispatch: async () => null } }));
vi.mock("./tools.js", () => ({ installToolAndWait: async () => ({ ok: true }) }));

function modalMarkup(): HTMLElement {
  const start = indexHtml.indexOf('<div id="mcp-modal"');
  const end = indexHtml.indexOf("<!-- Git output popup -->", start);
  expect(start, "#mcp-modal not found").toBeGreaterThan(-1);
  expect(end, "the marker after the MCP modal not found").toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = indexHtml.slice(start, end);
  return host;
}

const MODES = ["search", "remote", "npm", "raw"] as const;

describe("MCP add-integration tab bar (static/index.html)", () => {
  it("declares one tab per panel mode", () => {
    const host = modalMarkup();
    const bar = host.querySelector<HTMLElement>("#mcp-modal-tabs");
    expect(bar, "#mcp-modal-tabs must exist").not.toBeNull();

    const tabbed = Array.from(bar?.querySelectorAll<HTMLElement>(".seg") ?? []).map(
      (b) => b.dataset["mcpMode"],
    );
    const panelled = Array.from(host.querySelectorAll<HTMLElement>(".mcp-mode-panel")).map(
      (p) => p.dataset["mcpMode"],
    );

    // A mode with a panel and no tab is unreachable; a tab with no panel shows an empty modal.
    expect([...tabbed].sort()).toEqual([...MODES].sort());
    expect([...panelled].sort()).toEqual([...MODES].sort());
  });

  it("marks exactly one tab selected, and it is the mode the modal opens on", () => {
    const bar = modalMarkup().querySelector<HTMLElement>("#mcp-modal-tabs");
    const selected = Array.from(bar?.querySelectorAll<HTMLElement>(".seg") ?? []).filter(
      (b) => b.getAttribute("aria-selected") === "true",
    );
    expect(selected).toHaveLength(1);
    expect(selected[0]?.dataset["mcpMode"]).toBe("search");
  });
});

describe("switching mode hides panels only", () => {
  it("leaves every tab button visible in all four modes", async () => {
    document.body.replaceChildren(...Array.from(modalMarkup().childNodes));
    const { setEditing, initModal } = await import("./mcp-panels.js");

    for (const mode of MODES) {
      setEditing({ id: "" });
      initModal({ mode, server: null });

      const hiddenTabs = Array.from(
        document.querySelectorAll<HTMLElement>("#mcp-modal-tabs .seg"),
      ).filter((b) => b.classList.contains("hidden"));
      expect(
        hiddenTabs.map((b) => b.dataset["mcpMode"]),
        `mode=${mode}`,
      ).toEqual([]);

      const shownPanels = Array.from(
        document.querySelectorAll<HTMLElement>(".mcp-mode-panel"),
      ).filter((p) => !p.classList.contains("hidden"));
      expect(
        shownPanels.map((p) => p.dataset["mcpMode"]),
        `mode=${mode}`,
      ).toEqual([mode]);

      const active = Array.from(
        document.querySelectorAll<HTMLElement>("#mcp-modal-tabs .seg"),
      ).filter((b) => b.classList.contains("active"));
      expect(
        active.map((b) => b.dataset["mcpMode"]),
        `mode=${mode}`,
      ).toEqual([mode]);
    }
  });

  it("pairs every tab with its panel through the shared controller's ids", async () => {
    document.body.replaceChildren(...Array.from(modalMarkup().childNodes));
    const { setEditing, initModal } = await import("./mcp-panels.js");
    setEditing({ id: "" });
    initModal({ mode: "remote", server: null });

    for (const mode of MODES) {
      const tab = document.querySelector<HTMLElement>(
        `#mcp-modal-tabs .seg[data-mcp-mode="${mode}"]`,
      );
      const panel = document.querySelector<HTMLElement>(`.mcp-mode-panel[data-mcp-mode="${mode}"]`);
      expect(tab?.getAttribute("role"), mode).toBe("tab");
      expect(tab?.getAttribute("aria-controls"), mode).toBe(panel?.id);
      expect(panel?.getAttribute("role"), mode).toBe("tabpanel");
      expect(panel?.getAttribute("aria-labelledby"), mode).toBe(tab?.id);
      expect(tab?.getAttribute("aria-selected"), mode).toBe(String(mode === "remote"));
      expect(tab?.getAttribute("tabindex"), mode).toBe(mode === "remote" ? "0" : "-1");
    }
  });
});

describe("the bar keeps its labels on a phone", () => {
  // The segments carry no icon, so a truncated label would leave an empty button; the modal's wrap basis (60-mcp.css)
  // prevents it, measured at the phone card width.
  let style: HTMLStyleElement;
  beforeAll(() => {
    style = mountAppCSS();
  });
  afterAll(() => {
    style.remove();
    document.body.replaceChildren();
  });

  it("wraps to two rows of two rather than ellipsising", () => {
    document.body.replaceChildren(...Array.from(modalMarkup().childNodes));
    const card = document.querySelector<HTMLElement>("#mcp-modal");
    const bar = document.querySelector<HTMLElement>("#mcp-modal-tabs");
    if (card === null || bar === null) {
      throw new Error("#mcp-modal or #mcp-modal-tabs missing");
    }
    card.classList.remove("hidden");
    // On a 320px phone the card is 294px, and the dialog's inset leaves the bar about this much.
    card.style.width = "270px";

    const segs = [...bar.querySelectorAll<HTMLElement>(".seg")];
    expect(segs).toHaveLength(4);
    const truncated = segs.filter((s) => s.scrollWidth > s.clientWidth);
    expect(truncated.map((s) => s.textContent)).toEqual([]);
    const tops = new Set(segs.map((s) => s.getBoundingClientRect().top));
    expect(tops.size, "two rows").toBe(2);
    expect(bar.scrollWidth - bar.clientWidth, "the bar itself does not overflow").toBe(0);
  });
});
