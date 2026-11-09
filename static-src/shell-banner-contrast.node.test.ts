// The shell's connection banner text against its surface, measured by scripts/css-contrast.py in
// both themes from the app tokens shell.ts's SHELL_THEME hands the terminal.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { PUBLIC_THEME_TOKENS } from "@cplieger/web-terminal-ui/style-contract";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");
const shellSource = readFileSync(join(here, "shell.ts"), "utf8");

/** The app token SHELL_THEME maps a terminal token to, or undefined when it maps none. */
function themeToken(token: string): string | undefined {
  const escaped = token.replace(/[-]/g, "\\-");
  return new RegExp(`"${escaped}":\\s*"var\\((--c-[\\w-]+)\\)"`).exec(shellSource)?.[1];
}

/** Each theme's ratio for `fg` on `bg`. */
function ratios(fg: string, bg: string): { theme: string; ratio: number }[] {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  return out
    .trim()
    .split("\n")
    .map((line) => {
      const [theme = "", , , ratio = ""] = line.split("\t");
      return { theme, ratio: Number(ratio) };
    });
}

// Stryker's sandbox copies static-src alone, so a missing script is a skip.
describe.skipIf(!existsSync(script))("the shell's connection banner contrast", () => {
  it("holds the offline and restarted text to 4.5:1 on the banner surface in both themes", () => {
    const surface = themeToken("--surface");
    expect(surface, "SHELL_THEME maps --surface to an app token").toBeDefined();
    const misses: string[] = [];
    for (const token of ["--banner-offline", "--banner-restarted"]) {
      expect(PUBLIC_THEME_TOKENS, `${token} is a public theme token`).toContain(token);
      const fg = themeToken(token);
      expect(fg, `SHELL_THEME maps ${token} to an app token`).toBeDefined();
      const measured = ratios(fg!, surface!);
      expect(measured.map((m) => m.theme)).toEqual(["dark", "light"]);
      for (const m of measured) {
        if (!(m.ratio >= 4.5)) {
          misses.push(`${m.theme} ${token}: ${String(m.ratio)}:1`);
        }
      }
    }
    expect(misses, "WCAG 1.4.3 asks 4.5:1 of the banner's small text").toEqual([]);
  });
});
