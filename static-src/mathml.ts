// LaTeX subset -> MathML, zero dependencies (MathML Core is native in every engine). `createElementNS`, never
// `createElement`: a `math` element in the XHTML namespace renders as text, and the renderer's `el()` cannot build
// this subtree, hence a leaf module. An unsupported expression degrades whole to its raw string: a partial render lies.
// The math font stack is 13-messages.css's.

/** The MathML namespace, exported so a test can assert the tree is in it (a wrong-namespace `<math>` has the right tag). */
export const MATHML_NS = "http://www.w3.org/1998/Math/MathML";

/** Longer than this is a paste, not mathematics; raw reads better. */
const MAX_SRC = 4096;

/** Guards a pathological `{{{{{...}}}}}` without a stack overflow. */
const MAX_DEPTH = 32;

/** Identifiers (`<mi>`): a value, not an operation. */
const IDENT_CMDS: Readonly<Record<string, string>> = {
  alpha: "\u03b1",
  beta: "\u03b2",
  gamma: "\u03b3",
  delta: "\u03b4",
  epsilon: "\u03b5",
  varepsilon: "\u03b5",
  zeta: "\u03b6",
  eta: "\u03b7",
  theta: "\u03b8",
  vartheta: "\u03d1",
  iota: "\u03b9",
  kappa: "\u03ba",
  lambda: "\u03bb",
  mu: "\u03bc",
  nu: "\u03bd",
  xi: "\u03be",
  pi: "\u03c0",
  varpi: "\u03d6",
  rho: "\u03c1",
  varrho: "\u03f1",
  sigma: "\u03c3",
  varsigma: "\u03c2",
  tau: "\u03c4",
  upsilon: "\u03c5",
  phi: "\u03c6",
  varphi: "\u03d5",
  chi: "\u03c7",
  psi: "\u03c8",
  omega: "\u03c9",
  Gamma: "\u0393",
  Delta: "\u0394",
  Theta: "\u0398",
  Lambda: "\u039b",
  Xi: "\u039e",
  Pi: "\u03a0",
  Sigma: "\u03a3",
  Upsilon: "\u03a5",
  Phi: "\u03a6",
  Psi: "\u03a8",
  Omega: "\u03a9",
  infty: "\u221e",
  partial: "\u2202",
  nabla: "\u2207",
  emptyset: "\u2205",
  varnothing: "\u2205",
  aleph: "\u2135",
  hbar: "\u210f",
  ell: "\u2113",
};

/** Relations, binary operators and punctuation (`<mo>`). */
const OP_CMDS: Readonly<Record<string, string>> = {
  times: "\u00d7",
  cdot: "\u22c5",
  div: "\u00f7",
  pm: "\u00b1",
  mp: "\u2213",
  leq: "\u2264",
  le: "\u2264",
  geq: "\u2265",
  ge: "\u2265",
  neq: "\u2260",
  ne: "\u2260",
  approx: "\u2248",
  equiv: "\u2261",
  sim: "\u223c",
  simeq: "\u2243",
  cong: "\u2245",
  propto: "\u221d",
  to: "\u2192",
  rightarrow: "\u2192",
  leftarrow: "\u2190",
  leftrightarrow: "\u2194",
  Rightarrow: "\u21d2",
  Leftarrow: "\u21d0",
  Leftrightarrow: "\u21d4",
  mapsto: "\u21a6",
  in: "\u2208",
  notin: "\u2209",
  ni: "\u220b",
  subset: "\u2282",
  subseteq: "\u2286",
  supset: "\u2283",
  supseteq: "\u2287",
  cup: "\u222a",
  cap: "\u2229",
  setminus: "\u2216",
  forall: "\u2200",
  exists: "\u2203",
  nexists: "\u2204",
  neg: "\u00ac",
  land: "\u2227",
  wedge: "\u2227",
  lor: "\u2228",
  vee: "\u2228",
  oplus: "\u2295",
  ominus: "\u2296",
  otimes: "\u2297",
  ast: "\u2217",
  star: "\u22c6",
  circ: "\u2218",
  bullet: "\u2219",
  ldots: "\u2026",
  dots: "\u2026",
  cdots: "\u22ef",
  vdots: "\u22ee",
  ddots: "\u22f1",
  angle: "\u2220",
  perp: "\u22a5",
  parallel: "\u2225",
  prime: "\u2032",
  bmod: "mod",
  lceil: "\u2308",
  rceil: "\u2309",
  lfloor: "\u230a",
  rfloor: "\u230b",
  langle: "\u27e8",
  rangle: "\u27e9",
};

