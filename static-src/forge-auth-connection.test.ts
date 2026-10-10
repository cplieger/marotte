import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiPost: vi.fn(() => Promise.resolve(null)),
  CancellableSlot: class {
    start() {
      return new AbortController().signal;
    }
    abort() {
      /* mock no-op */
    }
  },
  withTimeout: vi.fn(
    (signal: AbortSignal | undefined, _ms: number) => signal ?? AbortSignal.timeout(30000),
  ),
  API_TIMEOUT_MS: 30000,
}));

vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  confirm: vi.fn(() => Promise.resolve(true)),
}));

// Routed through the mocked client so each case answers the forge list first with mockResolvedValueOnce.
vi.mock("./forge-store.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>();
  const { apiGetTyped } = await import("./api-client.js");
  const forgeRead = (): unknown => apiGetTyped("/api/forges", (v: unknown) => v);
  return {
    ...orig,
    refreshForges: forgeRead,
    ensureForges: forgeRead,
    initForgeStore: vi.fn(),
    oauthByKind: vi.fn(() => ({})),
    forgeLoadFailed: vi.fn(() => false),
  };
});

import { resetActionFramework } from "@cplieger/actions/testing";
import { renderForgesPanel } from "./forge-auth.js";
import { abortPoll } from "./forge-auth-oauth.js";
import { apiGetTyped } from "./api-client.js";
import { outcomesOf } from "./__test-helpers__/press-feedback.js";

const mockedApiGet = vi.mocked(apiGetTyped);

function setupDOM(): void {
  document.body.innerHTML = `<div id="forges-panel"></div>`;
}

function panel(): HTMLElement {
  return document.getElementById("forges-panel")!;
}

