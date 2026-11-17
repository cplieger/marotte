// Model switcher: the pill plus its expandable model list. The open card previews its picks and
// commits them once, on close (`commit`); a model routes through requestModelSwitch (no session,
// an empty chat, a chat with history).

import {
  activeSession,
  get,
  getActive,
  isEmptyChat,
  setEffort,
  setModel,
  setThinkingChoice,
} from "./store.js";
import { $, setBusy } from "./dom.js";
import { humanName, rateLabel } from "./strings.js";
import { switchModel } from "./actions/chat.js";
import { rovingFocus, type RovingFocusController } from "./legacy-ui/roving-focus.js";
import { setLastModel, getLastEffortFor } from "./session-context.js";
import {
  getCachedModels,
  catalogNotice,
  retryCatalog,
  watchCatalog,
  RETRY_LABEL,
} from "./model-list.js";
import { makeExpandable, collapseAll, type Expandable } from "./pill-expand.js";
import { transportAction, RETRY_STANDARD } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { repaintKeepingFocus } from "./focus-keep.js";
import { el, effect, untracked } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { errorAbout } from "./actions/subject.js";
import { retryUnlessStill, stillOr } from "./actions/still-applying.js";
import { ICON_MODEL, modelIcon } from "./icons.js";
import {
  THINKING_OFF,
  effortVocabulary,
  modelThinkingToggleable,
  sameLevels,
  thinkingIsOff,
  thinkingOffShown,
} from "./effort.js";
import { buildEffortSlider, type EffortSliderHandle } from "./effort-slider.js";
import {
  parkedStop,
  resolvePillCommit,
  type PillCommand,
  type PillKnob,
  type PillOpen,
} from "./pill-commit.js";
import { FallbackSlot } from "./model-fallback-row.js";
import { settingsAsShown, shownSettingsSignal } from "./persist.js";
import { currentIdentity } from "./settings.js";
import type { EffectiveSettings } from "./persist.js";
import type { ModelInfo, Session, SessionEffortLevel } from "./types.js";
import { bindLoadingSpinner } from "./spinner.js";

// effort.ts owns the effort vocabulary: the pill names the live tier too.

/** Effort is per-chat on the chat record, so the optimistic write goes to the store. */
export const setEffortAction = transportAction<
  { chatID: string; level: string },
  { prev: string; thinking: string; thinkingActive: string }
>({
  name: "chat.set_effort",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, level }) => ({
    type: "set_effort",
    chat_id: chatID,
    payload: { level },
  }),
  optimistic: ({ chatID, level }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const prev = session.effort ?? "";
    const thinking = session.thinking_choice ?? "";
    const thinkingActive = session.thinking_active ?? "";
    setEffort(chatID, level);
    // A tier pick turns thinking back on server-side (CmdSetEffort), so the slider
    // leaves its Off stop in the same gesture.
    if (thinkingIsOff(session, getCachedModels())) {
      setThinkingChoice(chatID, "on", thinkingActive === "" ? "" : "on");
    }
    return { prev, thinking, thinkingActive };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setEffort(chatID, op.prev);
      setThinkingChoice(chatID, op.thinking, op.thinkingActive);
    }
  },
  retryable: retryUnlessStill,
  retry: RETRY_STANDARD,
  error: errorAbout(
    ({ chatID }: { chatID: string }) => chatID,
    stillOr("setting reasoning effort", "Could not set reasoning effort", "chat"),
  ),
});

/** The slider's Off stop: `set_thinking {enabled:false}`. Turning thinking back on is
 *  never this command, because every way back is a tier pick and `set_effort` does it. */
export const setThinkingOffAction = transportAction<
  { chatID: string },
  { thinking: string; thinkingActive: string }
>({
  name: "chat.set_thinking",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID }) => ({
    type: "set_thinking",
    chat_id: chatID,
    payload: { enabled: false },
  }),
  optimistic: ({ chatID }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const thinking = session.thinking_choice ?? "";
    const thinkingActive = session.thinking_active ?? "";
    setThinkingChoice(chatID, "off", thinkingActive === "" ? "" : "off");
    return { thinking, thinkingActive };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setThinkingChoice(chatID, op.thinking, op.thinkingActive);
    }
  },
  retryable: retryUnlessStill,
  retry: RETRY_STANDARD,
  error: errorAbout(
    ({ chatID }: { chatID: string }) => chatID,
    stillOr("turning thinking off", "Couldn't turn thinking off", "chat"),
  ),
});

