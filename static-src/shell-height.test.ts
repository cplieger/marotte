import { describe, it, expect, afterEach } from "vitest";
import { page } from "vitest/browser";

import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { applyStoredShellPanel, releaseStoredShellPanel, shellPanelPx } from "./shell-height.js";

const root = document.documentElement;
const attr = (): boolean => root.hasAttribute("data-shell-open");
const height = (): string => root.style.getPropertyValue("--shell-h");

function store(blob: Record<string, unknown>): void {
  localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify(blob));
}

afterEach(async () => {
  releaseStoredShellPanel();
  localStorage.clear();
  await page.viewport(1280, 720);
});

describe("applyStoredShellPanel", () => {
  it("marks a stored-open panel and carries its height", () => {
    store({ shell_open: true, shell_h: 300 });
    applyStoredShellPanel();
    expect(attr()).toBe(true);
    expect(height()).toBe("300px");
  });

  it("leaves the height to the CSS default when none was stored", () => {
    store({ shell_open: true, shell_h: 0 });
    applyStoredShellPanel();
    expect(attr()).toBe(true);
    expect(height()).toBe("");
  });

  it("clamps a stored height to 80% of the viewport", async () => {
    await page.viewport(1280, 900);
    store({ shell_open: true, shell_h: 5000 });
    applyStoredShellPanel();
    expect(height()).toBe("720px");
  });

  it("clamps a stored height up to the minimum", () => {
    store({ shell_open: true, shell_h: 10 });
    applyStoredShellPanel();
    expect(height()).toBe("96px");
  });

  it("does nothing for a closed panel", () => {
    store({ shell_open: false, shell_h: 300 });
    applyStoredShellPanel();
    expect(attr()).toBe(false);
    expect(height()).toBe("");
  });
});

describe("releaseStoredShellPanel", () => {
  it("removes the attribute and the height", () => {
    store({ shell_open: true, shell_h: 300 });
    applyStoredShellPanel();
    releaseStoredShellPanel();
    expect(attr()).toBe(false);
    expect(height()).toBe("");
  });
});

describe("shellPanelPx", () => {
  it("rounds the clamped height to whole px", () => {
    expect(shellPanelPx(300.6)).toBe(301);
  });
});