/** Upright by MathML's multi-character `<mi>` rule. */
const FUNC_CMDS: ReadonlySet<string> = new Set([
  "sin",
  "cos",
  "tan",
  "cot",
  "sec",
  "csc",
  "arcsin",
  "arccos",
  "arctan",
  "sinh",
  "cosh",
  "tanh",
  "log",
  "ln",
  "exp",
  "det",
  "gcd",
  "deg",
  "dim",
  "ker",
  "arg",
]);

/** Scripts stack under and over in display mode, as TeX sets `\sum_{i=1}^{n}`. */
const UNDEROVER_CMDS: Readonly<Record<string, string>> = {
  sum: "\u2211",
  prod: "\u220f",
  coprod: "\u2210",
  bigcup: "\u22c3",
  bigcap: "\u22c2",
  lim: "lim",
  limsup: "lim sup",
  liminf: "lim inf",
  max: "max",
  min: "min",
  sup: "sup",
  inf: "inf",
};

/** Not in UNDEROVER_CMDS: TeX sets integral limits to the side even in display mode. */
const INTEGRAL_CMDS: Readonly<Record<string, string>> = {
  int: "\u222b",
  iint: "\u222c",
  iiint: "\u222d",
  oint: "\u222e",
};

/** In ems. */
const SPACE_CMDS: Readonly<Record<string, string>> = {
  ",": "0.167em",
  ":": "0.222em",
  ";": "0.278em",
  "!": "-0.167em",
  " ": "0.25em",
  thinspace: "0.167em",
  enspace: "0.5em",
  quad: "1em",
  qquad: "2em",
};

/** The argument is literal text, handed over unparsed. */
const TEXT_CMDS: ReadonlySet<string> = new Set(["text", "textrm", "mathrm", "operatorname"]);

/** Keyed by the token after `\left` / `\right`; `.` is the null delimiter. */
const DELIMS: Readonly<Record<string, string>> = {
  "(": "(",
  ")": ")",
  "[": "[",
  "]": "]",
  "|": "|",
  "{": "{",
  "}": "}",
  ".": "",
  langle: "\u27e8",
  rangle: "\u27e9",
  lceil: "\u2308",
  rceil: "\u2309",
  lfloor: "\u230a",
  rfloor: "\u230b",
  vert: "|",
  Vert: "\u2016",
};

/**
 * These only mean something in unsupported constructs (alignment, macro parameters, comments), so one degrades the
 * expression.
 */
const REJECTED_CHARS: ReadonlySet<string> = new Set(["&", "#", "%", "$"]);

type TokKind =
  "cmd" | "raw" | "num" | "ident" | "op" | "open" | "close" | "obrack" | "cbrack" | "sup" | "sub";

interface Tok {
  readonly k: TokKind;
  readonly v: string;
}

function isAlpha(ch: string): boolean {
  return (ch >= "a" && ch <= "z") || (ch >= "A" && ch <= "Z");
}

function isDigit(ch: string): boolean {
  return ch >= "0" && ch <= "9";
}

/** Inner text and the index past the closing brace; null when there is no braced group. */
function readBraced(src: string, from: number): { text: string; next: number } | null {
  let i = from;
  while (i < src.length && (src[i] === " " || src[i] === "\n" || src[i] === "\t")) {
    i++;
  }
  if (src.charAt(i) !== "{") {
    return null;
  }
  let depth = 0;
  const start = i + 1;
  for (; i < src.length; i++) {
    if (src[i] === "{") {
      depth++;
    } else if (src[i] === "}") {
      depth--;
      if (depth === 0) {
        return { text: src.slice(start, i), next: i + 1 };
      }
    }
  }
  return null;
}

