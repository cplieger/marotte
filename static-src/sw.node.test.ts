// The worker never writes the app badge: the count is a fold over server tab state that the page
// re-asserts on every relayed push and click (attention.ts).

import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, relative } from "node:path";
import ts from "typescript";
import { describe, it, expect } from "vitest";

const ROOT = import.meta.dirname;
const BADGE_WRITE = /\b(setAppBadge|clearAppBadge|app_badge)\b/;
const RESOLUTION: ts.CompilerOptions = {
  module: ts.ModuleKind.ESNext,
  moduleResolution: ts.ModuleResolutionKind.Bundler,
};

/** Every module specifier a file names: imports and re-exports (type-only ones too, which only
 *  widens the scan), side-effect imports, and `import()` or `require()` calls. A computed
 *  specifier answers "<dynamic>", which resolves to nothing and so fails the scan. */
function specifiers(file: string, source: string): string[] {
  const sf = ts.createSourceFile(file, source, ts.ScriptTarget.ESNext, true);
  const out: string[] = [];
  const walk = (node: ts.Node): void => {
    if (
      (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) &&
      node.moduleSpecifier !== undefined &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      out.push(node.moduleSpecifier.text);
    } else if (
      ts.isCallExpression(node) &&
      (node.expression.kind === ts.SyntaxKind.ImportKeyword ||
        (ts.isIdentifier(node.expression) && node.expression.text === "require"))
    ) {
      const arg = node.arguments[0];
      out.push(arg !== undefined && ts.isStringLiteral(arg) ? arg.text : "<dynamic>");
    }
    node.forEachChild(walk);
  };
  walk(sf);
  return out;
}

/** Every file reachable from `entry` through any module edge, packages included, resolved as
 *  tsconfig's "bundler" resolution does; `unresolved` names each edge the walk could not follow. */
function workerGraph(entry: string): { sources: Map<string, string>; unresolved: string[] } {
  const sources = new Map<string, string>();
  const unresolved: string[] = [];
  const queue = [entry];
  for (let file = queue.shift(); file !== undefined; file = queue.shift()) {
    if (sources.has(file)) {
      continue;
    }
    const text = readFileSync(file, "utf8");
    sources.set(file, text);
    for (const spec of specifiers(file, text)) {
      const resolved = ts.resolveModuleName(spec, file, RESOLUTION, ts.sys).resolvedModule;
      if (resolved === undefined || resolved.extension === ts.Extension.Dts) {
        unresolved.push(`${relative(ROOT, file)}: ${spec}`);
        continue;
      }
      queue.push(resolved.resolvedFileName);
    }
  }
  return { sources, unresolved };
}

function badgeWriters(sources: ReadonlyMap<string, string>, root: string): string[] {
  return [...sources]
    .filter(([, text]) => BADGE_WRITE.test(text))
    .map(([file]) => relative(root, file));
}

describe("the service worker and the app badge", () => {
  const worker = workerGraph(join(ROOT, "sw.ts"));

  it("writes no badge in any module the worker bundle can contain", () => {
    expect(worker.unresolved).toEqual([]);
    expect(badgeWriters(worker.sources, ROOT)).toEqual([]);
  });

  it("follows the worker into its packages, so an empty answer means something", () => {
    const reached = [...worker.sources.keys()];
    expect(reached).toContain(join(ROOT, "push-subject.ts"));
    // A package path may come back through its realpath, so only its tail is pinned.
    expect(reached.filter((f) => f.endsWith("/@cplieger/keyenc/src/keyenc.ts"))).toHaveLength(1);
  });

  it("reports a badge write reached through a side-effect import, a re-export and import()", () => {
    const dir = mkdtempSync(join(tmpdir(), "sw-badge-"));
    try {
      const files: Record<string, string> = {
        "entry.ts": 'import "./side.js";\n',
        "side.ts": 'export { lazy } from "./reexport.js";\n',
        "reexport.ts": 'export const lazy = (): Promise<unknown> => import("./write.js");\n',
        "write.ts": "void navigator.setAppBadge(3);\n",
      };
      for (const [name, body] of Object.entries(files)) {
        writeFileSync(join(dir, name), body);
      }
      const planted = workerGraph(join(dir, "entry.ts"));
      expect(planted.unresolved).toEqual([]);
      expect(badgeWriters(planted.sources, dir)).toEqual(["write.ts"]);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it("refuses an edge it cannot follow rather than skipping it", () => {
    const dir = mkdtempSync(join(tmpdir(), "sw-badge-"));
    try {
      writeFileSync(join(dir, "entry.ts"), 'const name = "./x.js";\nvoid import(name);\n');
      expect(workerGraph(join(dir, "entry.ts")).unresolved).toHaveLength(1);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
