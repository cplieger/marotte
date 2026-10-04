import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const mocks = vi.hoisted(() => ({
  loadSettings: vi.fn(),
  patchDispatch: vi.fn(),
  readAffordances: vi.fn(),
}));

vi.mock("./persist.js", () => ({ loadSettings: mocks.loadSettings }));
vi.mock("./actions/settings.js", () => ({
  patchAppSettings: { dispatch: mocks.patchDispatch },
}));
vi.mock("./actions/git-prs.js", () => ({
  readAffordances: { dispatch: mocks.readAffordances },
}));

import { openMergeMethodDialog } from "./merge-dialog.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { WALKERS } from "./audit/walkers.js";

/** The static dialog markup from index.html, reduced to what the module
 *  resolves: the ids, the method group and the close hooks. */
function mountDialog(): void {
  document.body.innerHTML = `
    <dialog id="pr-merge-dialog">
      <h3 id="pr-merge-title"></h3>
      <button type="button" data-pr-merge-close>x</button>
      <p id="pr-merge-message"></p>
      <fieldset id="pr-merge-methods"><legend>Merge method</legend></fieldset>
      <div id="pr-merge-status" role="status" hidden></div>
      <button type="button" data-pr-merge-close>Cancel</button>
      <button type="button" id="pr-merge-confirm-btn">Merge</button>
    </dialog>`;
}

/** A dispatch handle settling to `outcome`, the shape the action framework returns. */
function handle(outcome: Record<string, unknown>): unknown {
  const settled = Promise.resolve(outcome);
  return Object.assign(
    settled.then((o) => (o["status"] === "success" ? o["value"] : null)),
    { outcome: settled, abort: vi.fn() },
  );
}

/** The affordances read answering `strategies`. */
function lists(strategies: string[]): unknown {
  return handle({
    status: "success",
    value: {
      merge_strategies: strategies,
      has_issues: { support: "yes", source: "response_body", detail: "" },
      can_push: { support: "yes", source: "response_body", detail: "" },
      merge_train: { support: "unknown", source: "default", detail: "" },
      default_branch: "main",
    },
  });
}

function values(): string[] {
  return [...document.querySelectorAll<HTMLInputElement>('input[name="pr-merge-method"]')].map(
    (r) => r.value,
  );
}

function names(): string[] {
  return [...document.querySelectorAll(".pr-merge-method-name")].map((n) => n.textContent);
}

function descriptions(): string[] {
  return [...document.querySelectorAll(".pr-merge-method-desc")].map((n) => n.textContent);
}

function checkedValue(): string | undefined {
  return document.querySelector<HTMLInputElement>('input[name="pr-merge-method"]:checked')?.value;
}

function confirmBtn(): HTMLButtonElement {
  const btn = document.getElementById("pr-merge-confirm-btn") as HTMLButtonElement | null;
  if (btn === null) {
    throw new Error("confirm button missing");
  }
  return btn;
}

function status(): HTMLElement {
  const el = document.getElementById("pr-merge-status");
  if (el === null) {
    throw new Error("status missing");
  }
  return el;
}

function cancel(): void {
  document.querySelector<HTMLButtonElement>("[data-pr-merge-close]")?.click();
}

const OPTS = {
  title: "Merge pull request",
  message: "PR #7",
  confirmLabel: "Merge",
  forge_id: "github:github.com",
  repo_id: "v1.6f72672f7265706f",
};

