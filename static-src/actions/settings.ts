// Settings actions. Callsites dispatch with { silent: true } and pair showSaving() with
// showSaved()/showError() from the result, so no action sets `success`/`error` for the indicator.
// Saves set no `retryable`: their args hold DOM refs that go stale, and a save is cheap to redo.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { decodeEffectiveSettings } from "../wire/decoders.gen.js";
import type { EffectiveSettings } from "../wire/types.gen.js";
import type { IdentityVerdict } from "../identity.js";
import { paintSettingLocks, writeSwitch } from "../governance.js";

/** PUT the whole global-instructions document, guarded by the GET's validator, answering the
 *  write's validator. The server refuses a stale `If-Match` (409) and a missing one (428); an EMPTY
 *  etag sends no header, since inventing one makes a working save a permanent 428. `""` back
 *  means the token could not be read; the caller keeps its own. */
export const saveSteering = apiAction<{ content: string; etag: string }, string>({
  name: "settings.save_steering",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  scope: "settings",
  request: ({ content, etag }) =>
    etag === ""
      ? { method: "PUT", path: "/api/steering", body: { content } }
      : { method: "PUT", path: "/api/steering", body: { content }, headers: { "If-Match": etag } },
  decode: (data) => {
    const etag = (data as { etag?: unknown } | null)?.etag;
    return typeof etag === "string" ? etag : "";
  },
  error: "Could not save steering",
});

export const logout = apiAction<
  { render: (v: IdentityVerdict) => void; prev: IdentityVerdict },
  unknown,
  IdentityVerdict
>({
  name: "settings.logout",
  retryable: retryNetwork,
  request: () => ({ method: "POST", path: "/api/logout" }),
  // INJECTED, not imported: settings.ts imports this action, so importing renderIdentity is a cycle.
  optimistic: ({ render, prev }) => {
    render({ state: "signed_out" });
    return prev;
  },
  // The whole VERDICT, so all three arms are restorable (the address cannot tell `signed_out` from
  // `unavailable`).
  rollback: ({ render }, op) => {
    if (op === undefined) {
      return;
    }
    render(op);
  },
  error: "Could not log out",
});

interface KiroSettingArgs {
  key: string;
  value: string;
  input: HTMLInputElement;
}

interface KiroSettingOp {
  prevChecked: boolean;
}

export const setKiroSetting = apiAction<KiroSettingArgs, unknown, KiroSettingOp>({
  name: "settings.set_kiro_setting",
  scope: "settings",
  request: ({ key, value }) => ({
    method: "PUT",
    path: "/api/kiro-settings",
    body: { key, value },
  }),
  // Checkbox-only. A number-valued setting needs a focus-time snapshot for rollback: `input.value`
  // is already the refused value when a change event fires.
  optimistic: ({ input }) => {
    return { prevChecked: !input.checked }; // user just toggled, so prev is opposite
  },
  rollback: ({ input }, op) => {
    if (op === undefined) {
      return;
    }
    writeSwitch(input, op.prevChecked);
    paintSettingLocks();
  },
  error: "Could not save setting",
});

// The generated decoder gives EffectiveSettings' required fields runtime force; a decode failure
// (including an empty-body 2xx) resolves null and each caller leaves its UI as it was.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
export const loadSettings = apiAction<void, EffectiveSettings>({
  name: "settings.load",
  dedupe: true,
  retryable: retryNetwork,
  request: () => ({ method: "GET", path: "/api/settings" }),
  decode: (data) => decodeEffectiveSettings(data),
  error: false,
  success: false,
});

interface PatchAppArgs {
  body: Record<string, unknown>;
  /** Input(s) whose checked state flips back on failure. */
  inputs?: readonly HTMLInputElement[];
}

interface PatchAppOp {
  inputs: { el: HTMLInputElement; prevChecked: boolean; prevValue: string }[];
}

export const patchAppSettings = apiAction<PatchAppArgs, unknown, PatchAppOp>({
  name: "settings.patch",
  scope: "settings",
  request: ({ body }) => ({
    method: "PATCH",
    path: "/api/settings",
    body,
  }),
  optimistic: ({ inputs }) => {
    const list = inputs ?? [];
    return {
      inputs: list.map((el) => ({
        el,
        prevChecked: !el.checked, // user just changed, so prev is opposite of current
        prevValue: el.value,
      })),
    };
  },
  rollback: (_args, op) => {
    if (op === undefined) {
      return;
    }
    for (const { el, prevChecked, prevValue } of op.inputs) {
      if (el.type === "checkbox") {
        writeSwitch(el, prevChecked);
      } else {
        el.value = prevValue;
      }
    }
    paintSettingLocks();
  },
  error: "Could not save setting",
});
