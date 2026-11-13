import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
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
let promptReply: ListReply;
const syncWrites: { on: boolean }[] = [];
let confirmAnswer = true;
const deleted: string[] = [];
const refusedDeletes = new Set<string>();
const updated: { id: string; edit: unknown }[] = [];
/** Holds the list read until released, with the reply it was asked under. */
let holdList: Promise<void> | null = null;

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTypedOrError: vi.fn(async (path: string, decode: (v: unknown) => unknown) => {
    const r =
      path === "/api/memory"
        ? listReply
        : path === "/api/memory/sync"
          ? syncReply
          : path === "/api/memory/import-prompt"
            ? promptReply
            : oneReply;
    if (path === "/api/memory" && holdList !== null) {
      await holdList;
    }
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
import { BUS_ENGINE_READY, dispatch, emitBus } from "./bus.js";
import { confirmText } from "./__test-helpers__/confirm-text.js";
import { _resetForTest as _resetModalForTest } from "./legacy-ui/modal.js";

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
  holdList = null;
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
  promptReply = comingSoon();
});

function comingSoon(): ListReply {
  return {
    ok: false,
    status: 409,
    data: null,
    error: "Memory import and export are not available for this account yet.",
    body: {
      error: "Memory import and export are not available for this account yet.",
      code: "coming_soon",
    },
  };
}

