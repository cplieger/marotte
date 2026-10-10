// The single source of recognized file extensions, read by icons.ts, linkify.ts and highlight.ts.

/** Per-extension metadata. All fields optional — absence means "not applicable". */
interface ExtMeta {
  /** Internal language key for syntax highlighting (e.g. "go", "ts", "py"). */
  lang?: string;
  /** Icon key into FILE_ICONS (e.g. "go", "ts", "config"). */
  iconKey?: string;
  /** Emoji badge for attachment pills (e.g. "📄", "🖼️"). */
  badge?: string;
}

/** Every recognized extension appears here exactly once. */
export const KNOWN_EXTENSIONS: Readonly<Record<string, ExtMeta>> = {
  ts: { lang: "ts", iconKey: "ts" },
  tsx: { lang: "ts", iconKey: "ts" },
  js: { lang: "js", iconKey: "js" },
  jsx: { lang: "js", iconKey: "js" },
  mjs: { lang: "js", iconKey: "js" },
  cjs: { lang: "js", iconKey: "js" },
  go: { lang: "go", iconKey: "go" },
  mod: { lang: "go", iconKey: "go" },
  sum: { iconKey: "go" },
  py: { lang: "py", iconKey: "py" },
  rs: { lang: "rs", iconKey: "rs" },
  rb: { lang: "rb", iconKey: "ruby" },
  php: { lang: "php", iconKey: "php" },
  java: { lang: "java", iconKey: "java" },
  kt: { iconKey: "java" },
  c: { lang: "c", iconKey: "c" },
  h: { lang: "c", iconKey: "c" },
  cpp: { lang: "c", iconKey: "c" },
  cc: { lang: "c", iconKey: "c" },
  hpp: { lang: "c", iconKey: "c" },
  cs: { iconKey: "c" },
  sh: { lang: "sh", iconKey: "sh" },
  bash: { lang: "sh", iconKey: "sh" },
  zsh: { lang: "sh", iconKey: "sh" },
  json: { lang: "json", iconKey: "json" },
  yaml: { lang: "yaml", iconKey: "yaml" },
  yml: { lang: "yaml", iconKey: "yaml" },
  toml: { lang: "toml", iconKey: "yaml" },
  xml: { lang: "xml", iconKey: "xml" },
  svg: { iconKey: "xml" },
  ini: { iconKey: "config" },
  env: { iconKey: "lock" },
  conf: { iconKey: "config" },
  cfg: { iconKey: "config" },
  md: { lang: "md", iconKey: "md" },
  mdx: { lang: "md", iconKey: "md" },
  html: { lang: "html", iconKey: "html" },
  htm: { lang: "html", iconKey: "html" },
  css: { lang: "css", iconKey: "css" },
  scss: { iconKey: "sass" },
  sass: { iconKey: "sass" },
  less: { iconKey: "less" },
  vue: { iconKey: "vue" },
  svelte: { iconKey: "html" },
  sql: { lang: "sql", iconKey: "config" },
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
  dockerfile: { lang: "docker" },
  lua: { iconKey: "lua" },
};

/** Flat array of all recognized extensions (for regex construction). */
export const FILE_EXTS: readonly string[] = Object.keys(KNOWN_EXTENSIONS);

/** Look up the language key for an extension. Returns "" if unknown. */
export function extToLang(ext: string): string {
  return KNOWN_EXTENSIONS[ext]?.lang ?? "";
}

/** Look up the icon key for an extension. Returns "" if unknown. */
export function extToIconKey(ext: string): string {
  return KNOWN_EXTENSIONS[ext]?.iconKey ?? "";
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
  return KNOWN_EXTENSIONS[ext]?.badge ?? BADGE_ONLY[ext] ?? "";
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
