// Chat-input drag-drop and paste: files upload into the workspace uploads folder and their paths attach to the prompt.

import { el } from "@cplieger/reactive";
import { upload, partialUploadOf } from "./actions/files.js";
import { attachPathsToActiveChat } from "./chat.js";
import { byId } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { installDropZone } from "./drop-zone.js";
import { installComposerPaste } from "./composer-paste.js";
import { screenUploads, uploadLimitHint, UPLOADS_DIR } from "./upload-policy.js";
import * as toast from "./toast.js";
import { $ } from "./dom.js";

// Lucide upload at 32px for the overlay; ICON_DOWNLOAD is its inverse.
const ICON_UPLOAD =
  '<svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>';

export function initChatAttach(): void {
  const chatView = byId<HTMLDivElement>("chat-view");

  let overlay: HTMLDivElement | null = null;
  const getOverlay = (): HTMLDivElement => {
    if (overlay !== null) {
      return overlay;
    }
    overlay = el(
      "div",
      { className: "chat-drop-overlay hidden" },
      iconEl(ICON_UPLOAD),
      el("span", null, "Drop files to upload"),
      el("span", { className: "chat-drop-limit" }, uploadLimitHint()),
    ) as HTMLDivElement;
    chatView.style.position = "relative";
    chatView.appendChild(overlay);
    return overlay;
  };

  const uploadAndAttach = (files: FileList): void => {
    // Pre-flight before any bytes leave; rejections are stated, since a silently shortened batch reads as a lost file.
    const screened = screenUploads(files);
    if (screened.skipped !== "") {
      toast.error(screened.skipped);
    }
    if (screened.files === null) {
      return;
    }
    void upload.dispatch(
      { files: screened.files, targetDir: UPLOADS_DIR },
      {
        onSuccess: (paths) => {
          void attachPathsToActiveChat(paths);
        },
        onError: (err) => {
          // A partial batch is not rolled back, so attach what landed; the action already toasted.
          void attachPathsToActiveChat(partialUploadOf(err.cause));
        },
      },
    );
  };

  installDropZone({
    container: chatView,
    get overlay() {
      return getOverlay();
    },
    onDrop: uploadAndAttach,
  });

  // A paste takes the same journey as a drop; the listener lives on the textarea where the cursor is.
  installComposerPaste($.promptInput, uploadAndAttach);
}
