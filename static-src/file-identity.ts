// The file identity the viewer routes speak, checked where an answer arrives: the generated
// decoders type `file_id` optional because one wire struct serves several variants, so each
// variant's required identity is enforced here.

import { sha256Hex } from "./sha256.js";
import type { FileRefusal, FileStat } from "./wire/types.gen.js";

/** `internal/filebrowse.parseFileID`'s grammar. */
const FILE_ID = /^sha256:[0-9a-f]{64}$/u;

export function isFileId(v: string | undefined): v is string {
  return v !== undefined && FILE_ID.test(v);
}

/** Whether `text`, UTF-8 encoded, is exactly the `size` bytes `fileId` names. */
export function isIdentifiedText(text: string, size: number, fileId: string): boolean {
  const bytes = new TextEncoder().encode(text);
  return bytes.length === size && `sha256:${sha256Hex(bytes)}` === fileId;
}

/** The identity a stat's view is pinned to: "" for a file over the cap, which has none; null
 *  for a smaller file whose stat carries no valid identity, which no view may be picked from. */
export function statIdentity(stat: FileStat): string | null {
  if (stat.large) {
    return "";
  }
  return isFileId(stat.file_id) ? stat.file_id : null;
}

/** The disk text behind a stale save, when the refusal carries it and it is exactly the bytes
 *  the refusal's size and identity name; null otherwise. */
export function staleDiskText(r: FileRefusal): { text: string; fileId: string } | null {
  if (
    r.content_kind !== "text" ||
    r.content === undefined ||
    r.size === undefined ||
    !isFileId(r.file_id) ||
    !isIdentifiedText(r.content, r.size, r.file_id)
  ) {
    return null;
  }
  return { text: r.content, fileId: r.file_id };
}
