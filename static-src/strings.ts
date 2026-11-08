// Tiny string utilities, in a leaf module so importers avoid modals.ts.

// Type-only, so it is erased at compile time and this module keeps no runtime
// edge. The alternative was restating TextSpan's five fields inline, twice —
// which is a second declaration of a wire shape the generator owns.
import type { TextSpan } from "./types.js";

/** HTML-escape a string for safe interpolation into innerHTML. */
export function escText(t: string): string {
  return t
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** HTML-escape for use inside attribute values (superset of escText). */
export function escAttr(t: string): string {
  return escText(t).replace(/'/g, "&#39;");
}

/** Humanize a kebab-case or snake_case name for display. */
export function humanName(s: string): string {
  return s.replace(/[-_]/g, " ");
}

/** The first `end` UTF-16 units of `s`, one fewer when the cut would split a surrogate pair, so the
 *  result never ends in a lone high surrogate (drawn as U+FFFD). */
export function cutAt(s: string, end: number): string {
  const head = s.slice(0, end);
  const last = head.charCodeAt(head.length - 1);
  return head.length < s.length && last >= 0xd800 && last <= 0xdbff ? head.slice(0, -1) : head;
}

/** Truncate a string to at most `max` characters, the last an ellipsis (…). */
export function truncate(s: string, max = 40): string {
  return s.length > max ? cutAt(s, max - 1) + "\u2026" : s;
}

/** A model's credit multiplier as `Nx`, or "" when the catalog carried none. `rate_multiplier` is
 *  `omitempty`, so absent is not parity (coercing to 1 made every model read `1x`), and a
 *  non-positive value is a payload nobody produces. */
export function rateLabel(rate: number | undefined): string {
  return rate === undefined || !Number.isFinite(rate) || rate <= 0 ? "" : `${String(rate)}x`;
}

/** How many lines each end of a windowed command output keeps. */
const OUTPUT_WINDOW_LINES = 20;

/** One source range kept by a window, and where it landed in the result. */
interface KeptRange {
  /** Inclusive start offset in the source text. */
  from: number;
  /** Exclusive end offset in the source text. */
  to: number;
  /** Offset in the windowed text where this range begins. */
  at: number;
}

/** A windowed output: the text, how many lines it dropped, and the source
 *  ranges it kept so style spans can be mapped onto it. */
interface OutputWindow {
  text: string;
  elided: number;
  kept: KeptRange[];
}

/** Window a command's output to its first and last N lines with a marker between. Returns the
 *  text, the elided line count (0 = all fit), and the kept source ranges, since two joined slices
 *  cannot carry a style span by one offset. */
export function windowOutput(text: string, n = OUTPUT_WINDOW_LINES): OutputWindow {
  // Track each line's start offset while splitting, so the kept ranges are read
  // off the source rather than reconstructed by arithmetic over joined strings.
  const starts: number[] = [];
  const lines: string[] = [];
  let cursor = 0;
  for (;;) {
    const nl = text.indexOf("\n", cursor);
    if (nl === -1) {
      // A trailing newline leaves no final line, only an empty remainder.
      if (cursor < text.length) {
        starts.push(cursor);
        lines.push(text.slice(cursor));
      }
      break;
    }
    starts.push(cursor);
    lines.push(text.slice(cursor, nl));
    cursor = nl + 1;
  }
  if (lines.length <= n * 2) {
    return { text, elided: 0, kept: [{ from: 0, to: text.length, at: 0 }] };
  }

  // Indices are safe by the length check above (lines.length > n*2 implies both
  // n-1 and lines.length-n are in range), but read through a local so the
  // arithmetic is stated once and no non-null assertion is needed.
  const lastHead = n - 1;
  const firstTail = lines.length - n;
  const headFrom = 0;
  const headTo = (starts[lastHead] ?? 0) + (lines[lastHead] ?? "").length;
  const tailFrom = starts[firstTail] ?? 0;
  const tailTo = text.length;
  const head = text.slice(headFrom, headTo);
  const tail = text.slice(tailFrom, tailTo);
  return {
    text: head + "\n" + tail,
    elided: lines.length - n * 2,
    kept: [
      { from: headFrom, to: headTo, at: 0 },
      { from: tailFrom, to: tailTo, at: head.length + 1 },
    ],
  };
}

/** Map style spans onto a windowed text, clipping each to the kept ranges and
 *  rebasing it onto where that range landed. A span straddling the elision
 *  boundary yields one piece per side. */
export function windowSpans(spans: readonly TextSpan[], kept: readonly KeptRange[]): TextSpan[] {
  const out: TextSpan[] = [];
  for (const range of kept) {
    for (const span of spans) {
      const start = Math.max(span.start, range.from);
      const end = Math.min(span.end, range.to);
      if (end <= start) {
        continue;
      }
      out.push({
        ...span,
        start: start - range.from + range.at,
        end: end - range.from + range.at,
      });
    }
  }
  return out;
}

/** A positive credit figure for a reader. `toFixed(2)` alone renders a sub-cent charge as `0.00`,
 *  contradicting the caller's `> 0` gate. */
export function creditFigure(credits: number): string {
  return credits < 0.005 ? "<0.01" : credits.toFixed(2);
}

/** A credit figure to the nearest whole credit: "0" for none, "<1" for a charge that would round
 *  to 0, so a run that spent something never reads as free. */
export function wholeCreditFigure(credits: number): string {
  if (credits > 0 && credits < 0.5) {
    return "<1";
  }
  return String(Math.round(credits));
}

/** A wall-clock span for a reader: tenths below a minute, whole seconds above, "0.0s" for zero
 *  (callers check first). Shared by the turn footer and the run card. */
export function formatElapsed(ms: number): string {
  if (ms >= 3_600_000) {
    const h = Math.floor(ms / 3_600_000);
    const m = Math.floor((ms % 3_600_000) / 60_000);
    return `${String(h)}h ${String(m)}m`;
  }
  if (ms >= 60_000) {
    const m = Math.floor(ms / 60_000);
    const s = Math.floor((ms % 60_000) / 1000);
    return `${String(m)}m ${String(s)}s`;
  }
  return `${(ms / 1000).toFixed(1)}s`;
}