/** The tiers the slider renders and the stop it sits on: the effort vocabulary, led by
 *  an Off stop on a model whose thinking can be turned off. */
function sliderVocabulary(session: Session | undefined): {
  levels: readonly SessionEffortLevel[];
  active: string;
} {
  const models = getCachedModels();
  const { levels, active } = effortVocabulary(
    session,
    models,
    getLastEffortFor(session?.model ?? ""),
  );
  if (!modelThinkingToggleable(models, session?.model ?? "")) {
    return { levels, active };
  }
  return {
    levels: [THINKING_OFF, ...levels],
    active: thinkingOffShown(session, models) ? THINKING_OFF.id : active,
  };
}

/** The open card's unsent picks (`level`, `model`) and the chat as it opened (`open`, plus the
 *  thinking state the preview writes over and the close restores before it sends). */
interface PillDraft {
  readonly chatID: string;
  readonly open: PillOpen;
  readonly thinking: string;
  readonly thinkingActive: string;
  level?: string;
  model?: string;
}

class ModelSwitchController {
  init(rereadCatalog: () => Promise<void>): void {
    this.rereadCatalog = rereadCatalog;
    // Prepended because the label follows it. The `.switching` face hides this svg by
    // descendant selector, so it must not be wrapped.
    $.switchModelBtn.prepend(iconEl(ICON_MODEL));
    const expandContent = $.modelSwitchList;
    expandContent.append(this.mainPanel);
    this.card = makeExpandable($.switchModelBtn, expandContent, {
      onExpand: () => {
        this.expanded = true;
        const session = getActive();
        this.draft =
          session === undefined
            ? null
            : {
                chatID: session.id,
                open: {
                  chatModel: session.model,
                  effort: session.effort ?? "",
                  thinkingOff: thinkingOffShown(session, getCachedModels()),
                  pendingModel: session.pending_model ?? "",
                  pendingEffort: session.pending_effort ?? "",
                },
                thinking: session.thinking_choice ?? "",
                thinkingActive: session.thinking_active ?? "",
              };
        // Always the inline list, even on an empty chat: the effort control must be
        // reachable before the first prompt.
        this.renderCondensedList();
      },
      // Every close commits: a model click, Escape, a click away, the pill itself.
      onCollapse: () => {
        this.expanded = false;
        this.fallback.reset();
        this.commit();
      },
    });
    // App-lifetime: a settings change while the card is open (the Model fallback switch, another
    // device's choice) shows, repaints or removes the Fallback row.
    effect(() => {
      const shown = shownSettingsSignal.value;
      if (this.expanded) {
        this.repaint(() => {
          this.syncFallbackRow(shown);
        });
      }
    });
    // An open card repaints when the catalog moves. Untracked, so the render subscribes this
    // effect to nothing but the catalog.
    effect(() => {
      watchCatalog();
      if (this.expanded) {
        untracked(() => {
          this.repaint(() => {
            this.renderCondensedList();
          });
        });
      }
    });
    // Reading activeSession makes the knob per-chat.
    effect(() => {
      this.effortActive = sliderVocabulary(activeSession.value).active;
      this.syncEffortActive();
    });
    // Wired ONCE: re-wiring per render stacks keydown handlers. The first panel's own: the Fallback list has its.
    this.modelNav = rovingFocus(this.mainPanel, '.pill-model-item[role="option"]');
    bindLoadingSpinner("chat.switch_model", $.switchModelBtn, { pendingClass: "switching" });
  }

  /** Runs a repaint of the open card. One that takes focus out of the card (a removed or
   *  re-seated row) hands it to the chat's model, else the card's first control. */
  private repaint(paint: () => void): void {
    repaintKeepingFocus(
      $.modelSwitchList,
      () =>
        this.mainPanel.querySelector<HTMLElement>('.pill-model-item.active[role="option"]') ??
        this.mainPanel.querySelector<HTMLElement>(
          '[role="option"], [role="slider"], button:not(:disabled)',
        ),
      paint,
    );
  }

