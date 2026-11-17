// The inline name field the file browser's rows and the editor's file name share.
import { el } from "@cplieger/reactive";

interface InlineRenameSpec {
  /** The element the field takes the place of. */
  readonly target: Element;
  readonly original: string;
  readonly className: string;
  /** A changed, trimmed, non-empty name, called at most once; the field is still in place, and
   *  the caller replaces it. */
  readonly commit: (newName: string, field: HTMLInputElement) => void;
  /** Escape, or an empty or unchanged name; the field is still in place for the caller to swap
   *  back. */
  readonly restore: (field: HTMLInputElement) => void;
}

/** Swap `spec.target` for a focused name field with the stem before the extension selected.
 *  Enter or blur commits, Escape restores; the field settles once. */
export function startInlineRename(spec: InlineRenameSpec): HTMLInputElement {
  const field = el("input", {
    type: "text",
    className: spec.className,
    value: spec.original,
  }) as HTMLInputElement;
  field.setAttribute("aria-label", `New name for ${spec.original}`);
  field.spellcheck = false;
  spec.target.replaceWith(field);
  field.focus();
  const dot = spec.original.lastIndexOf(".");
  if (dot > 0) {
    field.setSelectionRange(0, dot);
  } else {
    field.select();
  }

  let settled = false;
  // Swapping the field out blurs it, so the guard also stops that blur from settling a second time.
  const settle = (keep: boolean): void => {
    if (settled) {
      return;
    }
    settled = true;
    const name = field.value.trim();
    if (keep && name !== "" && name !== spec.original) {
      spec.commit(name, field);
    } else {
      spec.restore(field);
    }
  };
  field.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      settle(true);
    } else if (e.key === "Escape") {
      e.preventDefault();
      settle(false);
    }
  });
  field.addEventListener("blur", () => {
    settle(true);
  });
  return field;
}
