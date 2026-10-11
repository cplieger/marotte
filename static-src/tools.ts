// Tool management (Settings -> Tools) over the v2 tools engine: a pure projection of the server's
// manifest, install state and single-flight job queue. Mutations return 202 with a job; progress
// arrives over tool_job_changed / tool_job_output (the output panel survives reloads via GET
// /api/tools/jobs). The add flow is search-first over the mise + aqua catalog. No manual-command
// form: the shell is one click away, and every result set's closing note says so.

import { closeModal, openModal, RollingOutput } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { findGlyph, ICON_PIN, ICON_PIN_FILLED, ICON_SPINNER, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import {
  loadTools,
  createTool,
  installTool,
  updateTools,
  patchTool,
  deleteTool,
  searchTools,
  getToolsJobs,
  getCatalogInfo,
  refreshCatalog,
  ensureTool,
  cancelToolJob,
  TOOLS_UNAVAILABLE,
} from "./actions/tools.js";
import type { CreateToolRequest, ToolSearchResponse } from "./actions/tools.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { onSSE } from "./bus.js";
import { $, byId } from "./dom.js";
import { openConfigFile } from "./editor-openers.js";
import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { reconcile, KEY_ATTR } from "./reconcile.js";
import { sigChanged, wireSignature } from "./paint-sig.js";
import { relativeTime } from "./relative-time.js";
import { classify, emptyNote, type Nouns } from "./textsearch/copy.js";
import { error as toastError, errorWithAction } from "./toast.js";
import { openGitView } from "./tabs.js";
import {
  accountRaisesLimit,
  bodyRateLimit,
  jobRateLimit,
  rateLimitText,
} from "./tool-rate-limit.js";
import type { ActionErrorLike } from "./actions/index.js";
import type { AptPackage, CatalogInfo, Inventory, Job, SearchHit, ToolInfo } from "./types.js";
import type { GitHubRateLimit } from "./wire/types.gen.js";

/** A hit is a tool; what the engine reads is the catalog and the host's package
 *  index, so the scanned unit is a source. */
const NOUNS: Nouns = {
  match: { one: "tool", many: "tools" },
  scanned: { one: "source", many: "sources" },
};

/** The readout over a non-empty list. An engine that reports how many rows
 *  matched gives the cut a denominator; one that does not can only state that
 *  there was a cut. */
function resultCount(shown: number, cut: boolean, matched?: number): string {
  const narrow = "narrow the query to see the rest";
  if (matched === undefined) {
    return cut
      ? `${String(shown)} shown. More matched than shown, so ${narrow}`
      : `${String(shown)} shown`;
  }
  return matched > shown
    ? `${String(shown)} of ${String(matched)} shown, ${narrow}`
    : `${String(shown)} shown`;
}

/** `indexing` is PENDING: the same search answers differently a moment later. An engine stating
 *  nothing names no cause. */
function aptNote(d: ToolSearchResponse): string {
  if (d.apt_state === undefined) {
    return d.apt_available ? "" : "Debian packages are not searchable right now.";
  }
  switch (d.apt_state) {
    case "available":
      return "";
    case "indexing":
      return "Debian packages are not searchable yet: the package index is still loading. Search again in a moment.";
    case "unavailable":
      return "Debian packages are not searchable here: this host has no usable apt.";
  }
}

function loadFailureText(err: ActionErrorLike): string {
  return err.code === TOOLS_UNAVAILABLE ? `Tools are off: ${err.message}` : "Failed to load tools";
}

/** The 409 cascade envelope both destructive mutations answer with. */
interface CascadeReply {
  code?: string;
  dependents?: string[];
}

/** `cancel` exists because the same search has two immediate doors (Enter, the button): without it
 *  each one is followed 200ms later by an identical query. */
function debounce(fn: () => void, ms: number): { (): void; cancel: () => void } {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const cancel = (): void => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };
  const run = (): void => {
    cancel();
    timer = setTimeout(() => {
      timer = null;
      fn();
    }, ms);
  };
  run.cancel = cancel;
  return run;
}

type ListEntry =
  | { kind: "label"; label: string }
  | { kind: "tool"; tool: ToolInfo }
  | { kind: "system"; name: string }
  | { kind: "apt"; pkg: AptPackage };

// The rest — install, uninstall, disable and reconcile — come from a tool row, a tools.json save or
// the boot, so they are the residual case in `pillOwns` and are never named here.
const JOB_UPDATE = "update";
const JOB_CATALOG_REFRESH = "catalog-refresh";

/** One predicate, because every control's face and the output panel's headline read the same answer. */
function jobIsLive(job: Job): boolean {
  return job.state === "queued" || job.state === "running";
}

/** Whether a pill carries the cancel for a job KIND, whoever launched it. Two kinds have a named
 *  pill; install, uninstall, disable and reconcile cancel through the shared Cancel pill, exhaustive
 *  over toolbelt's six kinds. A per-tool Update rides Update all. */
function pillOwns(kind: string): boolean {
  return kind === JOB_UPDATE || kind === JOB_CATALOG_REFRESH;
}

interface JobPillSpec {
  start: () => void;
  cancel: () => void;
  /** The visible label is just "Cancel"; a screen reader gets the whole "Cancel the running update",
   *  because out of the row's context a bare "Cancel" says nothing about what it stops. */
  busyAria: string;
}

/** An action pill that owns one job kind: it launches the work and, while it is live, BECOMES the
 *  control that stops it (spinner, "Cancel"), as the composer's send button does. The idle face is
 *  CAPTURED from index.html. Nothing sets `disabled`: the busy face IS the cancel; `requested`
 *  stops a double-sent cancel, and a pill whose click cancels cannot enqueue a duplicate. */
class JobPill {
  private readonly idleFace: readonly Node[];
  private readonly idleAria: string;
  private busy = false;
  private requested = false;

