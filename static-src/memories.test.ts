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
let syncReply: ListReply;
const syncWrites: { on: boolean }[] = [];
let confirmAnswer = true;
const deleted: string[] = [];
const refusedDeletes = new Set<string>();
const updated: { id: string; edit: unknown }[] = [];

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTypedOrError: vi.fn(async (path: string, decode: (v: unknown) => unknown) => {
    const r =
      path === "/api/memory" ? listReply : path === "/api/memory/sync" ? syncReply : oneReply;
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
      return refusedDeletes.has(args.id) ? null : undefined;
    }),
  },
  setMemorySync: {
    dispatch: vi.fn(async (args: { on: boolean }) => {
      syncWrites.push(args);
      return {};
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
import { apiGetTypedOrError } from "./api-client.js";
import { dispatch } from "./bus.js";
import { confirmText } from "./__test-helpers__/confirm-text.js";

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
  refusedDeletes.clear();
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
  syncReply = ok({
    status: { state: "off" },
    choice: { enabled: false, account: false },
  });
  syncWrites.length = 0;
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
    expect(confirmText(vi.mocked(confirm).mock.calls[0]?.[0])).toContain("Delete all 5 memories");
    expect(deleted.sort()).toEqual(["a", "b", "c", "d", "e"]);
  });

  it("stops at a refused record and shows what is left, still selected", async () => {
    await mount();
    for (const id of ["a", "c", "e"]) {
      panel.querySelector<HTMLInputElement>(`[data-memory-id="${id}"] .memories-select`)?.click();
    }
    refusedDeletes.add("c");
    listReply = ok({
      memories: [record("c", null, "Loose"), record("e", "workspace", "Local")],
      cap: 1000,
    });
    button("Delete selected").click();
    await settle();
    expect(deleted).toEqual(["a", "c"]);
    expect(titlesIn("Unscoped")).toEqual(["Loose"]);
    expect(titlesIn("workspace")).toEqual(["Local"]);
    expect(button("Delete selected").textContent).toBe("Delete selected (2)");
  });
});

// memories_changed follows a write kiro-cli applied, late or from another device.
describe("a memory change kiro-cli reports", () => {
  it("refetches the list while the panel shows", async () => {
    await mount();
    listReply = ok({ memories: [record("f", "global", "Fresh")], cap: 1000 });
    dispatch({ type: "memories_changed", chat_id: "" });
    await settle();
    expect(titlesIn("global")).toEqual(["Fresh"]);
  });

  it("re-reads the open memory's body", async () => {
    await mount();
    panel.querySelector<HTMLButtonElement>('[data-memory-id="b"] .entry-open')?.click();
    await settle();
    oneReply = ok({ memory: { ...record("b", "repo:org/one", "Newer"), content: "landed late" } });
    dispatch({ type: "memories_changed", chat_id: "" });
    await settle();
    expect(panel.querySelector(".memories-content")?.textContent).toBe("landed late");
  });

  it("keeps what is being typed in an open editor", async () => {
    await mount();
    panel
      .querySelector<HTMLButtonElement>('[data-memory-id="b"] [aria-label="Edit Newer"]')
      ?.click();
    await settle();
    const body = panel.querySelector<HTMLTextAreaElement>(".memories-form textarea");
    if (body === null) {
      throw new Error("no body field");
    }
    body.value = "half typed";
    // Even a body read that would fail leaves the editor alone.
    oneReply = { ok: false, status: 502, data: null, error: "memory request failed" };
    dispatch({ type: "memories_changed", chat_id: "" });
    await settle();
    expect(panel.querySelector(".memories-form textarea")).toBe(body);
    expect(body.value).toBe("half typed");
  });

  it("shows the landed body when an editor open across the change is cancelled", async () => {
    await mount();
    panel
      .querySelector<HTMLButtonElement>('[data-memory-id="b"] [aria-label="Edit Newer"]')
      ?.click();
    await settle();
    oneReply = ok({ memory: { ...record("b", "repo:org/one", "Newer"), content: "landed late" } });
    dispatch({ type: "memories_changed", chat_id: "" });
    await settle();
    const cancel = [...panel.querySelectorAll<HTMLButtonElement>(".memories-form button")].find(
      (b) => b.textContent === "Cancel",
    );
    cancel?.click();
    await settle();
    expect(panel.querySelector(".memories-content")?.textContent).toBe("landed late");
  });

  it("fetches nothing while the panel is hidden", async () => {
    await mount();
    panel.classList.add("hidden");
    const before = vi.mocked(apiGetTypedOrError).mock.calls.length;
    dispatch({ type: "memories_changed", chat_id: "" });
    await settle();
    expect(vi.mocked(apiGetTypedOrError).mock.calls.length).toBe(before);
  });
});

describe("the Cloud sync section", () => {
  function status(): string {
    return panel.querySelector(".memories-cloud-status")?.textContent ?? "";
  }
  function cloudBoxes(): HTMLInputElement[] {
    return [...panel.querySelectorAll<HTMLInputElement>(".memories-cloud-switch")];
  }
  function cloudBox(): HTMLInputElement {
    const [b, ...rest] = cloudBoxes();
    if (b === undefined || rest.length > 0) {
      throw new Error(`want one sync switch, found ${String(cloudBoxes().length)}`);
    }
    return b;
  }

  it("states the sync and offers the account's switch alone", async () => {
    await mount();
    expect(status()).toBe("Cloud sync: off");
    expect(cloudBoxes()).toHaveLength(1);
    expect(cloudBox().checked).toBe(false);
    expect(cloudBox().disabled).toBe(false);
    expect(panel.querySelector(".memories-cloud-hint")).toBeNull();
  });

  it("counts what is waiting to upload, and shows the account's choice", async () => {
    syncReply = ok({
      status: { state: "synced", last_sync_at: "2026-11-06T10:00:00.000Z", pending: 3 },
      choice: { enabled: true, account: true },
    });
    await mount();
    expect(status()).toBe("Cloud sync: synced, 3 not uploaded yet");
    expect(cloudBox().checked).toBe(true);
  });

  it("names the layer that locks the choice and holds the switch", async () => {
    syncReply = ok({
      status: { state: "off" },
      choice: {
        enabled: false,
        account: false,
        lock: { layer: "organization", name: "Your organization" },
      },
    });
    await mount();
    expect(panel.querySelector(".memories-cloud-hint")?.textContent).toBe(
      "Locked by Your organization",
    );
    expect(cloudBox().disabled).toBe(true);
  });

  it("writes the account's choice when its switch moves", async () => {
    await mount();
    const box = cloudBox();
    box.checked = true;
    box.dispatchEvent(new Event("change"));
    await settle();
    expect(syncWrites).toEqual([{ on: true }]);
  });

  it("refetches when KAS reports the sync moved", async () => {
    await mount();
    syncReply = ok({
      status: { state: "unavailable", reason: "notSignedIn" },
      choice: { enabled: false, account: false },
    });
    dispatch({ type: "memory_sync_changed", chat_id: "" });
    await settle();
    expect(status()).toBe("Cloud sync: sign in to Kiro to sync");
  });

  it("marks a record the cloud has not taken yet", async () => {
    listReply = ok({
      memories: [
        { ...record("a", "global", "Waiting"), cloud_state: "pending" },
        record("b", "global", "Taken"),
      ],
      cap: 1000,
    });
    await mount();
    const marks = [...panel.querySelectorAll(".entry")].map(
      (r) => r.querySelector(".memories-pending")?.textContent ?? "",
    );
    expect(marks).toEqual(["Not synced", ""]);
  });
});
