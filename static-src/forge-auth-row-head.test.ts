// A connection row's top line on the Sources tab keeps the account it names readable
// when its actions take most of a phone's column. A geometry property, so the row is
// built by `renderForgesPanel` and measured under the app's own stylesheet.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const H = vi.hoisted(() => ({ apiGetTyped: vi.fn() }));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: vi.fn(),
  apiGetTyped: H.apiGetTyped,
  apiPost: vi.fn(() => Promise.resolve(null)),
}));
vi.mock("./toast.js", async (importOriginal) => {
  const { toastMock } = await import("./__test-helpers__/toast-mock.js");
  return { ...(await importOriginal<Record<string, unknown>>()), ...toastMock() };
});
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  confirm: vi.fn(() => Promise.resolve(true)),
}));
vi.mock("./forge-store.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>();
  const { apiGetTyped } = await import("./api-client.js");
  const read = (): unknown => apiGetTyped("/api/forges", (v: unknown) => v);
  return { ...orig, refreshForges: read, ensureForges: read };
});

import { resetActionFramework } from "@cplieger/actions/testing";
import { renderForgesPanel } from "./forge-auth.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const ACCOUNT = {
  id: "gitea:git.example.com",
  kind: "gitea",
  host: "git.example.com",
  username: "someone",
  email: "someone@example.invalid",
  connected: true,
  reconnect_required: false,
};

let style: HTMLStyleElement;

beforeEach(() => {
  resetActionFramework();
  H.apiGetTyped.mockImplementation((url: string, decode: (v: unknown) => unknown) => {
    switch (url) {
      case "/api/forges":
        return Promise.resolve(decode({ forges: [ACCOUNT], kinds: ["github", "gitea"] }));
      default:
        return Promise.resolve(decode({ repos: [] }));
    }
  });
  // The Sources column of a 390px phone, in the viewport so every rect is real.
  document.body.innerHTML = `<div id="forges-panel" style="position:fixed;top:0;left:0;inline-size:366px"></div>`;
  style = mountAppCSS();
});

afterEach(() => {
  style.remove();
  delete document.documentElement.dataset["pointer"];
});

describe("a connection row's top line on a phone's column", () => {
  it.each(["fine", "coarse"] as const)(
    "shows the whole account beside its actions at %s",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      await renderForgesPanel({ revalidate: false });
      const top = document.querySelector<HTMLElement>(".forge-account-row-top")!;
      const primary = top.querySelector<HTMLElement>(".forge-account-primary")!;
      const meta = top.querySelector<HTMLElement>(".forge-account-meta")!;

      expect(primary.textContent).toBe("someone@example.invalid");
      expect(primary.scrollWidth, `${tier}: the account is not cut`).toBeLessThanOrEqual(
        primary.clientWidth,
      );
      expect(meta.scrollWidth, `${tier}: its user and host are not cut`).toBeLessThanOrEqual(
        meta.clientWidth,
      );
      expect(top.scrollWidth, `${tier}: the actions stay inside the row`).toBeLessThanOrEqual(
        top.clientWidth,
      );
    },
  );
});
