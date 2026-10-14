import { describe, expect, it } from "vitest";
import { manifestSheets } from "./__test-helpers__/css-rules.js";

/** Every declaration in a sheet with the chain of selectors it is nested under. A `;` inside a
 *  data URL splits one value into fragments, which match none of the patterns below. */
function declarations(css: string): { chain: string[]; decl: string }[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const stack: string[] = [];
  const out: { chain: string[]; decl: string }[] = [];
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === "{") {
      stack.push(text.slice(start, i).trim());
      start = i + 1;
    } else if (c === ";" || c === "}") {
      const decl = text.slice(start, i).trim();
      if (decl !== "") {
        out.push({ chain: [...stack], decl });
      }
      if (c === "}") {
        stack.pop();
      }
      start = i + 1;
    }
  }
  return out;
}

const NEUTRAL_EDGE = /^(0|none|1px solid (var\(--c-border\)|transparent))$/;

/** A declaration drawing a coloured leading edge: a leading border wider than a 1px neutral
 *  rule, a coloured leading-border longhand, or an inset shadow offset along the inline axis. */
function isLeadingEdge(decl: string): boolean {
  const m = /^([a-z-]+)\s*:\s*([\s\S]+)$/.exec(decl);
  if (m === null) {
    return false;
  }
  const prop = m[1] ?? "";
  const value = (m[2] ?? "").trim();
  if (/^border-(inline-start|left)$/.test(prop)) {
    return !NEUTRAL_EDGE.test(value);
  }
  if (/^border-(inline-start|left)-width$/.test(prop)) {
    return !/^(0|1px)$/.test(value);
  }
  if (/^border-(inline-start|left)-color$/.test(prop)) {
    return value !== "transparent";
  }
  if (prop === "box-shadow") {
    const inset = /^inset\s+(-?[\d.]+)/.exec(value);
    return inset !== null && Number.parseFloat(inset[1] ?? "0") !== 0;
  }
  return false;
}

describe("a notice is a hue wash of its state, never a coloured edge", () => {
  it("has no rule in the shipped sheet drawing a coloured leading edge", () => {
    const offenders: string[] = [];
    for (const { name, css } of manifestSheets()) {
      for (const { chain, decl } of declarations(css)) {
        // Rendered Markdown's blockquote is the one leading edge: a typographic convention, not a notice.
        if (name === "13-messages.css" && chain.at(-1) === "& blockquote") {
          continue;
        }
        if (isLeadingEdge(decl)) {
          offenders.push(`${name}: ${chain.join(" ")} { ${decl} }`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });
});
