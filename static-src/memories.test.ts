import { vi, describe, it, expect, beforeEach } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as Confirm from "./confirm.js";
import type * as MemoryActions from "./actions/memory.js";

interface ListReply {
  ok: boolean;
  status: number;
  data: unknown;
  error: string;
  body?: unknown;
}

let listReply: ListReply;
let oneReply: ListReply;
let confirmAnswer = true;
const deleted: string[] = [];
const updated: { id: string; edit: unknown }[] = [];

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTypedOrError: vi.fn(async (path: string, decode: (v: unknown) => unknown) => {
    const r = path === "/api/memory" ? listReply : oneReply;
    return r.ok ? { ...r, data: decode(r.data) } : r;
  }),
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: vi.fn(async () => confirmAnswer),
}));
vi.mock("./actions/memory.js", async (importOriginal) => ({
  ...(await importOriginal<typeof MemoryActions>()),
  deleteMemory: {
    dispatch: vi.fn(async (args: { id: string }) => {
      deleted.push(args.id);
      return undefined;
    }),
  },
  updateMemory: {
    dispatch: vi.fn(async (args: { id: string; edit: unknown }) => {
      updated.push(args);
      return {};
    }),
  },
}));

import { renderMemoriesPanel, setMemoryCountsListener, _resetMemoriesForTest } from "./memories.js";
import { confirm } from "./confirm.js";

function record(id: string, scope: string | null, title: string, updated = 1_700_000_000) {
  return {
    id,
    memory_type: "fact",
    title,
    summary: `${title} summary`,
    created_at: updated,
    updated_at: updated,
    scopes: scope === null ? [] : [scope],
  };
}

function ok(data: unknown): ListReply {
  return { ok: true, status: 200, data, error: "" };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
  }
}

let panel: HTMLElement;

async function mount(filter = ""): Promise<void> {
  renderMemoriesPanel(panel, filter);
  await settle();
}

function sectionLabels(): string[] {
  return [...panel.querySelectorAll(".docs-section > .entry-section-label")].map(
    (n) => n.textContent ?? "",
  );
}

function titlesIn(scope: string): string[] {
  const sec = [...panel.querySelectorAll(".docs-section")].find(
    (s) => s.querySelector(".entry-section-label")?.textContent === scope,
  );
  return [...(sec?.querySelectorAll(".entry .entry-title") ?? [])].map((n) => n.textContent ?? "");
}

function button(text: string): HTMLButtonElement {
  const b = [...panel.querySelectorAll("button")].find((x) => x.textContent?.startsWith(text));
  if (b === undefined) {
    throw new Error(`no button ${text}`);
  }
  return b;
}

beforeEach(() => {
  _resetMemoriesForTest();
  document.body.replaceChildren();
  panel = document.createElement("div");
  document.body.appendChild(panel);
  deleted.length = 0;
  updated.length = 0;
  confirmAnswer = true;
  vi.mocked(confirm).mockClear();
  listReply = ok({
    memories: [
      record("a", "repo:org/one", "Older", 1_700_000_000),
      record("b", "repo:org/one", "Newer", 1_700_000_500),
      record("c", null, "Loose"),
      record("d", "global", "Global"),
      record("e", "workspace", "Local"),
    ],
    cap: 1000,
  });
  oneReply = ok({ memory: { ...record("b", "repo:org/one", "Newer"), content: "full text" } });
});

