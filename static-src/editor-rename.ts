// Rename from the editor's toolbar or F2: the file browser's inline field and its `files.rename`
// action, so the refusals and their toast are the browser's. The open tab follows the file through
// the server's tabs frame; nothing here moves the record.

import { effect, el } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { renameFile } from "./actions/files.js";
import { bindLoadingState } from "./actions/index.js";
import { activeDirty, getActiveFilePath, routeForPath } from "./editor-types.js";
import { iconEl } from "./icon-el.js";
import { ICON_RENAME_UI } from "./icons.js";
import { startInlineRename } from "./inline-rename.js";
import { getActiveTabKind } from "./tabs.js";
import { error as toastError } from "./toast.js";

/** A rename open or in flight on the header: the file it renames and the header it replaced. */
interface Session {
  readonly path: string;
  readonly saved: readonly Node[];
}

let session: Session | null = null;

/** Whether a rename's field is open or its answer is pending. */
export function renaming(): boolean {
  return session !== null;
}

export function initEditorRename(): void {
  $.editorRenameBtn.replaceChildren(iconEl(ICON_RENAME_UI));
  $.editorRenameBtn.addEventListener("click", startRename);
  bindLoadingState(["files.rename"], $.editorRenameBtn, { preserveDisabled: true });

  // Another file shown repaints the header (activateFile), so a session for the old one is over.
  effect(() => {
    const path = getActiveFilePath();
    if (session !== null && session.path !== path) {
      session = null;
      $.editorFilename.classList.remove("is-renaming");
    }
  });

  // From the textarea too: F2 types nothing. A focused chat tab row takes F2 for its own rename and
  // prevents the default.
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key !== "F2" || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) {
      return;
    }
    const btn = $.editorRenameBtn;
    if (getActiveTabKind() !== "editor" || session !== null || btn.classList.contains("hidden")) {
      return;
    }
    e.preventDefault();
    startRename();
  });
}

function startRename(): void {
  const path = getActiveFilePath();
  if (path === "" || session !== null || $.editorRenameBtn.disabled) {
    return;
  }
  const cut = path.lastIndexOf("/");
  const name = path.slice(cut + 1);
  if (activeDirty.value) {
    toastError(`Save or discard your changes to ${name} before renaming it.`);
    return;
  }
  const header = $.editorFilename;
  const display = routeForPath(path).displayPath;
  const folder = display.slice(0, display.lastIndexOf("/") + 1);
  const s: Session = { path, saved: [...header.childNodes] };
  session = s;
  const target = el("span");
  header.replaceChildren(
    ...(folder === ""
      ? []
      : [el("span", { className: "editor-rename-dir" }, el("bdi", {}, folder))]),
    target,
  );
  header.classList.add("is-renaming");
  const field = startInlineRename({
    target,
    original: name,
    className: "editor-rename-input",
    restore: () => {
      end(s);
    },
    commit: (newName, f) => {
      f.replaceWith(newName);
      void renameFile.dispatch(
        { dir: cut <= 0 ? "/" : path.slice(0, cut), original: name, newName },
        {
          onSuccess: () => {
            end(s);
          },
          onError: () => {
            end(s);
          },
        },
      );
    },
  });
  field.dir = "ltr";
}

/** Put the header back unless another file has been shown since, which painted its own. */
function end(s: Session): void {
  if (session !== s) {
    return;
  }
  session = null;
  $.editorFilename.classList.remove("is-renaming");
  if (getActiveFilePath() === s.path) {
    $.editorFilename.replaceChildren(...s.saved);
  }
}
