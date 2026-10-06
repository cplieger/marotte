import { describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";

import {
  clipboardFiles,
  installComposerPaste,
  pastedImageName,
  pastedTextName,
  prepareForUpload,
  shouldSpillPaste,
} from "./composer-paste.js";

describe("pastedImageName", () => {
  it("is unique per second and sorts chronologically", () => {
    const a = pastedImageName("image/png", new Date("2026-08-15T08:42:11.000Z"));
    const b = pastedImageName("image/png", new Date("2026-08-15T08:42:12.000Z"));
    expect(a).toBe("pasted-2026-08-15T08-42-11.png");
    expect(a < b).toBe(true);
  });

  it("carries no colon, which is illegal in a filename on some platforms", () => {
    expect(pastedImageName("image/png")).not.toContain(":");
  });

  it("matches the extension to the encoded type", () => {
    // The server keys its MIME lookup off the extension, so a .png name on JPEG bytes inlines with the wrong mimeType.
    expect(pastedImageName("image/jpeg")).toMatch(/\.jpg$/);
    expect(pastedImageName("image/webp")).toMatch(/\.webp$/);
    // The server decodes an image by content, so any other type may be named .png.
    expect(pastedImageName("image/gif")).toMatch(/\.png$/);
  });
});

describe("pastedTextName", () => {
  it("shares the image stamp format, so both conventions read alike", () => {
    const at = new Date("2026-08-15T08:42:11.000Z");
    expect(pastedTextName(at)).toBe("paste-2026-08-15T08-42-11.txt");
    expect(pastedImageName("image/png", at)).toBe("pasted-2026-08-15T08-42-11.png");
  });

  it("is collision-free per second, which the server's overwrite makes load-bearing", () => {
    // atomicfile.WriteReaderInRoot renames over an existing destination, so a fixed name would destroy the previous paste.
    const a = pastedTextName(new Date("2026-08-15T08:42:11.000Z"));
    const b = pastedTextName(new Date("2026-08-15T08:42:12.000Z"));
    expect(a).not.toBe(b);
    expect(a < b).toBe(true);
    expect(a).not.toContain(":");
  });
});

describe("clipboardFiles", () => {
  const asDT = (files: File[]): DataTransfer => ({ files }) as unknown as DataTransfer;

  it("returns nothing for a text paste, so the event falls through", () => {
    expect(clipboardFiles(asDT([]))).toEqual({ images: [], others: [] });
    expect(clipboardFiles(null)).toEqual({ images: [], others: [] });
  });

  it("splits images from other files on the same clipboard, preserving order", () => {
    const png = new File(["x"], "a.png", { type: "image/png" });
    const txt = new File(["x"], "a.txt", { type: "text/plain" });
    const pdf = new File(["x"], "b.pdf", { type: "application/pdf" });
    expect(clipboardFiles(asDT([txt, png, pdf]))).toEqual({
      images: [png],
      others: [txt, pdf],
    });
  });

  it("keeps a pasted non-image file rather than dropping it", () => {
    const pdf = new File(["x"], "report.pdf", { type: "application/pdf" });
    expect(clipboardFiles(asDT([pdf])).others).toEqual([pdf]);
  });
});

describe("shouldSpillPaste", () => {
  const lines = (n: number): string =>
    Array.from({ length: n }, (_, i) => `line ${String(i)}`).join("\n");

  const cases: [string, string, boolean][] = [
    ["a short paste stays inline", "hello", false],
    ["a few lines stay inline", lines(5), false],
    ["exactly 50 lines stays inline", lines(50), false],
    ["51 lines spills", lines(51), true],
    ["exactly 10000 characters stays inline", `a\n${"b".repeat(9998)}`, false],
    ["over 10000 characters spills", `a\n${"b".repeat(9999)}`, true],
    ["an empty paste stays inline", "", false],
  ];

  for (const [name, text, want] of cases) {
    it(name, () => {
      expect(shouldSpillPaste(text)).toBe(want);
    });
  }

  it("keeps a long single-line paste inline whatever its length", () => {
    // A URL, JWT or API key: spilling one to a file would hide what the user pasted it to look at.
    const jwt = `eyJhbGciOiJIUzI1NiJ9.${"x".repeat(60_000)}.sig`;
    expect(jwt.length).toBeGreaterThan(10_000);
    expect(shouldSpillPaste(jwt)).toBe(false);
  });

  it("spills a long line only once it also carries a newline", () => {
    const long = "x".repeat(20_000);
    expect(shouldSpillPaste(long)).toBe(false);
    expect(shouldSpillPaste(`${long}\n`)).toBe(true);
  });
});

describe("prepareForUpload", () => {
  it("renames every clipboard image", () => {
    // Clipboard images are all "image.png" and the server's write replaces a collision.
    const a = new File(["x"], "image.png", { type: "image/png" });
    const b = new File(["y"], "image.png", { type: "image/png" });
    const [pa, pb] = prepareForUpload([a, b]);
    expect(pa?.name).toMatch(/^pasted-.*\.png$/);
    expect(pb?.name).toMatch(/^pasted-.*\.png$/);
    expect(pa?.type).toBe("image/png");
  });

  it("uploads a screenshot wider than 2000px at its own resolution", async () => {
    // The server fits an inlined image to its limits, so the client keeps the full-size screenshot.
    const canvas = new OffscreenCanvas(2400, 8);
    const ctx = canvas.getContext("2d");
    if (ctx === null) {
      throw new Error("no 2d context");
    }
    ctx.fillStyle = "#3a6";
    ctx.fillRect(0, 0, 2400, 8);
    const blob = await canvas.convertToBlob({ type: "image/png" });
    const shot = new File([blob], "image.png", { type: "image/png" });
    const [out] = prepareForUpload([shot]);
    if (out === undefined) {
      throw new Error("no file prepared");
    }
    const bmp = await createImageBitmap(out);
    expect(bmp.width).toBe(2400);
    bmp.close();
    expect(new Uint8Array(await out.arrayBuffer())).toEqual(
      new Uint8Array(await shot.arrayBuffer()),
    );
  });
});

describe("installComposerPaste", () => {
  async function paste(
    dt: { files?: File[]; text?: string },
    target: HTMLElement,
    onFiles: Mock<(files: FileList) => void>,
  ): Promise<Event> {
    installComposerPaste(target, onFiles);
    const event = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "clipboardData", {
      value: {
        files: dt.files ?? [],
        getData: (type: string) => (type === "text/plain" ? (dt.text ?? "") : ""),
      },
    });
    target.dispatchEvent(event);
    await Promise.resolve();
    return event;
  }

  function textarea(): HTMLTextAreaElement {
    const t = document.createElement("textarea");
    document.body.appendChild(t);
    return t;
  }

  it("leaves a small text paste to the browser", async () => {
    const onFiles = vi.fn();
    const event = await paste({ text: "just a line" }, textarea(), onFiles);
    expect(onFiles).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("uploads a pasted non-image file under the name the user chose", async () => {
    const onFiles = vi.fn();
    const pdf = new File(["x"], "report.pdf", { type: "application/pdf" });
    const event = await paste({ files: [pdf] }, textarea(), onFiles);
    expect(event.defaultPrevented).toBe(true);
    const got = onFiles.mock.calls.at(-1)?.[0] as FileList;
    expect(Array.from(got, (f) => f.name)).toEqual(["report.pdf"]);
  });

  it("renames a pasted image but not a pasted file on the same clipboard", async () => {
    const onFiles = vi.fn();
    const png = new File(["x"], "image.png", { type: "image/png" });
    const pdf = new File(["x"], "report.pdf", { type: "application/pdf" });
    await paste({ files: [png, pdf] }, textarea(), onFiles);
    const names = Array.from(onFiles.mock.calls.at(-1)?.[0] as FileList, (f) => f.name);
    expect(names[0]).toMatch(/^pasted-.*\.png$/);
    expect(names[1]).toBe("report.pdf");
  });

  it("spills a large text paste to a .txt instead of into the textarea", async () => {
    const onFiles = vi.fn();
    const big = Array.from({ length: 200 }, (_, i) => `line ${String(i)}`).join("\n");
    const target = textarea();
    const event = await paste({ text: big }, target, onFiles);
    expect(event.defaultPrevented).toBe(true);
    const got = onFiles.mock.calls.at(-1)?.[0] as FileList;
    expect(got.length).toBe(1);
    const file = got[0];
    expect(file?.name).toMatch(/^paste-.*\.txt$/);
    expect(file?.type).toBe("text/plain");
    await expect(file?.text()).resolves.toBe(big);
    // .txt takes the server's path-reference branch, so the agent reads the file with its file tools.
    expect(file?.name.endsWith(".txt")).toBe(true);
  });

  it("keeps a long single-line secret in the box where it can be edited", async () => {
    const onFiles = vi.fn();
    const jwt = `eyJhbGciOiJIUzI1NiJ9.${"x".repeat(60_000)}.sig`;
    const event = await paste({ text: jwt }, textarea(), onFiles);
    expect(onFiles).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("prefers the files on a clipboard carrying both files and large text", async () => {
    const onFiles = vi.fn();
    const pdf = new File(["x"], "report.pdf", { type: "application/pdf" });
    const big = Array.from({ length: 200 }, (_, i) => `line ${String(i)}`).join("\n");
    await paste({ files: [pdf], text: big }, textarea(), onFiles);
    const names = Array.from(onFiles.mock.calls.at(-1)?.[0] as FileList, (f) => f.name);
    expect(names).toEqual(["report.pdf"]);
  });

  it("installs exactly one listener, so nothing double-fires", async () => {
    // Two paste listeners would both run and both preventDefault; this module owns the only one.
    const target = textarea();
    const spy = vi.spyOn(target, "addEventListener");
    installComposerPaste(target, vi.fn());
    expect(spy.mock.calls.filter(([type]) => type === "paste")).toHaveLength(1);
  });
});
