// Writes the closed footer row's style table (`turn-footer-closed-row-css.test.ts`) from a base
// ref, so the pin holds what that ref paints and never what the tree under edit paints. Runs this
// tree's test file with `vitest -u` against `git archive <ref>`, borrowing this tree's
// node_modules. Usage: `npm run capture:closed-row [-- <ref>]`, default origin/dev.

import { execFileSync } from "node:child_process";
import { copyFileSync, mkdtempSync, rmSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const TEST = "turn-footer-closed-row-css.test.ts";
const TABLE = "testdata/turn-footer-closed-row-styles.json";

const ref = process.argv[2] ?? "origin/dev";
const pkg = resolve(import.meta.dirname, "..");
const tree = mkdtempSync(join(tmpdir(), "closed-row-"));
try {
  // The ref and the directory are positional parameters, never spliced into the script text.
  execFileSync("sh", ["-c", 'git archive --format=tar "$1" | tar -x -C "$2"', "sh", ref, tree], {
    cwd: resolve(pkg, ".."),
    stdio: "inherit",
  });
  const base = join(tree, "static-src");
  symlinkSync(join(pkg, "node_modules"), join(base, "node_modules"));
  copyFileSync(join(pkg, TEST), join(base, TEST));
  // `--update` last: it takes an optional value and would swallow a filter after it.
  execFileSync("npx", ["vitest", "run", TEST, "--project", "browser", "--update"], {
    cwd: base,
    stdio: "inherit",
  });
  copyFileSync(join(base, TABLE), join(pkg, TABLE));
  console.log(`captured ${TABLE} from ${ref}`);
} finally {
  rmSync(tree, { recursive: true, force: true });
}
