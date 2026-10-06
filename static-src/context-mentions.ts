// The `#` context menu's pure half. The server resolves each token at send (internal/command/context_mentions.go),
// so TOKEN_RE here and `mentionRe` there are one grammar and must change together.

/** A provider the `#` menu offers. `attach` opens the file picker instead of inserting a token. */
export type MentionProvider =
  "file" | "folder" | "attach" | "git" | "terminal" | "spec" | "steering" | "mcp";

export interface ProviderSpec {
  readonly id: MentionProvider;
  readonly label: string;
  readonly desc: string;
}

const PROVIDERS: readonly ProviderSpec[] = [
  { id: "file", label: "File", desc: "A workspace file; add :10-20 for a line range" },
  { id: "folder", label: "Folder", desc: "The entries of a workspace folder" },
  { id: "attach", label: "Attach file…", desc: "Upload a file and attach it" },
  { id: "git", label: "Git diff", desc: "A repository's staged and unstaged changes" },
  {
    id: "terminal",
    label: "Terminal",
    desc: "The shell panel's newest output; add :500 for more lines",
  },
  { id: "spec", label: "Spec", desc: "A spec's requirements, design and tasks" },
  { id: "steering", label: "Steering", desc: "One or more steering documents" },
  { id: "mcp", label: "MCP resource", desc: "A resource an MCP server offers" },
];

/** What follows a `#`: `provider` is null while still choosing; `query` is everything after the first colon. */
export interface HashQuery {
  readonly start: number;
  readonly head: string;
  readonly provider: MentionProvider | null;
  readonly query: string;
}

// `[` cannot follow the `#`, so a token already written never reopens the menu.
const HASH_RE = /(?:^|\s)#([a-z]*)(?::([^\s\]]*))?$/;

/** The `#` word ending at the caret, or null; editing inside a word never opens the menu. */
export function hashQuery(text: string, caret: number): HashQuery | null {
  const after = text.charAt(caret);
  if (after !== "" && !/\s/.test(after)) {
    return null;
  }
  const before = text.slice(0, caret);
  const m = HASH_RE.exec(before);
  if (m === null) {
    return null;
  }
  const head = m[1] ?? "";
  const start = m.index + (m[0].startsWith("#") ? 0 : 1);
  if (m[2] === undefined) {
    return { start, head, provider: null, query: "" };
  }
  const p = PROVIDERS.find((x) => x.id === head);
  return p === undefined ? null : { start, head, provider: p.id, query: m[2] };
}

export function matchProviders(filter: string): ProviderSpec[] {
  const q = filter.toLowerCase();
  const prefix: ProviderSpec[] = [];
  const inner: ProviderSpec[] = [];
  for (const p of PROVIDERS) {
    const label = p.label.toLowerCase();
    if (p.id.startsWith(q) || label.startsWith(q)) {
      prefix.push(p);
    } else if (p.id.includes(q) || label.includes(q)) {
      inner.push(p);
    }
  }
  return [...prefix, ...inner];
}

const TOKEN_RE = /#\[\[[a-z]+:[^\]\r\n]*\]\]/;

export function mentionable(query: string): boolean {
  return !/[\]\r\n]/.test(query);
}

export function mentionToken(
  provider: Exclude<MentionProvider, "attach">,
  query: string,
): string | null {
  return mentionable(query) ? `#[[${provider}:${query}]]` : null;
}

export function replaceSpan(
  text: string,
  start: number,
  end: number,
  insert: string,
): { value: string; caret: number } {
  return { value: text.slice(0, start) + insert + text.slice(end), caret: start + insert.length };
}

/** Whether `text` carries a token the server would resolve, which only a new turn does. */
export function carriesContextMention(text: string): boolean {
  return TOKEN_RE.test(text);
}

const FILE_RANGE_RE = /^(.+):(\d+-\d+)$/;

/**
 * A file token's query: `path`, then `:a-b` for a range; a path ending in `:` or `:a-b` takes one more `:`.
 * internal/command/testdata/file_query.json is the contract shared with the server's splitFileQuery.
 */
export function fileQuery(path: string, range: string): string {
  if (range !== "") {
    return `${path}:${range}`;
  }
  return path.endsWith(":") || /:\d+-\d+$/.test(path) ? `${path}:` : path;
}

export function splitFileQuery(query: string): { path: string; range: string } {
  const m = FILE_RANGE_RE.exec(query);
  if (m === null) {
    return { path: query.endsWith(":") ? query.slice(0, -1) : query, range: "" };
  }
  return { path: m[1] ?? "", range: m[2] ?? "" };
}

type Operator = "" | "+" | "#" | "." | "/" | ";" | "?" | "&";

interface OperatorRule {
  readonly first: string;
  readonly sep: string;
  readonly named: boolean;
  readonly ifemp: string;
  readonly reserved: boolean;
}

