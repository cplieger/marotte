// The editor pane's three renderers, one per page: every editor tab paints the same elements.

import { $ } from "./dom.js";
import { EditDecor, EditSurface, TextViewer } from "./viewer-render.js";

let textViewer: TextViewer | null = null;
let surface: EditSurface | null = null;
let decor: EditDecor | null = null;

/** The read state's windowed renderer. */
export function viewer(): TextViewer {
  textViewer ??= new TextViewer(paneBody(), $.editorViewer);
  return textViewer;
}

/** The edit state's gutter and textarea geometry. */
export function editSurface(): EditSurface {
  surface ??= new EditSurface($.editorEditGutter, $.editorContent, paneBody());
  return surface;
}

/** The edit state's find mark and line flash. */
export function editDecor(): EditDecor {
  decor ??= new EditDecor(paneBody(), $.editorContent, editSurface());
  return decor;
}

/** `.editor-body`: the scroller in read state, the textarea's frame in edit state. */
export function paneBody(): HTMLElement {
  // index.html always nests the viewer in `.editor-body`; a detached fixture frames itself.
  return $.editorViewer.parentElement ?? $.editorViewer;
}

/** Whether the textarea is the visible surface. */
export function editing(): boolean {
  return !$.editorContent.classList.contains("hidden");
}