  constructor(
    private readonly btn: HTMLButtonElement,
    private readonly spec: JobPillSpec,
  ) {
    this.idleFace = [...btn.childNodes].map((n) => n.cloneNode(true));
    this.idleAria = btn.getAttribute("aria-label") ?? "";
    btn.addEventListener("click", () => {
      this.onClick();
    });
  }

  /** Idempotent: the SSE reports one event per state transition and several of them describe the
   *  same live job. */
  setBusy(busy: boolean): void {
    if (busy === this.busy) {
      return;
    }
    this.busy = busy;
    this.requested = false;
    this.paint();
  }

  private onClick(): void {
    if (!this.busy) {
      this.spec.start();
      return;
    }
    if (this.requested) {
      return;
    }
    this.requested = true;
    this.paint();
    this.spec.cancel();
  }

  private paint(): void {
    if (!this.busy) {
      this.btn.replaceChildren(...this.idleFace.map((n) => n.cloneNode(true)));
      this.btn.setAttribute("aria-label", this.idleAria);
      this.btn.removeAttribute("data-tooltip");
      this.btn.classList.remove("is-busy");
      return;
    }
    const label = this.requested ? "Cancelling…" : "Cancel";
    const full = this.requested ? label : this.spec.busyAria;
    this.btn.replaceChildren(iconEl(ICON_SPINNER), el("span", null, label));
    // One visible word so the pill does not resize its row; the full phrase is the accessible name and
    // tooltip.
    this.btn.setAttribute("aria-label", full);
    this.btn.setAttribute("data-tooltip", full);
    this.btn.classList.add("is-busy");
  }
}

const f = {
  get cancel(): HTMLButtonElement {
    return byId("tool-cancel-btn");
  },
  get catalogRefresh(): HTMLButtonElement {
    return byId("tool-catalog-refresh-btn");
  },
  get openManifest(): HTMLButtonElement {
    return byId("tool-open-manifest");
  },
  get catalogMeta(): HTMLParagraphElement {
    return byId("tool-catalog-meta");
  },
  get search(): HTMLInputElement {
    return byId("tool-search");
  },
  get searchBtn(): HTMLButtonElement {
    return byId("tool-search-btn");
  },
  get shellNoteApt(): HTMLSpanElement {
    return byId("tool-shell-note-apt");
  },
  get sort(): HTMLSelectElement {
    return byId("tool-sort");
  },
  get resultCount(): HTMLOutputElement {
    return byId("tool-results-count");
  },
  get results(): HTMLDivElement {
    return byId("tool-search-results");
  },
};

/** `relevance` is the SERVER's, adopted verbatim: it scores both corpora on one scale and aliases
 *  never reach the wire. No `popularity`: nothing on the wire measures it. */
type SortOrder = "relevance" | "name-asc" | "name-desc";

function isSortOrder(v: string): v is SortOrder {
  return v === "relevance" || v === "name-asc" || v === "name-desc";
}

class ToolsManager {
  private data: Inventory | null = null;
  private output: RollingOutput | null = null;
  private followedJob = "";
  /** The job the SSE last reported queued or running, null when nothing is.
   *  Decides which of the three controls carries its cancel. */
  private live: Job | null = null;
  /** The last job whose rate limit was reported, so a repeated frame says it once. */
  private rateLimitReported = "";
  private updatePill: JobPill | null = null;
  private refreshPill: JobPill | null = null;
  private unsubscribes: (() => void)[] = [];
  /** The last search response, kept so the order picker and the name-match
   *  filter re-paint without a round trip. Null means the request failed,
   *  which is a different thing from an empty result set. */
  private lastSearch: ToolSearchResponse | null = null;
  /** The trimmed query behind `lastSearch`: empty makes a no-rows reply a featured browse. Not the
   *  live input, which may be edited past the results. */
  private lastQuery = "";

  /** Public hook for global cleanup: cancels in-flight tool fetch. */
  cancelLoad(): void {
    loadTools.cancel();
  }

