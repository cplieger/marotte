// The model picker's Fallback row: the account's fallback for the chat's model, as KAS composes it
// (settings.model_fallbacks), with the served models as targets. The row drills into a second panel of the card that
// lists them (card-drill.ts). `model-switcher.ts` holds its FallbackSlot.

import { el } from "@cplieger/reactive";
import { setModelFallback } from "./actions/model-fallback.js";
import { slidePanel, settlePanels } from "./card-drill.js";
import { chevronEl } from "./chevron.js";
import { setBusy } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { modelIcon } from "./icons.js";
import { rovingFocus } from "./legacy-ui/roving-focus.js";
import { humanName, rateLabel } from "./strings.js";
import type { IdentityVerdict } from "./identity.js";
import { showFallbackAnswer, type EffectiveSettings } from "./persist.js";
import type { ModelInfo } from "./types.js";
import type { ModelFallback } from "./wire/types.gen.js";

/** KAS refuses a fallback for Auto and for Auto as a target ("not eligible for model fallback"). */
const AUTO = "auto";

const PANEL_ID = "model-fallback-panel";
const HINT_ID = "model-fallback-hint";

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

/** The model card and its first panel, which the row sits last in. The list panel goes last in the card. */
interface FallbackHost {
  readonly card: HTMLElement;
  readonly main: HTMLElement;
}

/** An API key signs kiro-cli in without a Kiro account, so it has no account layer to write. */
function noAccount(identity: IdentityVerdict): boolean {
  return identity.state === "signed_out" || (identity.state === "signed_in" && identity.apiKey);
}

