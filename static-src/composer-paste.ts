// Composer paste: files become attachments, a large text paste spills to a .txt in the uploads folder. Clipboard
// images are renamed (they collide as "image.png") but their bytes are untouched: the server fits inlined images.

/** Deliberately high: the composer is where an error message or config block is pasted to be seen. */
const SPILL_LINES = 50;

/** The second half of the spill test: 40 very long lines are as unreadable as 400 short ones. */
const SPILL_CHARS = 10_000;

/** Anything but JPEG or WebP is named .png; the server decodes images by content. */
function extFor(mime: string): string {
  switch (mime) {
    case "image/jpeg":
      return ".jpg";
    case "image/webp":
      return ".webp";
    default:
      return ".png";
  }
}

/**
 * `2026-08-15T08-42-11`: sorts in a listing and is colon-free. One formatter for images and text. Two pastes in one
 * second collide and the second replaces the first; accepted, since that is a double-fire.
 */
function stamp(now: Date): string {
  return now.toISOString().slice(0, 19).replace(/[:]/g, "-");
}

/** A unique, sortable, human-readable name for a pasted image. */
export function pastedImageName(mime: string, now: Date = new Date()): string {
  return `pasted-${stamp(now)}${extFor(mime)}`;
}

/** A unique, sortable name for a spilled text paste. Always .txt, so the server routes it as a path reference. */
export function pastedTextName(now: Date = new Date()): string {
  return `paste-${stamp(now)}.txt`;
}

/** Clipboard files split into images and others, in order; both empty for a text paste. Reads `dt.files` only. */
export function clipboardFiles(dt: DataTransfer | null): { images: File[]; others: File[] } {
  if (dt === null) {
    return { images: [], others: [] };
  }
  const images: File[] = [];
  const others: File[] = [];
  for (const f of Array.from(dt.files)) {
    if (f.type.startsWith("image/")) {
      images.push(f);
    } else {
      others.push(f);
    }
  }
  return { images, others };
}

/**
 * Whether a text paste becomes a file. It must contain a newline: a long single line is a URL, JWT or key the user
 * needs to see. Both limits are exclusive.
 */
export function shouldSpillPaste(text: string): boolean {
  if (!text.includes("\n")) {
    return false;
  }
  return text.length > SPILL_CHARS || text.split("\n").length > SPILL_LINES;
}

/** Give each clipboard image a unique pasted-<stamp> name, bytes untouched. */
export function prepareForUpload(files: File[]): File[] {
  return files.map((f) => new File([f], pastedImageName(f.type), { type: f.type }));
}

function toFileList(files: File[]): FileList {
  const dt = new DataTransfer();
  for (const f of files) {
    dt.items.add(f);
  }
  return dt.files;
}

/**
 * Wire the paste handler; `onFiles` gets a FileList of everything the paste produced. Install exactly one per composer:
 * two would both fire and both preventDefault.
 */
export function installComposerPaste(
  target: HTMLElement,
  onFiles: (files: FileList) => void,
): void {
  target.addEventListener("paste", (event: ClipboardEvent) => {
    const { images, others } = clipboardFiles(event.clipboardData);
    if (images.length > 0 || others.length > 0) {
      event.preventDefault();
      // Only images are renamed: the others carry a filename the user chose.
      onFiles(toFileList([...prepareForUpload(images), ...others]));
      return;
    }
    const text = event.clipboardData?.getData("text/plain") ?? "";
    if (!shouldSpillPaste(text)) {
      return;
    }
    event.preventDefault();
    const name = pastedTextName();
    onFiles(toFileList([new File([text], name, { type: "text/plain" })]));
  });
}
