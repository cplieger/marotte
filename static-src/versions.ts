import { signal, type Signal } from "@cplieger/reactive";
import { apiGet } from "./api-client.js";

/** The wire shape of GET /api/version (internal/server/cli_handlers.go). Every
 *  field is optional: `kiro_cli` is omitted when the `--version` probe fails
 *  or times out, which is a normal state while the install is still running. */
interface VersionPayload {
  marotte?: string;
  kiro_cli?: string;
  config_dir?: string;
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
let configDir = "";

/** The pair, as a signal, so a reader inside an `effect` repaints when it lands. */
export function versionsSignal(): Signal<Versions> {
  return versions;
}

/** The pair, untracked. For a reader that is not an effect. */
export function getVersions(): Versions {
  return versions.peek();
}

let inflight: Promise<boolean> | null = null;
let loaded = false;

function load(): Promise<boolean> {
  if (loaded) {
    return Promise.resolve(true);
  }
  inflight ??= apiGet<VersionPayload>("/api/version").then((v) => {
    inflight = null;
    if (v === null) {
      return false;
    }
    loaded = true;
    configDir = (v.config_dir ?? "").replace(/\/+$/u, "");
    versions.value = {
      marotte: (v.marotte ?? "").trim(),
      kiroCli: bareKiroVersion(v.kiro_cli ?? ""),
    };
    return true;
  });
  return inflight;
}

/** Read the pair and publish it. Concurrent callers share one request; once it has answered no
 *  caller asks again, and a failed read is retried by the next caller. Never throws or reports:
 *  `apiGet` logs failures, and an unknown version is a missing suffix. */
export async function loadVersions(): Promise<void> {
  await load();
}

/** The absolute path of a file in the server's config directory, or null while GET /api/version
 *  cannot be read or names none. Waits for a read in flight and retries a failed one. */
export async function configFilePath(name: "tools.json" | "config.json"): Promise<string | null> {
  if (!(await load()) || configDir === "") {
    return null;
  }
  return `${configDir}/${name}`;
}

/** Reset for tests. Not part of the app's own lifecycle — the pair is read once
 *  per page load and a page load is the reset. */
export function _resetVersionsForTest(): void {
  inflight = null;
  loaded = false;
  versions.value = EMPTY;
  configDir = "";
}