describe("merge method dialog", () => {
  beforeEach(() => {
    mocks.loadSettings.mockReset();
    mocks.patchDispatch.mockReset();
    mocks.readAffordances.mockReset();
    mocks.loadSettings.mockResolvedValue(settingsPayload());
    mocks.readAffordances.mockImplementation(() => lists(["merge", "squash", "rebase"]));
    mountDialog();
  });

  it("reads the merge methods of the repository it was opened for", async () => {
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toHaveLength(3);
    });
    expect(mocks.readAffordances).toHaveBeenCalledWith({
      forge_id: "github:github.com",
      repo_id: "v1.6f72672f7265706f",
    });
    cancel();
    await p;
  });

  it("lists exactly the repository's merge methods, in its order", async () => {
    mocks.readAffordances.mockImplementation(() => lists(["squash", "rebase"]));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toEqual(["squash", "rebase"]);
    });
    expect(names()).toEqual(["Squash and merge", "Rebase and merge"]);
    expect(descriptions()).toEqual([
      "Fold the branch into one commit on the base.",
      "Replay the branch's commits onto the base, no merge commit.",
    ]);
    cancel();
    await p;
  });

  it("words each family's own spelling", async () => {
    mocks.readAffordances.mockImplementation(() =>
      lists(["merge", "rebase-merge", "fast-forward-only", "rebase_merge", "ff"]),
    );
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toHaveLength(5);
    });
    expect(names()).toEqual([
      "Create a merge commit",
      "Rebase, then create a merge commit",
      "Fast-forward only",
      "Merge commit with semi-linear history",
      "Fast-forward merge",
    ]);
    cancel();
    await p;
  });

  // Which spellings are merge strategies is the library's to say (ADR-0102), so
  // the dialog offers every one the repository lists and removes none.
  it("offers every strategy the repository lists, in its order", async () => {
    mocks.readAffordances.mockImplementation(() => lists(["merge", "squash", "manually-merged"]));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toEqual(["merge", "squash", "manually-merged"]);
    });
    cancel();
    await p;
  });

  it("names a method it has no words for as the forge spells it", async () => {
    mocks.readAffordances.mockImplementation(() => lists(["octopus"]));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(names()).toEqual(["octopus"]);
    });
    expect(descriptions()).toEqual([]);
    cancel();
    await p;
  });

  it("holds the confirm disabled and says so while the methods are read", async () => {
    let answer: (v: unknown) => void = () => undefined;
    const pending = new Promise((resolve) => {
      answer = resolve;
    });
    mocks.readAffordances.mockImplementation(() => {
      const outcome = pending.then(() => ({
        status: "success",
        value: { merge_strategies: ["squash"] },
      }));
      return Object.assign(
        outcome.then((o) => o.value),
        { outcome, abort: vi.fn() },
      );
    });
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(status().textContent).toBe("Reading this repository's merge methods…");
    });
    expect(confirmBtn().disabled).toBe(true);
    expect(status().hidden).toBe(false);
    expect(document.getElementById("pr-merge-methods")?.getAttribute("aria-busy")).toBe("true");

    answer(undefined);
    await vi.waitFor(() => {
      expect(confirmBtn().disabled).toBe(false);
    });
    expect(status().hidden).toBe(true);
    expect(document.getElementById("pr-merge-methods")?.hasAttribute("aria-busy")).toBe(false);
    cancel();
    await p;
  });

  it("states a failed read and keeps the confirm disabled", async () => {
    mocks.readAffordances.mockImplementation(() =>
      handle({ status: "error", error: { message: "the forge is down", code: "" } }),
    );
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(status().textContent).toBe(
        "Could not read this repository's merge methods. the forge is down",
      );
    });
    expect(confirmBtn().disabled).toBe(true);
    expect(values()).toEqual([]);
    cancel();
    await expect(p).resolves.toBeNull();
  });

  it("states a repository that names no merge method and keeps the confirm disabled", async () => {
    mocks.readAffordances.mockImplementation(() => lists([]));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(status().textContent).toBe("The forge names no merge method for this repository.");
    });
    expect(confirmBtn().disabled).toBe(true);
    cancel();
    await p;
  });

  it("preselects the remembered method when the repository offers it", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload({ last_merge_method: "squash" }));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("squash");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("squash");
  });

  it("preselects the first method the repository lists when the remembered one is not offered", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload({ last_merge_method: "rebase" }));
    mocks.readAffordances.mockImplementation(() => lists(["merge", "squash"]));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("merge");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("merge");
  });

  it("preselects the first listed method when nothing was ever picked", async () => {
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("merge");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("merge");
  });

  it("preselects the first listed method when the settings fetch fails", async () => {
    mocks.loadSettings.mockResolvedValue(null);
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("merge");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("merge");
  });

  it("persists a changed pick as the next default", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload({ last_merge_method: "rebase" }));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("rebase");
    });
    const squash = document.querySelector<HTMLInputElement>('input[value="squash"]');
    squash!.checked = true;
    confirmBtn().click();
    await expect(p).resolves.toBe("squash");
    expect(mocks.patchDispatch).toHaveBeenCalledWith({ body: { last_merge_method: "squash" } });
  });

  it("does not re-persist an unchanged pick", async () => {
    mocks.loadSettings.mockResolvedValue(settingsPayload({ last_merge_method: "squash" }));
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("squash");
    });
    confirmBtn().click();
    await expect(p).resolves.toBe("squash");
    expect(mocks.patchDispatch).not.toHaveBeenCalled();
  });

  it("resolves null on cancel and merges nothing", async () => {
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(checkedValue()).toBe("merge");
    });
    cancel();
    await expect(p).resolves.toBeNull();
    expect(mocks.patchDispatch).not.toHaveBeenCalled();
  });

  it("fills only the open it read for", async () => {
    let answer: (v: unknown) => void = () => undefined;
    const late = new Promise((resolve) => {
      answer = resolve;
    });
    mocks.readAffordances.mockImplementationOnce(() => {
      const outcome = late.then(() => ({ status: "success", value: { merge_strategies: ["ff"] } }));
      return Object.assign(
        outcome.then((o) => o.value),
        { outcome, abort: vi.fn() },
      );
    });
    const first = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(status().textContent).toBe("Reading this repository's merge methods…");
    });
    cancel();
    await first;

    mocks.readAffordances.mockImplementation(() => lists(["squash"]));
    const second = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toEqual(["squash"]);
    });
    answer(undefined);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(values()).toEqual(["squash"]);
    confirmBtn().click();
    await expect(second).resolves.toBe("squash");
  });

  it("replaces the previous open's methods", async () => {
    const first = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toHaveLength(3);
    });
    cancel();
    await first;

    mocks.readAffordances.mockImplementation(() => lists(["rebase"]));
    const second = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toEqual(["rebase"]);
    });
    cancel();
    await second;
  });

  it("renders the caller's title, message and confirm label", async () => {
    const p = openMergeMethodDialog({
      ...OPTS,
      title: "Merge when green",
      message: "PR #9: fix: x",
      confirmLabel: "Arm auto-merge",
    });
    await vi.waitFor(() => {
      expect(document.getElementById("pr-merge-confirm-btn")?.textContent).toBe("Arm auto-merge");
    });
    expect(document.getElementById("pr-merge-title")?.textContent).toBe("Merge when green");
    expect(document.getElementById("pr-merge-message")?.textContent).toBe("PR #9: fix: x");
    cancel();
    await p;
  });
});