  init(): void {
    this.output = new RollingOutput($.toolUpdateOutput, "git-output-modal");
    // Wiring resets what the panel believes is running (seed or first SSE event, never last time). The
    // pills CAPTURE their idle face from the live DOM, so re-mount the markup before a second init().
    this.live = null;
    this.followedJob = "";

    $.toolAddBtn.addEventListener("click", () => {
      this.openAddModal();
    });
    this.updatePill = new JobPill($.toolUpdateBtn, {
      start: () => {
        void updateTools.dispatch(undefined);
      },
      cancel: () => {
        this.cancelLiveJob();
      },
      busyAria: "Cancel the running update",
    });
    bindLoadingState("tools.update", $.toolUpdateBtn);
    this.refreshPill = new JobPill(f.catalogRefresh, {
      start: () => {
        void refreshCatalog.dispatch(undefined);
      },
      cancel: () => {
        this.cancelLiveJob();
      },
      busyAria: "Cancel the running catalog refresh",
    });
    bindLoadingState("tools.refresh_catalog", f.catalogRefresh);
    f.openManifest.addEventListener("click", () => {
      void openConfigFile("tools.json");
    });
    // The residual Cancel pill, for a job kind no pill above owns.
    f.cancel.addEventListener("click", () => {
      this.cancelLiveJob();
    });

    // Live job following; subscriptions are module-lifetime, surviving Settings open/close.
    this.unsubscribes.push(
      onSSE("tool_job_changed", (_chat, payload) => {
        const job = payload.job;
        if (job === undefined) {
          return;
        }
        // The SSE stream is ordered, so its account of what is running is never
        // stale: this is the authoritative writer for the control faces, and the
        // inventory only ever SEEDS them.
        const live = jobIsLive(job);
        this.setLive(live ? job : null);
        this.followJob(job);
        const limit = jobRateLimit(job);
        if (limit !== null && job.id !== this.rateLimitReported) {
          this.rateLimitReported = job.id;
          reportRateLimit(limit);
        }
        // loadToolsList refetches the catalog meta line too, so a
        // settling catalog-refresh job needs no extra fetch here.
        this.loadToolsList();
      }),
      onSSE("tool_job_output", (_chat, payload) => {
        if (payload.job_id !== this.followedJob || this.output === null) {
          return;
        }
        for (const line of payload.lines) {
          this.output.append(line);
        }
      }),
    );

    // Add-modal wiring: three doors into one search, plus the order picker over
    // the result set. The picker re-paints the cached response rather than
    // re-querying: it is a decision about a set in hand.
    const runSearch = debounce(() => {
      void this.renderSearch(f.search.value);
    }, 200);
    f.search.addEventListener("input", runSearch);
    // Enter and the button both mean NOW, so each cancels the pending debounce
    // rather than racing it into a second identical query.
    const searchNow = (): void => {
      runSearch.cancel();
      void this.renderSearch(f.search.value);
    };
    f.search.addEventListener("keydown", (ev: KeyboardEvent) => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        searchNow();
      }
    });
    f.searchBtn.addEventListener("click", searchNow);
    // Both faces come from the registry rather than the markup, so each glyph keeps
    // ONE drawing. A MAGNIFIER because this box reaches past what is on screen,
    // which is the distinction `findGlyph` exists to make.
    const magnifier = iconEl(findGlyph("search"));
    magnifier.classList.add("tool-search-glyph");
    const spinner = iconEl(ICON_SPINNER);
    spinner.classList.add("tool-search-spinner");
    f.searchBtn.replaceChildren(magnifier, spinner);
    bindLoadingState("tools.search", f.searchBtn, { pendingClass: "is-busy" });
    f.sort.addEventListener("change", () => {
      this.paintSearch();
    });
  }

  /** Point the output panel at a job: reset on a new id, headline the
   *  terminal state, refresh the follow target. */
  private followJob(job: Job): void {
    if (this.output === null) {
      return;
    }
    if (job.id !== this.followedJob) {
      this.followedJob = job.id;
      this.output.clear();
      this.output.append(jobHeadline(job));
      return;
    }
    if (!jobIsLive(job)) {
      this.output.append(jobHeadline(job));
    }
  }

  /** THE writer for the three controls a live job decides, because the ownership
   *  rule has to be asked the same way by the pill that turns into Cancel and by
   *  the pill that hides. Answering it in two places is how they disagree. */
  private setLive(job: Job | null): void {
    this.live = job;
    const kind = job?.kind ?? "";
    this.updatePill?.setBusy(kind === JOB_UPDATE);
    this.refreshPill?.setBusy(kind === JOB_CATALOG_REFRESH);
    f.cancel.classList.toggle("hidden", job === null || pillOwns(kind));
  }

  /** Give the inventory ONE chance to report a job that started before this lazily wired module
   *  listened. Inbound only: a set `followedJob` means the stream already spoke, and a stream outranks
   *  a snapshot, since a GET issued while queued can resolve after the finishing event and strand a
   *  pill on Cancel. */
  private seedLiveJob(job: Job | undefined): void {
    if (this.followedJob !== "" || job === undefined || !jobIsLive(job)) {
      return;
    }
    this.setLive(job);
  }

  /** Cancel whatever is running; the queue is single-flight. Read from `live`, not the output panel's
   *  follow target, which outlives its job. */
  private cancelLiveJob(): void {
    const id = this.live?.id;
    if (id === undefined) {
      return;
    }
    void cancelToolJob.dispatch({ id });
  }

  loadToolsList(): void {
    this.loadCatalogMeta();
    void loadTools.dispatch(undefined, {
      onSuccess: (d) => {
        this.data = d;
        this.renderToolsList();
        this.seedLiveJob(d.job);
        // A job already running when the panel opens (boot sync, or a
        // reload mid-install): seed the output panel with its tail.
        if (d.job !== undefined && d.job.id !== this.followedJob) {
          void this.resumeJobOutput(d.job);
        }
      },
      onError: (err) => {
        $.toolsList.replaceChildren(el("div", { className: "list-empty" }, loadFailureText(err)));
      },
    });
  }

  private async resumeJobOutput(job: Job): Promise<void> {
    this.followedJob = job.id;
    const jobs = await getToolsJobs.dispatch(undefined);
    if (jobs === null || this.output === null) {
      return;
    }
    const active = jobs.active;
    if (active?.id !== job.id) {
      return;
    }
    this.output.clear();
    this.output.append(jobHeadline(active));
    for (const line of active.output_tail ?? []) {
      this.output.append(line);
    }
  }

  /** Render the catalog freshness line: entry count, registry refs,
   *  where the live catalog came from, and the last refresh error. */
  private loadCatalogMeta(): void {
    void getCatalogInfo.dispatch(undefined, {
      onSuccess: (info) => {
        f.catalogMeta.replaceChildren(...catalogMetaParts(info));
        f.catalogMeta.classList.remove("hidden");
      },
      onError: () => {
        f.catalogMeta.classList.add("hidden");
      },
    });
  }

  // --- list rendering ---

  private renderToolsList(): void {
    const container = $.toolsList;
    const d = this.data;
    if (d === null) {
      return;
    }

    // THREE groups; the first two split one list on `essential`. A pre-bundled tool IS a catalog entry
    // the app needs, so the engine refuses its removal (ErrEssential) and its row has no bin; Disable is
    // the escape hatch. Labels appear only when the split is real.
    const flat: ListEntry[] = [];
    const essential = d.tools.filter((t) => t.essential === true);
    const chosen = d.tools.filter((t) => t.essential !== true);
    if (essential.length > 0) {
      flat.push({ kind: "label", label: "pre-bundled, kept current by the catalog" });
      for (const t of essential) {
        flat.push({ kind: "tool", tool: t });
      }
      if (chosen.length > 0) {
        flat.push({ kind: "label", label: "added by you" });
      }
    }
    for (const t of chosen) {
      flat.push({ kind: "tool", tool: t });
    }
    const system = d.system.filter((s) => s.installed);
    if (system.length > 0) {
      flat.push({ kind: "label", label: "built into the image" });
      for (const s of system) {
        flat.push({ kind: "system", name: s.name });
      }
    }
    // Debian packages somebody CHOSE (image or shell), minus apt's auto-installed dependencies and
    // required/important priorities. Read-only: no manifest row stands behind one. An ABSENT list means
    // apt is not the package manager or enumeration failed; both render as no group.
    const apt = d.apt_packages ?? [];
    if (apt.length > 0) {
      flat.push({ kind: "label", label: "installed with apt, outside the engine" });
      for (const pkg of apt) {
        flat.push({ kind: "apt", pkg });
      }
    }

    // Drop any non-keyed empty-state placeholder before reconciling.
    for (const child of [...container.children]) {
      if ((child as HTMLElement).getAttribute(KEY_ATTR) === null) {
        child.remove();
      }
    }

    if (d.tools.length === 0) {
      container.replaceChildren();
      container.appendChild(
        el(
          "div",
          { className: "list-empty" },
          "No tools installed yet. Add tool searches a catalog of ~700 runtimes, language servers, and CLIs.",
        ),
      );
      return;
    }

    reconcile(container, flat, {
      // keyenc `join` keeps the three key families in one uncrossable namespace (already injective: tool
      // names are colon-free and unique). A collision would REMOUNT the earlier duplicate every pass.
      key: (e: ListEntry) => {
        switch (e.kind) {
          case "label":
            return join("label", e.label);
          case "system":
            return join("sys", e.name);
          case "apt":
            return join("apt", e.pkg.name, e.pkg.version ?? "");
          case "tool":
            // State fields participate in the key so any transition
            // (installing spinner, error, new version) remounts the
            // row — reconcile's update path only patches text.
            return join(
              "tool",
              e.tool.name,
              // `?? ""`: the typed signature makes the coercion of an absent
              // optional field explicit.
              e.tool.version ?? "",
              e.tool.latest ?? "",
              String(e.tool.installed),
              String(e.tool.installing),
              String(e.tool.pin ?? false),
              String(e.tool.disabled ?? false),
              // The disable/remove pre-flight reads the row's captured `dependents`, which change without any
              // other field, so they are in the key.
              (e.tool.dependents ?? []).join(","),
              // Chips need a remount: a reinstall onto a definition that gained a checksum source moves only this.
              e.tool.checksum ?? "",
              String(e.tool.essential ?? false),
              e.tool.last_error === undefined || e.tool.last_error === "" ? "ok" : "err",
            );
        }
      },
      mount: (e: ListEntry) => {
        switch (e.kind) {
          case "label":
            return el("div", { className: "list-group-label" }, e.label);
          case "system":
            return this.renderSystemRow(e.name);
          case "apt":
            return renderAptRow(e.pkg);
          case "tool":
            return this.renderToolRow(e.tool);
        }
      },
      update: () => {
        // All volatile state is in the key; same-key rows are static.
      },
    });
  }

  private renderSystemRow(name: string): HTMLDivElement {
    return el(
      "div",
      { className: "list-row list-row-system" },
      el("span", { className: "tool-state-dot tool-state-ok", "aria-hidden": "true" }),
      el("span", { className: "list-row-name" }, name),
      el("span", { className: "list-row-meta" }, "system"),
    ) as HTMLDivElement;
  }

  private renderToolRow(t: ToolInfo): HTMLDivElement {
    const name = el("span", { className: "list-row-name", title: t.description ?? "" }, t.name);
    const chips = rowChips(t);
    const error = installError(t);
    // Everything that belongs to the name's column shares its flex slot, so a
    // flex-grown name cannot push it to the trailing edge.
    const named: HTMLElement[] = [name];
    if (chips.length > 0) {
      named.push(el("span", { className: "tool-hit-chips" }, ...chips));
    }
    if (error !== undefined) {
      named.push(el("span", { className: "list-row-meta tool-row-error" }, error));
    }
    const row = el(
      "div",
      { className: "list-row" },
      stateDot(t),
      named.length > 1 ? el("span", { className: "tool-name-wrap" }, ...named) : name,
      el("span", { className: "list-row-meta" }, metaText(t)),
    ) as HTMLDivElement;
    if (t.disabled === true) {
      // Dimming marks a TEMPLATE (not opted into), not "not installed": a missing or failed enabled tool
      // needs attention.
      row.classList.add("list-row-disabled");
    }
    row.appendChild(this.toolActions(t));
    return row;
  }

  /** Action cluster: the enabled/disabled toggle (every row), retry
   *  for failed installs, update when a newer version is known, pin
   *  toggle, delete. */
  private toolActions(t: ToolInfo): HTMLDivElement {
    const actions = el("div", { className: "list-row-actions" }) as HTMLDivElement;

    if (t.installing) {
      actions.append(el("span", { className: "list-row-meta tool-installing" }, "installing…"));
      return actions;
    }

    const disabled = t.disabled === true;
    if (!t.installed && !disabled) {
      const installBtn = el(
        "button",
        { className: "btn-small list-row-enable", "aria-label": `Install ${t.name}` },
        t.last_error !== undefined && t.last_error !== "" ? "Retry" : "Install",
      ) as HTMLButtonElement;
      installBtn.addEventListener("click", () => {
        void this.runInstall(t.name);
      });
      actions.append(installBtn);
    } else if (t.latest !== undefined && t.latest !== "") {
      const updateBtn = el(
        "button",
        {
          className: "btn-small",
          "data-tooltip": `Update to ${t.latest}`,
          "aria-label": `Update ${t.name} to ${t.latest}`,
        },
        "Update",
      ) as HTMLButtonElement;
      updateBtn.addEventListener("click", () => {
        void updateTools.dispatch({ names: [t.name] });
      });
      actions.append(updateBtn);
    }

    // The enable/disable switch: on = installed and reconciled, off =
    // template kept, footprint uninstalled.
    const toggleInput = el("input", {
      type: "checkbox",
      "aria-label": disabled
        ? `Enable ${t.name} (installs it)`
        : `Disable ${t.name} (uninstalls, keeps the entry)`,
    }) as HTMLInputElement;
    toggleInput.checked = !disabled;
    toggleInput.addEventListener("change", () => {
      const nextDisabled = !toggleInput.checked;
      toggleInput.disabled = true;
      void this.toggleDisabled(t, nextDisabled).then((changed) => {
        if (!changed) {
          // Nothing moved server-side, so every keyed field is unchanged and
          // reconcile reuses this exact node — a refetch cannot put the switch
          // back. Restore it to the state the row was rendered from.
          toggleInput.checked = !disabled;
          toggleInput.disabled = false;
        }
      });
    });
    const toggle = el(
      "label",
      {
        className: "toggle toggle-inline tool-toggle",
        "data-tooltip": disabled
          ? "Disabled template. Switch on to install"
          : "Enabled. Switch off to uninstall and keep the entry",
      },
      toggleInput,
      el("span", { className: "toggle-slider" }),
    );

    const pinned = t.pin ?? false;
    const pinBtn = el(
      "button",
      {
        className: "list-row-btn list-row-pin",
        "aria-label": pinned ? `Unpin ${t.name}` : `Pin ${t.name} version`,
        "data-tooltip": pinned
          ? "Pinned, so it will not auto-update. Click to resume auto-updates."
          : "Auto-updating. Click to pin this version.",
      },
      iconEl(pinned ? ICON_PIN_FILLED : ICON_PIN),
    );
    if (pinned) {
      pinBtn.classList.add("list-row-pin-active");
    }
    pinBtn.addEventListener("click", () => {
      void this.togglePin(t.name, !pinned);
    });

    // No bin on a pre-bundled row: the engine refuses the removal (ErrEssential).
    const trailing: HTMLElement[] = [];
    if (t.essential !== true) {
      const delBtn = el(
        "button",
        { className: "list-row-btn", "data-tooltip": "Remove", "aria-label": `Remove ${t.name}` },
        iconEl(ICON_TRASH),
      );
      delBtn.addEventListener("click", () => {
        void this.runDelete(t);
      });
      trailing.push(delBtn);
    } else {
      // A GHOST bin reserving the real one's box, so the switch column does not step. `visibility:
      // hidden`, `aria-hidden`, a non-button: space, no tab stop.
      trailing.push(
        el(
          "span",
          { className: "list-row-btn list-row-btn-ghost", "aria-hidden": "true" },
          iconEl(ICON_TRASH),
        ),
      );
    }

    // THE TRAILING ORDER IS AN ALIGNMENT RULE: `.list-row-actions` is right-aligned, so only controls
    // present on EVERY row may sit right of the switch. The conditional pin goes LEFT of it; only the
    // unconditional `trailing` slot (bin or ghost) sits right, so the switch column is fixed.
    if (disabled) {
      actions.append(toggle, ...trailing);
    } else {
      actions.append(pinBtn, toggle, ...trailing);
    }
    return actions;
  }

  private async runInstall(name: string): Promise<void> {
    await installTool.dispatch({ name });
    this.loadToolsList();
  }

  private async togglePin(name: string, pin: boolean): Promise<void> {
    await patchTool.dispatch({ name, pin });
    this.loadToolsList();
  }

  /** The cascade half of a destructive mutation, shared by switch and bin: ask once about the set,
   *  then force one request. The engine re-derives the set under the manifest lock and answers 409 on
   *  a stale row, which asks again with its own set. Answers whether anything reached the server. */
  private async cascade(
    dependents: readonly string[],
    ask: (deps: readonly string[]) => Promise<boolean>,
    send: (force: boolean) => Promise<CascadeReply | null>,
  ): Promise<boolean> {
    if (dependents.length > 0) {
      if (!(await ask(dependents))) {
        return false;
      }
      await send(true);
      return true;
    }
    const d = await send(false);
    const derived = d?.code === "has_dependents" ? d.dependents : undefined;
    if (derived === undefined) {
      return true;
    }
    if (!(await ask(derived))) {
      return false;
    }
    await send(true);
    return true;
  }

  /** Returns whether anything reached the server, so a declined confirm can put the switch back. */
  private async toggleDisabled(t: ToolInfo, disabled: boolean): Promise<boolean> {
    // Enabling never cascades: the engine derives the dependent set for a
    // DISABLE only, so there is nothing to ask about and nothing to force.
    if (!disabled) {
      await patchTool.dispatch({ name: t.name, disabled });
      this.loadToolsList();
      return true;
    }
    const sent = await this.cascade(
      t.dependents ?? [],
      (deps) =>
        confirmDialog(
          `${t.name} is required by: ${deps.join(", ")}. Disable it anyway?`,
          "Disable",
          "destructive",
        ),
      (force) =>
        patchTool.dispatch({
          name: t.name,
          disabled: true,
          ...(force ? { force: true } : {}),
        }),
    );
    if (sent) {
      this.loadToolsList();
    }
    return sent;
  }

  /** Remove, asking once. A row with no known dependents is confirmed plainly
   *  first; anything else is the shared cascade. */
  private async runDelete(t: ToolInfo): Promise<void> {
    const known = t.dependents ?? [];
    if (known.length === 0) {
      const ok = await confirmDialog(`Remove ${t.name}?`, "Remove", "destructive");
      if (!ok) {
        return;
      }
    }
    const sent = await this.cascade(
      known,
      (deps) =>
        confirmDialog(
          `Remove ${t.name}? It is required by: ${deps.join(", ")}. Removing it removes them too.`,
          "Remove all",
          "destructive",
        ),
      async (force) => {
        const d = await deleteTool.dispatch({
          name: t.name,
          ...(force ? { force: true } : {}),
        });
        if (d?.code === "essential") {
          // Reaching this means the row predates the essential flag; the refetch replaces it, so the refusal
          // is reported off the row.
          toastError(
            `${t.name} is essential to marotte and cannot be removed. Switch it off to uninstall it and keep the entry.`,
          );
        }
        return d;
      },
    );
    if (sent) {
      this.loadToolsList();
    }
  }

  // --- add modal (search-first) ---

  private openAddModal(): void {
    f.search.value = "";
    // Reset with the query: an order left over from a previous search would be
    // applied to a set the reader has not seen yet.
    f.sort.value = "relevance";
    openModal($.toolModal);
    void this.renderSearch("");
    f.search.focus();
  }

  /** Render the search: ONE relevance-ordered list of catalog entries and Debian packages, each row
   *  carrying its own source and version chip so a name at two versions stays a visible choice.
   *  Uninstallable entries are never requested (`unavailable=1` is not sent); the shell note answers
   *  anything absent. */
  private async renderSearch(query: string): Promise<void> {
    const q = query.trim();
    const d = await searchTools.dispatch({ q });
    this.lastSearch = d;
    this.lastQuery = q;
    this.paintSearch();
  }

  /** Paint the cached result set under the current order, so reordering costs no round trip. */
  private paintSearch(): void {
    const box = f.results;
    const d = this.lastSearch;
    if (d === null) {
      box.replaceChildren();
      f.resultCount.textContent = "";
      box.appendChild(el("div", { className: "list-empty" }, emptyNote({ kind: "failed" }, NOUNS)));
      return;
    }
    const hits = this.orderHits(d.results);
    f.resultCount.textContent =
      hits.length === 0 ? "" : resultCount(hits.length, d.truncated, d.matched);

    this.paintShellNote(d);

    if (hits.length === 0) {
      box.replaceChildren();
      box.appendChild(el("div", { className: "list-empty" }, this.emptyAnswer(d)));
      return;
    }
    // Drop a non-keyed empty-state or failure placeholder before reconciling —
    // the same prelude the installed list runs, for the same reason.
    for (const child of [...box.children]) {
      if ((child as HTMLElement).getAttribute(KEY_ATTR) === null) {
        child.remove();
      }
    }
    // Keyed, so a reorder moves rows rather than rebuilding them and losing an
    // in-flight Add button's feedback cycle. Source and name because a Debian package
    // and a catalog entry legitimately share a name (ripgrep is in both here).
    reconcile(box, hits, {
      key: (hit: SearchHit) => join(hit.source, hit.name),
      mount: (hit: SearchHit) => this.renderSearchHit(hit),
      // A kept row still repaints when the hit itself moved: a re-search can return
      // the same tool at a new version, or newly unavailable.
      update: (row: HTMLElement, hit: SearchHit) => {
        if (!sigChanged(row, [wireSignature(hit)])) {
          return;
        }
        row.replaceChildren(...Array.from(this.renderSearchHit(hit).childNodes));
      },
    });
  }

  /** An empty query is a featured browse with its own sentence. `apt_available` false means a corpus
   *  was asked for and not read. */
  private emptyAnswer(d: ToolSearchResponse): string {
    if (this.lastQuery === "") {
      return "Everything featured is already installed. Search by name.";
    }
    return emptyNote(classify({ matched: 0, shown: 0, truncated: !d.apt_available }), NOUNS);
  }

  /** `relevance` returns the server's own order, which is why this carries no scoring of its own —
   *  see [SortOrder]. */
  private orderHits(hits: readonly SearchHit[]): SearchHit[] {
    const raw = f.sort.value;
    const order: SortOrder = isSortOrder(raw) ? raw : "relevance";
    if (order === "relevance") {
      return [...hits];
    }
    const dir = order === "name-asc" ? 1 : -1;
    return [...hits].sort((a, b) => dir * a.name.localeCompare(b.name));
  }

  /** The footer's one variable half, toggled both ways: with the index unread no Debian hits come
   *  back, so it says WHY the corpus went unread, which only the reply knows. */
  private paintShellNote(d: ToolSearchResponse): void {
    const note = aptNote(d);
    f.shellNoteApt.textContent = note === "" ? "" : ` ${note}`;
    f.shellNoteApt.classList.toggle("hidden", note === "");
  }

  private renderSearchHit(hit: SearchHit): HTMLElement {
    const addBtn = el(
      "button",
      { className: "btn-small list-row-enable", "aria-label": `Install ${hit.name}` },
      "Install",
    ) as HTMLButtonElement;
    addBtn.addEventListener("click", () => {
      addBtn.disabled = true;
      addBtn.textContent = "Queued…";
      // An apt hit is not a catalog entry, so the engine has no source to
      // hydrate from and the request has to carry it. A catalog hit omits it,
      // which is what lets the engine resolve the source it published.
      void this.submitCatalogAdd(
        hit.apt === true ? { name: hit.name, source: hit.source } : { name: hit.name },
      );
    });
    const source = sourceChip(hit.source);
    // The apt caveat rides the chip: an apt package is not version-managed and returns at whatever apt
    // offers next boot.
    if (hit.apt === true) {
      source.setAttribute(
        "data-tooltip",
        "Installed with apt, in this container. The engine does not manage its version.",
      );
    }
    const chips: HTMLElement[] = [source];
    // The version rides its own chip because it is the reason both corpora are
    // searched: the same name can be one release in the catalog and another in
    // the distro, and a reader choosing between them needs both numbers.
    if (hit.version !== undefined && hit.version !== "") {
      chips.push(el("span", { className: "tool-source-chip" }, hit.version));
    }
    if (hit.lsp === true) {
      chips.push(el("span", { className: "tool-source-chip" }, "LSP"));
    }
    return el(
      "div",
      { className: "list-row tool-hit" },
      el(
        "div",
        { className: "tool-hit-text" },
        // A DIRECT child of the column, so it is blockified and its declared
        // ellipsis applies: `overflow` does not apply to a non-replaced inline box.
        el("span", { className: "list-row-name" }, hit.name),
        el("div", { className: "tool-hit-chips" }, ...chips),
        el("span", { className: "tool-hit-desc" }, hit.description ?? ""),
      ),
      addBtn,
    );
  }

  private async submitCatalogAdd(req: CreateToolRequest): Promise<void> {
    const d = await createTool.dispatch(req, { onError: reportAddFailure });
    if (d !== null) {
      closeModal($.toolModal);
      this.loadToolsList();
    } else {
      void this.renderSearch(f.search.value);
    }
  }
}

