// Every time or date the client shows is formatted by locale-time.ts. A file that reaches for a
// date API itself bypasses the browser-language rule that module owns, so this scans each
// production source for the APIs that format a time.

import { readdirSync, readFileSync } from "node:fs";
import { relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, it, expect } from "vitest";

const ROOT = fileURLToPath(new URL(".", import.meta.url));
const MODULE = "locale-time.ts";
const SKIPPED_DIRS = new Set(["node_modules", "testdata", "__test-helpers__", "e2e-sse", "dist"]);

/** Files that call `.toLocaleString(` on a number (a count or a byte total), never on a date. */
const NUMBER_FORMATTERS = new Set([
  "account-usage.ts",
  "context-breakdown.ts",
  "fundamentals/reasoning.ts",
  "memories.ts",
  "permission.ts",
  "settings.ts",
  "textsearch/copy.ts",
  "tool-card.ts",
]);

const BANNED_EVERYWHERE: readonly [RegExp, string][] = [
  [/\btoLocaleTimeString\b/, "toLocaleTimeString"],
  [/\btoLocaleDateString\b/, "toLocaleDateString"],
  [/\bIntl\.DateTimeFormat\b/, "Intl.DateTimeFormat"],
  [/\bIntl\.RelativeTimeFormat\b/, "Intl.RelativeTimeFormat"],
  [/\b(?:timeStyle|dateStyle|hour12|hourCycle)\b/, "a fixed hour cycle or style option"],
  [
    /\.(?:getHours|getUTCHours|getMinutes|getUTCMinutes|toTimeString|toUTCString)\(/,
    "a hand-built time",
  ],
];

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`\\])\/\/.*$/gm, "$1");
}

/** What a production file does that bypasses the shared formatter. */
function violations(file: string, source: string): string[] {
  if (file === MODULE) {
    const code = stripComments(source);
    return /DateTimeFormat\(\s*["'`]/.test(code) || /\b(?:hour12|hourCycle)\b/.test(code)
      ? [`${file}: the shared formatter passes a literal locale or forces the hour cycle`]
      : [];
  }
  const code = stripComments(source);
  const found: string[] = [];
  for (const [pattern, what] of BANNED_EVERYWHERE) {
    if (pattern.test(code)) {
      found.push(`${file}: ${what}`);
    }
  }
  if (/\.toLocaleString\(/.test(code) && !NUMBER_FORMATTERS.has(file)) {
    found.push(`${file}: toLocaleString outside the number-formatting files`);
  }
  return found;
}

function productionFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = `${dir}${entry.name}`;
    if (entry.isDirectory()) {
      if (!SKIPPED_DIRS.has(entry.name) && !entry.name.startsWith(".")) {
        out.push(...productionFiles(`${path}${sep}`));
      }
    } else if (
      entry.name.endsWith(".ts") &&
      !entry.name.endsWith(".test.ts") &&
      !entry.name.endsWith(".d.ts")
    ) {
      out.push(path);
    }
  }
  return out;
}

describe("the shared time formatter", () => {
  const files = productionFiles(ROOT).map((p) => relative(ROOT, p).split(sep).join("/"));

  it("is the only code that formats a time or a date", () => {
    expect(files).toContain(MODULE);
    const found = files.flatMap((f) => violations(f, readFileSync(`${ROOT}${f}`, "utf8")));
    expect(found).toEqual([]);
  });

  it("lists only number-formatting files that still call toLocaleString", () => {
    const stale = [...NUMBER_FORMATTERS].filter(
      (f) => !/\.toLocaleString\(/.test(stripComments(readFileSync(`${ROOT}${f}`, "utf8"))),
    );
    expect(stale).toEqual([]);
  });
});

describe("the scan", () => {
  it.each([
    ["a time string", "x.toLocaleTimeString(undefined, {})"],
    ["a date string", "x.toLocaleDateString()"],
    ["Intl.DateTimeFormat", "new Intl.DateTimeFormat('fr-FR')"],
    ["a forced 12-hour clock", "const o = { hour12: true };"],
    ["a hand-built clock", "`${d.getHours()}:${d.getMinutes()}`"],
    ["a date through toLocaleString", "when.toLocaleString()"],
  ])("flags %s in a client file", (_, source) => {
    expect(violations("some-view.ts", source)).not.toEqual([]);
  });

  it("flags a literal locale or a forced hour cycle in the shared module itself", () => {
    expect(violations(MODULE, "new Intl.DateTimeFormat('en-US', o)")).not.toEqual([]);
    expect(violations(MODULE, "new Intl.DateTimeFormat(l, { hour12: false })")).not.toEqual([]);
    expect(violations(MODULE, "new Intl.DateTimeFormat(l, o)")).toEqual([]);
  });

  it("ignores a mention in a comment", () => {
    expect(violations("some-view.ts", "// never toLocaleTimeString here\nconst a = 1;")).toEqual(
      [],
    );
  });
});
