// Its own file: these cases need the real router and history, which the sibling Memories files mock.
import { it, expect, vi, beforeAll, afterAll } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as Bus from "./bus.js";
import type * as GitStatusStore from "./git-status-store.js";
import type * as Memories from "./memories.js";
import type * as Persist from "./persist.js";
import type * as Powers from "./powers.js";
import type * as Recipes from "./recipes.js";
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
  loadSettings: vi.fn(() => Promise.resolve({ memory_mode: "off" })),
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
const originalHref = location.pathname + location.search + location.hash;

beforeAll(async () => {
  document.body.innerHTML = `
    <div id="docs-view">
      <nav id="docs-tab-bar" class="seg-bar">${TABS.map(
        (t) =>
          `<button type="button" class="seg" data-docs-tab="${t}"><span class="seg-label">${t}</span></button>`,
      ).join("")}</nav>
      ${TABS.map((t) => `<div data-docs-panel="${t}" class="docs-panel hidden"></div>`).join("")}
    </div>`;
  const { forceDocsTab, showDocsTab } = await import("./docs.js");
  forceDocsTab("steering");
  showDocsTab();
  await vi.waitFor(() => {
    expect(document.querySelector('[data-docs-tab="memories"]')!.classList.contains("hidden")).toBe(
      true,
    );
  });
});

afterAll(() => {
  history.replaceState(null, "", originalHref);
});

it("replaces a withdrawn /docs/memories entry, so Back skips it", async () => {
  const { forceDocsTab } = await import("./docs.js");
  history.replaceState(null, "", "/chat/c1");
  history.pushState(null, "", "/docs/memories");
  const entries = history.length;

  forceDocsTab("memories");

  expect(location.pathname).toBe("/docs");
  expect(history.length).toBe(entries);
  const back = new Promise((resolve) => {
    window.addEventListener("popstate", resolve, { once: true });
  });
  history.back();
  await back;
  expect(location.pathname).toBe("/chat/c1");
});

it("leaves the current page's entry alone when the withdrawn route came from elsewhere", async () => {
  const { forceDocsTab } = await import("./docs.js");
  history.replaceState(null, "", "/chat/c2");

  expect(forceDocsTab("memories")).toBe("steering");

  expect(location.pathname).toBe("/chat/c2");
});