// --- pure row helpers ---

/** Build the catalog meta line's content: "702 tools · aqua v4.541.0 +
 *  mise v2026.7.11 · compiled 2 h ago · checked 1 min ago · auto-refresh
 *  on" with an amber error suffix when the last refresh failed (the
 *  current catalog still stands — keep-last-good). */
function catalogMetaParts(info: CatalogInfo): (string | HTMLElement)[] {
  const bits: string[] = [`${String(info.entries)} tools`];
  const refs = Object.entries(info.refs ?? {})
    .map(([name, ref]) => `${name} ${ref}`)
    .sort()
    .join(" + ");
  if (refs !== "") {
    bits.push(refs);
  }
  // The catalog's own age (its compile stamp) is the freshness that
  // matters; the fetch time only says when we last checked. A stale
  // artifact fetched a minute ago is still stale.
  const generatedMs = info.generated !== undefined ? Date.parse(info.generated) : Number.NaN;
  if (!Number.isNaN(generatedMs)) {
    bits.push(`compiled ${relativeTime(generatedMs)}`);
  }
  if (info.fetched_at !== undefined && info.fetched_at > 0) {
    bits.push(`checked ${relativeTime(info.fetched_at)}`);
  } else if (info.source === "baked") {
    bits.push("from the image, not refreshed yet");
  } else if (info.source === "cached") {
    bits.push("from the last refresh in a previous run");
  }
  // Surface the engine's schedule state so "off" deployments can tell
  // why the catalog ages (only the Refresh button updates it). Skipped
  // when no refresh source is configured — neither mode could fetch.
  if (info.url !== undefined && info.url !== "") {
    bits.push(`auto-refresh ${info.scheduled ? "on" : "off"}`);
  }
  const parts: (string | HTMLElement)[] = [`Catalog: ${bits.join(" · ")}`];
  if (info.last_error !== undefined && info.last_error !== "") {
    parts.push(
      el(
        "span",
        { className: "catalog-meta-error" },
        ` · last refresh failed, kept the current catalog`,
      ),
    );
  }
  return parts;
}

