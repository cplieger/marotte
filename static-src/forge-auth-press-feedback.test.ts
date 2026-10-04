// Every control on the Sources tab that starts a request is busy at once, stays
// busy through a repaint while it runs, and shows its outcome in place. Only
// the network is held.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const H = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiPost: vi.fn(),
  confirm: vi.fn(async (_message: string, _label?: string, _variant?: string) => true),
  dialogOpen: vi.fn(),
  dialogClose: vi.fn(),
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: H.apiGet,
  apiGetTyped: H.apiGetTyped,
  apiPost: H.apiPost,
}));
vi.mock("./toast.js", async (importOriginal) => {
  const { toastMock } = await import("./__test-helpers__/toast-mock.js");
  return { ...(await importOriginal<Record<string, unknown>>()), ...toastMock() };
});
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  confirm: H.confirm,
}));
// The forge list is the shared store's, which reads it through an action; routed
// through the mocked client so each describe answers it with the rest.
vi.mock("./forge-store.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>();
  const { apiGetTyped } = await import("./api-client.js");
  const read = (): unknown => apiGetTyped("/api/forges", (v: unknown) => v);
  return { ...orig, refreshForges: read, ensureForges: read };
});
vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  presentedTag: () => "profile-tag",
}));
vi.mock("./search-popup.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createSearchPopup: () => ({ open: vi.fn(), close: vi.fn(), toggle: vi.fn() }),
}));
vi.mock("./git-scroll.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  preserveGitScroll: (fn: () => void) => {
    fn();
  },
}));
// Spread: the originals spread above import this module's other exports (the real
// confirm's `ask` imports `openDialog` and `closeDialog`).
vi.mock("@cplieger/ui-primitives/dialog", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createDialog: () => ({ open: H.dialogOpen, close: H.dialogClose }),
}));

import * as toast from "./toast.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { renderForgesPanel } from "./forge-auth.js";
import {
  heldFetch,
  json,
  cloneAnswer,
  outcomesOf,
  settle,
  expectBusy,
  expectIdle,
} from "./__test-helpers__/press-feedback.js";

beforeEach(() => {
  H.confirm.mockImplementation(async () => true);
});

