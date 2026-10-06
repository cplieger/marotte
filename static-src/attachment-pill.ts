// The attachment pill, shared by the composer and a sent turn's header (a pure view that
// must not reach `$.attachmentRow`). Removal is the composer's opt-in. Body and `×` are
// SIBLINGS, so a `×` click cannot reach the open handler.

import { el } from "@cplieger/reactive";
import { ICON_CLOSE } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { badgeForExt, extOf } from "./file-extensions.js";

/** The two fields a pill draws, satisfied by both `AttachedFile` and the wire `Attachment`. */
export interface AttachmentRef {
  path: string;
  name: string;
}

/** Shown when the extension carries no badge of its own. */
const FALLBACK_BADGE = "📎";

/**
 * Open handler, injected: opening reaches `editor-openers` and `tabs`, which neither the
 * composer nor a `fundamentals/` view may import. A no-op default for tests.
 */
let _open: (path: string) => void = () => {
  /* not wired */
};

export function initAttachmentPillCallbacks(cbs: { open: (path: string) => void }): void {
  _open = cbs.open;
}

/** Extension → emoji badge, derived from the file-extensions registry. */
export function iconForAttachment(name: string): string {
  return badgeForExt(extOf(name)) || FALLBACK_BADGE;
}

/** Build one attachment pill. Pass `onRemove` to get the `×`; omit it for a
 *  read-only pill (a sent turn's header). */
export function buildAttachmentPill(
  att: AttachmentRef,
  opts: { onRemove?: (path: string) => void } = {},
): HTMLElement {
  const open = el(
    "button",
    {
      type: "button",
      className: "attachment-open",
      "aria-label": `Open ${att.name}`,
    },
    el("span", { className: "attachment-icon" }, iconForAttachment(att.name)),
    el("span", { className: "attachment-name" }, att.name),
  );
  open.addEventListener("click", () => {
    _open(att.path);
  });

  const pill = el("li", { className: "attachment-pill", title: att.path }, open);

  const onRemove = opts.onRemove;
  if (onRemove !== undefined) {
    const close = el(
      "button",
      {
        type: "button",
        className: "icon-btn attachment-close",
        "aria-label": `Remove ${att.name}`,
      },
      iconEl(ICON_CLOSE),
    );
    close.addEventListener("click", () => {
      onRemove(att.path);
    });
    pill.appendChild(close);
  }
  return pill;
}