function stateDot(t: ToolInfo): HTMLElement {
  let cls = "tool-state-missing";
  let label = "not installed";
  if (t.installing) {
    cls = "tool-state-busy";
    label = "installing";
  } else if (t.disabled === true) {
    cls = "tool-state-off";
    label = "disabled template, switch on to install";
  } else if (t.last_error !== undefined && t.last_error !== "") {
    cls = "tool-state-error";
    label = `failed: ${t.last_error}`;
  } else if (t.installed) {
    cls = "tool-state-ok";
    label = "installed";
  }
  return el("span", {
    className: `tool-state-dot ${cls}`,
    "data-tooltip": label,
    role: "img",
    "aria-label": label,
  });
}

/** One Debian package the engine does not manage, shaped like the system row: dot, name, `apt` chip,
 *  installed version. No controls: the engine never installed it and cannot prove nothing needs it. */
function renderAptRow(pkg: AptPackage): HTMLDivElement {
  const meta: HTMLElement[] = [el("span", { className: "tool-source-chip" }, "apt")];
  if (pkg.version !== undefined && pkg.version !== "") {
    meta.push(el("span", { className: "list-row-meta" }, pkg.version));
  }
  return el(
    "div",
    { className: "list-row list-row-system" },
    el("span", { className: "tool-state-dot tool-state-ok", "aria-hidden": "true" }),
    el("span", { className: "list-row-name" }, pkg.name),
    el("span", { className: "list-row-actions" }, ...meta),
  ) as HTMLDivElement;
}