describe("the Sources tab", () => {
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

  let host: HTMLElement;
  /** The workspace's clones, as `/api/git/repos` answers them. */
  let clones = new Set<string>();
  let listed: string[] = [];

  function repo(name: string): Record<string, unknown> {
    const none = { support: "unknown", source: "unknown", detail: "" };
    return {
      repo_id: `id:alice/${name}`,
      owner: "alice",
      name,
      full_name: `alice/${name}`,
      url: `https://github.com/alice/${name}`,
      clone_url: `https://github.com/alice/${name}.git`,
      affordances: {
        has_issues: none,
        can_push: none,
        merge_train: none,
        default_branch: "",
        merge_strategies: [],
      },
    };
  }

  beforeEach(() => {
    resetActionFramework();
    host = document.createElement("div");
    host.id = "forges-panel";
    document.body.appendChild(host);
    H.apiGetTyped.mockImplementation((url: string, decode: (v: unknown) => unknown) => {
      switch (url) {
        case "/api/forges":
          return Promise.resolve(decode({ forges: [ACCOUNT], kinds: KINDS }));
        case "/api/git/repos":
          return Promise.resolve(decode({ repos: [...clones] }));
        case FIRST_PAGE:
          return Promise.resolve(decode({ repos: listed.map(repo) }));
        default:
          return Promise.resolve(null);
      }
    });
  });

  afterEach(() => {
    host.remove();
  });

  /** Paint the account listing `names`, `cloned` of them in the workspace, and
   *  open its repository list. */
  async function show(names: string[], cloned: string[]): Promise<HTMLElement> {
    listed = names;
    clones = new Set(cloned);
    await renderForgesPanel({ revalidate: false });
    const d = host.querySelector<HTMLElement>(".forge-account-repos")!;
    toggle(d).click();
    return d;
  }

  /** The control that opens and closes the repository list. */
  function toggle(d: HTMLElement): HTMLButtonElement {
    return d.querySelector<HTMLButtonElement>(".forge-account-repos-summary")!;
  }

  function row(name: string): HTMLElement {
    return [...host.querySelectorAll<HTMLElement>(".forge-account-repo-row")].find(
      (li) => li.querySelector(".forge-account-repo-name")?.textContent === `alice/${name}`,
    )!;
  }

  function control(name: string, label: string): HTMLButtonElement | undefined {
    return row(name).querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`) ?? undefined;
  }

  /** What the row says about its last press. */
  function note(name: string): string {
    return row(name).querySelector(".forge-account-error")?.textContent ?? "";
  }

  function listStatus(d: HTMLElement): string {
    return d.querySelector(".forge-account-repos-status")?.textContent ?? "";
  }

  it("Clone is busy from the press and through a repaint, and the row reads cloned once it lands", async () => {
    await show(["one"], []);
    const calls = heldFetch();
    const clone = control("one", "Clone into workspace")!;

    clone.click();

    expectBusy(clone);
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.url).toBe("/api/git/clone");
    await renderForgesPanel({ revalidate: false });
    expectBusy(control("one", "Clone into workspace"));
    clones.add("one");
    calls[0]!.answer(cloneAnswer({ output: "Cloning into 'one'..." }));
    await vi.waitFor(() => expect(control("one", "Remove local copy")).toBeDefined());
    expect(row("one").querySelector(".git-sources-cloned-dot")).not.toBeNull();
    expectIdle(control("one", "Remove local copy"));
  });

  it("Clone says a refusal in its row until the row's next press, with the control usable again", async () => {
    await show(["one"], []);
    const calls = heldFetch();

    control("one", "Clone into workspace")!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(cloneAnswer({ error: "fatal: repository not found" }));

    await vi.waitFor(() =>
      expect(note("one")).toBe("Could not clone. fatal: repository not found"),
    );
    await vi.waitFor(() => expectIdle(control("one", "Clone into workspace")), { timeout: 3000 });
    await renderForgesPanel({ revalidate: false });
    expect(note("one")).toBe("Could not clone. fatal: repository not found");
    expect(toast.error).not.toHaveBeenCalled();

    control("one", "Clone into workspace")!.click();
    await vi.waitFor(() => expect(note("one")).toBe(""));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    calls[1]!.answer(cloneAnswer({ output: "" }));
  });

  it("Remove local copy is busy once confirmed, keeps the row's copy until the delete lands, then reads not cloned", async () => {
    await show(["two"], ["two"]);
    const calls = heldFetch();

    control("two", "Remove local copy")!.click();

    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.url).toBe("/api/git/remove");
    expect(calls[0]!.body).toEqual({ repo: "two" });
    expectBusy(control("two", "Remove local copy"));
    expect(control("two", "Clone into workspace")).toBeUndefined();
    clones.delete("two");
    calls[0]!.answer(json({ status: "ok" }));
    await vi.waitFor(() => expectIdle(control("two", "Clone into workspace")));
    expect(row("two").querySelector(".git-sources-cloned-dot")).toBeNull();
  });

  it("Remove local copy sends nothing and reports no outcome when its confirm is cancelled", async () => {
    await show(["two"], ["two"]);
    const calls = heldFetch();
    H.confirm.mockImplementation(async () => false);
    const trash = control("two", "Remove local copy")!;
    const outcomes = outcomesOf(trash);

    trash.click();

    await vi.waitFor(() => expect(H.confirm).toHaveBeenCalledTimes(1));
    await settle();
    expect(calls).toHaveLength(0);
    expect(outcomes).toEqual([]);
    expectIdle(control("two", "Remove local copy"));
  });

  it("Remove local copy says a refusal in its row, which keeps its copy", async () => {
    await show(["two"], ["two"]);
    const calls = heldFetch();

    control("two", "Remove local copy")!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(json({ error: "remove failed" }));

    await vi.waitFor(() =>
      expect(note("two")).toBe("Could not remove the local copy. remove failed"),
    );
    await vi.waitFor(() => expectIdle(control("two", "Remove local copy")), { timeout: 3000 });
    expect(row("two").querySelector(".git-sources-cloned-dot")).not.toBeNull();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("Clone all is busy from the press, each row it reaches shows its clone, and the rows read cloned", async () => {
    const d = await show(["one", "three"], []);
    const calls = heldFetch();
    const all = d.querySelector<HTMLButtonElement>(".forge-account-repos-clone-all")!;
    const outcomes = outcomesOf(all);

    all.click();

    expectBusy(all);
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expectBusy(control("one", "Clone into workspace"));
    clones.add("one");
    calls[0]!.answer(cloneAnswer({ output: "" }));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    expect(control("one", "Remove local copy")).toBeDefined();
    expectBusy(control("three", "Clone into workspace"));
    clones.add("three");
    calls[1]!.answer(cloneAnswer({ output: "" }));
    await vi.waitFor(() => expect(control("three", "Remove local copy")).toBeDefined());
    await vi.waitFor(() => expect(d.querySelector(".forge-account-repos-clone-all")).toBeNull());
    expect(d.querySelector(".forge-account-repos-label")?.textContent).toBe(
      "2 repos, 2 cloned locally",
    );
    expect(listStatus(d)).toBe("");
    expect(outcomes).toEqual(["pending", "success"]);
  });

  it("Clone all says a refusal on the row it failed and in the list's status line, and opens the list on it", async () => {
    const d = await show(["one", "three"], []);
    toggle(d).click();
    expect(toggle(d).getAttribute("aria-expanded")).toBe("false");
    const calls = heldFetch();
    const all = d.querySelector<HTMLButtonElement>(".forge-account-repos-clone-all")!;
    const outcomes = outcomesOf(all);

    all.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(cloneAnswer({ error: "fatal: repository not found" }));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    clones.add("three");
    calls[1]!.answer(cloneAnswer({ output: "" }));

    await vi.waitFor(() => expect(listStatus(d)).toBe("Could not clone alice/one (1 of 2 repos)."));
    expect(note("one")).toBe("Could not clone. fatal: repository not found");
    expect(control("three", "Remove local copy")).toBeDefined();
    expect(toggle(d).getAttribute("aria-expanded")).toBe("true");
    expect(outcomes).toContain("error");
    expect(toast.error).not.toHaveBeenCalled();

    d.querySelector<HTMLButtonElement>(".forge-account-repos-clone-all")!.click();
    expect(listStatus(d)).toBe("");
    await vi.waitFor(() => expect(calls).toHaveLength(3));
    clones.add("one");
    calls[2]!.answer(cloneAnswer({ output: "" }));
    await vi.waitFor(() => expect(control("one", "Remove local copy")).toBeDefined());
  });

  it("Delete all sends nothing and reports no outcome when its confirm is cancelled", async () => {
    const d = await show(["two"], ["two"]);
    const calls = heldFetch();
    H.confirm.mockImplementation(async () => false);
    const del = d.querySelector<HTMLButtonElement>(".forge-account-repos-delete-all")!;
    const outcomes = outcomesOf(del);

    del.click();

    await vi.waitFor(() => expect(H.confirm).toHaveBeenCalledTimes(1));
    await settle();
    expect(calls).toHaveLength(0);
    expect(outcomes).toEqual([]);
    expectIdle(d.querySelector<HTMLButtonElement>(".forge-account-repos-delete-all") ?? undefined);
  });

  it("Delete all is busy once confirmed and says a refusal on its row and in the list's status line", async () => {
    const d = await show(["two", "four"], ["two", "four"]);
    const calls = heldFetch();
    const del = d.querySelector<HTMLButtonElement>(".forge-account-repos-delete-all")!;

    del.click();

    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expectBusy(del);
    expect(calls[0]!.body).toEqual({ repo: "two" });
    calls[0]!.answer(json({ error: "remove failed" }));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    clones.delete("four");
    calls[1]!.answer(json({ status: "ok" }));

    await vi.waitFor(() =>
      expect(listStatus(d)).toBe("Could not remove the local copy of alice/two (1 of 2 repos)."),
    );
    expect(note("two")).toBe("Could not remove the local copy. remove failed");
    expect(control("four", "Clone into workspace")).toBeDefined();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("Check is busy from a press that clears its row's last refusal, and says the next one", async () => {
    await show([], []);
    const calls = heldFetch();
    const check = (): HTMLButtonElement | undefined =>
      [...host.querySelectorAll<HTMLButtonElement>(".forge-account-actions button")].find(
        (b) => b.getAttribute("aria-label") === "Check the connection",
      );
    const rowError = (): string =>
      host.querySelector(".forge-account-identity .forge-account-error")?.textContent ?? "";

    check()!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(json({ error: "the forge is unreachable" }, 502));
    await vi.waitFor(() =>
      expect(rowError()).toBe("Could not check the connection. the forge is unreachable"),
    );
    await vi.waitFor(() => expectIdle(check()), { timeout: 3000 });

    check()!.click();

    await vi.waitFor(() => expect(rowError()).toBe(""));
    expectBusy(check());
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    calls[1]!.answer(json({ error: "still unreachable" }, 502));
    await vi.waitFor(() =>
      expect(rowError()).toBe("Could not check the connection. still unreachable"),
    );
  });
});