describe("forge-auth: the connection row", () => {
  const KINDS = ["github", "gitlab", "codeberg", "gitea"];

  beforeEach(() => {
    setupDOM();
    vi.clearAllMocks();
    resetActionFramework();
  });

  afterEach(() => {
    abortPoll();
    vi.unstubAllGlobals();
  });

  function account(over: Record<string, unknown> = {}): Record<string, unknown> {
    return {
      id: "github:github.com",
      kind: "github",
      host: "github.com",
      username: "alice",
      connected: true,
      reconnect_required: false,
      ...over,
    };
  }

  async function showRow(a: Record<string, unknown>): Promise<HTMLElement> {
    mockedApiGet.mockResolvedValueOnce({ forges: [a], kinds: KINDS, oauth: { github: true } });
    await renderForgesPanel({ revalidate: false });
    return panel().querySelector<HTMLElement>(
      `.forge-account-row[data-id='${a["id"] as string}']`,
    )!;
  }

  function queuedFetch(): {
    spy: ReturnType<typeof vi.fn<typeof fetch>>;
    answer: (call: number, r: Response) => void;
  } {
    const answers: ((r: Response) => void)[] = [];
    const spy = vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((r) => {
          answers.push(r);
        }),
    );
    vi.stubGlobal("fetch", spy);
    return { spy, answer: (call, r) => answers[call]!(r) };
  }

  function json(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
  }

  function bodyOf(spy: ReturnType<typeof vi.fn<typeof fetch>>, call: number): unknown {
    return JSON.parse(spy.mock.calls[call]![1]!.body as string);
  }

  function button(root: HTMLElement, label: string): HTMLButtonElement | undefined {
    return [...root.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent.includes(label),
    );
  }

  it("names the remedy on a reconnect_required row, and Reconnect opens that host's add pane", async () => {
    const row = await showRow(
      account({
        id: "gitlab:git.example.com",
        kind: "gitlab",
        host: "git.example.com",
        connected: false,
        reconnect_required: true,
        error_code: "reconnect_required",
        last_error:
          "the stored credential can be neither used nor renewed. Sign in to this forge again",
      }),
    );
    expect(row.textContent).toContain("needs a new sign-in");
    expect(button(row, "Check")).toBeUndefined();

    button(row, "Reconnect")!.click();

    const slot = panel().querySelector<HTMLElement>(
      ".forge-kind-section[data-kind='gitlab'] [data-forge-slot]",
    )!;
    expect(slot.querySelector<HTMLInputElement>("[data-forge-host]")?.value).toBe(
      "git.example.com",
    );
    expect(slot.querySelector("form.forge-pat-form")).not.toBeNull();
  });

  it("offers a reconnect the kind's device sign-in where it has one", async () => {
    const row = await showRow(account({ connected: false, reconnect_required: true }));

    button(row, "Reconnect")!.click();

    const slot = panel().querySelector<HTMLElement>(
      ".forge-kind-section[data-kind='github'] [data-forge-slot]",
    )!;
    expect(slot.querySelector("[data-forge-device-start]")?.textContent).toBe(
      "Sign in with GitHub",
    );
  });

  it("shows a degraded store's reason with no control that cannot succeed", async () => {
    const row = await showRow(
      account({
        connected: false,
        error_code: "connection_unusable",
        last_error: "the forge credential store /config/forge-store has mode 0750; it needs 0700",
      }),
    );
    expect(row.querySelector(".forge-account-error")?.textContent).toBe(
      "the forge credential store /config/forge-store has mode 0750; it needs 0700",
    );
    for (const label of ["Check", "Reconnect", "Sign out"]) {
      expect(button(row, label), label).toBeUndefined();
    }
    expect(row.querySelector(".forge-account-owners")).toBeNull();
  });

  it("names what the token lacks when a check answers scope_insufficient", async () => {
    const row = await showRow(account());
    const { answer } = queuedFetch();

    button(row, "Check")!.click();
    answer(
      0,
      json({
        connected: false,
        error: "Whoami: scope_insufficient (status 403): [User: Read] Access denied",
        forge: account({
          connected: false,
          last_error: "Whoami: scope_insufficient (status 403): [User: Read] Access denied",
          error_code: "scope_insufficient",
          error_kind: "forbidden",
        }),
      }),
    );

    await vi.waitFor(() => {
      const error = row.querySelector(".forge-account-error")?.textContent ?? "";
      expect(error).toContain("missing a permission");
      expect(error).toContain("[User: Read]");
    });
  });

  it("names the wait in seconds when a check answers rate_limited", async () => {
    const row = await showRow(account());
    const { answer } = queuedFetch();

    button(row, "Check")!.click();
    answer(
      0,
      json({
        connected: true,
        error: "Whoami (status 429)",
        forge: account({
          last_error: "Whoami (status 429)",
          error_kind: "rate_limited",
          retry_after_s: 42,
          last_probed: Date.now(),
        }),
      }),
    );

    await vi.waitFor(() => {
      expect(row.querySelector(".forge-account-error")?.textContent).toContain(
        "Try again in 42 seconds",
      );
    });
  });

  it("keeps a connected row with a temporary failure usable, and says it runs again", async () => {
    const reason = 'Whoami: SSRF dial: all 1 IPs for "api.github.com" failed: no route to host';
    const row = await showRow(account({ last_error: reason, error_kind: "transient" }));

    expect(row.querySelector(".forge-account-error")?.textContent).toBe(
      `The last check hit a temporary problem and runs again automatically. ${reason}`,
    );
    expect(row.classList.contains("forge-account-row-error")).toBe(false);
    expect(button(row, "Check")).toBeDefined();
    expect(button(row, "Reconnect")).toBeUndefined();
  });

  it("reports a check that met a temporary failure as failed, with the row saying why", async () => {
    const row = await showRow(account());
    const { answer } = queuedFetch();
    const check = button(row, "Check")!;
    const outcomes = outcomesOf(check);

    check.click();
    answer(
      0,
      json({
        connected: true,
        error: "Whoami: no route to host",
        forge: account({ last_error: "Whoami: no route to host", error_kind: "transient" }),
      }),
    );

    await vi.waitFor(() => expect(outcomes).toContain("error"));
    expect(row.querySelector(".forge-account-error")?.textContent).toContain("temporary problem");
  });

  it("keeps the server's sentence for any other code", async () => {
    const row = await showRow(
      account({
        connected: false,
        last_error: "Whoami: anonymous_refused: this connection has no credential",
        error_code: "anonymous_refused",
      }),
    );
    expect(row.querySelector(".forge-account-error")?.textContent).toBe(
      "Whoami: anonymous_refused: this connection has no credential",
    );
  });

  it("shows Check busy from the press until the answer lands, then clears the row's error", async () => {
    const row = await showRow(
      account({ connected: false, last_error: "Whoami (status 502): bad gateway" }),
    );
    const { spy, answer } = queuedFetch();
    const check = button(row, "Check")!;

    check.click();

    expect(check.disabled).toBe(true);
    expect(check.getAttribute("aria-busy")).toBe("true");
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    expect(spy.mock.calls[0]![0]).toBe("/api/forges/github%3Agithub.com/probe");
    answer(0, json({ connected: true, forge: account() }));
    await vi.waitFor(() => expect(row.querySelector(".forge-account-error")).toBeNull());
    await vi.waitFor(() => expect(check.disabled).toBe(false), { timeout: 3000 });
  });

  it("shows Sign out busy from the confirm until the delete lands, and names a refusal in the row", async () => {
    const row = await showRow(account());
    const { spy, answer } = queuedFetch();
    const out = button(row, "Sign out")!;

    out.click();
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));

    expect(out.disabled).toBe(true);
    expect(out.getAttribute("aria-busy")).toBe("true");
    expect(row.hidden).toBe(false);
    answer(0, json({ error: "the forge credential store cannot be used" }, 503));
    await vi.waitFor(() => {
      expect(row.querySelector(".forge-account-error")?.textContent).toBe(
        "Could not sign out. the forge credential store cannot be used",
      );
    });
    expect(row.isConnected && !row.hidden).toBe(true);
  });

  describe("owner scopes", () => {
    function owners(row: HTMLElement): HTMLElement {
      const d = row.querySelector<HTMLDetailsElement>("details.forge-account-owners")!;
      d.open = true;
      return d;
    }

    function chips(block: HTMLElement): string[] {
      return [...block.querySelectorAll(".chip .chip-label")].map((c) => c.textContent);
    }

    function addOwner(block: HTMLElement, owner: string): void {
      block.querySelector<HTMLInputElement>("form input")!.value = owner;
      block.querySelector<HTMLFormElement>("form")!.requestSubmit();
    }

    it("lists the stored owners", async () => {
      const block = owners(await showRow(account({ owner_scopes: ["acme", "beta"] })));
      expect(chips(block)).toEqual(["acme", "beta"]);
      expect(block.querySelector("summary")?.textContent).toContain("2 other owners");
    });

    it("sends the whole list on an add and renders the new chip on the answer", async () => {
      const block = owners(await showRow(account({ owner_scopes: ["acme"] })));
      const { spy, answer } = queuedFetch();

      addOwner(block, "beta");
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));

      expect(spy.mock.calls[0]![0]).toBe("/api/forges/github%3Agithub.com/owners");
      expect(spy.mock.calls[0]![1]!.method).toBe("PUT");
      expect(bodyOf(spy, 0)).toEqual({ owners: ["acme", "beta"] });
      answer(0, json({ owners: ["acme", "beta"] }));
      await vi.waitFor(() => expect(chips(block)).toEqual(["acme", "beta"]));
      expect(block.querySelector<HTMLInputElement>("form input")!.value).toBe("");
    });

    it("keeps the typed owner and shows the refusal beside it on a list_owner_invalid 400", async () => {
      const block = owners(await showRow(account({ owner_scopes: ["acme"] })));
      const { spy, answer } = queuedFetch();

      addOwner(block, "a b");
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
      answer(
        0,
        json(
          {
            error: "list_owner_invalid: owners[1]: not an owner name",
            code: "list_owner_invalid",
            kind: "unknown",
          },
          400,
        ),
      );

      await vi.waitFor(() => {
        expect(block.querySelector(".forge-card-status")?.textContent).toContain(
          "list_owner_invalid: owners[1]: not an owner name",
        );
      });
      expect(block.querySelector<HTMLInputElement>("form input")!.value).toBe("a b");
      expect(chips(block)).toEqual(["acme"]);
    });

    it("sends the list without an owner when its chip is removed", async () => {
      const block = owners(await showRow(account({ owner_scopes: ["acme", "beta"] })));
      const { spy, answer } = queuedFetch();

      block.querySelector<HTMLButtonElement>(".chip-remove")!.click();
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));

      expect(bodyOf(spy, 0)).toEqual({ owners: ["beta"] });
      answer(0, json({ owners: ["beta"] }));
      await vi.waitFor(() => expect(chips(block)).toEqual(["beta"]));
    });

    it("sends a second write after the first lands, from the list the first stored", async () => {
      const block = owners(await showRow(account({ owner_scopes: ["acme"] })));
      const { spy, answer } = queuedFetch();

      addOwner(block, "beta");
      block.querySelector<HTMLButtonElement>(".chip-remove")!.click();
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
      answer(0, json({ owners: ["acme", "beta"] }));

      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
      expect(bodyOf(spy, 1)).toEqual({ owners: ["beta"] });
    });

    it("asks for an owner and sends nothing when the field is empty", async () => {
      const block = owners(await showRow(account({ owner_scopes: ["acme"] })));
      const { spy } = queuedFetch();

      addOwner(block, "  ");
      await new Promise((r) => setTimeout(r, 0));

      expect(spy).not.toHaveBeenCalled();
      expect(block.querySelector(".forge-card-status")?.textContent).toBe("Name an owner first.");
    });

    it("shows Add busy from the press", async () => {
      const block = owners(await showRow(account()));
      queuedFetch();
      const add = button(block, "Add")!;

      addOwner(block, "beta");

      expect(add.disabled).toBe(true);
      expect(add.getAttribute("aria-busy")).toBe("true");
    });

    it("says on GitLab that an owner is a group", async () => {
      const block = owners(
        await showRow(account({ id: "gitlab:gitlab.com", kind: "gitlab", host: "gitlab.com" })),
      );
      expect(block.textContent).toContain("an owner is a group");
    });
  });
});