  private renderCondensedList(): void {
    const list = this.mainPanel;
    const scroll = this.ensureScroll(list);
    const session = getActive();
    if (session === undefined) {
      this.syncFallbackRow(null);
      // No options mount here, so the scroller is not a listbox (see syncCatalogNotice).
      scroll.removeAttribute("role");
      scroll.removeAttribute("aria-label");
      reconcile(scroll, [] as ModelInfo[], {
        key: () => "",
        mount: () => el("div"),
      });
      return;
    }
    const current = session.model;

    // Before sliderVocabulary prepends Off, so a tierless toggleable model shows no row (effort.ts effortVocabulary).
    if (effortVocabulary(session, getCachedModels(), "").levels.length > 0) {
      this.ensureEffortRow(scroll);
    } else {
      this.removeEffortRow();
    }
    this.syncFallbackRow(settingsAsShown());

    this.syncCatalogNotice(scroll);
    reconcile(scroll, getCachedModels(), {
      key: (m: ModelInfo) => m.model_id,
      mount: (m: ModelInfo) => this.buildModelOption(m),
      update: (node, m) => {
        this.syncModelOption(node, m, current);
      },
    });
    this.modelNav?.refresh();
  }

  /** The listbox role goes with the OPTIONS; the copy is model-list.ts's. The row and its Retry
   *  are kept across notices, so a focused Retry keeps focus when a read fails again. */
  private syncCatalogNotice(scroll: HTMLElement): void {
    const notice = catalogNotice();
    if (notice === null) {
      this.noticeRow?.remove();
      this.noticeRow = null;
      this.noticeRetry?.remove();
      this.noticeRetry = null;
      scroll.setAttribute("role", "listbox");
      scroll.setAttribute("aria-label", "Available models");
      scroll.removeAttribute("aria-busy");
      return;
    }
    scroll.removeAttribute("role");
    scroll.removeAttribute("aria-label");
    setBusy(scroll, notice.busy);
    if (this.noticeRow === null) {
      this.noticeRow = el("div", { className: "list-empty" }) as HTMLDivElement;
      scroll.prepend(this.noticeRow);
    }
    this.noticeRow.textContent = notice.text;
    if (!notice.retry) {
      this.noticeRetry?.remove();
      this.noticeRetry = null;
    } else if (this.noticeRetry === null) {
      this.noticeRetry = this.buildRetryRow();
      this.noticeRow.after(this.noticeRetry);
    }
  }

