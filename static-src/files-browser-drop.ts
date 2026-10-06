import { $ } from "./dom.js";
import { type FileEntry, joinPath } from "./files-shared.js";
import { attachPathsToActiveChat } from "./chat.js";
import { installDropZone } from "./drop-zone.js";
import { upload } from "./actions/files.js";
import { screenUploads } from "./upload-policy.js";
import * as toast from "./toast.js";

export interface DragDropContext {
  getCurrentPath: () => string;
  getEntryMap: () => Map<string, FileEntry>;
  reload: () => void;
}

export function initBrowserDragDrop(ctx: DragDropContext): void {
  const wrap = $.fbList.parentElement;
  if (wrap === null) {
    return;
  }
  let dropTargetFolder = "";

  const clearDropTarget = (): void => {
    dropTargetFolder = "";
    for (const row of [...$.fbList.children]) {
      (row as HTMLElement).classList.remove("fb-drop-target");
    }
  };

  installDropZone({
    container: wrap,
    overlay: $.fbDropOverlay,
    onDragOver: (e) => {
      const row = (e.target as HTMLElement).closest<HTMLElement>(".fb-row");
      if (row !== null) {
        const name = row.dataset["name"];
        const entryMap = ctx.getEntryMap();
        const entry = name !== undefined ? entryMap.get(name) : undefined;
        if (entry?.isDir === true) {
          if (dropTargetFolder !== entry.name) {
            clearDropTarget();
            dropTargetFolder = entry.name;
            row.classList.add("fb-drop-target");
          }
          return;
        }
      }
      if (dropTargetFolder !== "") {
        clearDropTarget();
      }
    },
    onDragLeave: clearDropTarget,
    onDrop: (files) => {
      const currentPath = ctx.getCurrentPath();
      const targetDir =
        dropTargetFolder !== "" ? joinPath(currentPath, dropTargetFolder) : currentPath;
      dropTargetFolder = "";
      // Screened like the composer's drop: an over-cap file would otherwise end in a bare 413 naming nothing.
      const screened = screenUploads(files);
      if (screened.skipped !== "") {
        toast.error(screened.skipped);
      }
      if (screened.files === null) {
        return;
      }
      void upload.dispatch(
        { files: screened.files, targetDir },
        {
          onSuccess: (paths) => {
            ctx.reload();
            // Detached, one call for the batch (see attachPathsToActiveChat).
            void attachPathsToActiveChat(paths);
          },
        },
      );
    },
  });
}
