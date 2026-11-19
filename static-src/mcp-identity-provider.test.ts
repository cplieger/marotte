// The Organization sign-in card renders kiro-cli's organization sign-in from GET /api/mcp/identity-provider, and its
// actions reach the routes internal/mcp/http.go and internal/agent/mcp_identity_provider.go serve.
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { setupWorker } from "msw/browser";

// The panels' graph reaches the tools installer; no case installs anything.
vi.mock("./actions/tools.js", () => ({ getToolsStatus: { dispatch: async () => null } }));
vi.mock("./tools.js", () => ({ installToolAndWait: async () => ({ ok: true }) }));

import { mountIdentityProviderCard, type SignInSlot } from "./mcp-identity-provider.js";
import { mcpState } from "./mcp-state.js";
import type { IdentityProvider, MCPIdentityProviderPayload } from "./wire/types.gen.js";

const ACME: IdentityProvider = { issuer: "https://acme.okta.com", client_id: "kiro-cli", name: "Acme" };
const ISSUER_STATUS = { issuer: ACME.issuer, display_name: "Acme" };
const CONSENT = "https://acme.okta.com/oauth2/v1/authorize?state=st-1";

let answer: { config: IdentityProvider | null; status: MCPIdentityProviderPayload } = {
  config: null,
  status: { known: true },
};
// The server's `mcp_identity_provider` counter: the status mirror is module state that outlives a case.
let version = 0;
const posted: string[] = [];
const puts: unknown[] = [];

const worker = setupWorker(
  http.get("/api/mcp/identity-provider", () => {
    version++;
    return HttpResponse.json({
      ...answer,
      subject: { kind: "mcp_identity_provider", ref: "", version: String(version) },
    });
  }),
  http.post("/api/mcp/identity-provider/sign-in", () => {
    posted.push("sign-in");
    return new HttpResponse(null, { status: 202 });
  }),
  http.post("/api/mcp/identity-provider/sign-out", () => {
    posted.push("sign-out");
    return new HttpResponse(null, { status: 204 });
  }),
  http.put("/api/mcp/identity-provider", async ({ request }) => {
    const body = await request.json();
    puts.push(body);
    return HttpResponse.json(body);
  }),
);

beforeAll(async () => {
  await worker.start({ quiet: true, onUnhandledFrame: "error" });
});

let stop: (() => void) | undefined;
afterEach(() => {
  stop?.();
  stop = undefined;
  worker.resetHandlers();
  posted.length = 0;
  puts.length = 0;
  document.body.replaceChildren();
});

afterAll(() => {
  worker.stop();
});

const slot: SignInSlot = {
  link: (url) => {
    const a = document.createElement("a");
    a.className = "slot-link";
    a.href = url;
    return a;
  },
  paste: () => {
    const d = document.createElement("details");
    d.className = "slot-paste";
    return d;
  },
};

async function card(
  config: IdentityProvider | null,
  status: MCPIdentityProviderPayload,
): Promise<HTMLElement> {
  answer = { config, status };
  await mcpState.refetchIdentityProvider();
  const host = document.createElement("section");
  document.body.append(host);
  stop = mountIdentityProviderCard(host, slot);
  return host;
}

const text = (host: HTMLElement, cls: string): string =>
  host.querySelector(`.${cls}`)?.textContent ?? "";