function metaText(t: ToolInfo): string {
  if (t.disabled === true) {
    return "template";
  }
  const version =
    t.installed && t.installed_version !== undefined && t.installed_version !== ""
      ? (t.installed_version ?? "")
      : (t.version ?? "");
  if (t.latest !== undefined && t.latest !== "") {
    return `${version} → ${t.latest}`;
  }
  return version;
}

/** The engine's failure text for an enabled tool whose install did not land. */
function installError(t: ToolInfo): string | undefined {
  if (t.disabled === true || t.installed) {
    return undefined;
  }
  return t.last_error !== undefined && t.last_error !== "" ? t.last_error : undefined;
}

/** Short source chip text: "aqua:cli/cli" -> "github", "npm:x" -> "npm". */
function sourceChip(source: string): HTMLElement {
  const kind = source.split(":", 1)[0] ?? source;
  const label = kind === "aqua" ? "binary" : kind;
  return el("span", { className: "tool-source-chip" }, label);
}

/** The chips a TABLE row carries, at most two: the LSP badge and one mutually exclusive honesty chip.
 *  An apt package's integrity is the distro's; a self-managed entry is neither verified nor updated
 *  (`updateOne` returns silently for a manual source). Otherwise the chip reads ONE fact,
 *  `ToolInfo.Checksum`, never the source kind; package-manager sources earn none. */
