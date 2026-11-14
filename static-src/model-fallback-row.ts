// The model picker's Fallback row: the account's fallback for the chat's model, as KAS composes it
// (settings.model_fallbacks), with the served models as targets. `model-switcher.ts` holds its FallbackSlot.

import { el } from "@cplieger/reactive";
import { setModelFallback } from "./actions/model-fallback.js";
import { setBusy } from "./dom.js";
import { humanName } from "./strings.js";
import type { IdentityVerdict } from "./identity.js";
import { showFallbackAnswer, type EffectiveSettings } from "./persist.js";
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

interface FallbackRowInput {
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

/** A built Fallback row. */
interface FallbackRow {
  readonly el: HTMLElement;
  /** True while a write the row sent is unanswered. */
  busy(): boolean;
  /** The row the select shows once no write is pending. */
  shown(): ModelFallback;
  /** Repaints the select and its reason in place, so a focused select keeps focus. */
  show(row: ModelFallback): void;
}

/** The stated row for the input's model, or null when no row is shown. Signed out or with an API key
 *  there is a row, read-only, whether or not KAS states one. */
function rowFor(input: FallbackRowInput): ModelFallback | null {
  const { model, settings, identity } = input;
  if (settings?.model_fallback_enabled !== true || model === "" || model === AUTO) {
    return null;
  }
  const stated = settings.model_fallbacks.find((f) => f.model === model);
  if (stated !== undefined) {
    return stated;
  }
  return noAccount(identity)
    ? { model, configured: "", vendor_default: "", read_only_reason: "" }
    : null;
}

/** The row, or null while the Model fallback switch is off, for Auto, or when KAS states no fallback
 *  for the model and an account is signed in; signed out or with an API key, a read-only row.
 *  `onSettled` runs when the last write the row sent settles. */
export function buildFallbackRow(
  input: FallbackRowInput,
  onSettled: () => void = () => undefined,
): FallbackRow | null {
  const row = rowFor(input);
  if (row === null) {
    return null;
  }
  const { model, catalog } = input;
  const signedOut = noAccount(input.identity);
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
  const showReason = (r: ModelFallback): void => {
    const reason = signedOut ? SIGN_IN_WORDS : (READ_ONLY_WORDS.get(r.read_only_reason) ?? "");
    hint.textContent = reason;
    hint.hidden = reason === "";
    select.disabled = reason !== "";
  };
  paint(row);
  showReason(row);
  let shown = row;
  let pending = 0;

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
            showFallbackAnswer(answer);
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
          onSettled();
        }
      });
  });

  return {
    el: el(
      "div",
      { className: "effort-row" },
      el("span", { className: "effort-label" }, "Fallback"),
      select,
      hint,
    ),
    busy: () => pending > 0,
    shown: () => shown,
    show: (r) => {
      shown = r;
      paint(r);
      showReason(r);
    },
  };
}

function sameRow(a: ModelFallback, b: ModelFallback): boolean {
  return (
    a.configured === b.configured &&
    a.vendor_default === b.vendor_default &&
    a.read_only_reason === b.read_only_reason
  );
}

/** The card's one Fallback row, kept across settings steps: rebuilding it would replace a select the
 *  user has focus on. A step that comes while a write is pending is not applied then: `onSettled`
 *  runs when the write settles, for the card to sync again from the settings shown by then. */
export class FallbackSlot {
  private row: FallbackRow | null = null;
  private built: FallbackRowInput | null = null;

  constructor(private readonly onSettled: () => void) {}

  /** Shows, repaints in place, or removes the row for `input`, last in `list`. */
  sync(list: HTMLElement, input: FallbackRowInput): void {
    if (this.row?.busy() === true) {
      return;
    }
    const next = rowFor(input);
    const was = this.built;
    if (
      this.row !== null &&
      was !== null &&
      next !== null &&
      was.model === input.model &&
      was.catalog === input.catalog &&
      noAccount(was.identity) === noAccount(input.identity)
    ) {
      if (!sameRow(this.row.shown(), next)) {
        this.row.show(next);
      }
    } else {
      this.row?.el.remove();
      this.row = buildFallbackRow(input, this.onSettled);
    }
    this.built = next === null ? null : input;
    // Re-seating a node blurs it, so only a row that is not already last moves.
    if (this.row !== null && list.lastElementChild !== this.row.el) {
      list.appendChild(this.row.el);
    }
  }
}
