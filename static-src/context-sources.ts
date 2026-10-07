// The `#` menu's item sources. Every query is a workspace-relative path (or `server:uri` for MCP), which is what the
// server confines a token to at send.

import { apiGetTyped } from "./api-client.js";
import { currentRepos } from "./git-status-store.js";
import { decodeResourceInfo, decodeResourceTemplateInfo } from "./mcp-state.js";
import type { MCPResourceInfo, MCPResourceTemplateInfo } from "./mcp-state.js";
import { fileQuery, mentionable, parseTemplate, splitFileQuery } from "./context-mentions.js";
import type { MentionProvider, UriTemplate } from "./context-mentions.js";
import { specDirOf } from "./spec-path.js";
import { getActiveId } from "./store.js";
import { asObject, decodeArray, reqStr } from "./validators.js";
import type { Decoder } from "./validators.js";
import { decodeFileSearchResult, decodeKiroDocsResponse } from "./wire/decoders.gen.js";
import type { KiroDoc } from "./wire/types.gen.js";
import { relToWorkspace, workspaceRoot } from "./workspace.js";

/** One row under a provider. A `template` row asks for its variables before it
 *  becomes a token; every other row's `query` is the token's query as is. */
export interface MentionItem {
  readonly label: string;
  readonly hint: string;
  readonly query: string;
  readonly template?: {
    readonly server: string;
    readonly info: MCPResourceTemplateInfo;
    readonly parsed: UriTemplate;
  };
}

const MAX_ITEMS = 20;

function matches(q: string, ...fields: string[]): boolean {
  const needle = q.toLowerCase();
  return fields.some((f) => f.toLowerCase().includes(needle));
}

function underWorkspace(abs: string): string | null {
  const rel = relToWorkspace(abs);
  return rel === abs || !mentionable(rel) ? null : rel;
}

/** null is a list the server could not give, which the menu offers to retry. */
type Read<T> = Promise<T | null>;

async function searchPaths(needle: string, kind: "name" | "dir"): Read<string[]> {
  const root = workspaceRoot();
  if (root === "" || needle.trim() === "") {
    return [];
  }
  // Quoted and escaped, so the name search reads the typed fragment as one literal rather than as terms and globs.
  // Ignored files stay mentionable (a gitignored `.agents/` holds task reports); node_modules stays pruned, which
  // also keeps the walk inside the endpoint's entry budget.
  const params = new URLSearchParams({
    path: root,
    q: `"${needle.replace(/[\\"]/g, "\\$&")}"`,
    ignored: "1",
    files: "!node_modules",
  });
  const res = await apiGetTyped(`/api/files/search?${params.toString()}`, decodeFileSearchResult);
  if (res === null) {
    return null;
  }
  const out: string[] = [];
  for (const m of res.matches) {
    const rel = m.kind === kind ? underWorkspace(m.path) : null;
    if (rel !== null && !out.includes(rel)) {
      out.push(rel);
    }
  }
  return out.slice(0, MAX_ITEMS);
}

async function fileItems(query: string): Read<MentionItem[]> {
  const { path, range } = splitFileQuery(query);
  const paths = await searchPaths(path, "name");
  return paths === null
    ? null
    : paths.map((p) => ({ label: p, hint: range, query: fileQuery(p, range) }));
}

async function folderItems(query: string): Read<MentionItem[]> {
  const root: MentionItem[] = matches(query, "workspace root", ".")
    ? [{ label: "Workspace root", hint: "", query: "." }]
    : [];
  const dirs = await searchPaths(query, "dir");
  return dirs === null ? null : [...root, ...dirs.map((d) => ({ label: d, hint: "", query: d }))];
}

function gitItems(query: string): MentionItem[] {
  return currentRepos()
    .filter((r) => r.is_repo && mentionable(r.repo))
    .map((r) => ({
      label: r.repo === "." ? "Workspace root" : r.repo,
      hint: r.branch,
      query: r.repo,
    }))
    .filter((it) => matches(query, it.label, it.query))
    .slice(0, MAX_ITEMS);
}

// defaultTerminalLines and maxTerminalLines in internal/command/context_mentions_terminal.go own these values.
const TERMINAL_DEFAULT_LINES = 200;
const TERMINAL_MAX_LINES = 5000;

/** One row for the shell's newest output: an empty query is the server's
 *  default count, a count above the server's cap is written as the cap. */
