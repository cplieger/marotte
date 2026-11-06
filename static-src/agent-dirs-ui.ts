// "Additional directories" in the Settings panel: the directories beyond the workspace that agents
// may read and write. The server decides which entries it accepts and says why it refuses one, on a
// write and for every stored entry at each settings read.

import { el } from "@cplieger/ui";
import { join } from "@cplieger/keyenc";
import { maybeEl } from "./dom.js";
import { buildChip } from "./chip.js";
import { wireAddForm } from "./add-form.js";
import type { AddForm } from "./add-form.js";
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

/** The add row, once initAgentDirsUI has wired it. */
let row: AddForm | null = null;

function input(): HTMLInputElement | null {
  return maybeEl<HTMLInputElement>("agent-dirs-input");
}

function paintError(message: string | readonly (Node | string)[] | null): void {
  row?.setError(message, input() ?? undefined);
}

function paintLock(): void {
  const locked = lock !== "none";
  const i = input();
  if (i !== null) {
    i.disabled = locked;
  }
  row?.sync();
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
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- a settings event's applyAgentDirs can release the lock during the await
  if (lock !== "behind") {
    return;
  }
  const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
  retry.addEventListener("click", () => {
    void catchUp();
  });
  paintError(["Saved, but the newest list could not be loaded. ", retry]);
}

/** Sends next as the whole list; a refusal keeps the list and shows the server's reason on the add
 *  row's error line. On success the panel adopts the settings the write committed rather than painting next:
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
  const field = input();
  const form = field?.form ?? null;
  if (field === null || form === null) {
    return;
  }
  row = wireAddForm(form, {
    busy: () => lock !== "none",
    onSubmit: () => {
      const val = field.value;
      if (dirs.includes(val)) {
        field.value = "";
        row?.sync();
        return;
      }
      return save([...dirs, val]).then((ok) => {
        if (ok) {
          field.value = "";
        }
        field.focus();
      });
    },
  });
}
