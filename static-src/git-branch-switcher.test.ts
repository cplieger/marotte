import { describe, it, expect, vi, afterEach } from "vitest";
import { userEvent } from "vitest/browser";
import type * as ApiClient from "./api-client.js";

const m = vi.hoisted(() => ({
  loads: [] as { signal: AbortSignal | undefined; answer: (data: unknown) => void }[],
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(
    (_path: string, signal?: AbortSignal) =>
      new Promise((resolve) => {
        m.loads.push({ signal, answer: resolve });
      }),
  ),
}));

const { openBranchSwitcher } = await import("./git-branch-switcher.js");
const { checkoutBranch } = await import("./actions/git-branch.js");

/** Start a checkout whose request never answers, so the action stays pending until cancelled. */
async function holdCheckout(): Promise<void> {
  vi.stubGlobal(
    "fetch",
    (_url: string, init?: RequestInit) =>
      new Promise((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          reject(init.signal?.reason as Error);
        });
      }),
  );
  void checkoutBranch
    .dispatch({ repo: "repo", branch: "topic", create: false })
    .catch(() => undefined);
  await settle();
}

const BRANCHES = {
  branches: [
    { name: "main", current: true },
    { name: "dev", current: false },
  ],
};

let anchor: HTMLButtonElement | null = null;

function open(): HTMLDivElement {
  anchor = document.createElement("button");
  document.body.append(anchor);
  openBranchSwitcher("repo", anchor);
  const pop = document.querySelector<HTMLDivElement>(".git-branch-popover");
  expect(pop).not.toBeNull();
  return pop!;
}

/** A re-click on the same anchor closes the switcher. */
function close(): void {
  openBranchSwitcher("repo", anchor!);
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

afterEach(() => {
  checkoutBranch.cancel();
  vi.unstubAllGlobals();
  if (document.querySelector(".git-branch-popover") !== null) {
    close();
  }
  anchor?.remove();
  anchor = null;
  m.loads.length = 0;
});

describe("the branch switcher's lifetime", () => {
  it("restores its busy controls and aborts its branches load when it closes", async () => {
    const pop = open();
    m.loads[0]!.answer(BRANCHES);
    await settle();
    const row = pop.querySelector<HTMLButtonElement>(".git-branch-popover-row");
    const create = pop.querySelector<HTMLButtonElement>(".git-branch-popover-go");
    await holdCheckout();
    expect(row?.getAttribute("aria-busy")).toBe("true");
    expect(create?.getAttribute("aria-busy")).toBe("true");
    expect(m.loads[0]!.signal?.aborted).toBe(false);

    close();
    expect(document.querySelector(".git-branch-popover")).toBeNull();
    expect(row?.getAttribute("aria-busy")).toBeNull();
    expect(row?.disabled).toBe(false);
    expect(create?.getAttribute("aria-busy")).toBeNull();
    expect(create?.disabled).toBe(false);
    expect(m.loads[0]!.signal?.aborted).toBe(true);
  });

  it("binds no row from a load that answers after it closed", async () => {
    const pop = open();
    await holdCheckout();
    close();
    m.loads[0]!.answer(BRANCHES);
    await settle();
    const rows = pop.querySelectorAll<HTMLButtonElement>(".git-branch-popover-row");
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      expect(row.getAttribute("aria-busy")).toBeNull();
      expect(row.disabled).toBe(false);
    }
  });
});

/** A checkout whose request answers when `answer` runs, as a failed checkout (HTTP 200 with an
 *  error, the git routes' shape), so nothing else is imported on its success. */
function heldCheckout(): { answer: () => void } {
  let answer = (): void => undefined;
  vi.stubGlobal(
    "fetch",
    (_url: string, init?: RequestInit) =>
      new Promise<Response>((resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          reject(init.signal?.reason as Error);
        });
        answer = () => {
          resolve(
            new Response(JSON.stringify({ error: "nope" }), {
              status: 200,
              headers: { "content-type": "application/json" },
            }),
          );
        };
      }),
  );
  return {
    answer: () => {
      answer();
    },
  };
}

async function openWithRows(): Promise<{ pop: HTMLDivElement; row: HTMLButtonElement }> {
  const pop = open();
  m.loads[0]!.answer(BRANCHES);
  await settle();
  const row = [...pop.querySelectorAll<HTMLButtonElement>(".git-branch-popover-row")].find(
    (r) => r.textContent === "dev",
  );
  expect(row).toBeDefined();
  return { pop, row: row! };
}

/** The pressed row is disabled while its checkout runs, which blurs it on a queued task. */
async function pressPending(row: HTMLButtonElement): Promise<void> {
  const blurred = new Promise((r) => {
    row.addEventListener("blur", r, { once: true });
  });
  await userEvent.click(row);
  await blurred;
  await settle();
  expect(row.disabled).toBe(true);
  expect(document.activeElement).toBe(document.body);
}

describe("focus when the branch switcher closes", () => {
  it("returns focus to its anchor after a checkout from a branch row", async () => {
    const held = heldCheckout();
    const { row } = await openWithRows();
    await pressPending(row);
    held.answer();
    await vi.waitFor(() => {
      expect(document.querySelector(".git-branch-popover")).toBeNull();
    });
    expect(document.activeElement).toBe(anchor);
  });

  it("returns focus to its anchor on Escape during a checkout in flight", async () => {
    heldCheckout();
    const { row } = await openWithRows();
    await pressPending(row);
    await userEvent.keyboard("{Escape}");
    expect(document.querySelector(".git-branch-popover")).toBeNull();
    expect(document.activeElement).toBe(anchor);
  });

  it("leaves focus on <body> after an idle click on a spot that takes no focus", async () => {
    await openWithRows();
    const padding = document.createElement("div");
    padding.style.cssText = "position:fixed;inset:auto auto 0 0;inline-size:100px;block-size:40px";
    document.body.append(padding);
    await userEvent.click(padding);
    padding.remove();
    expect(document.querySelector(".git-branch-popover")).toBeNull();
    expect(document.activeElement).toBe(document.body);
  });

  it("leaves focus in the composer after an outside click into it during a checkout", async () => {
    heldCheckout();
    const { row } = await openWithRows();
    await pressPending(row);
    const composer = document.createElement("textarea");
    composer.style.cssText = "position:fixed;inset:auto 0 0 auto";
    document.body.append(composer);
    await userEvent.click(composer);
    expect(document.querySelector(".git-branch-popover")).toBeNull();
    expect(document.activeElement).toBe(composer);
    composer.remove();
  });
});
