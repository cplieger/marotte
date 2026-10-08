import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest";

// The served page, mounted above the imports: the real chat graph reads its hosts at module load, and the picker's
// own markup is then the shipped one.
await vi.hoisted(async () => {
  const { default: page } = await import("../static/index.html?raw");
  const doc = new DOMParser().parseFromString(page, "text/html");
  document.body.append(...doc.body.children);
});

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetOrError: vi.fn(),
}));
vi.mock("./modals.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Modals>()),
  openModal: vi.fn(),
  closeModal: vi.fn(),
}));

import type * as ApiClient from "./api-client.js";
import type * as Modals from "./modals.js";
import { apiGetOrError } from "./api-client.js";
import { initFilePicker, openFilePicker } from "./files-picker.js";

/** Per-path answers; an unlisted path answers an empty listing. */
const answers = new Map<string, unknown>();

function listing(...names: string[]): unknown {
  return {
    ok: true,
    status: 200,
    data: { files: names.map((name) => ({ name, isDir: false })), writable: true },
    error: "",
  };
}

function byId<T extends HTMLElement>(id: string): T {
  const found = document.getElementById(id);
  if (found === null) {
    throw new Error(`no #${id}`);
  }
  return found as T;
}

function list(): HTMLElement {
  return byId("filepicker-list");
}

beforeAll(() => {
  initFilePicker();
});

beforeEach(() => {
  answers.clear();
  vi.mocked(apiGetOrError).mockImplementation((url: string) => {
    const path = new URLSearchParams(url.slice(url.indexOf("?") + 1)).get("path") ?? "";
    return Promise.resolve((answers.get(path) ?? listing()) as never);
  });
});

describe("the file picker's listing", () => {
  it("shows a failure in the server's own words, as a notice and not a row", async () => {
    answers.set("/a/gone", { ok: false, status: 404, data: null, error: "not found" });
    openFilePicker(undefined, "/a/gone");
    await vi.waitFor(() => {
      expect(list().querySelector(".fb-notice")?.textContent).toContain("not found");
    });
    expect(list().querySelector(".fb-row")).toBeNull();
  });

  it("swaps a failure for the loading rows the moment Retry is pressed, until the answer", async () => {
    answers.set("/a/gone", { ok: false, status: 404, data: null, error: "not found" });
    openFilePicker(undefined, "/a/gone");
    await vi.waitFor(() => {
      expect(list().querySelector(".fb-notice")).not.toBeNull();
    });
    let release: (v: unknown) => void = () => undefined;
    answers.set(
      "/a/gone",
      new Promise((r) => {
        release = r;
      }),
    );
    const retry = [...list().querySelectorAll("button")].find((b) => b.textContent === "Retry");
    if (retry === undefined) {
      throw new Error("no Retry button");
    }
    retry.click();
    expect(list().querySelector(".fb-notice")).toBeNull();
    await vi.waitFor(() => {
      expect(list().querySelector(".fb-skeleton")).not.toBeNull();
    });
    expect(list().getAttribute("aria-busy")).toBe("true");
    release(listing("back.txt"));
    await vi.waitFor(() => {
      expect(list().querySelector('input[aria-label="Select back.txt"]')).not.toBeNull();
    });
    expect(list().querySelector(".fb-skeleton")).toBeNull();
    expect(list().hasAttribute("aria-busy")).toBe(false);
  });

  it("lands a typed file path on its folder with the file selected", async () => {
    answers.set("/a/note.txt", {
      ok: false,
      status: 400,
      data: null,
      error: "not a directory",
      code: "not_a_directory",
    });
    answers.set("/a", listing("note.txt", "other.txt"));
    openFilePicker(undefined, "/a");
    await vi.waitFor(() => {
      expect(list().querySelector('input[aria-label="Select note.txt"]')).not.toBeNull();
    });
    const path = byId<HTMLInputElement>("filepicker-path");
    path.click();
    path.value = "/a/note.txt";
    path.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await vi.waitFor(() => {
      expect(path.value).toBe("/a");
      expect(
        list().querySelector<HTMLInputElement>('input[aria-label="Select note.txt"]')?.checked,
      ).toBe(true);
    });
    expect(
      list().querySelector<HTMLInputElement>('input[aria-label="Select other.txt"]')?.checked,
    ).toBe(false);
    expect(byId("filepicker-attach").textContent).toBe("Attach 1 item");
    expect(list().querySelector(".fb-notice")).toBeNull();
  });

  it("says an empty mounts listing is empty, as a notice and not a row", async () => {
    openFilePicker(undefined, "/");
    await vi.waitFor(() => {
      expect(list().querySelector(".fb-notice")?.textContent).toBe("Empty");
    });
    expect(list().querySelector(".fb-row")).toBeNull();
  });
});