function rowChips(t: ToolInfo): HTMLElement[] {
  const chips: HTMLElement[] = [];
  if (t.lsp === true) {
    chips.push(el("span", { className: "tool-source-chip" }, "LSP"));
  }
  const kind = (t.source ?? "").split(":", 1)[0] ?? "";
  if (kind === "apt") {
    chips.push(
      el(
        "span",
        {
          className: "tool-source-chip",
          "data-tooltip":
            "A Debian package. Reinstalled at every boot, at whatever version apt offers then.",
        },
        "apt",
      ),
    );
  } else if (kind === "manual") {
    chips.push(
      el(
        "span",
        {
          className: "tool-source-chip",
          "data-tooltip": "Installed by hand. The engine does not update it.",
        },
        "self-managed",
      ),
    );
  } else if (t.checksum === "unverified") {
    chips.push(
      el(
        "span",
        {
          className: "tool-source-chip",
          "data-tooltip":
            "The definition declares no checksum, so the download was not verified against one.",
        },
        "no checksum",
      ),
    );
  }
  return chips;
}

function jobHeadline(job: Job): string {
  const names = (job.names ?? []).join(", ");
  const what = names === "" ? job.kind : `${job.kind} ${names}`;
  switch (job.state) {
    case "queued":
      return `queued: ${what}`;
    case "running":
      return `running: ${what}`;
    case "done":
      return `✓ ${what} finished`;
    case "cancelled":
      return `${what} cancelled`;
    default: {
      const limit = jobRateLimit(job);
      const why = limit !== null ? rateLimitText(limit) : (job.error ?? "");
      return `✗ ${what} failed${why !== "" ? `: ${why}` : ""}`;
    }
  }
}