describe("merge method dialog on a phone", () => {
  let style: HTMLStyleElement | null = null;

  beforeEach(() => {
    mocks.loadSettings.mockReset();
    mocks.readAffordances.mockReset();
    mocks.loadSettings.mockResolvedValue(settingsPayload());
    mocks.readAffordances.mockImplementation(() => lists(["merge", "squash", "rebase"]));
    // The page's markup at a 390px phone's 90vw, under the app's stylesheet.
    document.body.innerHTML = `
      <dialog id="pr-merge-dialog" class="pr-dialog pr-merge-dialog" style="width: 351px">
        <h3 id="pr-merge-title"></h3>
        <button type="button" data-pr-merge-close>x</button>
        <div class="pr-dialog-body">
          <p id="pr-merge-message" class="pr-merge-message"></p>
          <fieldset id="pr-merge-methods" class="pr-merge-methods"><legend>Merge method</legend></fieldset>
          <div id="pr-merge-status" class="forge-status" role="status" hidden></div>
        </div>
        <button type="button" id="pr-merge-confirm-btn">Merge</button>
      </dialog>`;
    style = mountAppCSS();
  });

  afterEach(() => {
    style?.remove();
    style = null;
    delete document.documentElement.dataset["theme"];
  });

  it.each(["dark", "light"] as const)(
    "keeps the picked method's description legible on its selected fill, %s",
    async (theme) => {
      document.documentElement.dataset["theme"] = theme;
      const p = openMergeMethodDialog(OPTS);
      await vi.waitFor(() => {
        expect(checkedValue()).toBe("merge");
      });
      const contrast = WALKERS["contrast"] as unknown as (o: object) => {
        findings: { sel: string; ratio: number }[];
      };
      const failing = contrast({}).findings.filter((f) => f.sel.includes("pr-merge-method"));
      expect(failing).toEqual([]);
      cancel();
      await p;
    },
  );

  it("keeps every method's radio at its own size beside a description that wraps", async () => {
    const p = openMergeMethodDialog(OPTS);
    await vi.waitFor(() => {
      expect(values()).toHaveLength(3);
    });
    const widths = [
      ...document.querySelectorAll<HTMLInputElement>('input[name="pr-merge-method"]'),
    ].map((r) => Math.round(r.getBoundingClientRect().width));
    expect(widths).toEqual([16, 16, 16]);
    cancel();
    await p;
  });
});
