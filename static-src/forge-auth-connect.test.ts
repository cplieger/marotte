// The forge connect dialog: each kind's credential paths and their outcomes.

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

// The forge list is the shared store's; routed through the mocked client so each
// case answers it with mockResolvedValueOnce, the forge list first.
vi.mock("./forge-store.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>();
  const { apiGetTyped } = await import("./api-client.js");
  const forgeRead = (): unknown => apiGetTyped("/api/forges", (v: unknown) => v);
  return {
    ...orig,
    refreshForges: forgeRead,
    ensureForges: forgeRead,
    initForgeStore: vi.fn(),
    onForgeChange: vi.fn(() => () => undefined),
    currentForges: vi.fn(() => []),
    oauthByKind: vi.fn(() => ({})),
    forgeLoadFailed: vi.fn(() => false),
  };
});

import { resetActionFramework } from "@cplieger/actions/testing";
import { renderForgesPanel } from "./forge-auth.js";
import { abortPoll } from "./forge-auth-oauth.js";
import { apiGetTyped } from "./api-client.js";

const mockedApiGet = vi.mocked(apiGetTyped);

function setupDOM(): void {
  document.body.innerHTML = `<div id="forges-panel"></div>`;
}

function panel(): HTMLElement {
  return document.getElementById("forges-panel")!;
}