/** Tell the reader GitHub's rate limit stopped a tools request. When an account
 *  is the fix, the notice opens Git -> Sources in-app. */
function reportRateLimit(limit: GitHubRateLimit): void {
  const text = rateLimitText(limit);
  if (!accountRaisesLimit(limit)) {
    toastError(text);
    return;
  }
  errorWithAction(text, {
    label: "Connect GitHub",
    onClick: () => {
      void openGitView("sources");
    },
  });
}

/** An Add that failed before any job started. */
function reportAddFailure(err: ActionErrorLike): void {
  const limit = bodyRateLimit(err.code, err.cause);
  if (limit !== null) {
    reportRateLimit(limit);
    return;
  }
  toastError(`Could not add tool: ${err.message}`);
}

/** Install a tool by name (creating it from the catalog if needed) and resolve at its terminal
 *  state, streaming lines into onLine. Listeners register BEFORE the dispatch, buffering until the
 *  job id is known; a post-dispatch jobs poll covers a terminal event lost to an SSE reconnect. */
export async function installToolAndWait(
  name: string,
  onLine: (line: string) => void,
): Promise<{ ok: boolean; error?: string }> {
  const jobRef: { id: string | undefined } = { id: undefined };
  let settled = false;
  let settle: (r: { ok: boolean; error?: string }) => void = () => {
    /* replaced below */
  };
  const result = new Promise<{ ok: boolean; error?: string }>((resolve) => {
    settle = (r) => {
      if (!settled) {
        settled = true;
        unOut();
        unChanged();
        resolve(r);
      }
    };
  });
  const buffered: { id: string; lines: string[] }[] = [];
  const terminal = new Map<string, { state: string; error?: string }>();
  const settleFromState = (state: string, error?: string): void => {
    if (state === "done") {
      settle({ ok: true });
    } else if (state === "failed" || state === "cancelled") {
      settle({ ok: false, ...(error !== undefined && error !== "" ? { error } : {}) });
    }
  };
  const unOut = onSSE("tool_job_output", (_c, p) => {
    if (jobRef.id === undefined) {
      buffered.push({ id: p.job_id, lines: [...p.lines] });
      return;
    }
    if (p.job_id !== jobRef.id) {
      return;
    }
    for (const line of p.lines) {
      onLine(line);
    }
  });
  const unChanged = onSSE("tool_job_changed", (_c, p) => {
    const job = p.job;
    if (job === undefined) {
      return;
    }
    if (jobRef.id === undefined) {
      terminal.set(job.id, {
        state: job.state,
        ...(job.error !== undefined ? { error: job.error } : {}),
      });
      return;
    }
    if (job.id !== jobRef.id) {
      return;
    }
    settleFromState(job.state, job.error);
  });

  const d = await ensureTool.dispatch({ name });
  const id = d?.job?.id;
  if (id === undefined) {
    settle({ ok: false, error: "install request failed" });
    return result;
  }
  jobRef.id = id;
  // Drain anything that arrived before the id was known.
  for (const b of buffered) {
    if (b.id === id) {
      for (const line of b.lines) {
        onLine(line);
      }
    }
  }
  const t = terminal.get(id);
  if (t !== undefined) {
    settleFromState(t.state, t.error);
  }
  return result;
}

// Singleton instance — internal to the module.
const manager = new ToolsManager();
registerCleanup(() => {
  manager.cancelLoad();
});

// Public delegate functions preserving the existing module API.
export function initTools(): void {
  manager.init();
}
export function loadToolsList(): void {
  manager.loadToolsList();
}
