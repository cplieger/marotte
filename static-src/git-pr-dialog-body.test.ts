// The New PR dialog's body starts as the branch's commit list and follows base and head until the reader writes in
// it. Only the network is staged: GET /api/git/branch-commits answers per test.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  H,
  entry,
  githubForge,
  load,
  pr,
  repos,
  routeAPI,
  serve,
  resetPRsTab,
  restorePRsTab,
  mount,
} from "./__test-helpers__/git-prs-tab-harness.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.apiClient(),
}));
vi.mock("./forge-store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.forgeStore(),
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.bus(),
}));
vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.sseAdapter(),
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.confirm(),
}));
vi.mock("./merge-dialog.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.mergeDialog(),
}));
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.actionsIndex(),
}));
vi.mock("./actions/git-prs.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.gitPRs(),
}));
vi.mock("./search-popup.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.searchPopup(),
}));
vi.mock("@cplieger/ui-primitives/dialog", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.dialog(),
}));
vi.mock("./git-scroll.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.gitScroll(),
}));

const COMMITS = "/api/git/branch-commits";

/** Each read of the commit list, answered when the test calls `answer`. */
interface Read {
  readonly base: string;
  readonly head: string;
  answer(subjects: unknown): void;
}
let reads: Read[] = [];

beforeEach(async () => {
  await resetPRsTab();
  reads = [];
  H.apiGet.mockImplementation((url: string) => {
    if (!url.startsWith(COMMITS + "?")) {
      return Promise.resolve(null);
    }
    const q = new URLSearchParams(url.slice(COMMITS.length + 1));
    return new Promise((resolve) => {
      reads.push({
        base: q.get("base") ?? "",
        head: q.get("head") ?? "",
        answer: (subjects) => {
          resolve({ subjects });
        },
      });
    });
  });
  routeAPI();
  serve(
    entry(githubForge, "1", [pr(1, 0)], {
      clones: [{ dir: "one-clone", forge_id: githubForge.id, repo_id: repos[0]?.repo_id }],
    }),
  );
  document.body.insertAdjacentHTML(
    "beforeend",
    `<dialog id="pr-create-dialog">
      <input id="pr-base"><input id="pr-head" readonly><input id="pr-title"><textarea id="pr-body"></textarea>
      <input id="pr-draft" type="checkbox"><div id="pr-dialog-status"></div>
      <button id="pr-submit-btn"></button>
    </dialog>`,
  );
});
afterEach(() => {
  document.getElementById("pr-create-dialog")?.remove();
  restorePRsTab();
});

const field = (id: string): HTMLInputElement | HTMLTextAreaElement =>
  document.getElementById(id) as HTMLInputElement | HTMLTextAreaElement;
const body = (): HTMLTextAreaElement => field("pr-body") as HTMLTextAreaElement;

/** A reader edits a branch field and leaves it, which is when `change` fires. */
async function setBranch(id: "pr-base" | "pr-head", value: string): Promise<void> {
  field(id).value = value;
  field(id).dispatchEvent(new Event("change"));
  await vi.advanceTimersByTimeAsync(0);
}

async function answer(read: Read | undefined, subjects: unknown): Promise<void> {
  if (read === undefined) {
    throw new Error("no commit-list read was made");
  }
  read.answer(subjects);
  await vi.advanceTimersByTimeAsync(0);
}

/** Opens the dialog from the Changes tab's post-push hint, naming the head. */
async function openForPush(): Promise<void> {
  const { openNewPRForRepo } = await load();
  await openNewPRForRepo("one-clone", "feat/x");
  await vi.advanceTimersByTimeAsync(0);
}

/** Opens the dialog from the list's "+ New PR", which leaves the head for the reader. */
async function openFromList(): Promise<void> {
  const { refreshPRs } = await load();
  await refreshPRs();
  [...mount().querySelectorAll<HTMLButtonElement>('[data-repo="cplieger/one"] button')]
    .find((b) => b.textContent === "+ New PR")
    ?.click();
  await vi.advanceTimersByTimeAsync(0);
}

describe("the New PR dialog's body", () => {
  it("is the branch's commit subjects, oldest first, one bullet each, once the dialog opens", async () => {
    await openForPush();

    expect(reads.map((r) => [r.base, r.head])).toEqual([["main", "feat/x"]]);
    await answer(reads[0], ["add the parser", "wire the parser in"]);

    expect(body().value).toBe("- add the parser\n- wire the parser in");
  });

  it("is refilled when the base changes", async () => {
    await openForPush();
    await answer(reads[0], ["one"]);

    await setBranch("pr-base", "dev");

    expect(reads.map((r) => r.base)).toEqual(["main", "dev"]);
    await answer(reads[1], ["one", "two"]);
    expect(body().value).toBe("- one\n- two");
  });

  it("is refilled when the head changes", async () => {
    await openFromList();
    await answer(reads[0], []);

    await setBranch("pr-head", "feat/y");

    expect(reads.map((r) => r.head)).toEqual(["", "feat/y"]);
    await answer(reads[1], ["feat y's change"]);
    expect(body().value).toBe("- feat y's change");
  });

  it("never replaces text the reader typed, before or after the list arrives", async () => {
    await openForPush();
    body().value = "my own words";
    await answer(reads[0], ["one"]);
    expect(body().value).toBe("my own words");

    await setBranch("pr-base", "dev");
    await answer(reads[1], ["one", "two"]);
    expect(body().value).toBe("my own words");
  });

  it("is refilled after the reader edits the prefill away to nothing", async () => {
    await openForPush();
    await answer(reads[0], ["one"]);
    body().value = "";

    await setBranch("pr-base", "dev");
    await answer(reads[1], ["two"]);

    expect(body().value).toBe("- two");
  });

  it("is empty when the head has no commit the base lacks, clearing an earlier prefill", async () => {
    await openForPush();
    await answer(reads[0], []);
    expect(body().value).toBe("");

    await setBranch("pr-base", "dev");
    await answer(reads[1], ["one"]);
    await setBranch("pr-base", "feat/x");
    await answer(reads[2], []);

    expect(body().value).toBe("");
  });

  it("takes the newest read's answer, not a slower earlier one", async () => {
    await openForPush();
    await setBranch("pr-base", "dev");

    await answer(reads[1], ["from dev"]);
    await answer(reads[0], ["from main"]);

    expect(body().value).toBe("- from dev");
  });

  it("reads nothing for an empty base and leaves the body empty", async () => {
    await openForPush();
    await answer(reads[0], ["one"]);

    await setBranch("pr-base", "  ");

    expect(reads).toHaveLength(1);
    expect(body().value).toBe("");
  });

  it("keeps only the string subjects of an answer, and reads a non-list as no commits", async () => {
    await openForPush();
    await answer(reads[0], [42, "kept", null]);
    expect(body().value).toBe("- kept");

    await setBranch("pr-base", "dev");
    await answer(reads[1], "not a list");
    expect(body().value).toBe("");
  });
});