function tokenize(src: string): Tok[] | null {
  const out: Tok[] = [];
  let i = 0;
  // charAt is typed `string`, so the walk needs no cast or non-null assertion.
  while (i < src.length) {
    const ch = src.charAt(i);
    if (ch === " " || ch === "\n" || ch === "\t" || ch === "\r") {
      i++;
      continue;
    }
    if (ch === "\\") {
      let j = i + 1;
      while (j < src.length && isAlpha(src.charAt(j))) {
        j++;
      }
      if (j === i + 1) {
        // A single-character control sequence: `\,` `\{` `\\` `\ `.
        if (j >= src.length) {
          return null;
        }
        out.push({ k: "cmd", v: src.charAt(j) });
        i = j + 1;
        continue;
      }
      const name = src.slice(i + 1, j);
      out.push({ k: "cmd", v: name });
      i = j;
      if (TEXT_CMDS.has(name)) {
        const arg = readBraced(src, i);
        if (arg === null) {
          return null;
        }
        out.push({ k: "raw", v: arg.text });
        i = arg.next;
      }
      continue;
    }
    if (isDigit(ch)) {
      let j = i;
      while (j < src.length) {
        const c = src.charAt(j);
        if (isDigit(c)) {
          j++;
          continue;
        }
        // A decimal point belongs to the number only before a digit, so `f(1).x` keeps its `.`.
        if (c === "." && j + 1 < src.length && isDigit(src.charAt(j + 1))) {
          j += 2;
          continue;
        }
        break;
      }
      out.push({ k: "num", v: src.slice(i, j) });
      i = j;
      continue;
    }
    if (isAlpha(ch)) {
      out.push({ k: "ident", v: ch });
      i++;
      continue;
    }
    if (REJECTED_CHARS.has(ch)) {
      return null;
    }
    switch (ch) {
      case "{":
        out.push({ k: "open", v: ch });
        break;
      case "}":
        out.push({ k: "close", v: ch });
        break;
      case "[":
        out.push({ k: "obrack", v: ch });
        break;
      case "]":
        out.push({ k: "cbrack", v: ch });
        break;
      case "^":
        out.push({ k: "sup", v: ch });
        break;
      case "_":
        out.push({ k: "sub", v: ch });
        break;
      default:
        out.push({ k: "op", v: ch });
    }
    i++;
  }
  return out;
}

function mel(tag: string, ...children: (Element | string)[]): Element {
  const node = document.createElementNS(MATHML_NS, tag);
  for (const c of children) {
    node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
  }
  return node;
}

/** A single element needs no `<mrow>`. */
function row(items: Element[]): Element {
  const [only] = items;
  return items.length === 1 && only !== undefined ? only : mel("mrow", ...items);
}

/** Carried beside the node, not as an attribute, so nothing is stripped from the output afterwards. */
interface Atom {
  node: Element;
  underover: boolean;
}

interface Cursor {
  /** Mutable: parseArg splits a multi-digit number in place. */
  toks: Tok[];
  readonly display: boolean;
  i: number;
  depth: number;
}

/** Null propagates: one unsupported item degrades the whole expression. */
function parseSeq(c: Cursor, stop: TokKind | null): Element[] | null {
  const out: Element[] = [];
  while (c.i < c.toks.length) {
    const t = c.toks[c.i];
    if (t === undefined || (stop !== null && t.k === stop)) {
      break;
    }
    const item = parseItem(c);
    if (item === null) {
      return null;
    }
    out.push(item);
  }
  return out;
}

function parseItem(c: Cursor): Element | null {
  const base = parseAtom(c);
  if (base === null) {
    return null;
  }
  return applyScripts(c, base);
}

/** Attach `_` and `^` to a base, in either written order. */
function applyScripts(c: Cursor, base: Atom): Element | null {
  let sub: Element | null = null;
  let sup: Element | null = null;
  for (;;) {
    const t = c.toks[c.i];
    if (t === undefined) {
      break;
    }
    if (t.k === "sub" && sub === null) {
      c.i++;
      sub = parseArg(c);
      if (sub === null) {
        return null;
      }
      continue;
    }
    if (t.k === "sup" && sup === null) {
      c.i++;
      sup = parseArg(c);
      if (sup === null) {
        return null;
      }
      continue;
    }
    break;
  }
  if (sub === null && sup === null) {
    return base.node;
  }
  const stacked = c.display && base.underover;
  if (sub !== null && sup !== null) {
    return mel(stacked ? "munderover" : "msubsup", base.node, sub, sup);
  }
  if (sub !== null) {
    return mel(stacked ? "munder" : "msub", base.node, sub);
  }
  if (sup !== null) {
    return mel(stacked ? "mover" : "msup", base.node, sup);
  }
  return base.node;
}

/** A braced group or the single atom after it (`x^2`, `\frac12`). Scripts are not consumed: `x^2^3` fails upstream. */
function parseArg(c: Cursor): Element | null {
  const t = c.toks[c.i];
  if (t === undefined) {
    return null;
  }
  if (t.k === "open") {
    return parseGroup(c);
  }
  // TeX takes one token as an unbraced argument (`\frac12` is `\frac{1}{2}`), but the tokenizer groups digit runs, so
  // the first digit is split off here and the rest put back.
  if (t.k === "num" && t.v.length > 1) {
    c.toks[c.i] = { k: "num", v: t.v.slice(1) };
    return mel("mn", t.v.slice(0, 1));
  }
  const atom = parseAtom(c);
  return atom === null ? null : atom.node;
}

function parseGroup(c: Cursor): Element | null {
  if (c.depth >= MAX_DEPTH) {
    return null;
  }
  c.i++;
  c.depth++;
  const items = parseSeq(c, "close");
  c.depth--;
  if (items === null) {
    return null;
  }
  if (c.toks[c.i]?.k !== "close") {
    return null; // Unbalanced.
  }
  c.i++;
  return items.length === 0 ? mel("mrow") : row(items);
}