// A read the server refused while no engine was ready is asked again when the engine is back.
describe("the engine's return", () => {
  function reads(path: string): number {
    return vi.mocked(apiGetTypedOrError).mock.calls.filter((c) => c[0] === path).length;
  }

  it("re-asks a refused list, sync state and import probe while the tab is on screen", async () => {
    const answered = listReply;
    const answeredSync = syncReply;
    listReply = { ok: false, status: 502, data: null, error: "engine not ready" };
    syncReply = { ok: false, status: 502, data: null, error: "engine not ready" };
    promptReply = { ok: false, status: 502, data: null, error: "engine not ready" };
    await mount();
    expect(panel.textContent).toContain("Couldn't load memories.");
    listReply = answered;
    syncReply = answeredSync;
    promptReply = comingSoon();
    vi.mocked(apiGetTypedOrError).mockClear();

    emitBus(BUS_ENGINE_READY);
    await settle();

    expect(reads("/api/memory")).toBe(1);
    expect(reads("/api/memory/sync")).toBe(1);
    expect(reads("/api/memory/import-prompt")).toBe(1);
    expect(titlesIn("repo:org/one")).toEqual(["Newer", "Older"]);
    vi.mocked(apiGetTypedOrError).mockClear();
    emitBus(BUS_ENGINE_READY);
    await settle();
    expect(vi.mocked(apiGetTypedOrError), "reads that answered").not.toHaveBeenCalled();
  });

  it("reads the list once more after the engine came back during a refused read", async () => {
    const answered = listReply;
    listReply = { ok: false, status: 502, data: null, error: "engine not ready" };
    let release = (): void => undefined;
    holdList = new Promise((r) => {
      release = r;
    });
    await mount();
    emitBus(BUS_ENGINE_READY);
    emitBus(BUS_ENGINE_READY);
    await settle();
    expect(reads("/api/memory"), "no second read beside the one out").toBe(1);
    holdList = null;
    listReply = answered;
    release();
    await settle();
    expect(reads("/api/memory"), "one more read after it").toBe(2);
    expect(titlesIn("repo:org/one")).toEqual(["Newer", "Older"]);
  });

  it("asks nothing while the tab is off screen", async () => {
    listReply = { ok: false, status: 502, data: null, error: "engine not ready" };
    await mount();
    panel.classList.add("hidden");
    vi.mocked(apiGetTypedOrError).mockClear();

    emitBus(BUS_ENGINE_READY);
    await settle();

    expect(vi.mocked(apiGetTypedOrError)).not.toHaveBeenCalled();
  });
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

describe("Import and Export", () => {
  const PROMPT = "List every fact you remember about me, one JSON object per line.";
  const sent: { url: string; init: RequestInit | undefined }[] = [];
  let answer: (url: string) => Response;

  beforeEach(() => {
    sent.length = 0;
    answer = () => new Response("{}", { status: 200 });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const url = input instanceof Request ? input.url : String(input);
        sent.push({ url, init });
        return Promise.resolve(answer(url));
      }),
    );
  });

  afterEach(() => {
    for (const d of document.querySelectorAll("dialog")) {
      d.remove();
    }
    _resetModalForTest();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  function maybeButton(root: ParentNode, text: string): HTMLButtonElement | undefined {
    return [...root.querySelectorAll("button")].find((x) => x.textContent === text);
  }

  function dialog(): HTMLDialogElement {
    const d = document.querySelector<HTMLDialogElement>("dialog[open]");
    if (d === null) {
      throw new Error("no open dialog");
    }
    return d;
  }

  function pasteBox(): HTMLTextAreaElement {
    const box = dialog().querySelector<HTMLTextAreaElement>("textarea:not([readonly])");
    if (box === null) {
      throw new Error("no paste box");
    }
    return box;
  }

  async function openImport(): Promise<void> {
    promptReply = ok({ prompt: PROMPT });
    await mount();
    maybeButton(panel, "Import")?.click();
    await settle();
  }

  it("hides Import and Export while kiro-cli says coming soon", async () => {
    await mount();
    expect(maybeButton(panel, "Import")).toBeUndefined();
    expect(maybeButton(panel, "Export")).toBeUndefined();
    promptReply = ok({ prompt: PROMPT });
    await mount();
    expect(maybeButton(panel, "Import")).toBeDefined();
    expect(maybeButton(panel, "Export")).toBeDefined();
  });

  it("shows kiro-cli's prompt, read-only, to give to the other assistant", async () => {
    await openImport();
    const prompt = dialog().querySelector<HTMLTextAreaElement>("textarea[readonly]");
    expect(prompt?.value).toBe(PROMPT);
  });

  it("opens a prompt longer than its box at the first line", async () => {
    const long = Array.from({ length: 20 }, (_, i) => `Rule ${String(i + 1)} of the prompt.`).join(
      "\n",
    );
    promptReply = ok({ prompt: long });
    await mount();
    maybeButton(panel, "Import")?.click();
    await settle();
    const prompt = dialog().querySelector<HTMLTextAreaElement>("textarea[readonly]");
    expect(prompt?.scrollHeight).toBeGreaterThan(prompt?.clientHeight ?? Infinity);
    expect(prompt?.scrollTop).toBe(0);
  });

  it("the import dialog says an export is not an import file", async () => {
    await openImport();
    expect(dialog().textContent).toContain("A file from Export cannot be imported back.");
  });

  it("imports pasted lines and shows the per-line report", async () => {
    answer = () =>
      new Response(
        JSON.stringify({
          total_lines: 5,
          added: 2,
          duplicates: 1,
          skipped: [{ line: 2, reason: "duplicate_in_file" }],
          failed: [{ line: 3, reason: "parse_error", detail: "Unexpected token" }],
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      );
    await openImport();
    const lines = '{"a":1}\n{"a":1}\nnot json\n{"b":2}\n{"c":3}';
    pasteBox().value = lines;
    pasteBox().dispatchEvent(new Event("input"));
    maybeButton(dialog(), "Import")?.click();
    await settle();

    const post = sent.find((s) => s.url.endsWith("/api/memory/import"));
    expect(post?.init?.method).toBe("POST");
    expect(post?.init?.body).toBe(lines);
    expect(new Headers(post?.init?.headers).get("Content-Type")).toBe("text/plain; charset=utf-8");
    const report = dialog().querySelector(".cp-batch-report");
    expect(report?.querySelector("p")?.textContent).toBe("Added 2, 1 already stored.");
    const rows = [...(report?.querySelectorAll("li") ?? [])].map((li) =>
      [...li.children].map((c) => c.textContent),
    );
    expect(rows).toEqual([
      ["Line 2", "duplicate in the file"],
      ["Line 3", "not valid JSON: Unexpected token"],
    ]);
  });

  it("reads a chosen file into the paste box", async () => {
    await openImport();
    vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(function (
      this: HTMLInputElement,
    ) {
      const dt = new DataTransfer();
      dt.items.add(new File(['{"from":"file"}'], "memories.jsonl"));
      this.files = dt.files;
      this.dispatchEvent(new Event("change"));
    });
    maybeButton(dialog(), "Choose file")?.click();
    await settle();
    expect(pasteBox().value).toBe('{"from":"file"}');
  });

  it("Export downloads the document", async () => {
    answer = () =>
      new Response('{\n  "format": "kiro-memory"\n}', {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    const names: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      names.push(this.download);
    });
    promptReply = ok({ prompt: PROMPT });
    await mount();
    maybeButton(panel, "Export")?.click();
    await settle();
    const get = sent.find((s) => s.url.endsWith("/api/memory/export"));
    expect(get?.init?.method).toBe("GET");
    expect(names).toHaveLength(1);
    expect(names[0]).toMatch(/^kiro-memories-\d{4}-\d{2}-\d{2}\.json$/);
  });
});