function terminalItems(query: string): MentionItem[] {
  const q = query.trim();
  if (q === "") {
    return [{ label: `Last ${String(TERMINAL_DEFAULT_LINES)} lines`, hint: "", query: "" }];
  }
  if (!/^\d+$/.test(q) || Number(q) < 1) {
    return [];
  }
  const n = Math.min(Number(q), TERMINAL_MAX_LINES);
  return [{ label: n === 1 ? "Last line" : `Last ${String(n)} lines`, hint: "", query: String(n) }];
}

let docs: Read<readonly KiroDoc[]> | null = null;

function kiroDocs(): Read<readonly KiroDoc[]> {
  const read = (docs ??= apiGetTyped("/api/workspace/kiro-docs", decodeKiroDocsResponse).then(
    (d) => d?.docs ?? null,
  ));
  void read.then((list) => {
    // A failed read is not kept, so the menu's retry asks again.
    if (list === null && docs === read) {
      docs = null;
    }
  });
  return read;
}

async function specItems(query: string): Read<MentionItem[]> {
  const list = await kiroDocs();
  if (list === null) {
    return null;
  }
  const out: MentionItem[] = [];
  for (const d of list) {
    const dir = d.category === "spec" ? specDirOf(relToWorkspace("/" + d.path)) : null;
    if (dir !== null && mentionable(dir) && !out.some((it) => it.query === dir)) {
      out.push({ label: d.group ?? dir, hint: dir, query: dir });
    }
  }
  return out.filter((it) => matches(query, it.label, it.query)).slice(0, MAX_ITEMS);
}

async function steeringItems(query: string): Read<MentionItem[]> {
  const list = await kiroDocs();
  if (list === null) {
    return null;
  }
  const out: MentionItem[] = [];
  for (const d of list) {
    const rel = d.category === "steering" ? relToWorkspace("/" + d.path) : "";
    if (rel.endsWith(".md") && !rel.startsWith("/") && mentionable(rel)) {
      out.push({ label: d.name, hint: rel, query: rel });
    }
  }
  return out.filter((it) => matches(query, it.label, it.query)).slice(0, MAX_ITEMS);
}

interface PoolServer {
  readonly name: string;
  readonly resources: MCPResourceInfo[];
  readonly resource_templates: MCPResourceTemplateInfo[];
}

const decodePoolServer: Decoder<PoolServer> = (v) => {
  const o = asObject(v, "$.mcp_pool.server");
  return {
    name: reqStr(o, "name", "$.mcp_pool.server"),
    resources: decodeArray(o["resources"], decodeResourceInfo, "$.mcp_pool.resources"),
    resource_templates: decodeArray(
      o["resource_templates"],
      decodeResourceTemplateInfo,
      "$.mcp_pool.resource_templates",
    ),
  };
};

const decodePool: Decoder<PoolServer[]> = (v) =>
  decodeArray(asObject(v, "$.mcp_pool")["servers"], decodePoolServer, "$.mcp_pool.servers");

// The active chat's own pool: a token resolves on the bridge its prompt is sent on.
async function mcpItems(query: string): Read<MentionItem[]> {
  const chatID = getActiveId();
  if (chatID === "") {
    return [];
  }
  const params = new URLSearchParams({ chat_id: chatID });
  const pool = await apiGetTyped(`/api/mcp/pool?${params.toString()}`, decodePool);
  if (pool === null) {
    return null;
  }
  const out: MentionItem[] = [];
  for (const { name: server, ...d } of pool) {
    for (const r of d.resources) {
      const q = `${server}:${r.uri}`;
      if (mentionable(q) && matches(query, r.name, r.uri, server)) {
        out.push({ label: r.name, hint: server, query: q });
      }
    }
    for (const t of d.resource_templates) {
      const parsed = parseTemplate(t.uri_template);
      if (parsed !== null && matches(query, t.name, t.uri_template, server)) {
        out.push({
          label: t.name,
          hint: `${server} · template`,
          query: "",
          template: { server, info: t, parsed },
        });
      }
    }
  }
  return out.slice(0, MAX_ITEMS);
}

/** The rows `provider` lists for `query`, or null when a read the list needs
 *  failed. Never rejects. */
export async function itemsFor(
  provider: Exclude<MentionProvider, "attach">,
  query: string,
): Read<MentionItem[]> {
  switch (provider) {
    case "file":
      return fileItems(query);
    case "folder":
      return folderItems(query);
    case "git":
      return gitItems(query);
    case "terminal":
      return terminalItems(query);
    case "spec":
      return specItems(query);
    case "steering":
      return steeringItems(query);
    case "mcp":
      return mcpItems(query);
  }
}

export function resetMentionSources(): void {
  docs = null;
}
