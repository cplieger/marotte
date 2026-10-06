import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { formatSize, joinPath, parentPath, sortEntries } from "./files-shared.js";
import { isSafeUrl } from "./utils-url.js";

describe("formatSize", () => {
  const cases: [number, string][] = [
    [0, "0 B"],
    [1, "1 B"],
    [512, "512 B"],
    [1023, "1023 B"],
    [1024, "1.0 KB"],
    [1536, "1.5 KB"],
    [10240, "10.0 KB"],
    [1048576, "1.0 MB"],
    [1572864, "1.5 MB"],
    [1073741824, "1.0 GB"],
    [1610612736, "1.5 GB"],
  ];

  for (const [input, expected] of cases) {
    it(`formats ${String(input)} bytes as "${expected}"`, () => {
      expect(formatSize(input)).toBe(expected);
    });
  }
});

// The space is container-absolute, not rootless.
describe("joinPath", () => {
  const cases: [string, string, string][] = [
    ["/", "workspace", "/workspace"],
    ["/", "file.txt", "/file.txt"],
    ["/workspace", "marotte", "/workspace/marotte"],
    ["/workspace/marotte", "static-src", "/workspace/marotte/static-src"],
    ["/workspace/", "marotte", "/workspace/marotte"],
    ["/workspace//", "marotte", "/workspace/marotte"],
  ];

  for (const [base, name, expected] of cases) {
    it(`joins "${base}" + "${name}" → "${expected}"`, () => {
      expect(joinPath(base, name)).toBe(expected);
    });
  }
});

describe("parentPath", () => {
  const cases: [string, string][] = [
    ["/", "/"],
    ["", "/"],
    ["/workspace", "/"],
    ["/workspace/marotte", "/workspace"],
    ["/workspace/marotte/static-src", "/workspace/marotte"],
    ["/a/b/c/d", "/a/b/c"],
  ];

  for (const [input, expected] of cases) {
    it(`parent of "${input}" → "${expected}"`, () => {
      expect(parentPath(input)).toBe(expected);
    });
  }
});

describe("isSafeUrl", () => {
  const safe: string[] = [
    "https://example.com",
    "http://localhost:8080/path",
    "mailto:user@example.com",
    "/relative/path",
    "./local",
    "#anchor",
    "//cdn.example.com/image.png",
  ];

  for (const url of safe) {
    it(`allows safe URL: "${url}"`, () => {
      expect(isSafeUrl(url)).toBe(true);
    });
  }

  const unsafe: [string, string][] = [
    ["javascript:alert(1)", "basic javascript:"],
    ["JAVASCRIPT:alert(1)", "uppercase javascript:"],
    ["JavaScript:void(0)", "mixed case javascript:"],
    ["java\tscript:alert(1)", "tab bypass javascript:"],
    ["java\nscript:alert(1)", "newline bypass javascript:"],
    ["java\rscript:alert(1)", "carriage return bypass"],
    ["java\x00script:alert(1)", "null byte bypass"],
    ["  javascript:alert(1)", "leading whitespace"],
    ["\x01javascript:alert(1)", "C0 control lead javascript:"],
    ["\x1fjavascript:alert(1)", "high C0 control lead javascript:"],
    [" \x01 javascript:alert(1)", "C0 control between spaces javascript:"],
    ["\x01data:text/html,x", "C0 control lead data:"],
    ["vbscript:MsgBox", "basic vbscript:"],
    ["VBSCRIPT:run", "uppercase vbscript:"],
    ["data:text/html,<script>alert(1)</script>", "basic data:"],
    ["  data:text/html,...", "leading whitespace data:"],
    ["file:///etc/passwd", "basic file:"],
    ["FILE:///etc/shadow", "uppercase file:"],
    ["vscode://file/workspace/main.go", "unapproved vscode:"],
    ["blob:https://example.com/id", "unapproved blob:"],
    ["tel:+1234567890", "unapproved tel:"],
  ];

  for (const [url, desc] of unsafe) {
    // eslint-disable-next-line no-control-regex -- defensive check
    it(`blocks unsafe URL (${desc}): "${url.replace(/[\x00-\x1f]/g, "·")}"`, () => {
      expect(isSafeUrl(url)).toBe(false);
    });
  }
});

