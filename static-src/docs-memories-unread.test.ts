// Its own file: the never-answered state exists only before the first settings read lands, and
// `initDocsView` runs once per module.
import { it, expect, vi, beforeAll } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as Bus from "./bus.js";
import type * as GitStatusStore from "./git-status-store.js";
import type * as Memories from "./memories.js";
import type * as Persist from "./persist.js";
import type * as Powers from "./powers.js";
import type * as Recipes from "./recipes.js";
import type * as Router from "./router.js";
import type * as Tabs from "./tabs.js";
import type * as Toast from "./toast.js";

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
          : { hooks: [] },
    ),
  ),
}));
vi.mock("./persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  loadSettings: vi.fn(() => Promise.resolve(null)),
}));
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
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
  setDocsTab: vi.fn(),
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Bus>()),
  onSSE: vi.fn(() => () => undefined),
}));
vi.mock("./git-status-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof GitStatusStore>()),
  onGitStatusChange: vi.fn(() => () => undefined),
}));

const TABS = ["steering", "memories"];

beforeAll(() => {
  document.body.innerHTML = `
    <div id="docs-view">
      <nav id="docs-tab-bar" class="seg-bar">${TABS.map(
        (t) =>
          `<button type="button" class="seg" data-docs-tab="${t}"><span class="seg-label">${t}</span></button>`,
      ).join("")}</nav>
      ${TABS.map((t) => `<div data-docs-panel="${t}" class="docs-panel hidden"></div>`).join("")}
    </div>`;
});

it("opens a deep-linked Memories tab when the settings never answer", async () => {
  const { forceDocsTab, showDocsTab } = await import("./docs.js");
  const { renderMemoriesPanel } = await import("./memories.js");

  forceDocsTab("memories");
  showDocsTab();

  await vi.waitFor(() => {
    expect(renderMemoriesPanel).toHaveBeenCalled();
  });
  expect(document.querySelector('[data-docs-tab="memories"]')!.classList.contains("hidden")).toBe(
    false,
  );
  expect(document.querySelector('[data-docs-panel="memories"]')!.classList.contains("hidden")).toBe(
    false,
  );
});
