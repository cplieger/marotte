#!/usr/bin/env node
// Usage: node internal/spec/testdata/gen-goldens.mjs <acp-server.js | kas.mjs>
// Runs Kiro's own tasks.md parser (the slice of the KAS bundle from
// `function _gt(e){` to `function VTr(`, evaluated in isolation and never
// written to the repo) over every file under testdata/inputs/, then models
// the Go parser's three additions (heading cut, U+2028/U+2029 rejection,
// widened near-miss count) and writes testdata/goldens/<name>.json.
// Synthetic inputs are .txt because they are deliberately malformed markdown
// and every *.md in the repo is linted.
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const inputsDir = join(here, "inputs");
const goldensDir = join(here, "goldens");
const MAX_NODES = 1000;
const DEFAULT_KAS_VERSION = "2.21.4";
const CRLF_TWINS = ["greenhouse-controller.tasks.md"];

const sha256 = (s) => createHash("sha256").update(s, "utf8").digest("hex");

function loadOracle(bundlePath) {
  const src = readFileSync(bundlePath, "utf8");
  const start = src.indexOf("function _gt(e){");
  const end = src.indexOf("function VTr(", start);
  if (start < 0 || end < 0) {
    throw new Error(`${bundlePath}: the five parser functions were not found`);
  }
  const slice = src.slice(start, end);
  const fns = new Function(`${slice}; return { _gt, Sgt, jTr, jqi, Llc, uw };`)();
  const m = bundlePath.match(/\/kas\/(\d+(?:\.\d+)+)-[0-9a-f]+\//);
  return { ...fns, sliceSha256: sha256(slice), kasVersion: m ? m[1] : DEFAULT_KAS_VERSION };
}

// The Go regex with `.` covering U+2028/U+2029 (dotAll), used only to keep
// such lines out of the near-miss count.
const goShape = /^(\s*)([-*+])\s+\[([ xX\-~])\](\\?\*?)\s+(.+)$/s;
const widened = /^\s*(?:[-*+]|\d+[.)])\s+\[[^\]]*\]/;
const heading = /^#{1,6}\s/;

function normalize(text) {
  return text.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
}

function statusOf(markdownStatus) {
  switch (markdownStatus) {
    case "completed":
      return { status: "completed", queued: false };
    case "in_progress":
      return { status: "in_progress", queued: false };
    case "queued":
      return { status: "pending", queued: true };
    default:
      return { status: "pending", queued: false };
  }
}

function detailOf(content) {
  if (content === "") return "";
  const ls = content.split("\n");
  const cut = ls.findIndex((l) => heading.test(l));
  if (cut >= 0) ls.length = cut;
  while (ls.length > 0 && ls[ls.length - 1] === "") ls.pop();
  return ls.join("\n");
}

function toNode(oracle, lines, t) {
  const info = oracle.jTr(lines[t.startLine]);
  return {
    id: `L${t.startLine + 1}`,
    line: t.startLine + 1,
    indent: info.indent,
    number: info.taskNumber ?? "",
    text: t.text,
    ...statusOf(t.markdownStatus),
    optional: t.isOptional,
    hash: sha256(t.text),
    detail: detailOf(t.content),
    children: t.subTasks.map((c) => toNode(oracle, lines, c)),
  };
}

function countNodes(nodes) {
  return nodes.reduce((n, node) => n + 1 + countNodes(node.children), 0);
}

function tally(nodes, p) {
  for (const n of nodes) {
    if (n.children.length > 0) {
      tally(n.children, p);
      continue;
    }
    if (n.optional) continue;
    p.total++;
    if (n.status === "completed") p.completed++;
    else if (n.status === "in_progress") p.in_progress++;
    else {
      p.pending++;
      if (n.queued) p.queued++;
    }
  }
}

function capped(nodes, budget) {
  const out = [];
  for (const n of nodes) {
    if (budget.left === 0) break;
    budget.left--;
    const kept = { ...n, children: capped(n.children, budget) };
    if (kept.children.length < n.children.length) kept.truncated_children = true;
    out.push(kept);
  }
  return out;
}

function parse(oracle, text) {
  const lines = normalize(text).split("\n");
  const tree = oracle.uw(text).map((t) => toNode(oracle, lines, t));
  const total = countNodes(tree);
  const progress = { pending: 0, in_progress: 0, completed: 0, queued: 0, total: 0 };
  tally(tree, progress);
  const unreadable = lines.filter(
    (l) => widened.test(l) && !oracle.jTr(l).isTask && !goShape.test(l),
  ).length;
  return {
    tasks: capped(tree, { left: MAX_NODES }),
    progress,
    unreadable_lines: unreadable,
    truncated: total > MAX_NODES ? { returned: MAX_NODES, total } : null,
  };
}

function writeGolden(oracle, inputFile, text, crlf) {
  const stem = basename(inputFile).replace(/\.(md|txt)$/, "");
  const name = crlf ? `${stem}.crlf.json` : `${stem}.json`;
  const fixture = {
    input_file: inputFile,
    crlf,
    kas_version: oracle.kasVersion,
    slice_sha256: oracle.sliceSha256,
    ...parse(oracle, crlf ? text.replace(/\n/g, "\r\n") : text),
  };
  writeFileSync(join(goldensDir, name), `${JSON.stringify(fixture, null, 2)}\n`);
  return name;
}

function main() {
  const bundlePath = process.argv[2];
  if (!bundlePath) {
    console.error("usage: node gen-goldens.mjs <acp-server.js | kas.mjs>");
    process.exit(2);
  }
  const oracle = loadOracle(bundlePath);
  const inputs = readdirSync(inputsDir)
    .filter((f) => !f.startsWith("."))
    .sort();
  const written = [];
  for (const f of inputs) {
    const text = readFileSync(join(inputsDir, f), "utf8");
    written.push(writeGolden(oracle, f, text, false));
    if (CRLF_TWINS.includes(f)) written.push(writeGolden(oracle, f, text, true));
  }
  console.log(
    `kas ${oracle.kasVersion} slice ${oracle.sliceSha256.slice(0, 12)}: ${written.length} goldens`,
  );
}

main();