describe("sortEntries", () => {
  const cases: { desc: string; input: { name: string; isDir: boolean }[]; expected: string[] }[] = [
    {
      desc: "directories before files",
      input: [
        { name: "file.txt", isDir: false },
        { name: "dir", isDir: true },
      ],
      expected: ["dir", "file.txt"],
    },
    {
      desc: "alphabetical within same type",
      input: [
        { name: "banana", isDir: false },
        { name: "apple", isDir: false },
        { name: "cherry", isDir: false },
      ],
      expected: ["apple", "banana", "cherry"],
    },
    {
      desc: "dirs sorted among dirs, files among files",
      input: [
        { name: "z-file", isDir: false },
        { name: "b-dir", isDir: true },
        { name: "a-file", isDir: false },
        { name: "a-dir", isDir: true },
      ],
      expected: ["a-dir", "b-dir", "a-file", "z-file"],
    },
    {
      desc: "empty array",
      input: [],
      expected: [],
    },
    {
      desc: "single entry",
      input: [{ name: "only", isDir: false }],
      expected: ["only"],
    },
  ];

  for (const { desc, input, expected } of cases) {
    it(desc, () => {
      const result = sortEntries(input);
      expect(result.map((e) => e.name)).toEqual(expected);
    });
  }

  it("does not mutate the original array", () => {
    const original = [
      { name: "b", isDir: false },
      { name: "a", isDir: true },
    ];
    const copy = [...original];
    sortEntries(original);
    expect(original).toEqual(copy);
  });
});

describe("isSafeUrl property-based", () => {
  const blockedPrefixes = ["javascript:", "vbscript:", "data:", "file:"] as const;

  it("no false negatives: blocked prefix + suffix is always rejected", () => {
    fc.assert(
      fc.property(fc.constantFrom(...blockedPrefixes), fc.string(), (prefix, suffix) => {
        expect(isSafeUrl(prefix + suffix)).toBe(false);
      }),
      { numRuns: 500 },
    );
  });

  // The WHATWG URL parser strips every leading C0 control or space before the scheme, so the gate strips at least as much.
  it("no false negatives: a C0 control or space lead is stripped before the scheme", () => {
    const lead = fc
      .array(
        fc.integer({ min: 0x00, max: 0x20 }).map((c) => String.fromCharCode(c)),
        { minLength: 1, maxLength: 8 },
      )
      .map((chars) => chars.join(""));

    fc.assert(
      fc.property(lead, fc.constantFrom(...blockedPrefixes), fc.string(), (pre, prefix, suffix) => {
        expect(isSafeUrl(pre + prefix + suffix)).toBe(false);
      }),
      { numRuns: 500 },
    );
  });

  // The allowlist is the contract: any absolute scheme it does not name is refused.
  it("no false negatives: an absolute scheme outside the allowlist is rejected", () => {
    const alpha = fc.constantFrom(..."abcdefghijklmnopqrstuvwxyz".split(""));
    const schemeChar = fc.constantFrom(..."abcdefghijklmnopqrstuvwxyz0123456789+.-".split(""));
    const scheme = fc
      .tuple(alpha, fc.array(schemeChar, { maxLength: 12 }))
      .map(([head, rest]) => head + rest.join(""))
      .filter((s) => s !== "http" && s !== "https" && s !== "mailto");

    fc.assert(
      fc.property(scheme, fc.string(), (s, rest) => {
        expect(isSafeUrl(`${s}:${rest}`)).toBe(false);
      }),
      { numRuns: 1000 },
    );
  });

  // A scheme-less value resolves against the document's HTTP(S) location, so it is always allowed.
  it("no false positives: a scheme-less value is allowed", () => {
    const pathChar = fc.constantFrom(..."aZ0/._-~?&=%#".split(""));
    const tail = fc.array(pathChar, { maxLength: 20 }).map((chars) => chars.join(""));

    fc.assert(
      fc.property(fc.constantFrom("", "/", "./", "../", "#", "?", "//"), tail, (prefix, rest) => {
        expect(isSafeUrl(prefix + rest)).toBe(true);
      }),
      { numRuns: 1000 },
    );
  });
});
