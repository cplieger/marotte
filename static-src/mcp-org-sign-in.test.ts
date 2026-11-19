// The organization sign-in card: each kiro-cli state, the controls each offers, and the sign-in page while one runs.
import { afterEach, describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => ({ signIn: 0, signOut: 0, saved: [] as unknown[] }));
vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    registerCleanup:
      (_fn: () => void): (() => void) =>
      () => {
        /* noop: the singleton cleanup registry is process-wide */
      },
  };
});
vi.mock(import("./actions/mcp.js"), async (importOriginal) => {
  const actual = await importOriginal();
  const done = (): Promise<null> => Promise.resolve(null);
  return {
    ...actual,
    orgSignIn: {
      dispatch: () => {
        calls.signIn++;
        return done();
      },
    },
    orgSignOut: {
      dispatch: () => {
        calls.signOut++;
        return done();
      },
    },
    saveIdentityProvider: {
      dispatch: (body: unknown) => {
        calls.saved.push(body);
        return done();
      },
    },
  } as unknown as typeof actual;
});

import { finishByText, mountOrgCard, orgStateText, orgView } from "./mcp-org-sign-in.js";
import { identityProvider, orgSignInStatus } from "./mcp-state.js";
import type { MCPIdentityProviderStatus } from "./wire/types.gen.js";

const saved = {
  loaded: true,
  unreadable: false,
  provider: { issuer: "https://idp.example", client_id: "k", name: "Acme" },
};

let stops: (() => void)[] = [];
afterEach(() => {
  for (const s of stops) {
    s();
  }
  stops = [];
  document.body.replaceChildren();
  identityProvider.value = { loaded: false, provider: null, unreadable: false };
  orgSignInStatus.value = undefined;
  calls.signIn = 0;
  calls.signOut = 0;
  calls.saved = [];
});

function mount(locked = false): HTMLElement {
  const { card, stop } = mountOrgCard({
    pill: (url) =>
      Object.assign(document.createElement("a"), { href: url, textContent: "Open sign-in page" }),
    relay: () => document.createElement("details"),
    locked: () => locked,
  });
  stops.push(stop);
  document.body.appendChild(card);
  return card;
}

const buttons = (card: HTMLElement): string[] =>
  [...card.querySelectorAll<HTMLButtonElement>(".mcp-row-actions button")]
    .filter((b) => !b.hidden && b.closest("[hidden]") === null)
    .map((b) => b.getAttribute("aria-label") ?? b.textContent ?? "");

describe("orgView", () => {
  const st = (s: MCPIdentityProviderStatus): MCPIdentityProviderStatus => s;
  it.each([
    ["nothing reported", undefined, "signed_out"],
    ["signed in", st({ reported: { state: "signed-in", account: "a@acme" } }), "signed_in"],
    ["expired", st({ reported: { state: "session-expired" } }), "expired"],
    [
      "invalid",
      st({ reported: { state: "invalid-config", error_message: "bad issuer" } }),
      "invalid",
    ],
    ["an unknown state", st({ reported: { state: "paused" } }), "signed_out"],
    [
      "a refusal",
      st({ error: "No identity provider", reported: { state: "signed-out" } }),
      "failed",
    ],
    [
      "a standing sign-in outranks a refusal",
      st({ error: "x", sign_in_deadline: "2026-11-18T11:42:00Z" }),
      "signing_in",
    ],
  ])("%s", (_label, status, kind) => {
    expect(orgView(saved, status).kind).toBe(kind);
  });

  it("is not set up until a provider is saved, and loading before the read answers", () => {
    expect(orgView({ loaded: true, provider: null, unreadable: false }, undefined).kind).toBe(
      "not_set_up",
    );
    expect(orgView({ loaded: false, provider: null, unreadable: false }, undefined).kind).toBe(
      "loading",
    );
  });
});

