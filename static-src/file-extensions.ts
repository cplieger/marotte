// The single source of recognized file extensions, read by icons.ts and linkify.ts.

/** Per-extension metadata. All fields optional — absence means "not applicable". */
interface ExtMeta {
  /** Icon key into FILE_ICONS (e.g. "go", "ts", "config"). */
  iconKey?: string;
  /** Emoji badge for attachment pills (e.g. "📄", "🖼️"). */
  badge?: string;
}

/** Every recognized extension appears here exactly once. */
export const KNOWN_EXTENSIONS: Readonly<Record<string, ExtMeta>> = {
  ts: { iconKey: "ts" },
  tsx: { iconKey: "ts" },
  js: { iconKey: "js" },
  jsx: { iconKey: "js" },
  mjs: { iconKey: "js" },
  cjs: { iconKey: "js" },
  go: { iconKey: "go" },
  mod: { iconKey: "go" },
  sum: { iconKey: "go" },
  py: { iconKey: "py" },
  rs: { iconKey: "rs" },
  rb: { iconKey: "ruby" },
  php: { iconKey: "php" },
  java: { iconKey: "java" },
  kt: { iconKey: "java" },
  c: { iconKey: "c" },
  h: { iconKey: "c" },
  cpp: { iconKey: "c" },
  cc: { iconKey: "c" },
  hpp: { iconKey: "c" },
  cs: { iconKey: "c" },
  sh: { iconKey: "sh" },
  bash: { iconKey: "sh" },
  zsh: { iconKey: "sh" },
  json: { iconKey: "json" },
  yaml: { iconKey: "yaml" },
  yml: { iconKey: "yaml" },
  toml: { iconKey: "yaml" },
  xml: { iconKey: "xml" },
  svg: { iconKey: "xml" },
  ini: { iconKey: "config" },
  env: { iconKey: "lock" },
  conf: { iconKey: "config" },
  cfg: { iconKey: "config" },
  md: { iconKey: "md" },
  mdx: { iconKey: "md" },
  html: { iconKey: "html" },
  htm: { iconKey: "html" },
  css: { iconKey: "css" },
  scss: { iconKey: "sass" },
  sass: { iconKey: "sass" },
  less: { iconKey: "less" },
  vue: { iconKey: "vue" },
  svelte: { iconKey: "html" },
  sql: { iconKey: "config" },
  graphql: {},
  proto: {},
  txt: { iconKey: "file" },
  rst: {},
  log: { iconKey: "file" },
  lock: {},
  tmp: {},
  png: { iconKey: "img" },
  jpg: { iconKey: "img" },
  jpeg: { iconKey: "img" },
  gif: { iconKey: "img" },
  webp: { iconKey: "img" },
  ico: { iconKey: "img" },
  bmp: { iconKey: "img" },
  // A pseudo-extension for fenced-code detection.
  dockerfile: {},
  lua: { iconKey: "lua" },
};

/** Flat array of all recognized extensions (for regex construction). */
export const FILE_EXTS: readonly string[] = Object.keys(KNOWN_EXTENSIONS);

/** Look up the icon key for an extension. Returns "" if unknown. */
export function extToIconKey(ext: string): string {
  return Object.hasOwn(KNOWN_EXTENSIONS, ext) ? (KNOWN_EXTENSIONS[ext]?.iconKey ?? "") : "";
}

/** Badge-only extensions (binary formats outside KNOWN_EXTENSIONS); `badgeForExt` checks both registries. */
const BADGE_ONLY: Readonly<Record<string, string>> = {
  pdf: "📄",
  doc: "📝",
  docx: "📝",
  xls: "📊",
  xlsx: "📊",
  csv: "📊",
  ppt: "📽️",
  pptx: "📽️",
  png: "🖼️",
  jpg: "🖼️",
  jpeg: "🖼️",
  gif: "🖼️",
  svg: "🖼️",
  webp: "🖼️",
  zip: "📦",
  tar: "📦",
  gz: "📦",
  json: "{ }",
  yaml: "{ }",
  yml: "{ }",
  toml: "{ }",
};

/** Look up the emoji badge for an extension (without leading dot).
 *  Returns "" if no badge is defined. */
export function badgeForExt(ext: string): string {
  const known = Object.hasOwn(KNOWN_EXTENSIONS, ext) ? KNOWN_EXTENSIONS[ext]?.badge : undefined;
  return known ?? (Object.hasOwn(BADGE_ONLY, ext) ? BADGE_ONLY[ext] : undefined) ?? "";
}

/**
 * The lowercased extension without the dot, "" when none (a dotfile's leading dot names the file). Only after the last
 * separator.
 */
export function extOf(path: string): string {
  const slash = path.lastIndexOf("/");
  const dot = path.lastIndexOf(".");
  if (dot <= slash + 1) {
    return "";
  }
  return path.slice(dot + 1).toLowerCase();
}

/** What a browser renders as an image: one answer, read by the editor's image surface and the markdown `<img>` rewrite. */
const VIEWABLE_IMAGE_EXTS: ReadonlySet<string> = new Set([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "svg",
  "avif",
  "ico",
  "bmp",
]);

/** Whether a path names an image the browser can paint in an `<img>`. */
export function isViewableImage(path: string): boolean {
  return VIEWABLE_IMAGE_EXTS.has(extOf(path));
}

/** What `<audio controls>` can play: the native player degrades to its fallback content on an unsupported codec. */
const PLAYABLE_AUDIO_EXTS: ReadonlySet<string> = new Set([
  "mp3",
  "wav",
  "ogg",
  "m4a",
  "flac",
  "aac",
  "opus",
]);

/** Whether a path names audio the browser can play in an `<audio>` element. */
export function isPlayableAudio(path: string): boolean {
  return PLAYABLE_AUDIO_EXTS.has(extOf(path));
}
