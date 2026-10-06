// The build versions: ONE owner of GET /api/version, fetched ONCE per page load (both are fixed for
// the container's life). Signal-backed: the status card paints before `--version` answers.

import { signal, type Signal } from "@cplieger/reactive";
import { apiGet } from "./api-client.js";

/** The wire shape of GET /api/version (internal/server/cli_handlers.go). Both
 *  fields are optional: `kiro_cli` is omitted when the `--version` probe fails
 *  or times out, which is a normal state while the install is still running. */
interface VersionPayload {
  marotte?: string;
  kiro_cli?: string;
}

/** A version pair. `""` means "not known", never "absent" — a reader renders
 *  what it has and says nothing about what it does not. */
export interface Versions {
  readonly marotte: string;
  readonly kiroCli: string;
}

const EMPTY: Versions = { marotte: "", kiroCli: "" };

/** Strip the program name kiro-cli prints with its version: the server passes raw `--version`
 *  stdout (`kiro-cli 2.20.2`), and every consumer already supplies the word. Only that exact leading
 *  token goes, so a bare number survives untouched. */
function bareKiroVersion(raw: string): string {
  const trimmed = raw.trim();
  const rest = trimmed.startsWith("kiro-cli ") ? trimmed.slice("kiro-cli ".length) : trimmed;
  return rest.trim();
}

const versions: Signal<Versions> = signal<Versions>(EMPTY);

/** The pair, as a signal, so a reader inside an `effect` repaints when it lands. */
export function versionsSignal(): Signal<Versions> {
  return versions;
}

/** The pair, untracked. For a reader that is not an effect. */
export function getVersions(): Versions {
  return versions.peek();
}

let started = false;

/** Read the pair once and publish it; idempotent. Never throws or reports: `apiGet` logs failures,
 *  and an unknown version is a missing suffix. */
export async function loadVersions(): Promise<void> {
  if (started) {
    return;
  }
  started = true;
  const v = await apiGet<VersionPayload>("/api/version");
  if (v === null) {
    return;
  }
  versions.value = {
    marotte: (v.marotte ?? "").trim(),
    kiroCli: bareKiroVersion(v.kiro_cli ?? ""),
  };
}

/** Reset for tests. Not part of the app's own lifecycle — the pair is read once
 *  per page load and a page load is the reset. */
export function _resetVersionsForTest(): void {
  started = false;
  versions.value = EMPTY;
}
