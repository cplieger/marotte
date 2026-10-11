import { announce } from "@cplieger/ui-primitives/announce";

/** Options for installing a drop zone on a container element. */
interface DropZoneOptions {
  /** The element that receives drag events. */
  container: HTMLElement;
  /** The overlay element to show/hide (must have a "hidden" class toggle). */
  overlay: HTMLElement;
  /** Called on dragover (e.g. folder targeting); dropEffect is always "copy". */
  onDragOver?: (e: DragEvent) => void;
  /** Called on dragleave when the drag fully exits the container. */
  onDragLeave?: () => void;
  /** Called when files are dropped. Receives the FileList. */
  onDrop: (files: FileList) => void;
}

/**
 * Install drag-drop listeners with overlay feedback. A dragenter/dragleave counter stops nested elements flickering
 * the overlay. Announces drop-target activation through the shared announce() live region.
 */
export function installDropZone(opts: DropZoneOptions): void {
  let dragCounter = 0;

  opts.container.addEventListener("dragenter", (e: DragEvent) => {
    e.preventDefault();
    dragCounter++;
    if (dragCounter === 1) {
      opts.overlay.classList.remove("hidden");
      announce("Drop target active, release to upload files", "assertive");
    }
  });

  opts.container.addEventListener("dragleave", (e: DragEvent) => {
    e.preventDefault();
    dragCounter--;
    if (dragCounter <= 0) {
      dragCounter = 0;
      opts.overlay.classList.add("hidden");
      opts.onDragLeave?.();
    }
  });

  opts.container.addEventListener("dragover", (e: DragEvent) => {
    e.preventDefault();
    if (e.dataTransfer !== null) {
      e.dataTransfer.dropEffect = "copy";
    }
    opts.onDragOver?.(e);
  });

  opts.container.addEventListener("drop", (e: DragEvent) => {
    e.preventDefault();
    dragCounter = 0;
    opts.overlay.classList.add("hidden");
    if (e.dataTransfer !== null && e.dataTransfer.files.length > 0) {
      opts.onDrop(e.dataTransfer.files);
    }
    opts.onDragLeave?.();
  });
}
