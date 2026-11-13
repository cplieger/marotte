import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { resetActionFramework } from "@cplieger/reactive/actions/testing";
import type * as ApiClient from "./api-client.js";
import type * as Bus from "./bus.js";
import type * as Docs from "./docs.js";
import type * as GitStatusStore from "./git-status-store.js";
import type * as Memories from "./memories.js";
import type * as Persist from "./persist.js";
import type * as Powers from "./powers.js";
import type * as Recipes from "./recipes.js";
import type * as Router from "./router.js";
import type * as Tabs from "./tabs.js";
import type * as Toast from "./toast.js";
import type { DocsTab } from "./route-path.js";

/** The docs row's route as the strip holds it, open on its default. A fresh signal per case: each
 *  case's docs.ts instance stays subscribed to the cell it first read. */
const docsRoute = await vi.hoisted(async () => {
  const { signal } = await import("@cplieger/reactive");
  return { cell: signal<DocsTab | null>("steering") };
});
const H = vi.hoisted(() => ({
  memoryMode: "off",
  setDocsTab: vi.fn((tab: DocsTab) => {
    docsRoute.cell.value = tab;
  }),
  sse: new Map<string, () => void>(),
}));

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: vi.fn((path: string) =>
    Promise.resolve(
      path === "/api/workspace/kiro-docs"
        ? { docs: [], truncated: false }
        : path === "/api/steering/issues"
          ? { issues: {} }
          : path === "/api/config/standing"
            ? { standing: [], load_errors: {} }
            : { hooks: [] },
    ),
  ),
}));
vi.mock("./persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  loadSettings: vi.fn(() =>
    Promise.resolve({ memory_mode: H.memoryMode, settings_epoch: 0, settings_revision: 0 }),
  ),
  refreshSettings: vi.fn(() =>
    Promise.resolve({ memory_mode: H.memoryMode, settings_epoch: 0, settings_revision: 0 }),
  ),
}));
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
// The three RPC panels render their own rows; only the Memories handoff is watched.
vi.mock("./recipes.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Recipes>()),
  renderRecipesPanel: vi.fn(),
}));
vi.mock("./powers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Powers>()),
  renderPowersPanel: vi.fn(),
}));
vi.mock("./memories.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Memories>()),
  renderMemoriesPanel: vi.fn(),
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  subTabOf: (kind: string) => (kind === "docs" ? docsRoute.cell.value : null),
  setDocsTab: H.setDocsTab,
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Bus>()),
  onSSE: vi.fn((type: string, fn: () => void) => {
    H.sse.set(type, fn);
    return () => undefined;
  }),
}));
// Subscribing fetches through the transport, which would outlive the file.
vi.mock("./git-status-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof GitStatusStore>()),
  onGitStatusChange: vi.fn(() => () => undefined),
}));

const TABS = [
  "steering",
  "skills",
  "prompts",
  "agents",
  "specs",
  "hooks",
  "workflows",
  "memories",
  "powers",
];

/** Busts the specifier: `initDocsView` runs once per module instance and Browser Mode's module map
 *  is URL-keyed, so each case needs its own instance. Only docs.ts is busted; its mocks still apply. */
let instance = 0;

async function freshDocs(): Promise<typeof Docs> {
  instance++;
  return (await import(/* @vite-ignore */ `./docs.ts?case=${instance}`)) as typeof Docs;
}

function segment(tab: string): HTMLElement {
  return document.querySelector<HTMLElement>(`[data-docs-tab="${tab}"]`)!;
}

function panelShown(tab: string): boolean {
  return !document.querySelector(`[data-docs-panel="${tab}"]`)!.classList.contains("hidden");
}

function settingsChanged(mode: string): void {
  H.memoryMode = mode;
  H.sse.get("settings_updated")?.();
}

/** Opens Docs with Memory in `mode` and waits for the settings answer to reach the bar. */
async function openDocs(mode: string): Promise<typeof Docs> {
  H.memoryMode = mode;
  const docs = await freshDocs();
  docs.showDocsTab();
  await vi.waitFor(() => {
    expect(segment("memories").classList.contains("hidden")).toBe(mode === "off");
  });
  return docs;
}

beforeEach(async () => {
  const { signal } = await import("@cplieger/reactive");
  docsRoute.cell = signal<DocsTab | null>("steering");
  resetActionFramework();
  (await import("./persist.js")).__testResetTracking();
  H.sse.clear();
  document.body.innerHTML = `
    <div id="docs-view">
      <div id="docs-new-slot" class="page-toolbar"></div>
      <nav id="docs-tab-bar" class="seg-bar">${TABS.map(
        (t) =>
          `<button type="button" class="seg" data-docs-tab="${t}"><span class="seg-label">${t}</span></button>`,
      ).join("")}</nav>
      ${TABS.map((t) => `<div data-docs-panel="${t}" class="docs-panel hidden"></div>`).join("")}
    </div>`;
  // The URL is corrected only on an open page.
  Object.defineProperty(document.getElementById("docs-view"), "offsetParent", {
    get: () => document.body,
    configurable: true,
  });
});

afterEach(() => {
  document.body.replaceChildren();
});

describe("the Memories tab follows the Memory setting", () => {
  it("withdraws a deep-linked Memories tab once the settings say Off, opening no panel", async () => {
    H.memoryMode = "off";
    const { showDocsTab } = await freshDocs();
    const { renderMemoriesPanel } = await import("./memories.js");
    const { replaceRoute } = await import("./router.js");

    // The row a /docs/memories deep link opens before the settings answer.
    docsRoute.cell.value = "memories";
    showDocsTab();

    await vi.waitFor(() => {
      expect(segment("memories").classList.contains("hidden")).toBe(true);
    });
    expect(panelShown("steering")).toBe(true);
    expect(panelShown("memories")).toBe(false);
    expect(renderMemoriesPanel).not.toHaveBeenCalled();
    expect(replaceRoute).toHaveBeenCalledWith({ kind: "docs", tab: "steering" });
    expect(H.setDocsTab.mock.calls.at(-1)).toEqual(["steering"]);
    expect(TABS.filter((t) => segment(t).classList.contains("hidden"))).toEqual(["memories"]);
  });

  it("resolves a route to a withdrawn tab to the first tab", async () => {
    const { resolveDocsTab } = await openDocs("off");

    expect(resolveDocsTab("memories")).toBe("steering");
    expect(resolveDocsTab("hooks")).toBe("hooks");
  });

  it("brings the tab back as soon as Memory turns on", async () => {
    await openDocs("off");

    settingsChanged("learn");

    await vi.waitFor(() => {
      expect(segment("memories").classList.contains("hidden")).toBe(false);
    });
  });

  it("moves a reader off the open Memories tab when Memory turns off", async () => {
    await openDocs("learn");
    const { renderMemoriesPanel } = await import("./memories.js");
    docsRoute.cell.value = "memories";
    expect(panelShown("memories")).toBe(true);
    expect(renderMemoriesPanel).toHaveBeenCalled();

    settingsChanged("off");

    await vi.waitFor(() => {
      expect(panelShown("steering")).toBe(true);
    });
    expect(segment("memories").classList.contains("hidden")).toBe(true);
  });
});
