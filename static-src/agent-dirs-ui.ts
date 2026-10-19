// "Additional directories" in the Settings panel: the directories beyond the workspace that agents
// may read and write. The server decides which entries it accepts and says why it refuses one, on a
// write and for every stored entry at each settings read.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { maybeEl } from "./dom.js";
import { buildChip } from "./chip.js";
import { reconcile } from "./reconcile.js";
import { serverSentence } from "./actions/server-sentence.js";
import { setAdditionalDirectories } from "./actions/settings.js";
import { showSaving, showSaved, showError } from "./save-indicator.js";
import { setAdditionalDirectories as setLinkableDirectories } from "./workspace.js";
import { adoptWriteAnswer, newestSettings } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";

const SAVE_KEY = "additional_directories";

let dirs: readonly string[] = [];
/** The server's reason for each stored entry it does not grant, keyed by the entry. */
let refused: Readonly<Record<string, string>> = {};
/** Each write sends the whole list, so the panel is locked while one is in flight ("saving") and
 *  after one landed under newer settings it could not read ("behind"): a write built from an older
 *  list would drop the change it missed. Only an adopted payload unlocks a "behind" panel. */
let lock: "none" | "saving" | "behind" = "none";

function els(): { input: HTMLInputElement; add: HTMLButtonElement; error: HTMLElement } | null {
  const input = maybeEl<HTMLInputElement>("agent-dirs-input");
  const add = maybeEl<HTMLButtonElement>("agent-dirs-add");
  const error = maybeEl("agent-dirs-error");
  if (input === null || add === null || error === null) {
    return null;
  }
  return { input, add, error };
}

function paintError(message: string | null): void {
  const e = els();
  if (e === null) {
    return;
  }
  e.error.textContent = message ?? "";
  e.error.classList.toggle("hidden", message === null);
  if (message === null) {
    e.input.removeAttribute("aria-invalid");
  } else {
    e.input.setAttribute("aria-invalid", "true");
  }
}

function paintLock(): void {
  const locked = lock !== "none";
  const e = els();
  if (e !== null) {
    e.input.disabled = locked;
    e.add.disabled = locked;
  }
  const chips = maybeEl("agent-dirs-chips");
  for (const b of chips?.querySelectorAll<HTMLButtonElement>(".chip-remove") ?? []) {
    b.disabled = locked;
  }
}

/** Read the newest settings for a panel left "behind", offering a retry while they cannot be read. */
async function catchUp(): Promise<void> {
  lock = "behind";
  paintLock();
  const s = await newestSettings();
  if (s !== null) {
    applyAgentDirs(s);
    return;
  }
  const e = els();
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- a settings event's applyAgentDirs can release the lock during the await
  if (lock !== "behind" || e === null) {
    return;
  }
  const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
  retry.addEventListener("click", () => {
    void catchUp();
  });
  e.error.replaceChildren("Saved, but the newest list could not be loaded. ", retry);
  e.error.classList.remove("hidden");
}

/** Sends next as the whole list; a refusal keeps the list and shows the server's reason beside the
 *  field. On success the panel adopts the settings the write committed rather than painting next:
 *  the server decides which kept entries it grants, and removing one entry can change another's verdict.
 *  An answer the page does not adopt gives way to the newest settings (catchUp). */
async function save(next: readonly string[]): Promise<boolean> {
  lock = "saving";
  paintLock();
  showSaving(SAVE_KEY);
  const o = await setAdditionalDirectories.dispatch({ dirs: next }).outcome;
  if (o.status !== "success") {
    lock = "none";
    paintLock();
    showError(SAVE_KEY);
    paintError(
      o.status === "error"
        ? (serverSentence(o.error) ?? "Could not save the directories.")
        : "Could not save the directories.",
    );
    return false;
  }
  showSaved(SAVE_KEY);
  paintError(null);
  if (adoptWriteAnswer(o.value)) {
    lock = "none";
    applyAgentDirs(o.value);
  } else {
    await catchUp();
  }
  return true;
}

function reasonFor(dir: string): string | undefined {
  return Object.hasOwn(refused, dir) ? refused[dir] : undefined;
}

function renderChips(): void {
  const container = maybeEl("agent-dirs-chips");
  maybeEl("agent-dirs-empty-hint")?.classList.toggle("hidden", dirs.length > 0);
  renderRefusals();
  if (container === null) {
    return;
  }
  // The key carries the verdict, so an entry granted again (or refused anew) remounts unmarked (or marked).
  reconcile(container, [...dirs], {
    key: (dir) => join(dir, reasonFor(dir) === undefined ? "granted" : "refused"),
    mount: (dir) =>
      buildChip({
        label: dir,
        code: true,
        chipClass: "chip mono",
        ...(reasonFor(dir) === undefined ? {} : { note: "not granted" }),
        removeTitle: `Remove ${dir}`,
        onRemove: () => {
          if (lock === "none") {
            void save(dirs.filter((d) => d !== dir));
          }
        },
      }),
  });
  paintLock();
}

function renderRefusals(): void {
  const list = maybeEl("agent-dirs-refused");
  if (list === null) {
    return;
  }
  const reasons = dirs.flatMap((dir) => reasonFor(dir) ?? []);
  list.replaceChildren(...reasons.map((reason) => el("li", {}, reason)));
  list.classList.toggle("hidden", reasons.length === 0);
}

/** Adopt a settings payload, at boot, on every settings_updated and after this panel's own save:
 *  links and file routes follow the entries the server grants, and the panel shows every stored
 *  entry, marking each refused one with the server's reason. */
export function applyAgentDirs(s: EffectiveSettings): void {
  if (lock === "behind") {
    lock = "none";
    paintError(null);
  }
  setLinkableDirectories(
    s.additional_directories.filter((d) => !Object.hasOwn(s.additional_directories_refused, d)),
  );
  dirs = [...s.additional_directories];
  refused = s.additional_directories_refused;
  renderChips();
}

/** Register the add form's listeners, once per page. */
export function initAgentDirsUI(): void {
  const e = els();
  if (e === null) {
    return;
  }
  const { input, add } = e;
  const submit = (): void => {
    const val = input.value;
    if (val === "" || lock !== "none") {
      return;
    }
    if (dirs.includes(val)) {
      input.value = "";
      return;
    }
    void save([...dirs, val]).then((ok) => {
      if (ok) {
        input.value = "";
      }
      input.focus();
    });
  };
  add.addEventListener("click", submit);
  input.addEventListener("keydown", (ev: KeyboardEvent) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      submit();
    }
  });
  input.addEventListener("input", () => {
    paintError(null);
  });
}
