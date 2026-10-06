// Reads the shipped stylesheets as source, for facts computed style cannot answer (a synthetic
// hover drives no style recalc). Sheets are `?raw` imports, so importers run in the browser project.

import { expect } from "vitest";
import manifest from "../css/MANIFEST?raw";

const sheets = import.meta.glob<string>("../css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

// The manifest also pulls the ui-primitives base in from node_modules, and
// `import.meta.glob` needs a static pattern per directory.
const vendor = import.meta.glob<string>("../node_modules/@cplieger/ui-primitives/css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

/** One stylesheet from css/, by filename. */
export function loadCSS(name: string): string {
  const hit = sheets[`../css/${name}`];
  if (hit === undefined) {
    throw new Error(`no stylesheet ../css/${name}`);
  }
  return hit;
}

/** Every stylesheet `css/MANIFEST` declares, in declared order. Read rather than restated, so a
 *  sheet added to the bundle joins a sweep with no test edit. */
export function manifestSheets(): { name: string; css: string }[] {
  const names = manifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"));
  // An entry is relative to `css/`, so one that climbs out of it (the
  // ui-primitives base) resolves against the package.
  const text = (entry: string): string | undefined =>
    entry.startsWith("../") ? vendor[`../${entry.slice(3)}`] : sheets[`../css/${entry}`];

  const missing = names.filter((n) => text(n) === undefined);
  expect(missing, "every MANIFEST entry resolves to a stylesheet").toEqual([]);
  return names.map((n) => ({ name: n, css: text(n) ?? "" }));
}

/** The shipped stylesheet in `css/MANIFEST` order, as `cmd/bundle` concatenates it: that order
 *  decides this app's equal-specificity ties. */
function appCSS(): string {
  return manifestSheets()
    .map((s) => s.css)
    .join("\n");
}

/** Install the shipped stylesheet for a suite that measures real layout; remove the returned
 *  element in `afterAll`. */
export function mountAppCSS(): HTMLStyleElement {
  const style = document.createElement("style");
  style.textContent = appCSS();
  document.head.appendChild(style);
  return style;
}

/** Every style rule, at-rule bodies included, as (selector, body) pairs; a body includes its
 *  nested blocks. */
export function allRules(css: string): { selector: string; body: string }[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const out: { selector: string; body: string }[] = [];
  const scan = (src: string): void => {
    let depth = 0;
    let selStart = 0;
    let bodyStart = 0;
    let sel = "";
    for (let i = 0; i < src.length; i++) {
      if (src[i] === "{") {
        if (depth === 0) {
          sel = src.slice(selStart, i).trim();
          bodyStart = i + 1;
        }
        depth++;
      } else if (src[i] === "}") {
        depth--;
        if (depth === 0) {
          const body = src.slice(bodyStart, i);
          if (sel.startsWith("@")) {
            scan(body);
          } else {
            out.push({ selector: sel, body });
          }
          selStart = i + 1;
        }
      }
    }
  };
  scan(text);
  return out;
}

/** The body of a top-level rule by its exact selector line, nested `&` blocks included. */
export function ruleBody(css: string, selector: string): string {
  const at = css.indexOf(`\n${selector} {`);
  expect(at, `rule not found: ${selector}`).toBeGreaterThan(-1);
  const open = css.indexOf("{", at);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") {
      depth++;
    }
    if (css[i] === "}") {
      depth--;
      if (depth === 0) {
        return css.slice(open + 1, i);
      }
    }
  }
  throw new Error(`unbalanced braces after ${selector}`);
}

/** The body of the one at-rule whose prelude contains `prelude`. The fixed test viewport never
 *  matches a width query, so mounting the body unwrapped is how a suite computes the narrow arm.
 *  Exactly one match is required. */
export function atRuleBody(css: string, prelude: string): string {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const found: string[] = [];
  let depth = 0;
  let selStart = 0;
  let bodyStart = 0;
  let sel = "";
  for (let i = 0; i < text.length; i++) {
    if (text[i] === "{") {
      if (depth === 0) {
        sel = text.slice(selStart, i).trim();
        bodyStart = i + 1;
      }
      depth++;
    } else if (text[i] === "}") {
      depth--;
      if (depth === 0) {
        if (sel.startsWith("@") && sel.includes(prelude)) {
          found.push(text.slice(bodyStart, i));
        }
        selStart = i + 1;
      }
    }
  }
  expect(found.length, `expected exactly one at-rule matching ${prelude}`).toBe(1);
  const [only] = found;
  if (only === undefined) {
    // Unreachable: `expect` is not a type guard, so the throw is what types the return.
    throw new Error(`no at-rule matching ${prelude}`);
  }
  return only;
}

/** The one rule whose selector LIST contains `selector`, with its body; `@media` wrappers are
 *  descended. `scope`: "*" anywhere (default), "top" outside every at-rule, any other string
 *  inside an at-rule whose prelude contains it. Exactly one match is required, so a selector
 *  that gained a second home fails rather than silently picking the first. */
export function ruleContaining(
  css: string,
  selector: string,
  scope: string = "*",
): { selector: string; body: string } {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const found: { selector: string; body: string }[] = [];

  const scan = (src: string, inScope: boolean): void => {
    let depth = 0;
    let selStart = 0;
    let bodyStart = 0;
    let sel = "";
    for (let i = 0; i < src.length; i++) {
      if (src[i] === "{") {
        if (depth === 0) {
          sel = src.slice(selStart, i).trim();
          bodyStart = i + 1;
        }
        depth++;
      } else if (src[i] === "}") {
        depth--;
        if (depth === 0) {
          const body = src.slice(bodyStart, i);
          if (sel.startsWith("@")) {
            // "*" keeps whatever scope we were in; "top" leaves it on the way in;
            // a named scope is entered when this at-rule's prelude matches.
            const inner =
              scope === "*" ? inScope : scope === "top" ? false : inScope || sel.includes(scope);
            scan(body, inner);
          } else if (inScope) {
            const members = sel.split(",").map((s) => s.trim());
            if (members.includes(selector)) {
              found.push({ selector: sel, body });
            }
          }
          selStart = i + 1;
        }
      }
    }
  };
  scan(text, scope === "*" || scope === "top");

  expect(
    found.length,
    `expected exactly one rule listing ${selector} (scope: ${scope}), found ${found.length}`,
  ).toBe(1);
  const [only] = found;
  if (only === undefined) {
    // Unreachable: `expect` is not a type guard, so the throw is what types the return.
    throw new Error(`no rule listing ${selector} (scope: ${scope})`);
  }
  return only;
}
