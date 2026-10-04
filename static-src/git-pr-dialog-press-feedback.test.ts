// Every control on the New PR dialog that starts a request is busy at once, stays
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

import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { openNewPRForRepo } from "./git-prs-tab.js";
import { _resetForTest, applyInventoryList, setPRForges } from "./git-prs-state.js";
import { decodeInventoryList } from "./wire/decoders.gen.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { heldFetch, json, expectBusy, expectIdle } from "./__test-helpers__/press-feedback.js";

beforeEach(() => {
  H.confirm.mockImplementation(async () => true);
});

describe("the New PR dialog", () => {
  const FORGE = {
    id: "github:github.com",
    kind: "github" as const,
    host: "github.com",
    connected: true,
    reconnect_required: false,
  };
  const REPO_ID = "v1.616c6963652f6f6e65";

  let host: HTMLElement;
  // The page's own stylesheet: `.btn-small` declares a display that outranks the
  // UA's `[hidden]` rule, which decides whether a control the dialog hides is gone.
  let css: HTMLStyleElement;

  beforeEach(() => {
    resetActionFramework();
    css = mountAppCSS();
    host = document.createElement("div");
    host.innerHTML = `
      <div id="git-prs-mount"></div>
      <dialog id="pr-create-dialog" class="pr-dialog" aria-label="Open a pull request">
        <header class="pr-dialog-header">
          <button type="button" class="icon-btn" data-pr-close aria-label="Close"></button>
        </header>
        <div class="pr-dialog-body">
          <input type="text" id="pr-base"><input type="text" id="pr-head" readonly>
          <input type="text" id="pr-title"><textarea id="pr-body"></textarea>
          <input type="checkbox" id="pr-draft">
          <div id="pr-dialog-status" class="forge-status" role="status" aria-live="polite"></div>
        </div>
        <footer class="pr-dialog-footer">
          <button type="button" class="btn-small" data-pr-close>Cancel</button>
          <button type="button" id="pr-generate-btn" class="btn-small">Regenerate description</button>
          <button type="button" id="pr-submit-btn" class="btn-small btn-primary">Open pull request</button>
        </footer>
      </dialog>`;
    document.body.appendChild(host);
    _resetForTest();
    setPRForges([FORGE]);
    applyInventoryList(
      decodeInventoryList({
        entries: [
          {
            forge_id: FORGE.id,
            state: "ready",
            cycle_id: "1",
            credential: "valid",
            scopes: [{ scope: "owner", owner: "alice", rows: [] }],
            clones: [{ dir: "one", forge_id: FORGE.id, repo_id: REPO_ID }],
            fetched_at: Date.now(),
          },
        ],
        subject: [],
        viewing: false,
      }),
    );
    H.apiPost.mockResolvedValue({ output: "drafted" });
  });

  afterEach(() => {
    host.remove();
    css.remove();
  });

  /** The row the forge answers once it opened the pull request. */
  const OPENED = {
    repo_id: REPO_ID,
    repo: "alice/one",
    number: 12,
    title: "Add x",
    state: "open",
    source_branch: "feat/x",
    target_branch: "main",
    url: "https://github.com/alice/one/pull/12",
    action: {
      mergeable: "unknown",
      checks: "unknown",
      checks_passing: 0,
      checks_failing: 0,
      checks_pending: 0,
      checks_neutral: 0,
      checks_unknown: 0,
      checks_total: 0,
      auto_merge_armed: "no",
      queue_state: "none",
      queue_position: -1,
      merge_blocked: "none",
    },
  };

  const submit = (): HTMLButtonElement =>
    document.getElementById("pr-submit-btn") as HTMLButtonElement;
  const regenerate = (): HTMLButtonElement =>
    document.getElementById("pr-generate-btn") as HTMLButtonElement;
  const status = (): HTMLElement => document.getElementById("pr-dialog-status")!;

  /** Open the dialog for the clone in `one`, its description drafted. */
  async function open(): Promise<void> {
    await openNewPRForRepo("one", "feat/x");
    // The dialog controller is a mock, so the dialog is shown here.
    document.getElementById("pr-create-dialog")!.setAttribute("open", "");
    await vi.waitFor(() =>
      expect(status().textContent).toBe("Description generated. Edit and submit."),
    );
    (document.getElementById("pr-title") as HTMLInputElement).value = "Add x";
  }

  it("Open pull request is busy from the press and says a refusal in the server's words, usable again", async () => {
    await open();
    const calls = heldFetch();

    submit().click();

    expectBusy(submit());
    expect(status().textContent).toBe("Opening the pull request…");
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.url).toBe(`/api/forges/github%3Agithub.com/repos/${REPO_ID}/prs`);
    calls[0]!.answer(
      json({ error: "a pull request already exists for alice:feat/x", code: "conflict" }, 422),
    );
    await vi.waitFor(() =>
      expect(status().textContent).toBe(
        "Could not open the pull request. a pull request already exists for alice:feat/x",
      ),
    );
    await vi.waitFor(() => expectIdle(submit()), { timeout: 3000 });
    expect(H.dialogClose).not.toHaveBeenCalled();
  });

  it("Open pull request names and links the one it opened, cannot open it twice, and asks the list for a cycle", async () => {
    await open();
    const calls = heldFetch();

    submit().click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(json(OPENED));

    await vi.waitFor(() => expect(status().textContent).toContain("Opened pull request #12."));
    expect(status().querySelector("a")?.getAttribute("href")).toBe(
      "https://github.com/alice/one/pull/12",
    );
    await vi.waitFor(() =>
      expect(calls.some((c) => c.url === "/api/forges/inventory/refresh")).toBe(true),
    );
    expect(submit().checkVisibility(), "Open pull request is off screen").toBe(false);
    expect(regenerate().checkVisibility(), "Regenerate description is off screen").toBe(false);
    expect(host.querySelector(".pr-dialog-footer [data-pr-close]")?.textContent).toBe("Close");
    expect(H.dialogClose).not.toHaveBeenCalled();
  });

  it("a new open starts from the dialog's own controls, whatever the last one ended on", async () => {
    await open();
    const calls = heldFetch();
    submit().click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(json({ error: "no commits between main and feat/x" }, 422));
    await vi.waitFor(() =>
      expect(status().textContent).toContain("Could not open the pull request"),
    );

    await open();

    expectIdle(submit());
    expect(submit().checkVisibility()).toBe(true);
    expect(submit().textContent).toBe("Open pull request");
    expect(regenerate().checkVisibility()).toBe(true);
    expect(host.querySelector(".pr-dialog-footer [data-pr-close]")?.textContent).toBe("Cancel");
  });

  it("a new open after one that opened a pull request offers it again, with Cancel", async () => {
    await open();
    const calls = heldFetch();
    submit().click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(json(OPENED));
    await vi.waitFor(() =>
      expect(host.querySelector(".pr-dialog-footer [data-pr-close]")?.textContent).toBe("Close"),
    );

    await open();

    expect(submit().checkVisibility()).toBe(true);
    expect(regenerate().checkVisibility()).toBe(true);
    expect(host.querySelector(".pr-dialog-footer [data-pr-close]")?.textContent).toBe("Cancel");
  });

  it("Regenerate description is busy while it drafts and says the outcome in the status line", async () => {
    await open();
    // The draft made at open holds its outcome on the button for a moment.
    await vi.waitFor(() => expectIdle(regenerate()), { timeout: 3000 });
    let answer: (v: unknown) => void = () => undefined;
    H.apiPost.mockImplementation(
      () =>
        new Promise((r) => {
          answer = r;
        }),
    );

    regenerate().click();

    expectBusy(regenerate());
    expect(status().textContent).toBe("Generating description…");
    answer({ error: "no commits between main and feat/x" });
    await vi.waitFor(() => expect(status().textContent).toBe("no commits between main and feat/x"));
    await vi.waitFor(() => expectIdle(regenerate()), { timeout: 3000 });
  });
});
