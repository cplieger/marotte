// The terminal's two web faces as the bundle ships them. Four sites must agree with no compiler
// joining them (css/00-fonts.css, css/MANIFEST, the Dockerfile, shell.ts's SHELL_THEME); any
// disagreement silently falls back to platform monospace and the row-gap defect. Reads the SHIPPED
// files, deriving the Dockerfile side from its `# repin:` markers as scripts/dev-fonts.sh does. A
// NODE test: the Dockerfile is outside vitest's `server.fs.allow`, and style.css is gitignored
// build output, so the bundle is read from css/MANIFEST in order as cmd/bundle concatenates it.

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const read = (rel: string): string => readFileSync(join(here, rel), "utf8");

const FONTS_SHEET = "00-fonts.css";
const fontsCSS = read(`css/${FONTS_SHEET}`);
const manifest = read("css/MANIFEST");
const tokensCSS = read("css/01-tokens.css");
const dockerfile = read("../Dockerfile");

/** The two families, glyph overlay first — the order `shell.ts` asks for. */
const GLYPH_FAMILY = "Web Terminal Glyphs";
const TEXT_FAMILY = "Monaspace Neon NF";

/** The four descriptor sets each family declares. */
const PAIRS = [
  ["400", "normal"],
  ["700", "normal"],
  ["400", "italic"],
  ["700", "italic"],
] as const;

interface Face {
  family: string;
  weight: string;
  style: string;
  url: string;
  format: string;
  body: string;
}

/** Every `@font-face` block with the descriptors this suite rules on. Parsed, so a missing
 *  descriptor is a distinguishable failure rather than an unmatched rule. */
