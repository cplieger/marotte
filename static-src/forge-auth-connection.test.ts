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
import { abortPoll } from "./forge-auth-oauth.js";
import { apiGetTyped } from "./api-client.js";
import { confirm as confirmDialog } from "./confirm.js";
import { outcomesOf } from "./__test-helpers__/press-feedback.js";
import { _resetForTest as resetForgeStore, readForges } from "./forge-store.js";
import { useSteppableClock, waitForOnClock } from "./__test-helpers__/fake-clock.js";

afterEach(() => {
  vi.useRealTimers();
});

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
    resetForgeStore();
    pageNow = (account) => Promise.resolve({ account, repos: [] });
    setupDOM();
    vi.clearAllMocks();
    resetActionFramework();
  });

  afterEach(() => {
    abortPoll();
    vi.unstubAllGlobals();
  });

  /** The rows every read of /api/forges answers; a case moves it to what the server would say next. */
  let rowsNow: Record<string, unknown>[] = [];
  /** First-page reads answer from here; null is a failed read, a function a held one. */
  let pageNow: (account: string) => Promise<unknown> = (account) =>
    Promise.resolve({ account, repos: [] });

  function routeReads(): void {
    mockedApiGet.mockImplementation(((url: string, decode: (v: unknown) => unknown) => {
      if (url === "/api/forges") {
        return Promise.resolve({ forges: rowsNow, kinds: KINDS, oauth: { github: true } });
      }
      if (url === "/api/git/repos") {
        return Promise.resolve({ repos: [] });
      }
      if (url.endsWith("/repos")) {
        const row = rowsNow.find((r) => url.includes(encodeURIComponent(r["id"] as string)));
        return pageNow((row?.["account"] as string | undefined) ?? "").then((p) =>
          p === null ? null : decode(p),
        );
      }
      return Promise.resolve(null);
    }) as typeof apiGetTyped);
  }

  function reads(path: string): number {
    return mockedApiGet.mock.calls.filter(([url]) => url === path).length;
  }

  function account(over: Record<string, unknown> = {}): Record<string, unknown> {
    return {
      id: "github:github.com",
      kind: "github",
      host: "github.com",
      username: "alice",
      account: "acct-1",
      status: "connected",
      ...over,
    };
  }

  async function showRow(a: Record<string, unknown>): Promise<HTMLElement> {
    rowsNow = [a];
    routeReads();
    await renderForgesPanel();
    return rowOf(a["id"] as string);
  }

  function rowOf(id: string): HTMLElement {
    return panel().querySelector<HTMLElement>(`.forge-account-row[data-id='${id}']`)!;
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

  const RECONNECT_REASON =
    "the stored credential can be neither used nor renewed. Sign in to this forge again";

  it("names the remedy on a reconnect row, and Reconnect opens that host's add pane", async () => {
    const row = await showRow(
      account({
        id: "gitlab:git.example.com",
        kind: "gitlab",
        host: "git.example.com",
        status: "reconnect",
        error: { message: RECONNECT_REASON, code: "reconnect_required", kind: "unauthorized" },
      }),
    );
    expect(row.querySelector(".forge-account-identity .forge-account-error")?.textContent).toBe(
      `This account needs a new sign-in: ${RECONNECT_REASON}.`,
    );
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

  it("names no one on a reconnect row with no identity, and claims nothing is loading", async () => {
    const { username: _none, ...anonymous } = account({
      status: "reconnect",
      error: { message: RECONNECT_REASON, code: "reconnect_required", kind: "unauthorized" },
    });
    const row = await showRow(anonymous);
    const primary = row.querySelector<HTMLElement>(".forge-account-primary")!;

    expect(primary.textContent).toBe("Unknown account");
    expect(row.querySelector(".skeleton")).toBeNull();
    expect(row.querySelector("[aria-label*='Loading']")).toBeNull();
    expect(row.querySelector(".forge-account-meta")?.textContent).toBe("github.com");
  });

  it("offers a reconnect the kind's device sign-in where it has one", async () => {
    const row = await showRow(
      account({ status: "reconnect", error: { message: RECONNECT_REASON } }),
    );

    button(row, "Reconnect")!.click();

    const slot = panel().querySelector<HTMLElement>(
      ".forge-kind-section[data-kind='github'] [data-forge-slot]",
    )!;
    expect(slot.querySelector("[data-forge-device-start]")?.textContent).toBe(
      "Sign in with GitHub",
    );
  });

  it("shows an unusable store's reason with no control that cannot succeed", async () => {
    const reason = "the forge credential store /config/forge-store has mode 0750; it needs 0700";
    const row = await showRow(
      account({ status: "unusable", error: { message: reason, code: "connection_unusable" } }),
    );
    expect(row.querySelector(".forge-account-identity .forge-account-error")?.textContent).toBe(
      reason,
    );
    for (const label of ["Check", "Reconnect", "Sign out"]) {
      expect(button(row, label), label).toBeUndefined();
    }
    expect(row.querySelector(".forge-account-owners")).toBeNull();
    expect(row.querySelector(".forge-account-repos")).toBeNull();
  });

  it("names what the token lacks on a row the forge refused for a missing permission", async () => {
    const row = await showRow(account());
    const { answer } = queuedFetch();
    const refused = account({
      status: "refused",
      error: {
        message: "Whoami: scope_insufficient (status 403): [User: Read] Access denied",
        code: "scope_insufficient",
        kind: "forbidden",
      },
    });

    button(row, "Check")!.click();
    rowsNow = [refused];
    answer(0, json({ forge: refused }));

    await vi.waitFor(() => {
      const error =
        row.querySelector(".forge-account-identity .forge-account-error")?.textContent ?? "";
      expect(error).toContain("missing a permission");
      expect(error).toContain("[User: Read]");
    });
    expect(row.classList.contains("forge-account-row-error")).toBe(true);
  });

  it("counts a rate limit down to the deadline the forge named", async () => {
    const row = await showRow(
      account({
        status: "retrying",
        error: {
          message: "Whoami (status 429)",
          kind: "rate_limited",
          retry_at: Date.now() + 42_000,
        },
      }),
    );
    expect(row.querySelector(".forge-account-note")?.textContent).toBe(
      "The forge is rate limiting this account. Try again in 42 seconds.",
    );
  });

  it("restarts the count when the store applies the forge's renewed wait", async () => {
    const t0 = Date.now();
    const limited = (retryAt: number): Record<string, unknown> =>
      account({
        status: "retrying",
        error: { message: "Whoami (status 429)", kind: "rate_limited", retry_at: retryAt },
      });
    const now = vi.spyOn(Date, "now").mockReturnValue(t0);
    try {
      const row = await showRow(limited(t0 + 60_000));
      expect(row.querySelector(".forge-account-note")?.textContent).toContain(
        "Try again in 60 seconds.",
      );

      now.mockReturnValue(t0 + 61_000);
      rowsNow = [limited(t0 + 121_000)];
      await renderForgesPanel();

      expect(row.querySelector(".forge-account-note")?.textContent).toContain(
        "Try again in 60 seconds.",
      );
    } finally {
      now.mockRestore();
    }
  });

  it("keeps a retrying row usable, and says muted that it runs again", async () => {
    const reason = 'Whoami: SSRF dial: all 1 IPs for "api.github.com" failed: no route to host';
    const row = await showRow(
      account({ status: "retrying", error: { message: reason, kind: "transient" } }),
    );

    expect(row.querySelector(".forge-account-note")?.textContent).toBe(
      `The last check hit a temporary problem and runs again automatically. ${reason}`,
    );
    expect(row.querySelector(".forge-account-identity .forge-account-error")).toBeNull();
    expect(row.classList.contains("forge-account-row-error")).toBe(false);
    expect(button(row, "Check")).toBeDefined();
    expect(button(row, "Reconnect")).toBeUndefined();
    expect(row.querySelector(".forge-account-repos")).not.toBeNull();
  });

  it("reports a check that met a temporary failure as failed, with the row saying why", async () => {
    const row = await showRow(account());
    const { answer } = queuedFetch();
    const check = button(row, "Check")!;
    const outcomes = outcomesOf(check);
    const retrying = account({
      status: "retrying",
      error: { message: "Whoami: no route to host", kind: "transient" },
    });

    check.click();
    rowsNow = [retrying];
    answer(0, json({ forge: retrying }));

    await vi.waitFor(() => expect(outcomes).toContain("error"));
    expect(row.querySelector(".forge-account-note")?.textContent).toContain("temporary problem");
  });

  it("keeps the server's sentence for a refusal with any other code", async () => {
    const row = await showRow(
      account({
        status: "refused",
        error: { message: "the forge refused this account at its last check", kind: "forbidden" },
      }),
    );
    expect(row.querySelector(".forge-account-identity .forge-account-error")?.textContent).toBe(
      "the forge refused this account at its last check",
    );
  });

  it("shows Check busy from the press until the answer lands, then repaints the row from the store", async () => {
    useSteppableClock();
    const row = await showRow(account({ status: "refused", error: { message: "no access" } }));
    const { spy, answer } = queuedFetch();
    const check = button(row, "Check")!;
    const before = reads("/api/forges");

    check.click();

    expect(check.disabled).toBe(true);
    expect(check.getAttribute("aria-busy")).toBe("true");
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    expect(spy.mock.calls[0]![0]).toBe("/api/forges/github%3Agithub.com/probe");
    rowsNow = [account()];
    answer(0, json({ forge: account() }));
    await vi.waitFor(() =>
      expect(row.querySelector(".forge-account-identity .forge-account-error")).toBeNull(),
    );
    expect(reads("/api/forges")).toBe(before + 1);
    await waitForOnClock(() => expect(check.disabled).toBe(false), 3000);
  });

  it("says a check already running is running, without marking the press failed", async () => {
    const row = await showRow(account());
    const { answer } = queuedFetch();
    const check = button(row, "Check")!;
    const outcomes = outcomesOf(check);
    const busy = "a check of this account is already running; its result shows here when it ends";

    check.click();
    answer(0, json({ error: busy, code: "check_busy" }, 409));

    await vi.waitFor(() =>
      expect(row.querySelector(".forge-account-identity .forge-account-error")?.textContent).toBe(
        busy,
      ),
    );
    await vi.waitFor(() => expect(outcomes.length).toBeGreaterThan(0));
    expect(outcomes).not.toContain("error");
  });

  const BUSY = "a check of this account is already running; its result shows here when it ends";

  async function answeredBusy(): Promise<HTMLElement> {
    const row = await showRow(account());
    const { answer } = queuedFetch();
    button(row, "Check")!.click();
    answer(0, json({ error: BUSY, code: "check_busy" }, 409));
    await vi.waitFor(() => expect(row.textContent).toContain(BUSY));
    return row;
  }

  it("keeps a busy check's note through a read that changes nothing, and drops it once the row moves", async () => {
    const row = await answeredBusy();

    await renderForgesPanel();
    expect(row.textContent).toContain(BUSY);

    rowsNow = [account({ status: "refused", error: { message: "no access" } })];
    await renderForgesPanel();
    expect(row.textContent).not.toContain(BUSY);
    expect(row.textContent).toContain("no access");
  });

  it("drops a busy check's note after an ordinary check's time, whatever the row says", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const row = await answeredBusy();

      await vi.advanceTimersByTimeAsync(19_000);
      expect(row.textContent).toContain(BUSY);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(row.textContent).not.toContain(BUSY);
    } finally {
      vi.useRealTimers();
    }
  });

  it("signs nothing out when the connection is signed in to another account while the confirm is open", async () => {
    let decide: (ok: boolean) => void = () => undefined;
    vi.mocked(confirmDialog).mockImplementationOnce(
      () =>
        new Promise<boolean>((resolve) => {
          decide = resolve;
        }),
    );
    const row = await showRow(account());
    const { spy, answer } = queuedFetch();
    const oldOut = button(row, "Sign out")!;

    oldOut.click();
    expect(vi.mocked(confirmDialog).mock.calls[0]?.[0]).toBe(
      "Sign out of alice? Its stored token will be removed.",
    );
    rowsNow = [account({ account: "acct-2", username: "bob" })];
    await renderForgesPanel();
    decide(true);

    await vi.waitFor(() =>
      expect(row.textContent).toContain(
        "This connection was signed in to another account while you confirmed, so nothing was signed out.",
      ),
    );
    expect(spy).not.toHaveBeenCalled();

    const newOut = button(row, "Sign out")!;
    expect(newOut).not.toBe(oldOut);
    newOut.click();
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    expect(vi.mocked(confirmDialog).mock.calls[1]?.[0]).toBe(
      "Sign out of bob? Its stored token will be removed.",
    );
    rowsNow = [];
    answer(0, new Response(null, { status: 204 }));
    await vi.waitFor(() => expect(panel().querySelector(".forge-account-row")).toBeNull());
  });

  it("reads the rows on a reconnect_required answer, so the row offers Reconnect and no Check", async () => {
    const row = await showRow(account({ status: "refused", error: { message: "no access" } }));
    const { answer } = queuedFetch();
    const before = reads("/api/forges");
    const sentence = "this account needs a new sign-in; a check cannot revive it";

    button(row, "Check")!.click();
    rowsNow = [account({ status: "reconnect", error: { message: RECONNECT_REASON } })];
    answer(0, json({ error: sentence, code: "reconnect_required" }, 409));

    await vi.waitFor(() => expect(button(row, "Reconnect")).toBeDefined());
    expect(button(row, "Check")).toBeUndefined();
    expect(reads("/api/forges")).toBe(before + 1);
    expect(row.textContent).toContain(sentence);
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
    answer(0, json({ error: "the records file could not be written" }, 500));
    await vi.waitFor(() => {
      expect(row.querySelector(".forge-account-identity .forge-account-error")?.textContent).toBe(
        "Could not sign out. the records file could not be written",
      );
    });
    expect(row.isConnected && !row.hidden).toBe(true);
  });

  it("retries a sign-out whose cleanup is pending, then reads the rows after its last answer", async () => {
    useSteppableClock();
    const row = await showRow(account());
    const { spy, answer } = queuedFetch();
    const before = reads("/api/forges");
    const pending = json(
      {
        error: "signed out, but a stored credential could not be deleted",
        code: "sign_out_cleanup_pending",
      },
      503,
    );

    button(row, "Sign out")!.click();
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    answer(0, pending.clone());
    await waitForOnClock(() => expect(spy).toHaveBeenCalledTimes(2), 3000);
    answer(1, pending.clone());
    await waitForOnClock(() => expect(spy).toHaveBeenCalledTimes(3), 3000);
    expect(reads("/api/forges")).toBe(before);
    rowsNow = [];
    answer(2, pending);

    await vi.waitFor(() => expect(reads("/api/forges")).toBe(before + 1));
    await vi.waitFor(() => expect(panel().querySelector(".forge-account-row")).toBeNull());
  });

  describe("repository listings", () => {
    it("reads one first page per row, none on a repaint while it runs, and settles the row", async () => {
      let land: (v: unknown) => void = () => undefined;
      pageNow = () =>
        new Promise((r) => {
          land = r;
        });
      const row = await showRow(account());
      const first = "/api/forges/github%3Agithub.com/repos";
      expect(reads(first)).toBe(1);
      expect(row.querySelector(".forge-account-repos-pending")?.textContent).toBe(
        "Loading repositories…",
      );

      await renderForgesPanel();
      expect(reads(first)).toBe(1);
      land({ account: "acct-1", repos: [] });

      await vi.waitFor(() => expect(row.querySelector("[data-pending]")).toBeNull());
      expect(reads(first)).toBe(1);
    });

    it("reads once more for a new sign-in on the row, and never shows the old account's list", async () => {
      pageNow = (acct) =>
        Promise.resolve({
          account: acct,
          repos: [
            {
              repo_id: `id:${acct}`,
              owner: acct,
              name: "r",
              full_name: `${acct}/r`,
              clone_url: `https://github.com/${acct}/r.git`,
              affordances: {
                has_issues: { support: "unknown", source: "unknown", detail: "" },
                can_push: { support: "unknown", source: "unknown", detail: "" },
                merge_train: { support: "unknown", source: "unknown", detail: "" },
                default_branch: "",
                merge_strategies: [],
              },
            },
          ],
        });
      const row = await showRow(account());
      const first = "/api/forges/github%3Agithub.com/repos";
      await vi.waitFor(() => expect(row.querySelector("[data-pending]")).toBeNull());
      let landB: (v: unknown) => void = () => undefined;
      const held = pageNow;
      pageNow = () =>
        new Promise((r) => {
          landB = r;
        });

      rowsNow = [account({ account: "acct-2" })];
      await renderForgesPanel();

      expect(reads(first)).toBe(2);
      expect(row.textContent).not.toContain("acct-1/r");
      expect(row.querySelector("[data-pending]")).not.toBeNull();
      landB(await held("acct-2"));
      await vi.waitFor(() => expect(row.querySelector("[data-pending]")).toBeNull());
      expect(reads(first)).toBe(2);
    });

    it("holds a failed read until the row applies connected again", async () => {
      pageNow = () => Promise.resolve(null);
      const row = await showRow(account());
      const first = "/api/forges/github%3Agithub.com/repos";
      await vi.waitFor(() =>
        expect(
          row.querySelector(".forge-account-repos-more p.forge-account-error")?.textContent,
        ).toBe("Could not list the repositories on this account."),
      );

      rowsNow = [
        account(),
        account({ id: "gitlab:gitlab.com", kind: "gitlab", host: "gitlab.com" }),
      ];
      await renderForgesPanel();
      await renderForgesPanel();
      expect(reads(first)).toBe(1);

      rowsNow = [account({ status: "retrying", error: { message: "slow", kind: "transient" } })];
      await renderForgesPanel();
      expect(reads(first)).toBe(1);
      rowsNow = [account()];
      await renderForgesPanel();
      expect(reads(first)).toBe(2);
    });
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

    it("paints a durable write's list at once, reads the rows after it, and an older read keeps it", async () => {
      const block = owners(await showRow(account({ owner_scopes: ["acme"] })));
      const { spy, answer } = queuedFetch();
      const held = (): { land: (v: unknown) => void } => {
        const h: { land: (v: unknown) => void } = { land: () => undefined };
        mockedApiGet.mockImplementationOnce(
          () =>
            new Promise((r) => {
              h.land = r;
            }) as never,
        );
        return h;
      };
      const older = held();
      const olderRead = readForges();
      const after = held();
      const before = reads("/api/forges");

      addOwner(block, "beta");
      await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
      answer(0, json({ owners: ["acme", "beta"] }));

      await vi.waitFor(() => expect(chips(block)).toEqual(["acme", "beta"]));
      await vi.waitFor(() => expect(reads("/api/forges")).toBe(before + 1));
      after.land({ forges: [account({ owner_scopes: ["acme", "beta"] })], kinds: KINDS });
      older.land({ forges: [account({ owner_scopes: ["acme"] })], kinds: KINDS });
      await olderRead;
      await new Promise((r) => setTimeout(r, 0));

      expect(chips(block)).toEqual(["acme", "beta"]);
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