function parseAtom(c: Cursor): Atom | null {
  const t = c.toks[c.i];
  if (t === undefined) {
    return null;
  }
  switch (t.k) {
    case "num":
      c.i++;
      return { node: mel("mn", t.v), underover: false };
    case "ident":
      c.i++;
      return { node: mel("mi", t.v), underover: false };
    case "op":
      c.i++;
      return { node: mel("mo", t.v === "'" ? "\u2032" : t.v), underover: false };
    case "obrack":
    case "cbrack":
      c.i++;
      return { node: mel("mo", t.v), underover: false };
    case "open": {
      const g = parseGroup(c);
      return g === null ? null : { node: g, underover: false };
    }
    case "cmd":
      return parseCommand(c, t.v);
    // A stray `}`, a base-less script, or a commandless text argument is malformed.
    case "close":
    case "sup":
    case "sub":
    case "raw":
      return null;
  }
}

function parseCommand(c: Cursor, name: string): Atom | null {
  c.i++;
  switch (name) {
    case "frac":
    case "dfrac":
    case "tfrac": {
      const num = parseArg(c);
      if (num === null) {
        return null;
      }
      const den = parseArg(c);
      if (den === null) {
        return null;
      }
      return { node: mel("mfrac", num, den), underover: false };
    }
    case "sqrt": {
      let index: Element | null = null;
      if (c.toks[c.i]?.k === "obrack") {
        c.i++;
        const items = parseSeq(c, "cbrack");
        if (items === null || c.toks[c.i]?.k !== "cbrack") {
          return null;
        }
        c.i++;
        index = items.length === 0 ? mel("mrow") : row(items);
      }
      const arg = parseArg(c);
      if (arg === null) {
        return null;
      }
      return {
        node: index === null ? mel("msqrt", arg) : mel("mroot", arg, index),
        underover: false,
      };
    }
    case "left":
    case "right": {
      const d = c.toks[c.i];
      if (d === undefined) {
        return null;
      }
      const ch = DELIMS[d.v];
      if (ch === undefined) {
        return null;
      }
      c.i++;
      if (ch === "") {
        return { node: mel("mrow"), underover: false }; // `\left.` is the null fence.
      }
      const mo = mel("mo", ch);
      mo.setAttribute("stretchy", "true");
      return { node: mo, underover: false };
    }
    default:
      break;
  }
  if (TEXT_CMDS.has(name)) {
    const arg = c.toks[c.i];
    if (arg?.k !== "raw") {
      return null;
    }
    c.i++;
    if (name === "text" || name === "textrm") {
      return { node: mel("mtext", arg.v), underover: false };
    }
    const mi = mel("mi", arg.v);
    // A one-character `<mi>` is italic; `\mathrm{d}` is the upright differential, so the variant is stated.
    mi.setAttribute("mathvariant", "normal");
    return { node: mi, underover: false };
  }
  const space = SPACE_CMDS[name];
  if (space !== undefined) {
    const ms = mel("mspace");
    ms.setAttribute("width", space);
    return { node: ms, underover: false };
  }
  const large = UNDEROVER_CMDS[name];
  if (large !== undefined) {
    return { node: mel("mo", large), underover: true };
  }
  const integral = INTEGRAL_CMDS[name];
  if (integral !== undefined) {
    return { node: mel("mo", integral), underover: false };
  }
  const ident = IDENT_CMDS[name];
  if (ident !== undefined) {
    return { node: mel("mi", ident), underover: false };
  }
  const op = OP_CMDS[name];
  if (op !== undefined) {
    return { node: mel("mo", op), underover: false };
  }
  if (FUNC_CMDS.has(name)) {
    return { node: mel("mi", name), underover: false };
  }
  return null;
}

/**
 * Convert a LaTeX expression to a MathML `<math>` element, or null outside the supported subset. Null is the
 * degradation signal, not an error, so rejections are silent. `display` selects `display="block"` and stacked limits.
 */
export function latexToMathML(src: string, display: boolean): Element | null {
  if (src.length === 0 || src.length > MAX_SRC || src.trim() === "") {
    return null;
  }
  const toks = tokenize(src);
  if (toks === null || toks.length === 0) {
    return null;
  }
  const c: Cursor = { toks, display, i: 0, depth: 0 };
  const items = parseSeq(c, null);
  if (items === null || items.length === 0 || c.i !== toks.length) {
    return null;
  }
  const math = mel("math", row(items));
  if (display) {
    math.setAttribute("display", "block");
  }
  return math;
}
