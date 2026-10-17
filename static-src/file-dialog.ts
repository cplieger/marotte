// The OS file dialog behind both "Upload" buttons (the picker modal and the file browser).

import { el } from "@cplieger/reactive";
import * as toast from "./toast.js";

/** The input whose dialog is open, so a dialog that answers neither `change` nor `cancel` leaves
 *  at most one stray input behind. */
let pending: HTMLInputElement | null = null;

/**
 * Open the OS file dialog and hand the chosen files to `onPick`. A dialog that ends with no files
 * shows an info toast, and a throw from `onPick` an error toast. Call it synchronously from the
 * press's own handler: the dialog opens only under that press's user activation.
 */
export function openFileDialog(trigger: Element, onPick: (files: FileList) => void): void {
  pending?.remove();
  const input = el("input", {
    type: "file",
    multiple: true,
    className: "file-dialog-input",
    tabIndex: -1,
    "aria-hidden": "true",
  }) as HTMLInputElement;
  const settle = (): void => {
    input.remove();
    if (pending === input) {
      pending = null;
    }
  };
  input.addEventListener("change", () => {
    settle();
    try {
      // Never empty: an unchanged (empty) selection fires `cancel` instead (HTML "show the picker,
      // if applicable"; WebKit FileInputType::setFiles).
      const files = input.files;
      if (files === null) {
        throw new Error("the file input has no file list");
      }
      onPick(files);
    } catch (err) {
      console.error("upload: handling the picked files threw", err);
      toast.error(`Upload failed: ${err instanceof Error ? err.message : String(err)}`);
    }
  });
  input.addEventListener("cancel", () => {
    settle();
    // Worded for both causes: iOS also reports a failed hand-over (a Photos export error, a pick
    // with no usable file) as `cancel` (WebKit UIProcess/ios/forms/WKFileUploadPanel.mm _cancel).
    toast.info("No file was attached.");
  });
  // Connected for the whole dialog: WebKit can collect a detached input's wrapper mid-dialog
  // (bindings/js/JSNodeCustom.cpp), and ~FileInputType invalidates the FileChooser, dropping the
  // pick with no event (html/FileInputType.cpp, platform/FileChooser.cpp). Inside the trigger's
  // <dialog> as a precaution: showModal() makes the rest of the page inert (MDN
  // HTMLDialogElement.showModal), though WebKit's click() path does not check inertness.
  (trigger.closest("dialog") ?? document.body).appendChild(input);
  overlay(input, trigger);
  pending = input;
  input.click();
}

/** Lays `input` over `trigger`'s box: iPadOS anchors its Photo Library / Take Photo / Choose File
 *  menu at a rendered input's rect when it is on screen (WebKit WKFileUploadPanel.mm
 *  presentWithParameters), so the menu comes from the pressed button. The probe measures where a
 *  transformed containing block (a modal mid-way through its entry scale) puts the origin and unit. */
function overlay(input: HTMLInputElement, trigger: Element): void {
  const want = trigger.getBoundingClientRect();
  const s = input.style;
  const PROBE = 100;
  s.left = "0px";
  s.top = "0px";
  s.width = `${String(PROBE)}px`;
  s.height = `${String(PROBE)}px`;
  const probe = input.getBoundingClientRect();
  const kx = probe.width / PROBE;
  const ky = probe.height / PROBE;
  s.left = `${String((want.left - probe.left) / kx)}px`;
  s.top = `${String((want.top - probe.top) / ky)}px`;
  s.width = `${String(want.width / kx)}px`;
  s.height = `${String(want.height / ky)}px`;
}
