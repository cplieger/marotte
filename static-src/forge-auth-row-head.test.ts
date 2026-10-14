// The account a row names stays readable when its actions take most of a phone's column. A geometry property, so
// the row is built by `renderForgesPanel` and measured under the app's stylesheet.

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
// The rows read is routed through the mocked client, so each case answers the forge list first.
vi.mock("./actions/forge-list.js", async (importOriginal) => {
  const { apiGetTyped } = await import("./api-client.js");
  return {
    ...(await importOriginal<Record<string, unknown>>()),
    listForges: {
      dispatch: () =>
        Promise.resolve(apiGetTyped("/api/forges", (v: unknown) => v)).then((v) => v ?? null),
    },
  };
});

import { resetActionFramework } from "@cplieger/actions/testing";
import { renderForgesPanel } from "./forge-auth.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { _resetForTest as resetForgeStore } from "./forge-store.js";

const ACCOUNT = {
  id: "gitea:git.example.com",
  kind: "gitea",
  host: "git.example.com",
  username: "someone",
  email: "someone@example.invalid",
  account: "acct-1",
  status: "connected",
};

let style: HTMLStyleElement;

beforeEach(() => {
  resetForgeStore();
  resetActionFramework();
  H.apiGetTyped.mockImplementation((url: string, decode: (v: unknown) => unknown) => {
    switch (url) {
      case "/api/forges":
        return Promise.resolve(decode({ forges: [ACCOUNT], kinds: ["github", "gitea"] }));
      default:
        return Promise.resolve(decode({ account: "acct-1", repos: [] }));
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
      await renderForgesPanel();
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
