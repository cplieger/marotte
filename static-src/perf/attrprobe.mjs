// The decode share of a profiled window: which part of the page's busy CPU-profile samples ran in
// each decode family (one family above 10% is worth moving off the main thread), with every frame
// mapped through the served bundle's sourcemap.
import { TraceMap, originalPositionFor } from "@jridgewell/trace-mapping";

// First match wins, so a sample counts once. A family is a source path a frame maps to, and for the
// two JSON families the native parse those sources call.
const FAMILIES = [
  { name: "frame-json", parse: true, source: "static-src/sse-adapter.ts" },
  { name: "fetch-json", parse: true, source: "@cplieger/reactive/src/fetch/" },
  { name: "validators", source: "static-src/wire/validators.gen.ts" },
  { name: "markdown", source: "@cplieger/ui/src/content/markdown/" },
  { name: "markdown", source: "static-src/markdown.ts" },
  { name: "wire-binary", source: "@cplieger/web-terminal-engine/src/wire-binary.ts" },
];
const IDLE = new Set(["(idle)"]);
const isParse = (f) =>
  f.url === "" && (f.functionName === "parse" || f.functionName === "JSON.parse");

async function mapper() {
  const maps = new Map();
  const load = async (url) => {
    try {
      const r = await fetch(`${url}.map`);
      return r.ok ? new TraceMap(await r.text()) : null;
    } catch {
      return null;
    }
  };
  return async (f) => {
    if (!f.url.startsWith("http")) {
      return "";
    }
    if (!maps.has(f.url)) {
      maps.set(f.url, await load(f.url));
    }
    const m = maps.get(f.url);
    if (!m) {
      return null;
    }
    // A profile's call frames count lines and columns from 0, a sourcemap's lines from 1.
    return originalPositionFor(m, { line: f.lineNumber + 1, column: f.columnNumber }).source ?? "";
  };
}

/** Each family's share of the profile's busy samples, the busy sample count, and the samples
 *  whose script served no sourcemap (their families are unknown, so a share can read low). */
export async function decodeShares(profile) {
  const map = await mapper();
  const nodes = new Map(profile.nodes.map((n) => [n.id, n]));
  const parent = new Map();
  for (const n of profile.nodes) {
    for (const c of n.children ?? []) {
      parent.set(c, n.id);
    }
  }
  const sources = new Map();
  for (const n of profile.nodes) {
    sources.set(n.id, await map(n.callFrame));
  }
  const family = new Map();
  const familyOf = (id) => {
    if (family.has(id)) {
      return family.get(id);
    }
    const stack = [];
    for (let at = id; at !== undefined; at = parent.get(at)) {
      stack.push(at);
    }
    const leafParse = isParse(nodes.get(id).callFrame);
    const hit = FAMILIES.find((fam) =>
      stack.some((s) => sources.get(s)?.includes(fam.source) && (!fam.parse || leafParse)),
    );
    const unmapped = stack.some((s) => sources.get(s) === null);
    const out = { name: hit?.name ?? null, unmapped };
    family.set(id, out);
    return out;
  };
  const counts = {};
  let busy = 0;
  let unmapped = 0;
  for (const id of profile.samples) {
    if (IDLE.has(nodes.get(id).callFrame.functionName)) {
      continue;
    }
    busy++;
    const f = familyOf(id);
    if (f.unmapped) {
      unmapped++;
    }
    if (f.name) {
      counts[f.name] = (counts[f.name] ?? 0) + 1;
    }
  }
  const shares = {};
  for (const { name } of FAMILIES) {
    shares[name] = busy ? Math.round(((counts[name] ?? 0) / busy) * 10000) / 10000 : 0;
  }
  return { busy, unmapped, shares };
}
