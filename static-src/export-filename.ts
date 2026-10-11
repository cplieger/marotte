// A DOM-free leaf so the anchor exports and the Kiro session download share one rule.

type ExportFormat = "md" | "json" | "kiro-session";

const EXT: Record<ExportFormat, string> = {
  md: ".md",
  json: ".json",
  "kiro-session": ".kiro-session.zip",
};

/** Strip control chars and filesystem-/header-unsafe punctuation, then trim. */
function sanitizeFilenamePart(s: string): string {
  // eslint-disable-next-line no-control-regex -- intentionally scrubbing control chars from a filename
  return s.replace(/[\x00-\x1f\x7f"\\/:*?<>|]/g, "_").trim();
}

/** "<name>-<id><ext>", else "<id><ext>", else "chat<ext>". Mirrors the server's exportFilename. */
export function exportFilename(name: string, chatID: string, format: ExportFormat): string {
  const ext = EXT[format];
  let stem = sanitizeFilenamePart(name);
  const runes = Array.from(stem);
  if (runes.length > 80) {
    stem = runes.slice(0, 80).join("").trim();
  }
  const id = sanitizeFilenamePart(chatID);
  if (stem === "" && id === "") {
    return `chat${ext}`;
  }
  if (stem === "") {
    return `${id}${ext}`;
  }
  if (id === "") {
    return `${stem}${ext}`;
  }
  return `${stem}-${id}${ext}`;
}
