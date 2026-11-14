// The model picker's Fallback row: the account's fallback for the chat's model, as KAS composes it
// (settings.model_fallbacks), with the served models as targets. `model-switcher.ts` places it.

import { el } from "@cplieger/reactive";
import { setModelFallback } from "./actions/model-fallback.js";
import { setBusy } from "./dom.js";
import { humanName } from "./strings.js";
import type { IdentityVerdict } from "./identity.js";
import type { EffectiveSettings } from "./persist.js";
import type { ModelInfo } from "./types.js";
import type { ModelFallback } from "./wire/types.gen.js";

/** KAS refuses a fallback for Auto and for Auto as a target ("not eligible for model fallback"). */
const AUTO = "auto";

const NAV_KEYS: ReadonlySet<string> = new Set(["ArrowUp", "ArrowDown", "Home", "End"]);

export const SIGN_IN_WORDS = "Sign in to Kiro to choose a fallback.";

/** kiro-cli's own words for a row it will not let a client edit (its terminal UI's reasons). */
const READ_ONLY_WORDS: ReadonlyMap<string, string> = new Map([
  ["locked", "This fallback setting is locked by policy."],
  ["multiple-targets", "This fallback chain has multiple targets and cannot be edited here."],
]);

export interface FallbackRowInput {
  /** The chat's model id. */
  readonly model: string;
  readonly settings: EffectiveSettings | null;
  readonly catalog: readonly ModelInfo[];
  readonly identity: IdentityVerdict;
}

/** An API key signs kiro-cli in without a Kiro account, so it has no account layer to write. */
function noAccount(identity: IdentityVerdict): boolean {
  return identity.state === "signed_out" || (identity.state === "signed_in" && identity.apiKey);
}

/** The row, or null while the Model fallback switch is off or KAS states no fallback for the model. */
export function buildFallbackRow(input: FallbackRowInput): HTMLElement | null {
  const { model, settings, catalog, identity } = input;
  if (settings?.model_fallback_enabled !== true || model === "" || model === AUTO) {
    return null;
  }
  const signedOut = noAccount(identity);
  const stated = settings.model_fallbacks.find((f) => f.model === model);
  if (stated === undefined && !signedOut) {
    return null;
  }
  const row: ModelFallback = stated ?? {
    model,
    configured: "",
    vendor_default: "",
    read_only_reason: "",
  };
  const nameOf = (id: string): string => {
    const name = catalog.find((m) => m.model_id === id)?.model_name ?? "";
    return humanName(name === "" ? id : name);
  };

  const select = el("select", {
    className: "rf-select",
    "aria-label": "Fallback model",
    "aria-describedby": "model-fallback-hint",
  }) as HTMLSelectElement;
  const hint = el("span", {
    id: "model-fallback-hint",
    className: "section-hint",
    "aria-live": "polite",
  });
  const reason = signedOut ? SIGN_IN_WORDS : (READ_ONLY_WORDS.get(row.read_only_reason) ?? "");

  const paint = (r: ModelFallback): void => {
    const targets = catalog.map((m) => m.model_id).filter((id) => id !== model && id !== AUTO);
    if (r.configured !== "" && !targets.includes(r.configured)) {
      targets.push(r.configured);
    }
    const kiro =
      r.vendor_default === "" ? "Kiro default" : `Kiro default (${nameOf(r.vendor_default)})`;
    select.replaceChildren(
      el("option", { value: "" }, kiro),
      ...targets.map((id) => el("option", { value: id }, nameOf(id))),
    );
    select.value = r.configured;
  };
  paint(row);
  let shown = row;
  let pending = 0;
  hint.textContent = reason;
  hint.hidden = reason === "";
  select.disabled = reason !== "";

  // Stop the picker's own handlers: a click inside the card must not reach the pill trigger.
  select.addEventListener("click", (e) => {
    e.stopPropagation();
  });
  // The card's rovingFocus reads no target and would move focus into the model list; the select keeps
  // its native handling of these keys, and Escape still reaches the popup.
  select.addEventListener("keydown", (e) => {
    if (NAV_KEYS.has(e.key)) {
      e.stopPropagation();
    }
  });
  select.addEventListener("change", () => {
    const target = select.value;
    // Busy, not disabled: disabling the focused select drops focus to the page.
    pending += 1;
    setBusy(select, true);
    void setModelFallback
      .dispatch(
        { model, target },
        {
          silent: true,
          onSuccess: (answer) => {
            shown = answer;
          },
          onError: (err) => {
            if (err.code === "unavailable") {
              hint.textContent = SIGN_IN_WORDS;
              hint.hidden = false;
            }
          },
        },
      )
      .finally(() => {
        pending -= 1;
        // An answer for an earlier pick must not repaint over a later one still being written.
        if (pending === 0) {
          paint(shown);
          setBusy(select, false);
          select.disabled = hint.textContent !== "";
        }
      });
  });

  return el(
    "div",
    { className: "effort-row" },
    el("span", { className: "effort-label" }, "Fallback"),
    select,
    hint,
  );
}