function faces(css: string): Face[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const out: Face[] = [];
  const at = /@font-face\s*\{/g;
  let m: RegExpExecArray | null;
  while ((m = at.exec(text)) !== null) {
    const open = m.index + m[0].length;
    const close = text.indexOf("}", open);
    expect(close, "an @font-face block is not closed").toBeGreaterThan(open);
    const body = text.slice(open, close);
    const decl = (name: string): string =>
      new RegExp(`${name}\\s*:\\s*([^;]+)`).exec(body)?.[1]?.trim() ?? "";
    const src = decl("src");
    out.push({
      family: decl("font-family").replace(/^["']|["']$/g, ""),
      weight: decl("font-weight"),
      style: decl("font-style"),
      url: /url\(\s*["']?([^"')]+)/.exec(src)?.[1] ?? "",
      format: /format\(\s*["']?([^"')]+)/.exec(src)?.[1] ?? "",
      body,
    });
    at.lastIndex = close;
  }
  return out;
}

/**
 * The file names the Dockerfile lands in `static/vendor/fonts/`, from its `# repin:` markers as
 * `scripts/dev-fonts.sh` reads them: `dest=` when set, else the url's last segment. Not the `RUN`
 * block, whose paths are shell-interpolated. Scoped to the two font deps.
 */
function dockerfileFontFiles(): string[] {
  const FONT_DEPS = ["githubnext/monaspace", "cplieger/web-terminal-glyphs"];
  const out: string[] = [];
  for (const line of dockerfile.split("\n")) {
    const marker = /^#\s*repin:\s*(.*)$/.exec(line.trim());
    if (marker === null) {
      continue;
    }
    const tok = (name: string): string | undefined =>
      marker[1]
        ?.split(/\s+/)
        .find((t) => t.startsWith(`${name}=`))
        ?.slice(name.length + 1);
    const dep = tok("dep");
    const url = tok("url");
    if (dep === undefined || url === undefined || !FONT_DEPS.includes(dep)) {
      continue;
    }
    out.push(tok("dest") ?? decodeURIComponent(url.split("/").pop() ?? ""));
  }
  return out;
}

describe("the faces reach the bundle at all", () => {
  it("lists 00-fonts.css in css/MANIFEST exactly once", () => {
    // A sheet absent from the manifest is absent from style.css, silently.
    const entries = manifest
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l !== "" && !l.startsWith("#"));
    expect(entries.filter((e) => e === FONTS_SHEET)).toEqual([FONTS_SHEET]);
  });
});

describe("00-fonts.css declares both families in full", () => {
  const all = faces(fontsCSS);

  it("declares eight faces, four per family", () => {
    expect(all).toHaveLength(8);
    expect(all.filter((f) => f.family === TEXT_FAMILY)).toHaveLength(4);
    expect(all.filter((f) => f.family === GLYPH_FAMILY)).toHaveLength(4);
  });

  it.each([TEXT_FAMILY, GLYPH_FAMILY])("covers every weight/style pair for %s once", (family) => {
    // Glyphs are geometry: all four descriptor sets plus `.term`'s `font-synthesis: none` stop the
    // browser synthesising a bold or oblique box-drawing corner.
    const got = faces(fontsCSS)
      .filter((f) => f.family === family)
      .map((f) => `${f.weight}/${f.style}`)
      .sort();
    expect(got).toEqual(PAIRS.map(([w, s]) => `${w}/${s}`).sort());
  });

  it("carries no metric override and no unicode-range on any face", () => {
    // No ascent/descent-override: WebKit treats both as preview, so on iOS they did nothing; the cell
    // background is the runs' `padding-block: 1px`. No unicode-range: the glyph file's coverage is
    // the range, and letters fall through to Monaspace.
    for (const f of faces(fontsCSS)) {
      const at = `${f.family} ${f.weight}/${f.style}`;
      expect(f.body, at).not.toMatch(/ascent-override|descent-override|line-gap-override/);
      expect(f.body, at).not.toMatch(/size-adjust/);
      expect(f.body, at).not.toMatch(/unicode-range/);
    }
  });

  it("blocks rather than swaps while a face loads", () => {
    // Cell metrics size the PTY grid, so a swap-in after first paint changes geometry mid-session.
    for (const f of faces(fontsCSS)) {
      expect(f.body, `${f.family} ${f.weight}/${f.style}`).toMatch(/font-display:\s*block/);
    }
  });
});

describe("every src names a file the Dockerfile writes", () => {
  const written = dockerfileFontFiles();

  it("finds the font pins in the Dockerfile", () => {
    // The precondition: with no markers matched, every membership assertion
    // below would be against an empty set and pass for the wrong reason.
    expect(written.length, "no # repin: marker names a font dep").toBeGreaterThan(0);
    expect(written).toContain("WebTerminalGlyphs.woff2");
  });

  it("serves each face from /vendor/fonts/ as woff2", () => {
    // `fontAssetPrefix` (server_static.go) is cached thirty days; woff2 is what the Dockerfile fetches.
    for (const f of faces(fontsCSS)) {
      const at = `${f.family} ${f.weight}/${f.style}`;
      expect(f.url, at).toMatch(/^\/vendor\/fonts\/[^/]+$/);
      expect(f.format, at).toBe("woff2");
    }
  });

  it("points every face at a pinned, digest-verified file", () => {
    // A url naming a file no marker pins is a production 404 and a silent fallback.
    const wanted = [...new Set(faces(fontsCSS).map((f) => f.url.split("/").pop() ?? ""))].sort();
    expect(wanted.filter((n) => !written.includes(n))).toEqual([]);
  });
});

describe("the served cell is the cell the glyphs are drawn for", () => {
  // The pairing no single release can check: the `.term` cell comes from the ARG-pinned library and
  // the glyphs from the ARG-pinned overlay release, so two independent Renovate PRs decide whether
  // they agree, and a cell move reopens the row gap behind a green build.
  const CELL_JSON_RELEASE = "v1.1.7";
  const CELL = { fontSize: "14px", lineHeight: "17px" } as const;

  const libraryCSS = read("node_modules/@cplieger/web-terminal-ui/css/02-terminal.css");

  /** The `.term` declarations, comments stripped first so a `}` inside one cannot
   *  end the body early. The rule is flat, so the first close is its own. */
  function termDecl(name: string): string {
    const text = libraryCSS.replace(/\/\*[\s\S]*?\*\//g, " ");
    const open = text.indexOf(":where(.wt-root) .term {");
    expect(open, "the library declares no :where(.wt-root) .term rule").toBeGreaterThanOrEqual(0);
    const body = text.slice(open, text.indexOf("}", open));
    return new RegExp(`${name}\\s*:\\s*([^;]+)`).exec(body)?.[1]?.trim() ?? "";
  }

  it("is still reading the release CELL was transcribed from", () => {
    // CELL is a transcription: cell.json is fetched at build time, so no test can
    // open it. Pinning the version it came from is what makes the transcription
    // safe — a glyph bump fails HERE until the new release's cell block is read.
    const arg = /^ARG WEB_TERMINAL_GLYPHS_VERSION=(\S+)$/m.exec(dockerfile)?.[1];
    expect(arg, "the Dockerfile pins no glyph release").toBe(CELL_JSON_RELEASE);
  });

  it("sizes the row at the contract's cell and synthesises nothing", () => {
    expect(termDecl("font-size")).toBe(CELL.fontSize);
    expect(termDecl("line-height")).toBe(CELL.lineHeight);
    // The other half of 00-fonts.css's four-descriptor-sets-per-family rule:
    // nothing to synthesise from, and nothing asked for.
    expect(termDecl("font-synthesis")).toBe("none");
  });
});

describe("the terminal stack stays scoped to the terminal", () => {
  it("keeps both terminal families out of the app-wide --font-mono", () => {
    // `createTerminal` sets `--font-mono` on the terminal ROOT; the app-wide token governs ~100
    // non-terminal sites and must not gain the terminal's family.
    const decls = [...tokensCSS.matchAll(/--font-mono\s*:\s*([^;]+)/g)].map((m) => m[1] ?? "");
    expect(decls.length, "01-tokens.css declares no --font-mono").toBeGreaterThan(0);
    for (const d of decls) {
      expect(d).not.toContain(GLYPH_FAMILY);
      expect(d).not.toContain(TEXT_FAMILY);
    }
  });
});