/** A built Fallback row. */
interface FallbackRow {
  /** The row's section in the first panel. */
  readonly el: HTMLElement;
  /** The list it drills into. */
  readonly panel: HTMLElement;
  /** True while a write the row sent is unanswered. */
  busy(): boolean;
  /** The row the list shows once no write is pending. */
  shown(): ModelFallback;
  /** Repaints the row and its list in place, so a focused option keeps focus. */
  show(row: ModelFallback): void;
  /** Shows the first panel at once. */
  reset(): void;
  /** Takes the row and its list out of the card. */
  remove(): void;
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
function buildFallbackRow(
  input: FallbackRowInput,
  host: FallbackHost,
  onSettled: () => void,
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
  const kiroLabel = (r: ModelFallback): string =>
    r.vendor_default === "" ? "Kiro default" : `Kiro default (${nameOf(r.vendor_default)})`;

  const choice = el("span", { className: "pill-model-meta" });
  const opener = el(
    "button",
    {
      type: "button",
      className: "pill-model-item fallback-open",
      "aria-expanded": "false",
      "aria-controls": PANEL_ID,
      "aria-describedby": HINT_ID,
    },
    el("span", { className: "pill-model-name" }, "Fallback"),
    choice,
    chevronEl(),
  ) as HTMLButtonElement;
  const hint = el("span", { id: HINT_ID, className: "section-hint", "aria-live": "polite" });

  const list = el("div", {
    className: "pill-model-scroll",
    role: "listbox",
    "aria-label": "Fallback model",
  });
  const back = el(
    "button",
    { type: "button", className: "pill-model-item fallback-back" },
    chevronEl(),
    el("span", { className: "pill-model-name" }, "Back"),
  ) as HTMLButtonElement;
  const panel = el(
    "div",
    { id: PANEL_ID, className: "model-panel model-panel-fallback" },
    el("div", { className: "fallback-note" }, `Fallback for ${nameOf(model)}`),
    list,
    el("div", { className: "effort-row" }, back),
  );
  panel.hidden = true;
  const nav = rovingFocus(list, '[role="option"]');

  const option = (value: string, label: string, logo: string): HTMLElement => {
    const rate = rateLabel(catalog.find((m) => m.model_id === logo)?.rate_multiplier);
    const opt = el(
      "div",
      {
        role: "option",
        className: "pill-model-item",
        "data-target": value,
        "aria-label": rate === "" ? label : `${label}, ${rate} credits`,
      },
      el(
        "span",
        { className: "pill-model-item-icon", "aria-hidden": "true" },
        iconEl(modelIcon(logo, "ui")),
      ),
      el("span", { className: "pill-model-name" }, label),
    );
    if (rate !== "") {
      opt.append(el("span", { className: "pill-model-meta" }, rate));
    }
    opt.addEventListener("click", (e) => {
      e.stopPropagation();
      choose(value);
    });
    return opt;
  };

  let listed = "";
  let selected = row.configured;
  /** Shows `target` as the choice; the list is rebuilt only when its options change, so a focused one keeps focus. */
  const paint = (r: ModelFallback, target = r.configured): void => {
    const targets = catalog.map((m) => m.model_id).filter((id) => id !== model && id !== AUTO);
    if (r.configured !== "" && !targets.includes(r.configured)) {
      targets.push(r.configured);
    }
    const entries: [string, string, string][] = [
      ["", kiroLabel(r), r.vendor_default === "" ? AUTO : r.vendor_default],
      ...targets.map((id): [string, string, string] => [id, nameOf(id), id]),
    ];
    const sig = JSON.stringify(entries);
    if (sig !== listed) {
      listed = sig;
      list.replaceChildren(...entries.map(([value, label, logo]) => option(value, label, logo)));
      nav.refresh();
    }
    selected = target;
    for (const opt of list.querySelectorAll<HTMLElement>('[role="option"]')) {
      const on = opt.dataset["target"] === target;
      opt.classList.toggle("active", on);
      opt.setAttribute("aria-selected", on ? "true" : "false");
    }
    const label = target === "" ? kiroLabel(r) : nameOf(target);
    choice.textContent = label;
    opener.setAttribute("aria-label", `Fallback: ${label}`);
  };
  const showReason = (r: ModelFallback): void => {
    const reason = signedOut ? SIGN_IN_WORDS : (READ_ONLY_WORDS.get(r.read_only_reason) ?? "");
    hint.textContent = reason;
    hint.hidden = reason === "";
    opener.disabled = reason !== "";
  };

  const open = (): void => {
    if (opener.disabled || !panel.hidden) {
      return;
    }
    // The list takes the first panel's width, so the card does not narrow under the pointer.
    panel.style.minInlineSize = `${String(host.main.getBoundingClientRect().width)}px`;
    slidePanel(host.card, host.main, panel, true);
    opener.setAttribute("aria-expanded", "true");
    const target =
      list.querySelector<HTMLElement>('[aria-selected="true"]') ??
      list.querySelector<HTMLElement>('[role="option"]');
    nav.refresh();
    if (target !== null) {
      for (const opt of list.querySelectorAll<HTMLElement>('[role="option"]')) {
        opt.tabIndex = opt === target ? 0 : -1;
      }
      target.focus({ preventScroll: true });
    }
  };
  const close = (): void => {
    if (panel.hidden) {
      return;
    }
    slidePanel(host.card, panel, host.main, false);
    opener.setAttribute("aria-expanded", "false");
    opener.focus({ preventScroll: true });
  };

  let shown = row;
  let pending = 0;
  const choose = (target: string): void => {
    if (target === selected) {
      close();
      return;
    }
    pending += 1;
    setBusy(opener, true);
    paint(shown, target);
    close();
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
          setBusy(opener, false);
          opener.disabled = hint.textContent !== "";
          onSettled();
        }
      });
  };

  paint(row);
  showReason(row);
  // Stop the picker's own handlers: a click inside the card must not reach the pill trigger.
  opener.addEventListener("click", (e) => {
    e.stopPropagation();
    open();
  });
  back.addEventListener("click", (e) => {
    e.stopPropagation();
    close();
  });
  // The first Escape comes back from the list; the next one reaches the card's popup and closes it.
  panel.addEventListener("keydown", (e) => {
    const rtl = getComputedStyle(panel).direction === "rtl";
    if (e.key === "Escape" || e.key === (rtl ? "ArrowRight" : "ArrowLeft")) {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  });

  const reset = (): void => {
    settlePanels(host.card);
    if (!panel.hidden) {
      panel.hidden = true;
      host.main.hidden = false;
      opener.setAttribute("aria-expanded", "false");
    }
  };
  return {
    el: el("div", { className: "effort-row" }, opener, hint),
    panel,
    busy: () => pending > 0,
    shown: () => shown,
    show: (r) => {
      shown = r;
      paint(r);
      showReason(r);
    },
    reset,
    remove() {
      reset();
      nav.dispose();
      this.el.remove();
      panel.remove();
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

/** The card's one Fallback row, kept across settings steps: rebuilding it would replace a row the
 *  user has focus on. A step that comes while a write is pending is not applied then: `onSettled`
 *  runs when the write settles, for the card to sync again from the settings shown by then. */
export class FallbackSlot {
  private row: FallbackRow | null = null;
  private built: FallbackRowInput | null = null;

  constructor(private readonly onSettled: () => void) {}

  /** Shows, repaints in place, or removes the row for `input`, last in the host's first panel. */
  sync(host: FallbackHost, input: FallbackRowInput): void {
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
      this.row?.remove();
      this.row = buildFallbackRow(input, host, this.onSettled);
    }
    this.built = next === null ? null : input;
    // Re-seating a node blurs it, so only a row that is not already last moves.
    if (this.row !== null && host.main.lastElementChild !== this.row.el) {
      host.main.appendChild(this.row.el);
    }
    if (this.row !== null && host.card.lastElementChild !== this.row.panel) {
      host.card.appendChild(this.row.panel);
    }
  }

  /** Back to the first panel at once, as the card closes. */
  reset(): void {
    this.row?.reset();
  }
}