  /** `stopPropagation` keeps the click off the pill trigger. */
  private buildRetryRow(): HTMLButtonElement {
    const btn = el(
      "button",
      { type: "button", className: "btn-small pill-model-retry", "aria-label": RETRY_LABEL },
      "Retry",
    ) as HTMLButtonElement;
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      retryCatalog(this.rereadCatalog);
    });
    return btn;
  }

  private card: Expandable | null = null;

  /** Set by `init`, which runs before any card, and so any Retry, exists. */
  private rereadCatalog!: () => Promise<void>;

  private modelNav: RovingFocusController | null = null;

  private modelScroll: HTMLDivElement | null = null;

  /** Held rather than queried so it can never be confused with a real row. */
  private noticeRow: HTMLDivElement | null = null;

  /** Separate from `noticeRow`: an `unknown` notice carries a row and no button. */
  private noticeRetry: HTMLButtonElement | null = null;

  /** App-lifetime singleton; nothing to dispose. */
  private effortSlider: EffortSliderHandle | null = null;

  /** Null while the card is closed. */
  private draft: PillDraft | null = null;

  private expanded = false;

  /** The card's first panel: the models, the effort row and the Fallback row, which drills into its own. */
  private readonly mainPanel = el("div", { className: "model-panel model-panel-main" });

  private readonly fallback = new FallbackSlot(() => {
    if (this.expanded) {
      this.repaint(() => {
        this.syncFallbackRow(settingsAsShown());
      });
    }
  });

  /** Rebuild the ticks only when the vocabulary changed. */
  private effortLevelsShown: readonly SessionEffortLevel[] = [];

  /** Never persisted: a service default written onto the chat would become a
   *  choice `SessionOpts.Effort` pins to every later session. */
  private effortActive = "";

  /** The effort section is a SIBLING below this scroller, so the tiers stay fixed
   *  while a long list scrolls. Created once; reconcile owns its keyed children. */
  private ensureScroll(list: HTMLElement): HTMLElement {
    if (this.modelScroll === null) {
      // The listbox role belongs to the options; syncCatalogNotice writes it.
      this.modelScroll = el("div", { className: "pill-model-scroll" }) as HTMLDivElement;
      list.appendChild(this.modelScroll);
    }
    return this.modelScroll;
  }

  private removeEffortRow(): void {
    this.effortSlider?.el.remove();
  }

  /** Last in the first panel, for the chat's model. */
  private syncFallbackRow(settings: EffectiveSettings | null): void {
    this.fallback.sync(
      { card: $.modelSwitchList, main: this.mainPanel },
      {
        model: getActive()?.model ?? "",
        settings,
        catalog: getCachedModels(),
        identity: currentIdentity(),
      },
    );
  }

  /** Ticks are rebuilt when the model's vocabulary changes: the set is per model, so
   *  a track built once would offer `xhigh` on a model without it. Effort arrives
   *  on the chat record, so there is no settings fetch. */
  private ensureEffortRow(scroll: HTMLElement): void {
    const { levels, active } = sliderVocabulary(getActive());
    this.effortActive = active;
    // No role/aria-label: the knob carries the name, and a group around one slider
    // announces a nesting that is not there.
    this.effortSlider ??= buildEffortSlider({
      onPick: (level) => {
        this.previewStop(level);
      },
    });
    if (!sameLevels(this.effortLevelsShown, levels)) {
      this.effortSlider.setLevels(levels);
      this.effortLevelsShown = [...levels];
    }
    // Beside the scroller, so reconcile never sees it. Re-seating a node blurs it, so only a
    // slider out of place moves.
    if (scroll.nextElementSibling !== this.effortSlider.el) {
      scroll.after(this.effortSlider.el);
    }
    // The effect re-runs only on the active chat's own level; a rebuild or a newly
    // resolved tier is not a signal read, so re-apply the mark here.
    this.syncEffortActive();
  }

  /** A chat that has chosen nothing still RUNS at a level, so the knob always marks one. The
   *  open card's own pick, then a parked tier, outranks the store, so a header echo cannot move
   *  a previewed knob. */
  private syncEffortActive(): void {
    const draft = this.draft;
    this.effortSlider?.setActive(
      draft?.level ?? (draft === null ? undefined : parkedStop(draft.open)) ?? this.effortActive,
    );
  }

  /** The pill names a picked stop at once, through the store writes its send would make. */
  private previewStop(level: string): void {
    const draft = this.draft;
    if (draft === null) {
      return;
    }
    draft.level = level;
    if (level === THINKING_OFF.id) {
      setEffort(draft.chatID, draft.open.effort);
      setThinkingChoice(draft.chatID, "off", draft.thinkingActive === "" ? "" : "off");
      return;
    }
    setEffort(draft.chatID, level);
    setThinkingChoice(draft.chatID, draft.thinking, draft.thinkingActive);
    const session = get(draft.chatID);
    // A tier pick turns thinking back on server-side (CmdSetEffort), so the preview does too.
    if (thinkingIsOff(session, getCachedModels())) {
      setThinkingChoice(draft.chatID, "on", draft.thinkingActive === "" ? "" : "on");
    }
  }

  /** The card's one send (resolvePillCommit). The preview is put back first, so each action's
   *  optimistic write and rollback start from the chat's state. */
  private commit(): void {
    const draft = this.draft;
    this.draft = null;
    if (draft === null) {
      return;
    }
    if (draft.level !== undefined) {
      setEffort(draft.chatID, draft.open.effort);
      setThinkingChoice(draft.chatID, draft.thinking, draft.thinkingActive);
    }
    if (get(draft.chatID) === undefined) {
      return;
    }
    const knob: PillKnob =
      draft.level === undefined ? { touched: false } : { touched: true, level: draft.level };
    this.send(draft.chatID, resolvePillCommit(draft.open, knob, draft.model));
  }

  /** set_effort writes the seed (`last_effort_by_model`), so a refused level is never remembered. */
  private send(chatID: string, cmd: PillCommand): void {
    switch (cmd.kind) {
      case "none":
        return;
      case "set_effort":
        void setEffortAction.dispatch({ chatID, level: cmd.level });
        return;
      case "thinking_off":
        void setThinkingOffAction.dispatch({ chatID });
        return;
      case "switch":
        this.requestModelSwitch(cmd.model, cmd.effort, chatID);
    }
  }

  private buildModelOption(m: ModelInfo): HTMLElement {
    const label = humanName(m.model_name || m.model_id);
    const rate = rateLabel(m.rate_multiplier);
    const opt = el(
      "div",
      {
        "data-model": m.model_id,
        role: "option",
        "aria-label": rate === "" ? label : `${label}, ${rate} credits`,
      },
      el(
        "span",
        { className: "pill-model-item-icon", "aria-hidden": "true" },
        iconEl(modelIcon(m.model_id, "ui")),
      ),
      el("span", { className: "pill-model-name" }, label),
    );
    if (rate !== "") {
      opt.append(el("span", { className: "pill-model-meta" }, rate));
    }
    // The close commits the pick (`commit`).
    opt.addEventListener("click", (e: MouseEvent) => {
      e.stopPropagation();
      if (this.draft !== null) {
        this.draft.model = m.model_id;
      }
      collapseAll();
    });
    this.syncModelOption(opt, m, getActive()?.model ?? "");
    return opt;
  }

  private syncModelOption(opt: HTMLElement, m: ModelInfo, current: string): void {
    const isCurrent = m.model_id === current;
    opt.className = isCurrent ? "pill-model-item active" : "pill-model-item";
    opt.setAttribute("aria-selected", isCurrent ? "true" : "false");
  }

  openCard(): void {
    this.card?.open();
  }

  /** `effort` is a switch's effort (PillCommand); `chatID` names the chat the pick was made on,
   *  defaulting to the active one. */
  requestModelSwitch(modelID: string, effort?: string, chatID?: string): void {
    const session = chatID === undefined ? getActive() : get(chatID);
    if (session === undefined) {
      this.applyLocalChoice(modelID);
      return;
    }
    const isEmpty = isEmptyChat(session);
    if (isEmpty) {
      // Local apply PLUS the command: every header echo carries the record, so a
      // local-only pick would be clobbered.
      this.applyLocalChoice(modelID, session.id);
      this.fire(session.id, modelID, effort);
      return;
    }
    // Mid-turn too: the server applies `pending_model` at the running turn's close.
    this.fire(session.id, modelID, effort);
  }

  private applyLocalChoice(modelID: string, chatID?: string): void {
    applyLocalModel(modelID, chatID);
  }

  private fire(chatID: string, modelID: string, effort: string | undefined): void {
    void switchModel.dispatch({
      chatID,
      model: modelID,
      ...(effort === undefined ? {} : { effort }),
    });
  }
}

const controller = new ModelSwitchController();

/** `rereadCatalog` is the read the card's Retry runs; its promise settles once the read has. */
export function initModelSwitcher(rereadCatalog: () => Promise<void>): void {
  controller.init(rereadCatalog);
}

/** Open the model pill's card. */
export function openModelCard(): void {
  controller.openCard();
}

function applyLocalModel(modelID: string, chatID?: string): void {
  setLastModel(modelID);
  const session = chatID === undefined ? getActive() : get(chatID);
  if (session !== undefined) {
    // setModel replaces the session object; chat.ts's effect repaints from the new one.
    setModel(session.id, modelID);
  }
}

/** The one door for a model pick from ANY surface, so an empty-chat pick
 *  persists on the record. `chatID` names the chat the pick was made on, defaulting to the
 *  active one. */
export function pickModel(modelID: string, chatID?: string): void {
  controller.requestModelSwitch(modelID, undefined, chatID);
}
