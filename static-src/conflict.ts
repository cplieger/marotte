// Merge-conflict parser and 2-way resolution. diff3 `|||||||` base sections are recognized and excluded from ours:
// folding them in spliced the base marker and ancestor lines into the resolved file on save.

export interface ConflictHunk {
  /** 0-based line index of the `<<<<<<<` line in the file. */
  startLine: number;
  /** 0-based line index of the `>>>>>>>` line in the file. */
  endLine: number;
  /** Label after `<<<<<<<` (typically "HEAD" or a branch name). */
  ourLabel: string;
  /** Label after `>>>>>>>`. */
  theirLabel: string;
  /** Lines between `<<<<<<<` and `=======` (exclusive). */
  oursLines: string[];
  /** Lines between `=======` and `>>>>>>>` (exclusive). */
  theirsLines: string[];
}

export interface ConflictFile {
  /** Original file split into lines (no trailing newline preserved). */
  lines: string[];
  /** Whether the original text ended with a newline. */
  trailingNewline: boolean;
  hunks: ConflictHunk[];
}

const HEAD_RX = /^<{7}( .*)?$/;
const BASE_RX = /^\|{7}( .*)?$/;
const SEP_RX = /^={7}$/;
const END_RX = /^>{7}( .*)?$/;

/** Parse content into conflict hunks; empty when there are no markers. Safe on any text. */
export function parseConflicts(content: string): ConflictFile {
  const trailing = content.endsWith("\n");
  const text = trailing ? content.slice(0, -1) : content;
  const lines = text === "" ? [] : text.split("\n");
  const hunks: ConflictHunk[] = [];

  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const headMatch = HEAD_RX.exec(line);
    if (headMatch === null) {
      i++;
      continue;
    }
    // The diff3 base marker only counts between the head and the separator.
    let base = -1;
    let sep = -1;
    let end = -1;
    for (let j = i + 1; j < lines.length; j++) {
      const l = lines[j]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      if (sep === -1 && base === -1 && BASE_RX.test(l)) {
        base = j;
      } else if (sep === -1 && SEP_RX.test(l)) {
        sep = j;
      } else if (sep !== -1 && END_RX.test(l)) {
        end = j;
        break;
      } else if (sep === -1 && HEAD_RX.test(l)) {
        // A second opener before the separator: the first is malformed, so stop and let the outer loop re-enter there
        // (absorbing it splices a marker into the file). `sep === -1` keeps a theirs side that quotes an opener intact.
        break;
      }
    }
    if (sep === -1 || end === -1) {
      i++;
      continue;
    }

    const ourLabel = (headMatch[1] ?? "").trim();
    const endLine = lines[end]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const endMatch = END_RX.exec(endLine);
    const theirLabel = (endMatch?.[1] ?? "").trim();

    hunks.push({
      startLine: i,
      endLine: end,
      ourLabel,
      theirLabel,
      // Cut ours at the base marker so no resolution writes the marker or ancestor lines.
      oursLines: lines.slice(i + 1, base === -1 ? sep : base),
      theirsLines: lines.slice(sep + 1, end),
    });
    i = end + 1;
  }

  return { lines, trailingNewline: trailing, hunks };
}

export type Resolution = "ours" | "theirs" | "both";

/** Content with one hunk resolved; does not modify `file`. */
export function resolveHunk(file: ConflictFile, hunkIndex: number, resolution: Resolution): string {
  const hunk = file.hunks[hunkIndex];
  if (hunk === undefined) {
    return joinFile(file.lines, file.trailingNewline);
  }

  const replacement =
    resolution === "ours"
      ? hunk.oursLines
      : resolution === "theirs"
        ? hunk.theirsLines
        : [...hunk.oursLines, ...hunk.theirsLines];

  const out = [
    ...file.lines.slice(0, hunk.startLine),
    ...replacement,
    ...file.lines.slice(hunk.endLine + 1),
  ];
  return joinFile(out, file.trailingNewline);
}

function joinFile(lines: string[], trailing: boolean): string {
  // No empty-list arm: `[].join("\n")` is `""`, so the expression below already
  // answers `"\n"` / `""` for a file a resolution emptied.
  return lines.join("\n") + (trailing ? "\n" : "");
}