/** The labels of the actions a reader can press, in order. */
function actions(host: HTMLElement): string[] {
  const row = host.querySelector<HTMLElement>(":scope > .mcp-idp-actions");
  return [...(row?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
    .filter((b) => !b.hidden)
    .map((b) => b.textContent);
}

function press(host: HTMLElement, label: string): void {
  const row = host.querySelector<HTMLElement>(":scope > .mcp-idp-actions");
  const btn = [...(row?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
    (b) => !b.hidden && b.textContent === label,
  );
  expect(btn, `no visible ${label}`).toBeDefined();
  btn?.click();
}

async function until(check: () => boolean): Promise<void> {
  await expect.poll(check, { timeout: 2000 }).toBe(true);
}

describe("the Organization sign-in card", () => {
  it("offers only Set up while no provider is set up", async () => {
    const host = await card(null, { known: true });
    expect(text(host, "mcp-idp-state")).toBe("Not set up");
    expect(actions(host)).toEqual(["Set up"]);
  });

  it("names the provider and offers Sign in while not signed in", async () => {
    const host = await card(ACME, { known: true, status: { state: "signed-out", ...ISSUER_STATUS } });
    expect(text(host, "mcp-idp-identity")).toBe("Acme · https://acme.okta.com");
    expect(text(host, "mcp-idp-state")).toBe("Not signed in");
    expect(actions(host)).toEqual(["Sign in", "Edit", "Remove"]);
  });

  it("says why kiro-cli ended the last sign-in", async () => {
    const host = await card(ACME, {
      known: true,
      status: { state: "signed-out", ...ISSUER_STATUS },
      sign_in_error: "Organization sign-in was not completed within 300s",
    });
    expect(text(host, "mcp-idp-detail")).toBe(
      "The last sign-in did not complete: Organization sign-in was not completed within 300s",
    );
  });

  it("is signing in from the moment the user's Sign in stands, before kiro-cli reports it", async () => {
    const host = await card(ACME, {
      known: true,
      signing_in: true,
      status: { state: "signed-out", ...ISSUER_STATUS },
    });
    expect(text(host, "mcp-idp-state")).toBe("Signing in");
    expect(text(host, "mcp-idp-detail")).toBe("Waiting for kiro-cli to open the sign-in page.");
    expect(actions(host)).toEqual(["Cancel"]);
  });

  it("shows the consent link and the paste box while signing in, with Cancel only", async () => {
    const host = await card(ACME, {
      known: true,
      signing_in: true,
      oauth_url: CONSENT,
      status: { state: "signing-in", ...ISSUER_STATUS },
    });
    expect(text(host, "mcp-idp-state")).toBe("Signing in");
    expect(host.querySelector<HTMLAnchorElement>(".slot-link")?.href).toBe(CONSENT);
    expect(host.querySelector(".slot-paste")).not.toBeNull();
    expect(actions(host)).toEqual(["Cancel"]);
  });

  it("drops the paste box once its callback was relayed, keeping the link", async () => {
    const host = await card(ACME, {
      known: true,
      signing_in: true,
      oauth_url: CONSENT,
      relayed: true,
      status: { state: "signing-in", ...ISSUER_STATUS },
    });
    expect(host.querySelector(".slot-link")).not.toBeNull();
    expect(host.querySelector(".slot-paste")).toBeNull();
  });

  it("names the account once signed in, with Sign out", async () => {
    const host = await card(ACME, {
      known: true,
      status: { state: "signed-in", ...ISSUER_STATUS, account: "ana@acme.example" },
    });
    expect(text(host, "mcp-idp-state")).toBe("Signed in as ana@acme.example");
    expect(actions(host)).toEqual(["Sign out", "Edit", "Remove"]);
    expect(host.querySelector(".slot-link")).toBeNull();
  });

  it("offers Sign in again when the session expired", async () => {
    const host = await card(ACME, { known: true, status: { state: "session-expired", ...ISSUER_STATUS } });
    expect(text(host, "mcp-idp-state")).toBe("Sign-in expired");
    expect(actions(host)).toEqual(["Sign in", "Edit", "Remove"]);
  });

  it("shows kiro-cli's configuration error with Edit and Remove only", async () => {
    const host = await card(ACME, {
      known: true,
      status: { state: "invalid-config", error: "issuer must use https" },
    });
    expect(text(host, "mcp-idp-state")).toBe("Configuration error");
    expect(text(host, "mcp-idp-detail")).toBe("issuer must use https");
    expect(actions(host)).toEqual(["Edit", "Remove"]);
  });

  it("follows a newer status frame", async () => {
    const host = await card(ACME, { known: true, status: { state: "signed-out", ...ISSUER_STATUS } });
    version++;
    mcpState.applyIdentityProviderFrame(
      { known: true, status: { state: "signed-in", ...ISSUER_STATUS } },
      { kind: "mcp_identity_provider", ref: "", version: String(version) },
    );
    await until(() => text(host, "mcp-idp-state") === "Signed in");
  });
});

describe("the card's actions", () => {
  it("Sign in posts the organization sign-in", async () => {
    const host = await card(ACME, { known: true, status: { state: "signed-out", ...ISSUER_STATUS } });
    press(host, "Sign in");
    await until(() => posted.length > 0);
    expect(posted).toEqual(["sign-in"]);
  });

  it("Cancel posts the sign-out, which ends the sign-in in progress", async () => {
    const host = await card(ACME, {
      known: true,
      signing_in: true,
      status: { state: "signing-in", ...ISSUER_STATUS },
    });
    press(host, "Cancel");
    await until(() => posted.length > 0);
    expect(posted).toEqual(["sign-out"]);
  });

  it("Sign out posts the sign-out", async () => {
    const host = await card(ACME, { known: true, status: { state: "signed-in", ...ISSUER_STATUS } });
    press(host, "Sign out");
    await until(() => posted.length > 0);
    expect(posted).toEqual(["sign-out"]);
  });

  it("Edit fills the form from the record and Save puts what was typed, leaving out empty optional fields", async () => {
    const host = await card(
      { issuer: ACME.issuer, client_id: ACME.client_id },
      { known: true, status: { state: "signed-out", ...ISSUER_STATUS } },
    );
    press(host, "Edit");
    const form = host.querySelector<HTMLFormElement>(".mcp-idp-form");
    expect(form?.hidden).toBe(false);
    const issuer = host.querySelector<HTMLInputElement>("#mcp-idp-issuer");
    expect(issuer?.value).toBe(ACME.issuer);
    const name = host.querySelector<HTMLInputElement>("#mcp-idp-name");
    if (name !== null) {
      name.value = "  Acme  ";
    }
    form?.requestSubmit();
    await until(() => puts.length > 0);
    expect(puts).toEqual([{ issuer: ACME.issuer, client_id: ACME.client_id, name: "Acme" }]);
    await until(() => form?.hidden === true);
  });

  it("marks the field a refused save names", async () => {
    worker.use(
      http.put("/api/mcp/identity-provider", () =>
        HttpResponse.json(
          {
            error: "issuer: must be https",
            fields: [{ field: "identity_provider.issuer", message: "issuer: must be https" }],
          },
          { status: 400 },
        ),
      ),
    );
    const host = await card(null, { known: true });
    press(host, "Set up");
    host.querySelector<HTMLFormElement>(".mcp-idp-form")?.requestSubmit();
    const issuer = host.querySelector<HTMLInputElement>("#mcp-idp-issuer");
    await until(() => issuer?.getAttribute("aria-invalid") === "true");
    expect(host.querySelector(".mcp-form-error")?.textContent).toBe("issuer: must be https");
  });
});