describe("the list", () => {
  it("groups by scope, alphabetically with Unscoped last, newest first", async () => {
    await mount();
    expect(sectionLabels()).toEqual(["global", "repo:org/one", "workspace", "Unscoped"]);
    expect(titlesIn("repo:org/one")).toEqual(["Newer", "Older"]);
  });

  it("shows the count against the cap", async () => {
    await mount();
    expect(panel.querySelector(".memories-count")?.textContent).toBe("5 of 1,000");
  });

  it("reports counts for the page note and filters without refetching", async () => {
    const seen: { total: number; shown: number }[] = [];
    setMemoryCountsListener((c) => seen.push(c));
    await mount();
    await mount("newer");
    expect(seen.at(-1)).toEqual({ total: 5, shown: 1 });
    expect(titlesIn("repo:org/one")).toEqual(["Newer"]);
  });

  it("says when the account is not eligible", async () => {
    listReply = {
      ok: false,
      status: 409,
      data: null,
      error: "Memory is not available for this account.",
      body: { error: "Memory is not available for this account.", code: "not_enabled" },
    };
    await mount();
    expect(panel.textContent).toContain("Memory is not available for this account.");
    expect(panel.querySelectorAll(".entry")).toHaveLength(0);
  });

  it("offers a retry on any other failure", async () => {
    listReply = { ok: false, status: 502, data: null, error: "memory request failed" };
    await mount();
    expect(panel.textContent).toContain("Couldn't load memories");
    listReply = ok({ memories: [record("a", null, "Back")], cap: 1000 });
    button("Retry").click();
    await settle();
    expect(titlesIn("Unscoped")).toEqual(["Back"]);
  });
});

describe("one record", () => {
  it("opens its full content below the row", async () => {
    await mount();
    panel.querySelector<HTMLButtonElement>('[data-memory-id="b"] .entry-open')?.click();
    await settle();
    expect(panel.querySelector(".memories-content")?.textContent).toBe("full text");
    expect(
      panel.querySelector('[data-memory-id="b"] .entry-open')?.getAttribute("aria-expanded"),
    ).toBe("true");
  });

  it("shows a failed fetch as a failure with Retry, never as an editable blank body", async () => {
    oneReply = { ok: false, status: 502, data: null, error: "memory request failed" };
    await mount();
    panel
      .querySelector<HTMLButtonElement>('[data-memory-id="b"] [aria-label="Edit Newer"]')
      ?.click();
    await settle();
    expect(panel.querySelector(".memories-form")).toBeNull();
    expect(panel.querySelector(".memories-content")).toBeNull();
    expect(panel.textContent).toContain("Couldn't load this memory.");
    oneReply = ok({ memory: { ...record("b", "repo:org/one", "Newer"), content: "full text" } });
    button("Retry").click();
    await settle();
    expect(panel.querySelector<HTMLTextAreaElement>(".memories-form textarea")?.value).toBe(
      "full text",
    );
  });

  it("saves only the fields that changed", async () => {
    await mount();
    panel
      .querySelector<HTMLButtonElement>('[data-memory-id="b"] [aria-label="Edit Newer"]')
      ?.click();
    await settle();
    const inputs = panel.querySelectorAll<HTMLInputElement>(".memories-form input");
    const title = inputs[0];
    if (title === undefined) {
      throw new Error("no title field");
    }
    title.value = "Renamed";
    panel.querySelector<HTMLFormElement>(".memories-form")?.requestSubmit();
    await settle();
    expect(updated).toEqual([{ id: "b", edit: { title: "Renamed" } }]);
  });

  it("deletes one after confirming, and not when the confirm is refused", async () => {
    await mount();
    confirmAnswer = false;
    panel.querySelector<HTMLButtonElement>('[aria-label="Delete Older"]')?.click();
    await settle();
    expect(deleted).toEqual([]);
    confirmAnswer = true;
    panel.querySelector<HTMLButtonElement>('[aria-label="Delete Older"]')?.click();
    await settle();
    expect(deleted).toEqual(["a"]);
  });
});

describe("bulk delete", () => {
  it("deletes the selected records by id", async () => {
    await mount();
    expect(button("Delete selected").disabled).toBe(true);
    for (const id of ["a", "c"]) {
      const box = panel.querySelector<HTMLInputElement>(
        `[data-memory-id="${id}"] .memories-select`,
      );
      box?.click();
    }
    expect(button("Delete selected").textContent).toBe("Delete selected (2)");
    button("Delete selected").click();
    await settle();
    expect(deleted).toEqual(["a", "c"]);
  });

  it("Delete all confirms and removes every record", async () => {
    await mount();
    button("Delete all").click();
    await settle();
    expect(vi.mocked(confirm).mock.calls[0]?.[0]).toContain("Delete all 5 memories");
    expect(deleted.sort()).toEqual(["a", "b", "c", "d", "e"]);
  });
});