describe("the state line", () => {
  it("names the provider and the account", () => {
    expect(orgStateText({ kind: "signed_in", account: "a@acme" }, "Acme")).toBe(
      "Signed in to Acme as a@acme",
    );
    expect(orgStateText({ kind: "expired" }, "Acme")).toBe("Your Acme sign-in expired");
    expect(orgStateText({ kind: "invalid", message: "bad issuer" }, "Acme")).toBe(
      "The setting is invalid. bad issuer",
    );
  });

  it("gives the deadline as a clock time", () => {
    const at = new Date(2026, 10, 18, 11, 42);
    expect(finishByText(at)).toBe(
      `Finish by ${at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`,
    );
  });
});

describe("the card", () => {
  it("shows only the form while nothing is set up", () => {
    identityProvider.value = { loaded: true, provider: null, unreadable: false };
    const card = mount();
    expect(card.querySelector("form")?.hidden).toBe(false);
    expect(buttons(card)).toEqual([]);
    expect(
      card.querySelector<HTMLButtonElement>('[aria-label="Edit organization sign-in"]')?.hidden,
    ).toBe(true);
  });

  it("offers Sign in when signed out, and pressing it starts the sign-in", () => {
    identityProvider.value = saved;
    const card = mount();
    expect(card.querySelector("form")?.hidden).toBe(true);
    expect(buttons(card)).toEqual(["Sign in", "Edit organization sign-in"]);
    expect(card.querySelector(".mcp-dot")?.getAttribute("aria-label")).toBe(
      "Organization sign-in: Signed out of Acme",
    );
    card.querySelector<HTMLButtonElement>(".mcp-sign-in-btn")?.click();
    expect(calls.signIn).toBe(1);
  });

  it("while signing in shows the page, the paste box, the finish time and Cancel", () => {
    identityProvider.value = saved;
    orgSignInStatus.value = {
      reported: { state: "signing-in" },
      sign_in_deadline: "2026-11-18T11:42:00Z",
      sign_in_url: "https://idp.example/authorize?state=o",
    };
    const card = mount();
    expect(card.querySelector("a")?.getAttribute("href")).toBe(
      "https://idp.example/authorize?state=o",
    );
    expect(card.querySelector("details")).not.toBeNull();
    expect(card.querySelector(".mcp-sign-in-hint")?.textContent).toMatch(/^Finish by /);
    expect(buttons(card)).toEqual(["Cancel", "Edit organization sign-in"]);
    card.querySelector<HTMLButtonElement>(".mcp-sign-in-btn")?.click();
    expect(calls.signOut).toBe(1);

    orgSignInStatus.value = { reported: { state: "signed-in", account: "a@acme" } };
    expect(card.querySelector("a")).toBeNull();
    expect(card.querySelector("details")).toBeNull();
    expect(buttons(card)).toEqual(["Sign out", "Edit organization sign-in"]);
  });

  it("drops the paste box once the code was relayed", () => {
    identityProvider.value = saved;
    orgSignInStatus.value = {
      sign_in_deadline: "2026-11-18T11:42:00Z",
      sign_in_url: "https://idp.example/a",
      relayed: true,
    };
    expect(mount().querySelector("details")).toBeNull();
  });

  it("is hidden while the organization turns MCP off", () => {
    identityProvider.value = saved;
    expect(mount(true).hidden).toBe(true);
  });

  it("saves the form's provider without its empty optional fields", () => {
    identityProvider.value = { loaded: true, provider: null, unreadable: false };
    const card = mount();
    const inputs = [...card.querySelectorAll<HTMLInputElement>("form input")];
    const [issuer, clientID] = inputs;
    if (issuer === undefined || clientID === undefined) {
      throw new Error("form inputs missing");
    }
    issuer.value = " https://idp.example ";
    clientID.value = "k";
    card.querySelector("form")?.requestSubmit();
    expect(calls.saved).toEqual([{ issuer: "https://idp.example", client_id: "k" }]);
  });
});
