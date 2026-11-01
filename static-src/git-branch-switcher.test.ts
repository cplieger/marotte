import { describe, it, expect, vi, afterEach } from "vitest";
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
