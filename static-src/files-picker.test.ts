import { describe, it, expect, vi, afterEach, beforeAll, beforeEach } from "vitest";

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

vi.mock("./upload.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Upload>()),
  uploadFiles: vi.fn(),
}));

import type * as ApiClient from "./api-client.js";
import type * as Modals from "./modals.js";
import type * as Upload from "./upload.js";
import { apiGetOrError } from "./api-client.js";
import { uploadFiles } from "./upload.js";
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

describe("the Upload button", () => {
  // The upload mock never answers, and an in-flight upload disables the button for the next test.
  afterEach(async () => {
    for (const [opts] of vi.mocked(uploadFiles).mock.calls) {
      opts.onError?.("the test ended", []);
    }
    vi.mocked(uploadFiles).mockClear();
    await vi.waitFor(() => {
      expect(byId<HTMLButtonElement>("filepicker-upload").disabled).toBe(false);
    });
  });

  it("opens the dialog on an input in the document and uploads what it picks to the open folder", async () => {
    openFilePicker(undefined, "/a");
    const file = new File(["x"], "note.txt");
    let connected: boolean | null = null;
    const click = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(function (
      this: HTMLInputElement,
    ) {
      connected = this.isConnected;
      const dt = new DataTransfer();
      dt.items.add(file);
      this.files = dt.files;
      this.dispatchEvent(new Event("change"));
    });
    try {
      byId("filepicker-upload").click();
    } finally {
      click.mockRestore();
    }
    expect(connected).toBe(true);
    await vi.waitFor(() => {
      expect(uploadFiles).toHaveBeenCalledOnce();
    });
    const opts = vi.mocked(uploadFiles).mock.calls[0]?.[0];
    expect(opts?.targetDir).toBe("/a");
    expect(Array.from(opts?.files ?? [])).toEqual([file]);
  });

  it("keeps the picker open when the OS dialog is dismissed, and says no file was attached", async () => {
    const real = await vi.importActual<typeof Modals>("./modals.js");
    const modal = byId<HTMLDivElement>("filepicker-modal");
    const onClose = vi.fn();
    real.onModalClose(modal, onClose);
    openFilePicker(undefined, "/a");
    real.openModal(modal);
    const dlg = modal.closest("dialog");
    const opened: HTMLInputElement[] = [];
    const click = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(function (
      this: HTMLInputElement,
    ) {
      opened.push(this);
    });
    try {
      byId("filepicker-upload").click();
    } finally {
      click.mockRestore();
    }
    try {
      expect(dlg?.open).toBe(true);
      const input = opened[0];
      if (input === undefined) {
        throw new Error("the Upload press opened no dialog");
      }
      // Shaped like the platform's: HTML "show the picker, if applicable" fires a bubbling cancel.
      input.dispatchEvent(new Event("cancel", { bubbles: true }));
      expect(dlg?.classList.contains("is-leaving")).toBe(false);
      expect(onClose).not.toHaveBeenCalled();
      await vi.waitFor(() => {
        const shown = [...document.querySelectorAll(".uip-toast")].map((t) => t.textContent);
        expect(shown.some((t) => t.includes("No file was attached."))).toBe(true);
      });
      expect(dlg?.open).toBe(true);
      expect(onClose).not.toHaveBeenCalled();
    } finally {
      real.closeModal(modal);
    }
  });
});
