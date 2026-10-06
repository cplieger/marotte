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
    onForgeChange: vi.fn(() => () => undefined),
    currentForges: vi.fn(() => []),
    oauthByKind: vi.fn(() => ({})),
    forgeLoadFailed: vi.fn(() => false),
  };
});

import { resetActionFramework } from "@cplieger/actions/testing";
import { renderForgesPanel } from "./forge-auth.js";
import { apiGetTyped } from "./api-client.js";

const mockedApiGet = vi.mocked(apiGetTyped);

function setupDOM(): void {
  document.body.innerHTML = `<div id="forges-panel"></div>`;
}

function panel(): HTMLElement {
  return document.getElementById("forges-panel")!;
}

describe("forge-auth: the repository list", () => {
  const KINDS = ["github", "gitlab", "codeberg", "gitea"];
  const ACCOUNT = {
    id: "github:github.com",
    kind: "github",
    host: "github.com",
    username: "alice",
    connected: true,
    reconnect_required: false,
  };
  const FIRST_PAGE = "/api/forges/github%3Agithub.com/repos";

  beforeEach(async () => {
    setupDOM();
    vi.clearAllMocks();
    resetActionFramework();
    // A panel with no account drops the listings an earlier case left.
    mockedApiGet.mockImplementation((() =>
      Promise.resolve({ forges: [], kinds: KINDS })) as typeof apiGetTyped);
    await renderForgesPanel({ revalidate: false });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function repo(name: string, owner = "alice"): Record<string, unknown> {
    const none = { support: "unknown", source: "unknown", detail: "" };
    return {
      repo_id: `id:${owner}/${name}`,
      owner,
      name,
      full_name: `${owner}/${name}`,
      clone_url: `https://github.com/${owner}/${name}.git`,
      affordances: {
        has_issues: none,
        can_push: none,
        merge_train: none,
        default_branch: "",
        merge_strategies: [],
      },
    };
  }

  /** `first` null means the read failed. */
  async function showList(first: unknown, cloned: string[] = []): Promise<HTMLElement> {
    mockedApiGet.mockImplementation(((url: string, decode: (v: unknown) => unknown) => {
      switch (url) {
        case "/api/forges":
          return Promise.resolve({ forges: [ACCOUNT], kinds: KINDS });
        case "/api/git/repos":
          return Promise.resolve({ repos: cloned });
        case FIRST_PAGE:
          return Promise.resolve(first === null ? null : decode(first));
        default:
          return Promise.resolve(null);
      }
    }) as typeof apiGetTyped);
    await renderForgesPanel({ revalidate: false });
    const list = panel().querySelector<HTMLElement>(".forge-account-repos")!;
    list.querySelector<HTMLElement>(".forge-account-repos-summary")!.click();
    return list;
  }

  function names(details: HTMLElement): string[] {
    return [...details.querySelectorAll(".forge-account-repo-name")].map((n) => n.textContent);
  }

  /** Found by class: its label gives way to the outcome glyph after each press. */
  function loadMore(details: HTMLElement): HTMLButtonElement | undefined {
    return (
      details.querySelector<HTMLButtonElement>("button.forge-account-repos-load-more") ?? undefined
    );
  }

  function heldFetch(): {
    spy: ReturnType<typeof vi.fn<typeof fetch>>;
    answer: (r: Response) => void;
  } {
    let answer: (r: Response) => void = () => undefined;
    const spy = vi.fn<typeof fetch>(
      () =>
        new Promise<Response>((r) => {
          answer = r;
        }),
    );
    vi.stubGlobal("fetch", spy);
    return { spy, answer: (r) => answer(r) };
  }

  it("offers Load more while the forge has more, busy from the press, sending the cursor as after", async () => {
    const details = await showList({ repos: [repo("one")], next: "cursor-2" });
    expect(details.querySelector(".forge-account-repos-label")?.textContent).toBe(
      "1 repo so far, 0 cloned locally",
    );
    const { spy, answer } = heldFetch();
    const btn = loadMore(details)!;
    expect(btn.textContent).toBe("Load more repositories");

    btn.click();

    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    expect(spy.mock.calls[0]![0]).toBe(`${FIRST_PAGE}?after=cursor-2`);
    answer(
      new Response(JSON.stringify({ repos: [repo("two")], next: "cursor-3" }), { status: 200 }),
    );
    await vi.waitFor(() => expect(names(details)).toEqual(["alice/one", "alice/two"]));
    await vi.waitFor(() => expect(loadMore(details)?.disabled).toBe(false), { timeout: 3000 });

    loadMore(details)!.click();
    await vi.waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
    expect(spy.mock.calls[1]![0]).toBe(`${FIRST_PAGE}?after=cursor-3`);
    answer(new Response(JSON.stringify({ repos: [repo("three")] }), { status: 200 }));
    await vi.waitFor(() =>
      expect(names(details)).toEqual(["alice/one", "alice/three", "alice/two"]),
    );
    expect(loadMore(details)).toBeUndefined();
    expect(details.querySelector(".forge-account-repos-label")?.textContent).toBe(
      "3 repos, 0 cloned locally",
    );
  });

  it("keeps Clone all and Delete all outside the control that opens the list", async () => {
    // A button inside a `<summary>` is flattened or dropped by assistive technology (axe nested-interactive).
    const list = await showList({ repos: [repo("one"), repo("two")] }, ["one"]);
    const toggle = list.querySelector<HTMLElement>(".forge-account-repos-summary")!;
    const cloneAll = list.querySelector<HTMLButtonElement>(".forge-account-repos-clone-all")!;
    const deleteAll = list.querySelector<HTMLButtonElement>(".forge-account-repos-delete-all")!;

    expect(toggle.querySelector("a, button, input, select, textarea, [tabindex]")).toBeNull();
    expect(cloneAll.closest("summary, [aria-expanded]")).toBeNull();
    expect(deleteAll.closest("summary, [aria-expanded]")).toBeNull();
    expect(toggle.textContent).toBe("2 repos, 1 cloned locally");
  });

  it("keeps the pages a reader loaded when the same first page is read again", async () => {
    let details = await showList({ repos: [repo("one")], next: "cursor-2" });
    const { answer } = heldFetch();
    loadMore(details)!.click();
    answer(new Response(JSON.stringify({ repos: [repo("two")] }), { status: 200 }));
    await vi.waitFor(() => expect(names(details)).toEqual(["alice/one", "alice/two"]));

    await renderForgesPanel({ revalidate: false });

    details = panel().querySelector<HTMLElement>(".forge-account-repos")!;
    expect(names(details)).toEqual(["alice/one", "alice/two"]);
    expect(loadMore(details)).toBeUndefined();
  });

  it("lists two repositories that share a name under different owners, across pages", async () => {
    const details = await showList({ repos: [repo("x")], next: "cursor-2" });
    const { answer } = heldFetch();
    loadMore(details)!.click();
    answer(new Response(JSON.stringify({ repos: [repo("x", "zed")] }), { status: 200 }));
    await vi.waitFor(() => expect(names(details)).toEqual(["alice/x", "zed/x"]));
  });

  it("keeps the rows and says why when the next page is refused", async () => {
    const details = await showList({ repos: [repo("one")], next: "cursor-2" });
    const { answer } = heldFetch();
    const btn = loadMore(details)!;

    btn.click();
    answer(
      new Response(JSON.stringify({ error: "the forge said no", code: "scope_insufficient" }), {
        status: 403,
      }),
    );

    await vi.waitFor(() =>
      expect(details.querySelector(".forge-account-repos-more [role='status']")?.textContent).toBe(
        "Could not load more repositories. the forge said no",
      ),
    );
    expect(btn.dataset["asyncStatus"]).toBe("error");
    expect(names(details)).toEqual(["alice/one"]);
    await vi.waitFor(() => expect(loadMore(details)?.disabled).toBe(false), { timeout: 3000 });

    loadMore(details)!.click();
    expect(details.querySelector(".forge-account-repos-more [role='status']")?.textContent).toBe(
      "",
    );
  });

  it("names why an empty partial list stopped short, and shows no empty state", async () => {
    const details = await showList({
      repos: [],
      partial: { reason: "rate_limited", fetched: 0, omitted_at_least: 5 },
    });
    expect(details.querySelector(".forge-account-repos-empty")).toBeNull();
    expect(details.querySelector(".forge-account-repos-more .section-hint")?.textContent).toBe(
      "Not every repository on this account was read: reads are being held back to stay within the forge's rate limit. At least 5 more were not read.",
    );
  });

  it("names why a partial list stopped short, under the rows it read", async () => {
    const details = await showList({
      repos: [repo("one")],
      partial: { reason: "result_window", fetched: 1, omitted_at_least: 0 },
    });
    expect(names(details)).toEqual(["alice/one"]);
    expect(details.querySelector(".forge-account-repos-more .section-hint")?.textContent).toBe(
      "Not every repository on this account was read: the forge serves only part of this list.",
    );
  });

  it("names no page limit that Load more continues past", async () => {
    const details = await showList({
      repos: [repo("one")],
      next: "cursor-2",
      partial: { reason: "pagination_cap", fetched: 1, omitted_at_least: 1 },
    });
    expect(details.querySelector(".forge-account-repos-more .section-hint")).toBeNull();
    expect(loadMore(details)).toBeDefined();
  });

  it("shows the empty state for a whole list with no repositories", async () => {
    const details = await showList({ repos: [] });
    expect(details.querySelector(".forge-account-repos-empty")?.textContent).toBe(
      "No repositories accessible to this account.",
    );
  });

  it("keeps the rows it read when a later refresh fails, and says the refresh failed", async () => {
    await showList({ repos: [repo("one")] });
    const details = await showList(null);
    expect(names(details)).toEqual(["alice/one"]);
    expect(
      details.querySelector(".forge-account-repos-more p.forge-account-error")?.textContent,
    ).toBe("Could not refresh the repositories on this account. The list is from the last read.");
  });

  it("says the list could not be read rather than showing it empty", async () => {
    const details = await showList(null);
    expect(details.querySelector(".forge-account-repos-empty")).toBeNull();
    expect(
      details.querySelector(".forge-account-repos-more p.forge-account-error")?.textContent,
    ).toBe("Could not list the repositories on this account.");
    expect(details.querySelector(".forge-account-repos-label")?.textContent).toBe(
      "Repositories not read",
    );
  });
});