// RFC 6570 Appendix A.
const OPERATORS: Readonly<Record<Operator, OperatorRule>> = {
  "": { first: "", sep: ",", named: false, ifemp: "", reserved: false },
  "+": { first: "", sep: ",", named: false, ifemp: "", reserved: true },
  "#": { first: "#", sep: ",", named: false, ifemp: "", reserved: true },
  ".": { first: ".", sep: ".", named: false, ifemp: "", reserved: false },
  "/": { first: "/", sep: "/", named: false, ifemp: "", reserved: false },
  ";": { first: ";", sep: ";", named: true, ifemp: "", reserved: false },
  "?": { first: "?", sep: "&", named: true, ifemp: "=", reserved: false },
  "&": { first: "&", sep: "&", named: true, ifemp: "=", reserved: false },
};

interface VarSpec {
  readonly name: string;
  readonly prefix: number | null;
}

interface Expression {
  readonly op: Operator;
  readonly vars: readonly VarSpec[];
}

/** A parsed template: its variables in order, each once, and its expansion. */
export interface UriTemplate {
  readonly vars: readonly string[];
  expand(values: Readonly<Record<string, string>>): string;
}

const VARNAME_RE = /^(?:[A-Za-z0-9_]|%[0-9A-Fa-f]{2})+(?:\.(?:[A-Za-z0-9_]|%[0-9A-Fa-f]{2})+)*$/;
const VARSPEC_RE = /^([^:*]+)(?::([1-9][0-9]{0,3})|(\*))?$/;

function parseExpression(body: string): Expression | null {
  const lead = body.charAt(0);
  const op: Operator = Object.hasOwn(OPERATORS, lead) && lead !== "" ? (lead as Operator) : "";
  const list = op === "" ? body : body.slice(1);
  if (list === "") {
    return null;
  }
  const vars: VarSpec[] = [];
  for (const raw of list.split(",")) {
    const m = VARSPEC_RE.exec(raw);
    const name = m?.[1];
    if (m === null || name === undefined || !VARNAME_RE.test(name)) {
      return null;
    }
    vars.push({ name, prefix: m[2] === undefined ? null : Number(m[2]) });
  }
  return { op, vars };
}

const UNRESERVED_RE = /^[A-Za-z0-9\-._~]$/u;
const RESERVED_RE = /^[:/?#[\]@!$&'()*+,;=]$/u;
const PCT_RE = /^%[0-9A-Fa-f]{2}$/;
const utf8 = new TextEncoder();

function pctEncode(value: string, allowReserved: boolean): string {
  // Code points, which RFC 6570 counts and encodes.
  const chars = Array.from(value);
  let out = "";
  for (let i = 0; i < chars.length; i++) {
    const ch = chars[i] ?? "";
    if (UNRESERVED_RE.test(ch) || (allowReserved && RESERVED_RE.test(ch))) {
      out += ch;
      continue;
    }
    const triplet = chars.slice(i, i + 3).join("");
    if (allowReserved && PCT_RE.test(triplet)) {
      out += triplet;
      i += 2;
      continue;
    }
    for (const b of utf8.encode(ch)) {
      out += `%${b.toString(16).toUpperCase().padStart(2, "0")}`;
    }
  }
  return out;
}

function expandExpression(e: Expression, values: Readonly<Record<string, string>>): string {
  const rule = OPERATORS[e.op];
  const parts: string[] = [];
  for (const v of e.vars) {
    if (!Object.hasOwn(values, v.name)) {
      continue;
    }
    const whole = values[v.name] ?? "";
    const value = v.prefix === null ? whole : Array.from(whole).slice(0, v.prefix).join("");
    const encoded = pctEncode(value, rule.reserved);
    if (!rule.named) {
      parts.push(encoded);
    } else {
      parts.push(value === "" ? v.name + rule.ifemp : `${v.name}=${encoded}`);
    }
  }
  return parts.length === 0 ? "" : rule.first + parts.join(rule.sep);
}

/**
 * Parse an RFC 6570 template, or null when malformed or using a reserved operator (`=`, `,`, `!`, `@`, `|`).
 * Values are strings, so the explode modifier is accepted and changes nothing.
 */
export function parseTemplate(template: string): UriTemplate | null {
  const pieces: (string | Expression)[] = [];
  let rest = template;
  while (rest !== "") {
    const open = rest.indexOf("{");
    const close = rest.indexOf("}");
    if (open === -1) {
      if (close !== -1) {
        return null;
      }
      pieces.push(rest);
      break;
    }
    if (close !== -1 && close < open) {
      return null;
    }
    const end = rest.indexOf("}", open);
    if (end === -1) {
      return null;
    }
    const expr = parseExpression(rest.slice(open + 1, end));
    if (expr === null) {
      return null;
    }
    pieces.push(rest.slice(0, open), expr);
    rest = rest.slice(end + 1);
  }
  const vars: string[] = [];
  for (const p of pieces) {
    if (typeof p !== "string") {
      for (const v of p.vars) {
        if (!vars.includes(v.name)) {
          vars.push(v.name);
        }
      }
    }
  }
  return {
    vars,
    expand: (values) =>
      pieces
        .map((p) => (typeof p === "string" ? pctEncode(p, true) : expandExpression(p, values)))
        .join(""),
  };
}