describe("forge-auth: the connect dialog", () => {
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

  async function openPane(kind: string, oauth: Record<string, boolean> = {}): Promise<HTMLElement> {
    mockedApiGet.mockResolvedValueOnce({ forges: [], kinds: KINDS, oauth });
    await renderForgesPanel({ revalidate: false });
    panel()
      .querySelector<HTMLButtonElement>(
        `.forge-kind-section[data-kind='${kind}'] [data-forge-add]`,
      )!
      .click();
    return panel().querySelector<HTMLElement>(
      `.forge-kind-section[data-kind='${kind}'] [data-forge-slot]`,
    )!;
  }

  /** A fetch whose answer the test hands over, so a pending state is observable. */
  function deferredFetch(): {
    spy: ReturnType<typeof vi.fn<typeof fetch>>;
    answer: (r: Response) => void;
  } {
    let answer!: (r: Response) => void;
    const spy = vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((r) => {
          answer = r;
        }),
    );
    vi.stubGlobal("fetch", spy);
    return { spy, answer: (r) => answer(r) };
  }

  function json(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
  }

  /** A refusal keeps the pane open: a success repaints the whole panel after
   *  the test has ended, and that repaint lands in a later test's panel. */
  const refused = (): Response => json({ error: "refused" });

  function sentBody(spy: ReturnType<typeof vi.fn<typeof fetch>>): unknown {
    return JSON.parse(spy.mock.calls[0]![1]!.body as string);
  }

  function connectButton(slot: HTMLElement): HTMLButtonElement {
    return [...slot.querySelectorAll<HTMLButtonElement>("form.forge-pat-form button")].find((b) =>
      b.textContent.includes("Connect"),
    )!;
  }

  function typeToken(slot: HTMLElement, token: string): void {
    slot.querySelector<HTMLInputElement>("form.forge-pat-form input[type='password']")!.value =
      token;
  }

  function setHost(slot: HTMLElement, host: string): void {
    const input = slot.querySelector<HTMLInputElement>("[data-forge-host]")!;
    input.value = host;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  it("offers GitLab device sign-in when oauth.gitlab is true", async () => {
    const slot = await openPane("gitlab", { github: true, gitlab: true });
    const start = slot.querySelector<HTMLButtonElement>("[data-forge-device-start]");
    expect(start?.textContent).toBe("Sign in with GitLab");
  });

  it("offers GitLab the token path only when oauth.gitlab is false", async () => {
    const slot = await openPane("gitlab", { github: true, gitlab: false });
    expect(slot.querySelector("[data-forge-device-start]")).toBeNull();
    expect(slot.querySelector("form.forge-pat-form")).not.toBeNull();
  });

  it("notes on GitLab's device path that a sign-in lasts two hours and a token outlasts it", async () => {
    const slot = await openPane("gitlab", { gitlab: true });
    expect(slot.textContent).toContain("two hours");
    expect(slot.textContent).toContain("connect with a token instead");
  });

  it("names the organization-restriction remedy on GitHub's device path", async () => {
    const slot = await openPane("github", { github: true });
    expect(slot.textContent).toContain("restricts OAuth apps");
    expect(slot.textContent).toContain("connect with a token instead");
  });

  it.each([
    ["with a device sign-in", "github", { github: true }],
    ["with the token path only", "gitea", {}],
    ["for another server", "other", {}],
  ])("states in the pane %s that SSH remotes are not covered", async (_, kind, oauth) => {
    const slot = await openPane(kind, oauth);
    expect(slot.querySelector(".forge-add-pane-intro")?.textContent).toContain(
      "SSH remotes are not covered",
    );
  });

  it("hides the client id field on the public host", async () => {
    const slot = await openPane("github", { github: true });
    expect(slot.querySelector<HTMLElement>("[data-forge-client-id]")!.hidden).toBe(true);
  });

  it("shows the client id field for another host and sends its value on the start body", async () => {
    const slot = await openPane("github", { github: true });
    setHost(slot, "ghe.example.com");
    const field = slot.querySelector<HTMLElement>("[data-forge-client-id]")!;
    expect(field.hidden).toBe(false);
    field.querySelector<HTMLInputElement>("input")!.value = "Iv1.abc123";
    const { spy, answer } = deferredFetch();

    slot.querySelector<HTMLButtonElement>("[data-forge-device-start]")!.click();
    answer(json({ error: "no such application", code: "client_id_invalid" }, 400));

    await vi.waitFor(() => expect(spy).toHaveBeenCalledOnce());
    expect(spy.mock.calls[0]![0]).toBe("/api/forges/oauth/github/start");
    expect(sentBody(spy)).toEqual({ host: "ghe.example.com", client_id: "Iv1.abc123" });
  });

  it("starts a public-host sign-in with no client id", async () => {
    const slot = await openPane("github", { github: true });
    const { spy, answer } = deferredFetch();

    slot.querySelector<HTMLButtonElement>("[data-forge-device-start]")!.click();
    answer(json({ error: "rate limited", code: "rate_limited" }, 429));

    await vi.waitFor(() => expect(spy).toHaveBeenCalledOnce());
    expect(sentBody(spy)).toEqual({ host: "github.com" });
  });

  it("shows Sign in busy from the press and lands a refused start's reason in place", async () => {
    const slot = await openPane("github", { github: true });
    const { answer } = deferredFetch();
    const start = slot.querySelector<HTMLButtonElement>("[data-forge-device-start]")!;

    start.click();
    expect(start.disabled).toBe(true);
    expect(start.getAttribute("aria-busy")).toBe("true");

    answer(json({ error: "the store cannot be written" }, 503));
    await vi.waitFor(() => {
      expect(start.dataset["asyncStatus"]).toBe("error");
    });
    await vi.waitFor(
      () => {
        expect(start.disabled).toBe(false);
      },
      { timeout: 3000 },
    );
    expect(start.hasAttribute("aria-busy")).toBe(false);
    expect(slot.querySelector(".forge-device-start .forge-card-status")?.textContent).toBe(
      "the store cannot be written",
    );
  });

  it("sends a token connect with no connection field the user did not set", async () => {
    const slot = await openPane("gitlab");
    const { spy, answer } = deferredFetch();
    typeToken(slot, "glpat-x");

    connectButton(slot).click();
    answer(refused());

    await vi.waitFor(() => expect(spy).toHaveBeenCalledOnce());
    expect(spy.mock.calls[0]![0]).toBe("/api/forges/gitlab%3Agitlab.com/login/pat");
    expect(sentBody(spy)).toEqual({ token: "glpat-x" });
  });

  it("sends each connection field the user set on the token connect", async () => {
    const slot = await openPane("gitlab");
    setHost(slot, "gitlab.internal:8080");
    const options = slot.querySelector<HTMLDetailsElement>("details.forge-options")!;
    options.open = true;
    options.querySelector<HTMLInputElement>("[name='proxy']")!.value =
      "http://proxy.example.com:3128";
    options.querySelector<HTMLInputElement>("[name='private_addresses']")!.checked = true;
    options.querySelector<HTMLInputElement>("[name='plaintext_http']")!.checked = true;
    options.querySelector<HTMLTextAreaElement>("[name='ca_pem']")!.value =
      "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n";
    options.querySelector<HTMLTextAreaElement>("[name='client_cert_pem']")!.value = "CERT";
    options.querySelector<HTMLTextAreaElement>("[name='client_key_pem']")!.value = "KEY";
    const { spy, answer } = deferredFetch();
    typeToken(slot, "glpat-x");

    connectButton(slot).click();
    answer(refused());

    await vi.waitFor(() => expect(spy).toHaveBeenCalledOnce());
    expect(spy.mock.calls[0]![0]).toBe("/api/forges/gitlab%3Agitlab.internal%3A8080/login/pat");
    expect(sentBody(spy)).toEqual({
      token: "glpat-x",
      web_base_url: "http://gitlab.internal:8080",
      proxy: "http://proxy.example.com:3128",
      private_addresses: true,
      plaintext_http: true,
      ca_pem: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
      client_cert_pem: "CERT",
      client_key_pem: "KEY",
    });
  });

  it("states beside each opt-in what it lets through", async () => {
    const slot = await openPane("gitea");
    const hintFor = (name: string): string =>
      slot
        .querySelector(`details.forge-options [name='${name}']`)!
        .closest(".forge-option")!
        .querySelector(".tool-form-hint")!.textContent;

    expect(hintFor("private_addresses")).toContain("private or loopback address");
    expect(hintFor("plaintext_http")).toContain("the token travels unencrypted");
    expect(hintFor("proxy")).toContain("applies to the proxy, not to the forge");
  });

  it.each([
    ["github", "repo, read:org, workflow"],
    ["gitlab", "api"],
    ["codeberg", "write:repository, write:issue, read:user"],
    ["gitea", "write:repository, write:issue, read:user"],
  ])("names %s's minimum token scopes beside the token field", async (kind, scopes) => {
    const slot = await openPane(kind);
    expect(slot.querySelector(".forge-scopes")?.textContent).toContain(scopes);
  });

  it("shows Connect busy from the press and re-enables it with the refusal's sentence", async () => {
    const slot = await openPane("gitlab");
    const { answer } = deferredFetch();
    typeToken(slot, "glpat-bad");
    const connect = connectButton(slot);

    connect.click();
    expect(connect.disabled).toBe(true);
    expect(connect.getAttribute("aria-busy")).toBe("true");

    answer(json({ error: "the token was refused", code: "reconnect_required" }));
    await vi.waitFor(() => {
      expect(connect.dataset["asyncStatus"]).toBe("error");
    });
    await vi.waitFor(
      () => {
        expect(connect.disabled).toBe(false);
      },
      { timeout: 3000 },
    );
    expect(connect.hasAttribute("aria-busy")).toBe(false);
    expect(slot.querySelector("form.forge-pat-form .forge-card-status")?.textContent).toBe(
      "the token was refused",
    );
  });

  it("keeps Connect busy through a background repaint of the panel", async () => {
    const slot = await openPane("gitlab");
    const { answer } = deferredFetch();
    typeToken(slot, "glpat-x");
    const connect = connectButton(slot);

    connect.click();
    mockedApiGet.mockResolvedValueOnce({ forges: [], kinds: KINDS, oauth: {} });
    await renderForgesPanel({ revalidate: false });

    expect(connect.isConnected).toBe(true);
    expect(connect.disabled).toBe(true);
    answer(refused());
  });

  describe("Another server", () => {
    /** A fetch answering each call in order when the test hands over its response. */
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

    function bodyOf(spy: ReturnType<typeof vi.fn<typeof fetch>>, call: number): unknown {
      return JSON.parse(spy.mock.calls[call]![1]!.body as string);
    }

    function setOptions(slot: HTMLElement, set: { proxy?: string; plaintext?: boolean }): void {
      const options = slot.querySelector<HTMLDetailsElement>("details.forge-options")!;
      options.open = true;
      options.querySelector<HTMLInputElement>("[name='private_addresses']")!.checked = true;
      if (set.proxy !== undefined) {
        options.querySelector<HTMLInputElement>("[name='proxy']")!.value = set.proxy;
      }
      if (set.plaintext === true) {
        options.querySelector<HTMLInputElement>("[name='plaintext_http']")!.checked = true;
      }
    }

    function statusText(slot: HTMLElement): string {
      return slot.querySelector("form.forge-pat-form .forge-card-status")?.textContent ?? "";
    }

    it("continues a detect answering gitea as that kind's token connect with the same token and options", async () => {
      const slot = await openPane("other");
      setHost(slot, "git.example.com:3000");
      setOptions(slot, { proxy: "http://proxy.example.com:3128" });
      typeToken(slot, "tok-1");
      const { spy, answer } = queuedFetch();

      connectButton(slot).click();
      answer(0, json({ kind: "gitea" }));
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
      answer(1, refused());

      expect(spy.mock.calls[0]![0]).toBe("/api/forges/detect");
      expect(bodyOf(spy, 0)).toEqual({
        token: "tok-1",
        web_base_url: "https://git.example.com:3000",
        proxy: "http://proxy.example.com:3128",
        private_addresses: true,
      });
      expect(spy.mock.calls[1]![0]).toBe("/api/forges/gitea%3Agit.example.com%3A3000/login/pat");
      expect(bodyOf(spy, 1)).toEqual({
        token: "tok-1",
        proxy: "http://proxy.example.com:3128",
        private_addresses: true,
      });
    });

    it("detects over plain HTTP when the plaintext opt-in is set", async () => {
      const slot = await openPane("other");
      setHost(slot, "127.0.0.1:3000");
      setOptions(slot, { plaintext: true });
      typeToken(slot, "tok-1");
      const { spy, answer } = queuedFetch();

      connectButton(slot).click();
      answer(0, json({ error: "refused", code: "reconnect_required" }, 401));

      await vi.waitFor(() => expect(spy).toHaveBeenCalledOnce());
      expect(bodyOf(spy, 0)).toEqual({
        token: "tok-1",
        web_base_url: "http://127.0.0.1:3000",
        plaintext_http: true,
        private_addresses: true,
      });
    });

    it("says no supported forge answered on a family_undetected 422, sends no token connect and keeps the form filled", async () => {
      const slot = await openPane("other");
      setHost(slot, "git.example.com");
      typeToken(slot, "tok-1");
      const { spy, answer } = queuedFetch();
      const connect = connectButton(slot);

      connect.click();
      answer(
        0,
        json(
          {
            error: "forgeapi: no supported family answered",
            code: "family_undetected",
            kind: "not_found",
          },
          422,
        ),
      );

      await vi.waitFor(() =>
        expect(statusText(slot)).toContain("git.example.com did not answer as a supported forge"),
      );
      expect(connect.dataset["asyncStatus"]).toBe("error");
      await vi.waitFor(() => expect(connect.disabled).toBe(false), { timeout: 3000 });
      expect(spy).toHaveBeenCalledOnce();
      expect(slot.querySelector<HTMLInputElement>("[data-forge-host]")!.value).toBe(
        "git.example.com",
      );
      expect(
        slot.querySelector<HTMLInputElement>("form.forge-pat-form input[type='password']")!.value,
      ).toBe("tok-1");
    });

    it("shows another detect refusal in the server's own words", async () => {
      const slot = await openPane("other");
      setHost(slot, "git.example.com");
      typeToken(slot, "tok-1");
      const { spy, answer } = queuedFetch();

      connectButton(slot).click();
      answer(0, json({ error: "the token was refused", code: "reconnect_required" }, 401));

      await vi.waitFor(() => expect(statusText(slot)).toBe("the token was refused"));
      expect(spy).toHaveBeenCalledOnce();
    });

    it("keeps Connect busy from the press until the token connect lands, across both calls", async () => {
      const slot = await openPane("other");
      setHost(slot, "git.example.com");
      typeToken(slot, "tok-1");
      const { spy, answer } = queuedFetch();
      const connect = connectButton(slot);

      connect.click();
      expect(connect.disabled).toBe(true);
      expect(connect.getAttribute("aria-busy")).toBe("true");

      answer(0, json({ kind: "github" }));
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
      expect(connect.disabled).toBe(true);
      expect(connect.getAttribute("aria-busy")).toBe("true");
      expect(statusText(slot)).toBe("GitHub found. Connecting...");

      answer(1, json({ error: "the token was refused", code: "reconnect_required" }));
      await vi.waitFor(() => expect(connect.dataset["asyncStatus"]).toBe("error"));
      await vi.waitFor(() => expect(connect.disabled).toBe(false), { timeout: 3000 });
      expect(connect.hasAttribute("aria-busy")).toBe(false);
      expect(statusText(slot)).toBe("the token was refused");
    });

    it("sends nothing for an empty address", async () => {
      const slot = await openPane("other");
      typeToken(slot, "tok-1");
      const { spy } = queuedFetch();

      connectButton(slot).click();

      expect(statusText(slot)).toBe("Both host and token are required.");
      expect(spy).not.toHaveBeenCalled();
    });

    it("names each family's minimum token scopes", async () => {
      const slot = await openPane("other");
      const scopes = slot.querySelector(".forge-scopes")?.textContent ?? "";
      expect(scopes).toContain("repo, read:org, workflow");
      expect(scopes).toContain("api");
      expect(scopes).toContain("write:repository, write:issue, read:user");
    });

    it("closes the pane on a connect and lands the account, expanded, in its kind's section", async () => {
      const slot = await openPane("other");
      setHost(slot, "git.example.com");
      typeToken(slot, "tok-1");
      const { spy, answer } = queuedFetch();

      connectButton(slot).click();
      answer(0, json({ kind: "gitlab" }));
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
      mockedApiGet.mockResolvedValueOnce({
        forges: [
          {
            id: "gitlab:git.example.com",
            kind: "gitlab",
            host: "git.example.com",
            username: "bot",
            connected: true,
            reconnect_required: false,
          },
        ],
        kinds: KINDS,
        oauth: {},
      });
      answer(1, json({ status: "complete" }));

      const row = await vi.waitFor(() => {
        const r = panel().querySelector<HTMLElement>(
          ".forge-kind-section[data-kind='gitlab'] .forge-account-row[data-id='gitlab:git.example.com']",
        );
        expect(r).not.toBeNull();
        return r!;
      });
      expect(spy.mock.calls[1]![0]).toBe("/api/forges/gitlab%3Agit.example.com/login/pat");
      expect(row.querySelector(".forge-account-repos-summary")?.getAttribute("aria-expanded")).toBe(
        "true",
      );
      expect(slot.childElementCount).toBe(0);
      // The background re-probe after the repaint settles inside this case.
      await new Promise((r) => setTimeout(r, 0));
    });
  });
});
