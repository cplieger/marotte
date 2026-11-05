// Every time or date the client shows is formatted by locale-time.ts. A file that reaches for a
// date API itself bypasses the browser-language rule that module owns, so this scans each
// production source for the APIs that format a time.

import { readdirSync, readFileSync } from "node:fs";
import { relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, it, expect } from "vitest";

const ROOT = fileURLToPath(new URL(".", import.meta.url));
const MODULE = "locale-time.ts";
const SKIPPED_DIRS = new Set(["node_modules", "testdata", "__test-helpers__", "e2e-sse", "dist"]);

/** Files that call `.toLocaleString(` on a number (a count or a byte total), never on a date. The
 *  allowlist is per file, so a date `.toLocaleString()` added to one of them passes unseen. */
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

/** Names that format or take apart a time outside the shared module. */
const BANNED_NAMES = new Set([
  "toLocaleTimeString",
  "toLocaleDateString",
  "toDateString",
  "toTimeString",
  "toUTCString",
  "getHours",
  "getUTCHours",
  "getMinutes",
  "getUTCMinutes",
  "DateTimeFormat",
  "RelativeTimeFormat",
  "timeStyle",
  "dateStyle",
  "hour12",
  "hourCycle",
]);

/** What the shared module may use that no other file may. */
const MODULE_ALLOWED = new Set(["DateTimeFormat", "timeStyle", "dateStyle"]);

function sourceOf(file: string, source: string): ts.SourceFile {
  return ts.createSourceFile(file, source, ts.ScriptTarget.ESNext, true, ts.ScriptKind.TS);
}

/** The name a node spells, when it is an identifier or a string used as a property key. */
function nameOf(node: ts.Node): string | undefined {
  if (ts.isIdentifier(node)) {
    return node.text;
  }
  if (
    ts.isStringLiteral(node) &&
    ((ts.isPropertyAssignment(node.parent) && node.parent.name === node) ||
      (ts.isElementAccessExpression(node.parent) && node.parent.argumentExpression === node))
  ) {
    return node.text;
  }
  return undefined;
}

/** What a production file does that bypasses the shared formatter. Read off the AST, so a comment
 *  or a string that merely mentions an API is never a hit. */
function violations(file: string, source: string): string[] {
  const isModule = file === MODULE;
  const found: string[] = [];
  const walk = (node: ts.Node): void => {
    const name = nameOf(node);
    if (name !== undefined && BANNED_NAMES.has(name) && !(isModule && MODULE_ALLOWED.has(name))) {
      found.push(`${file}: ${name}`);
    }
    if (name === "toLocaleString" && !NUMBER_FORMATTERS.has(file)) {
      found.push(`${file}: toLocaleString outside the number-formatting files`);
    }
    if (
      isModule &&
      ts.isNewExpression(node) &&
      node.expression.getText() === "Intl.DateTimeFormat"
    ) {
      if (node.arguments?.[0]?.getText() !== "navigator.languages") {
        found.push(`${file}: DateTimeFormat does not take navigator.languages as its locale`);
      }
    }
    node.forEachChild(walk);
  };
  walk(sourceOf(file, source));
  return found;
}

function callsToLocaleString(file: string, source: string): boolean {
  let seen = false;
  const walk = (node: ts.Node): void => {
    seen ||= nameOf(node) === "toLocaleString";
    node.forEachChild(walk);
  };
  walk(sourceOf(file, source));
  return seen;
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
      (f) => !callsToLocaleString(f, readFileSync(`${ROOT}${f}`, "utf8")),
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
    ["a date string in English", "d.toDateString()"],
    ["a quoted option key", "const o = { 'hour12': true };"],
    [
      "a call after a slash-star inside a string",
      'const p = "src/**"; /** x */ d.toLocaleTimeString();',
    ],
    ["a call after slash-slash inside a string", 'const p = " // "; d.toLocaleTimeString();'],
  ])("flags %s in a client file", (_, source) => {
    expect(violations("some-view.ts", source)).not.toEqual([]);
  });

  it("flags a literal locale or a forced hour cycle in the shared module itself", () => {
    expect(violations(MODULE, "new Intl.DateTimeFormat('en-US', o)")).not.toEqual([]);
    expect(violations(MODULE, "new Intl.DateTimeFormat(['en-US'], o)")).not.toEqual([]);
    expect(violations(MODULE, "new Intl.DateTimeFormat(undefined, o)")).not.toEqual([]);
    expect(
      violations(MODULE, "new Intl.DateTimeFormat(navigator.languages, { hour12: false })"),
    ).not.toEqual([]);
    expect(violations(MODULE, "new Intl.DateTimeFormat(navigator.languages, o)")).toEqual([]);
  });

  it("ignores a mention in a comment", () => {
    expect(violations("some-view.ts", "// never toLocaleTimeString here\nconst a = 1;")).toEqual(
      [],
    );
  });
});
