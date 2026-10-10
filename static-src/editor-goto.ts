// Go to line: the toolbar control, Ctrl+G, and the `#L<n>` deep link's past-the-end notice.

import { effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { ICON_CLOSE, ICON_GOTO_LINE } from "./icons.js";
import { fileStates, getActiveFilePath, type FileState } from "./editor-types.js";
import { flashEditorLine, scrollToEditorLine } from "./editor-scroll.js";
import { getActiveTabKind } from "./tabs.js";

function status(): HTMLElement {
  return $.editorGoto.querySelector<HTMLElement>("#editor-goto-status") ?? $.editorGoto;
}

function pastEndSentence(asked: number, shown: number): string {
  return `Line ${String(asked)} is past the end; showing line ${String(shown)}.`;
}

/** Go to 1-based `line` on the active file, saying so when it was past the end. */
export function goToLine(line: number): void {
  const loc = scrollToEditorLine(line);
  if (loc === null) {
    return;
  }
  flashEditorLine(loc.line);
  if (loc.clamped) {
    openGoto();
    status().textContent = pastEndSentence(line, loc.line);
  }
}

function openGoto(): void {
  $.editorGoto.classList.remove("hidden");
  status().textContent = "";
  $.editorGotoInput.focus();
  $.editorGotoInput.select();
}

function closeGoto(): void {
  $.editorGoto.classList.add("hidden");
  status().textContent = "";
}

/** Whether `state` shows lines to go to: a loaded, readable text or conflict view. */
function hasLines(state: FileState | undefined): boolean {
  if (state === undefined || !state.loaded || state.error.value !== "") {
    return false;
  }
  const kind = state.mode.value.kind;
  return kind === "text" || kind === "conflict";
}

/** Show the toolbar control exactly when `state` has lines to go to, closing an open form when
 *  it has none. */
export function paintGoto(state: FileState): void {
  const ok = hasLines(state);
  $.editorGotoBtn.classList.toggle("hidden", !ok);
  if (!ok) {
    closeGoto();
  }
}

export function initGoto(): void {
  // The form serves one file; a different active file closes it.
  effect(() => {
    getActiveFilePath();
    closeGoto();
  });
  $.editorGotoBtn.replaceChildren(iconEl(ICON_GOTO_LINE));
  $.editorGotoBtn.addEventListener("click", () => {
    if ($.editorGoto.classList.contains("hidden")) {
      openGoto();
    } else {
      closeGoto();
    }
  });
  const close = $.editorGoto.querySelector<HTMLButtonElement>("#editor-goto-close");
  close?.replaceChildren(iconEl(ICON_CLOSE));
  close?.addEventListener("click", closeGoto);
  $.editorGoto.addEventListener("submit", (e) => {
    e.preventDefault();
    const n = Number($.editorGotoInput.value);
    if (Number.isFinite(n) && n >= 1) {
      goToLine(Math.floor(n));
    }
  });
  $.editorGoto.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      closeGoto();
    }
  });
  document.addEventListener("keydown", (e) => {
    const chord =
      e.key.toLowerCase() === "g" && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey;
    if (chord && getActiveTabKind() === "editor" && hasLines(fileStates.get(getActiveFilePath()))) {
      e.preventDefault();
      openGoto();
    }
  });
}
