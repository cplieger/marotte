// Policy lint: user-initiated mutations go through the actions framework (defineAction /
// apiAction / transportAction); background reads, cleanup and infrastructure stay silent. A
// write-shaped call (`void|await apiPost/apiDelete`, `await apiPutOrError`, `void|await
// transport.send`/`transportSend`) outside actions/, api-client.ts, transport.ts, tests and
// BACKGROUND_ALLOWLIST fails. A new mutation is an action in actions/<area>.ts.

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const ROOT = join(import.meta.dirname, "..");

/** Files where a write-shaped call is permitted as a background path or cleanup, each with a
 *  one-line reason. Matched by basename, so entries must be unique filenames. */
const BACKGROUND_ALLOWLIST = new Set<string>([
  "forge-auth.ts", // await apiPost in revalidateInBackground (probe per forge)

  // OAuth poll loop with its own status/error UI.
  "forge-auth-oauth.ts",

  // Modal dialogs surface errors inline.
  "modals.ts",

  "git-prs-tab.ts", // AI PR-description generation: the error surfaces in the dialog status line.
]);

/** Forbidden patterns: `void apiX(` (fire-and-forget) and `await apiX(` (bypassing the framework). */
const PATTERNS: { name: string; re: RegExp }[] = [
  { name: "void apiPost", re: /\bvoid\s+apiPost\s*[<(]/g },
  { name: "void apiDelete", re: /\bvoid\s+apiDelete\s*[<(]/g },
  { name: "void transport.send", re: /\bvoid\s+transport\.send\s*\(/g },
  { name: "await transport.send", re: /\bawait\s+transport\.send\s*\(/g },
  { name: "void transportSend", re: /\bvoid\s+transportSend\s*\(/g },
  { name: "await transportSend", re: /\bawait\s+transportSend\s*\(/g },
  { name: "await apiPost", re: /\bawait\s+apiPost\s*[<(]/g },
  { name: "await apiDelete", re: /\bawait\s+apiDelete\s*[<(]/g },
  { name: "await apiPutOrError", re: /\bawait\s+apiPutOrError\s*[<(]/g },
];

function listTSFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) {
      if (name === "node_modules" || name === ".vitest-cache" || name === "actions") {
        continue;
      }
      listTSFiles(p, out);
    } else if (name.endsWith(".ts") && !name.endsWith(".test.ts") && !name.endsWith(".d.ts")) {
      out.push(p);
    }
  }
  return out;
}

describe("action framework — regression guard", () => {
  it("no new `void apiX(` or `void transport.send(` outside allowlist", () => {
    const violations: string[] = [];
    for (const file of listTSFiles(ROOT)) {
      const rel = relative(ROOT, file);
      const base = rel.split("/").pop() ?? rel;
      if (BACKGROUND_ALLOWLIST.has(base)) {
        continue;
      }
      if (base === "api-client.ts" || base === "transport.ts") {
        continue;
      }
      const src = readFileSync(file, "utf8");
      for (const { name, re } of PATTERNS) {
        for (const m of src.matchAll(re)) {
          const lineIdx = src.slice(0, m.index).split("\n").length;
          violations.push(`${rel}:${String(lineIdx)}: ${name} (${m[0].trim()})`);
        }
      }
    }
    if (violations.length > 0) {
      const message = [
        "User-initiated mutations must go through the actions framework.",
        "See web/static-src/actions/index.ts.",
        "If this is a legitimate background poll, add the file to BACKGROUND_ALLOWLIST in actions/lint.node.test.ts.",
        "",
        "Violations:",
        ...violations.map((v) => `  ${v}`),
      ].join("\n");
      throw new Error(message);
    }
    expect(violations).toEqual([]);
  });
});
