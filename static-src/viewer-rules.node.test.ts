import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

import {
  LABEL_GIT_REVISION,
  LABEL_NOT_UTF8,
  LABEL_TOOL_OUTPUT,
  rulesFor,
  type FileFacts,
} from "./viewer-rules.js";

const small = (over: Partial<Extract<FileFacts, { kind: "small" }>> = {}): FileFacts => ({
  kind: "small",
  binary: false,
  utf8: true,
  conflict: false,
  readOnly: false,
  size: 10,
  ...over,
});

describe("rulesFor", () => {
  it.each(["/w/a.ts", "/w/a.md", "/w/photo.png", "/w/blob.bin"])(
    "gives %s over the cap only Download",
    (path) => {
      expect(rulesFor({ kind: "large", size: 3 << 20 }, path, "read")).toEqual({
        view: "large",
        edit: false,
        highlight: false,
        live: false,
        download: true,
        readOnlyReason: null,
      });
    },
  );

  it("reads small text highlighted and editable, and edits it unhighlighted", () => {
    expect(rulesFor(small(), "/w/a.ts", "read")).toMatchObject({
      view: "text",
      edit: true,
      highlight: true,
      live: true,
    });
    expect(rulesFor(small(), "/w/a.ts", "edit")).toMatchObject({
      view: "text",
      edit: true,
      highlight: false,
    });
  });

  it("offers nothing to edit before the bytes are read", () => {
    expect(
      rulesFor(
        { kind: "unread", binary: false, utf8: true, readOnly: false, size: 1 },
        "/w/a.ts",
        "read",
      ),
    ).toMatchObject({ view: "text", edit: false });
  });

  it.each([
    ["a KAS tool output", small({ readOnly: true }), LABEL_TOOL_OUTPUT],
    ["non-UTF-8 text", small({ utf8: false }), LABEL_NOT_UTF8],
  ])("keeps %s read-only and says why", (_name, facts, label) => {
    expect(rulesFor(facts, "/w/a.txt", "edit")).toMatchObject({
      view: "text",
      edit: false,
      readOnlyReason: label,
    });
  });

  it("renders markdown to read and edits its source", () => {
    expect(rulesFor(small(), "/w/README.md", "read")).toMatchObject({
      view: "markdown",
      edit: true,
    });
    expect(rulesFor(small(), "/w/README.md", "edit")).toMatchObject({ view: "text" });
  });

  it("opens a file with markers in the conflict view, unless it cannot be edited", () => {
    expect(rulesFor(small({ conflict: true }), "/w/a.go", "read").view).toBe("conflict");
    expect(rulesFor(small({ conflict: true, readOnly: true }), "/w/a.go", "read").view).toBe(
      "text",
    );
  });

  it("names the image and binary views, each with Download and no Edit", () => {
    expect(rulesFor(small(), "/w/a.png", "read")).toMatchObject({
      view: "image",
      edit: false,
      download: true,
    });
    expect(rulesFor(small({ binary: true }), "/w/a.dat", "read")).toMatchObject({
      view: "binary",
      edit: false,
      download: true,
    });
  });

  it("never drops Download when only the named view changes", () => {
    for (const requested of ["read", "edit", "diff"] as const) {
      expect(rulesFor(small(), "/w/a.ts", requested).download).toBe(true);
    }
  });

  it("offers nothing before the first stat answers", () => {
    expect(rulesFor(null, "/w/a.ts", "read")).toEqual({
      view: "text",
      edit: false,
      highlight: false,
      live: false,
      download: false,
      readOnlyReason: null,
    });
  });
});

describe("a diff whose base git refused as text", () => {
  it("gives a binary base the binary view with Download and no Edit", () => {
    expect(rulesFor(small(), "/w/a.dat", "diff", "binary")).toEqual({
      view: "binary",
      edit: false,
      highlight: false,
      live: true,
      download: true,
      readOnlyReason: null,
    });
  });

  it("gives a base that is not UTF-8 one sentence, with Edit for an editable working copy", () => {
    expect(rulesFor(small(), "/w/a.txt", "diff", "not_utf8")).toEqual({
      view: "undiffable",
      edit: true,
      highlight: false,
      live: true,
      download: true,
      readOnlyReason: null,
    });
    expect(rulesFor(small({ readOnly: true }), "/w/a.txt", "diff", "not_utf8").edit).toBe(false);
  });

  // Over an image path too: the refused base decides, not the extension.
  it("names the view the base picks on an image path", () => {
    expect(rulesFor(small(), "/w/logo.svg", "diff", "binary").view).toBe("binary");
  });

  it("gives the sentence and no Download when no working copy has been described", () => {
    for (const base of ["binary", "not_utf8"] as const) {
      expect(rulesFor(null, "/w/a.dat", "diff", base)).toMatchObject({
        view: "undiffable",
        edit: false,
        download: false,
      });
    }
  });

  it("labels a diff vs HEAD with a text base Git revision, and a diff vs saved with nothing", () => {
    expect(rulesFor(small(), "/w/a.ts", "diff", "text")).toMatchObject({
      view: "diff",
      readOnlyReason: LABEL_GIT_REVISION,
    });
    expect(rulesFor(small(), "/w/a.ts", "diff").readOnlyReason).toBeNull();
  });

  it("leaves the base alone outside a diff, and the cap above everything", () => {
    expect(rulesFor(small(), "/w/a.ts", "read", "binary").view).toBe("text");
    expect(rulesFor({ kind: "large", size: 3 << 20 }, "/w/a.ts", "diff", "binary").view).toBe(
      "large",
    );
  });
});

// The client never compares a size with the viewer's cap: `large` is the server's verdict.
describe("the viewer cap has one owner", () => {
  it("appears in no client module", () => {
    const hits: string[] = [];
    const walk = (dir: string): void => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name);
        if (name === "node_modules" || name.startsWith(".")) {
          continue;
        }
        if (statSync(p).isDirectory()) {
          walk(p);
        } else if (name.endsWith(".ts") && !name.includes(".test.")) {
          const src = readFileSync(p, "utf8");
          if (/2097152|2 \* 1024 \* 1024|2 << 20/.test(src)) {
            hits.push(p);
          }
        }
      }
    };
    walk(import.meta.dirname);
    expect(hits).toEqual([]);
  });
});
