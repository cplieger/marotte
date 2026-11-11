/**
 * announce.ts — Screen-reader announcements through pre-mounted live regions:
 * a polite and an assertive one per surface, the root's from `mountLiveRegions`
 * and each `<dialog>`'s that the dialog primitives drive. A region inserted or
 * moved in the mutation that fills it is not reliably announced, so `announce`
 * never creates or moves one.
 * @module
 */

import { acquireRegions, regionsOf } from "./live-regions.js";
import type { Politeness } from "./live-regions.js";
import { trackedModals, whenUncovered } from "./modal-host.js";

/** Gap (ms) between clearing the region and setting the new text: the
 *  empty-then-text change is what makes assistive tech re-announce identical
 *  text, and a microtask gap is too short for it to see two mutations. */
const ANNOUNCE_DELAY_MS = 100;

interface RootMount {
  readonly host: HTMLElement;
  readonly release: () => void;
}

/** Root mounts still held, oldest first; the newest one's host is the root. */
const roots: RootMount[] = [];

const pending = new Map<Politeness, ReturnType<typeof setTimeout>>();

/** The regions holding an announcement's text, emptied before a modal's close or
 *  removal uncovers them: a browser can read an alert region again when exposed
 *  (Chromium, ui/accessibility/ax_event_generator.cc OnIgnoredChanged). */
const written = new Set<HTMLElement>();

function clearWritten(): void {
  for (const region of written) {
    region.textContent = "";
  }
  written.clear();
}

/** Mount the page's root polite and assertive live regions as `host`'s last
 *  children and make `host` the root. Call it once at boot, before the first
 *  render, so the regions exist before anything is announced. A pair still
 *  inside `host` is kept rather than replaced. Returns this mount's release,
 *  which is idempotent: the root falls back to the newest mount still held,
 *  and the pair leaves `host` when its last owner releases (never, on a dialog
 *  the dialog primitives drive, which keeps its pair). */
export function mountLiveRegions(host: HTMLElement): () => void {
  const mount: RootMount = { host, release: acquireRegions(host) };
  roots.push(mount);
  return () => {
    const at = roots.indexOf(mount);
    if (at === -1) {
      return;
    }
    roots.splice(at, 1);
    mount.release();
  };
}

/** The regions an announcement reaches now: the pair of every open modal the
 *  library drives and the root's. `showModal()` takes everything outside the
 *  topmost modal out of the accessibility tree, so the browser, not an opening
 *  order, picks the one read. With no modal open every open dialog is exposed,
 *  so only the root's is written, else it would be read twice. */
function currentRegions(politeness: Politeness): HTMLElement[] {
  const hosts: HTMLElement[] = trackedModals();
  const root = roots.at(-1);
  if (root !== undefined) {
    hosts.push(root.host);
  }
  const regions = new Set<HTMLElement>();
  for (const host of hosts) {
    const region = regionsOf(host)?.[politeness];
    if (region !== undefined) {
      regions.add(region);
    }
  }
  return [...regions];
}

function deliver(message: string, politeness: Politeness): void {
  const cleared = currentRegions(politeness);
  if (cleared.length === 0) {
    return;
  }
  for (const region of cleared) {
    region.textContent = "";
    written.delete(region);
  }
  pending.set(
    politeness,
    setTimeout(() => {
      const live = currentRegions(politeness);
      // A region that joined during the gap (a modal opened, a root mounted) was not
      // cleared, so identical text would not be announced again there: start over.
      if (!live.every((region) => cleared.includes(region))) {
        deliver(message, politeness);
        return;
      }
      // Before the write: a landing run in the change's own task (as under fake timers)
      // sees records not yet delivered, and their clear must not empty this text.
      whenUncovered(clearWritten);
      for (const region of live) {
        region.textContent = message;
        written.add(region);
      }
    }, ANNOUNCE_DELAY_MS),
  );
}

/** Announce `message` to screen readers through the live regions of every open
 *  modal dialog the dialog primitives drive and the root's from
 *  {@link mountLiveRegions}; the browser reads the one inside the topmost modal,
 *  or the root's when no modal is open. A modal's close or removal empties every
 *  region before it is uncovered. Writes nothing when none is mounted.
 *  `"assertive"` interrupts, through its own `role="alert"` region; a second
 *  announcement at the same politeness before the first lands replaces it. */
export function announce(message: string, politeness: Politeness = "polite"): void {
  clearTimeout(pending.get(politeness));
  deliver(message, politeness);
}

/** Test-only: cancel pending announcements, forget the written regions and
 *  release every root mount. */
// deadset:ignore DS1102 -- test seam: cancels pending announcements and releases the root mounts
export function _resetForTest(): void {
  for (const timer of pending.values()) {
    clearTimeout(timer);
  }
  written.clear();
  for (const mount of roots.splice(0)) {
    mount.release();
  }
}
