// ---------------------------------------------------------------------------
// Editor: Diff-mode rendering.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { renderDiffPane } from "./diff-pane.js";
import type { FileMode, FileState } from "./editor-types.js";
import { diffTexts, getCachedDiff } from "./editor-types.js";
import {
  paintCommonControls,
  paintSurface,
  renderNoticeSurface,
  rulesOf,
  showSurface,
} from "./editor-ui.js";
import { bindDiffView } from "./editor-scroll.js";

export function renderDiffModeUI(state: FileState): void {
  const m = state.mode.value;
  if (m.kind !== "diff") {
    return;
  }
  const src = m.diffSource;
  const rules = rulesOf(state);
  if (rules.view === "binary" || rules.view === "undiffable") {
    renderNoticeSurface(state, rules.view);
  } else {
    // The line diff is part of the signature: a buffer diff's texts move under the same source.
    const diff = getCachedDiff(state);
    paintSurface(["diff", state, src, diff], () => {
      renderDiffSurface(state, src);
    });
  }

  // Each button owns its own diff KIND, so a git diff is exited by
  // #editor-git-diff-btn (which entered it) and this one stays hidden. Offering
  // both would make "enter with B, exit with A" spellable, and the add is not
  // redundant: renderTextModeUI un-hides this button whenever the buffer is
  // dirty, so a dirty file entering a git diff arrives here with it visible.
  // Nothing here writes #editor-git-diff-btn — editor-core.ts is its one writer.
  if (src.kind === "git") {
    $.editorDiffBtn.classList.add("hidden");
  } else {
    $.editorDiffBtn.classList.remove("hidden");
    $.editorDiffBtn.setAttribute("data-tooltip", "Exit diff view");
    $.editorDiffBtn.setAttribute("aria-label", "Exit diff view");
  }
  // Editing needs the file's own text, which is what `loaded` reports. A card's
  // diff carries its pair without one, so the control arrives with the buffer and
  // stays away for a file that cannot be read — where an enabled Edit would open
  // an empty box over real content and a save would write it.
  const offerEdit = state.loaded && rules.edit;
  $.editorEditBtn.classList.toggle("hidden", !offerEdit);
  $.editorEditBtn.disabled = !offerEdit;
  $.editorCancelBtn.classList.add("hidden");
  $.editorSaveBtn.classList.add("hidden");
  paintCommonControls(state);
}

type DiffSource = Extract<FileMode, { kind: "diff" }>["diffSource"];

function renderDiffSurface(state: FileState, src: DiffSource): void {
  $.editorDiffPane.replaceChildren();
  const diff = getCachedDiff(state);
  const paneOpts: Parameters<typeof renderDiffPane>[1] = {
    oldLabel: src.oldLabel,
    newLabel: src.newLabel,
    lineNumbers: true,
    syncScroll: true,
    // The file's own path is the language hint. Without it this pane — the
    // depth-2 view a chat's changed-file link opens — rendered unhighlighted
    // while the inline peek that sent the reader here was meant to be coloured.
    lang: state.path,
  };
  // The "Ignore whitespace" toggle re-diffs and re-renders in place from these texts.
  paneOpts.source = diffTexts(state, src);
  // No per-hunk accept/reject. KAS's decision wire is PER FILE, and the IDE ships
  // only `supervisedDiff.discussHunk` beside it — there is no per-hunk verdict to
  // send. The replacement is ordinary editing: approve the turn, then edit what
  // you partly disagree with.
  const pane = renderDiffPane(diff, paneOpts);
  $.editorDiffPane.appendChild(pane);
  showSurface("diff");
  // After the pane is visible: a scroll offset written to a box with no layout is dropped.
  bindDiffView(state, pane, src);
}
