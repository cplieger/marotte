import { effect, el } from "@cplieger/reactive";
import { rovingFocus } from "@cplieger/ui-primitives/roving-focus";
import { pollAction } from "./actions/index.js";
import { grantPreview, previewStamp } from "./actions/preview.js";
import { $ } from "./dom.js";
import { iconEl } from "./icon-el.js";
import {
  ICON_REFRESH,
  ICON_VIEWPORT_DESKTOP,
  ICON_VIEWPORT_FILL,
  ICON_VIEWPORT_PHONE,
  ICON_VIEWPORT_TABLET,
} from "./icons.js";
import { LS_WEB_VIEWPORT_KEY } from "./ls-keys.js";
import { readPerChat, writePerChat } from "./per-chat-store.js";
import { getActiveTabKind } from "./tabs.js";
import {
  PHONE_SHAPED_QUERY,
  PRESET_WIDTH,
  frameGeometry,
  parseStoredPick,
  resolvePick,
  scaleReadout,
  type ResolvedViewport,
  type ViewportMode,
  type ViewportPick,
} from "./web-viewport.js";
import { relToWorkspace } from "./workspace.js";
import type { PreviewHint, PreviewStamp } from "./wire/types.gen.js";

/** Never `allow-same-origin`: the frame's opaque origin is what keeps a
 *  previewed page out of marotte's cookies, storage and API. */
const SANDBOX = "allow-scripts allow-forms allow-modals";
const IDLE_UNLOAD_MS = 5 * 60_000;
const MAX_MOUNTED = 4;
const LOADING_DELAY_MS = 150;
const POLL_MS = 1000;

type TabStatus = "idle" | "loading" | "loaded" | "error";

interface WebTab {
  readonly path: string;
  readonly wrapper: HTMLDivElement;
  frame: HTMLIFrameElement | null;
  status: TabStatus;
  hint: PreviewHint | undefined;
  epoch: string;
  /** Bumped by every load and unload, so a grant that lands late is dropped. */
  loadSeq: number;
  /** The stamp the grant for the document on screen was minted at, and a
   *  changed one awaiting a second identical reading before it reloads. */
  baseline: string;
  pending: string | undefined;
  idleTimer: ReturnType<typeof setTimeout> | undefined;
  shownAt: number;
}

interface RadioSpec {
  readonly mode: ViewportMode;
  readonly label: string;
  readonly icon: string;
}

const tabs = new Map<string, WebTab>();
let shownPath: string | null = null;
let webActive = false;
let initialized = false;
let stopPoll: (() => void) | null = null;
let layoutFrame = 0;
let radiosSig: string | null = null;
let showCounter = 0;

// Read on every use rather than cached: the sign-out reset deletes the key, and
// a cached copy would keep the old picks active and write them back.
function storedPicks(): Record<string, ViewportPick> {
  return readPerChat(LS_WEB_VIEWPORT_KEY, parseStoredPick);
}

function phoneShaped(): boolean {
  return window.matchMedia(PHONE_SHAPED_QUERY).matches;
}

function resolved(tab: WebTab): ResolvedViewport {
  return resolvePick(storedPicks()[tab.path], tab.hint, phoneShaped());
}

function shownTab(): WebTab | undefined {
  return shownPath === null ? undefined : tabs.get(shownPath);
}

function basename(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

/** Shows the preview for `path`, mounting it when it has no document. Runs on
 *  every activation, so it never reloads a document already on screen. */
export function showWebTab(path: string): void {
  ensureInit();
  const tab = tabs.get(path) ?? createTab(path);
  const prev = shownTab();
  if (prev !== undefined && prev !== tab) {
    hideTab(prev);
  }
  shownPath = path;
  webActive = true;
  clearIdle(tab);
  tab.wrapper.hidden = false;
  tab.shownAt = ++showCounter;
  paintPath(tab);
  paintRadios(tab);
  if (tab.status === "idle") {
    void load(tab);
  }
  startPoll(tab);
  scheduleLayout();
}

/** Drops the tab's frame and every piece of its state. */
export function releaseWebTab(path: string): void {
  const tab = tabs.get(path);
  if (tab === undefined) {
    return;
  }
  clearIdle(tab);
  tab.loadSeq++;
  tab.wrapper.remove();
  tabs.delete(path);
  if (shownPath === path) {
    stopPolling();
    shownPath = null;
  }
}

function createTab(path: string): WebTab {
  const wrapper = document.createElement("div");
  wrapper.className = "web-frame";
  wrapper.hidden = true;
  $.webStage.append(wrapper);
  const tab: WebTab = {
    path,
    wrapper,
    frame: null,
    status: "idle",
    hint: undefined,
    epoch: "",
    loadSeq: 0,
    baseline: "",
    pending: undefined,
    idleTimer: undefined,
    shownAt: 0,
  };
  tabs.set(path, tab);
  return tab;
}

function hideTab(tab: WebTab): void {
  tab.wrapper.hidden = true;
  armIdle(tab);
}

function armIdle(tab: WebTab): void {
  if (tab.idleTimer !== undefined || tab.frame === null) {
    return;
  }
  tab.idleTimer = setTimeout(() => {
    tab.idleTimer = undefined;
    unload(tab);
  }, IDLE_UNLOAD_MS);
}

function clearIdle(tab: WebTab): void {
  if (tab.idleTimer !== undefined) {
    clearTimeout(tab.idleTimer);
    tab.idleTimer = undefined;
  }
}

function unload(tab: WebTab): void {
  tab.loadSeq++;
  tab.frame = null;
  tab.status = "idle";
  tab.baseline = "";
  tab.pending = undefined;
  tab.wrapper.replaceChildren();
}

/** Leaving the web view entirely: the poll stops and a mounted shown tab
 *  starts its idle clock. A tab whose grant is still in flight has no frame
 *  yet, so load arms its clock when the grant lands. Web-to-web switches go
 *  through showWebTab instead. */
function onLeave(): void {
  if (!webActive) {
    return;
  }
  webActive = false;
  stopPolling();
  const tab = shownTab();
  if (tab !== undefined) {
    armIdle(tab);
  }
}

function ensureInit(): void {
  if (initialized) {
    return;
  }
  initialized = true;
  window.matchMedia(PHONE_SHAPED_QUERY).addEventListener("change", () => {
    scheduleLayout();
  });
  $.webReloadBtn.replaceChildren(iconEl(ICON_REFRESH));
  $.webReloadBtn.addEventListener("click", () => {
    const tab = shownTab();
    if (tab !== undefined) {
      void load(tab);
    }
  });
  $.webScaleBtn.addEventListener("click", () => {
    const tab = shownTab();
    if (tab === undefined) {
      return;
    }
    const r = resolved(tab);
    storePick(tab, { mode: r.mode, fit: !r.fit });
  });
  const viewport = $.webViewport;
  rovingFocus(viewport, '[role="radio"]', { orientation: "horizontal" });
  viewport.addEventListener("click", (e) => {
    choose(e.target);
  });
  // Selection follows focus, so the arrow keys pick; a mode already in force
  // is not re-stored, which keeps a Tab into the group from writing a pick.
  viewport.addEventListener("focusin", (e) => {
    choose(e.target);
  });
  new ResizeObserver(() => {
    scheduleLayout();
  }).observe($.webStage);
  effect(() => {
    if (getActiveTabKind() !== "web") {
      onLeave();
    }
  });
}

function choose(target: EventTarget | null): void {
  const tab = shownTab();
  if (tab === undefined || !(target instanceof Element)) {
    return;
  }
  const radio = target.closest<HTMLElement>('[role="radio"]');
  const mode = radio?.dataset["mode"] as ViewportMode | undefined;
  if (mode === undefined) {
    return;
  }
  const r = resolved(tab);
  if (mode !== r.mode) {
    storePick(tab, { mode, fit: r.fit });
  }
}

function storePick(tab: WebTab, pick: ViewportPick): void {
  writePerChat(LS_WEB_VIEWPORT_KEY, storedPicks(), tab.path, pick);
  layout();
}

/** The ONE load operation, whatever triggered it: a fresh grant, so the hint
 *  is the document's current one; then the hint, the radios, the frame and
 *  the layout, in that order. No grant is ever reused. */
async function load(tab: WebTab): Promise<void> {
  const seq = ++tab.loadSeq;
  tab.status = "loading";
  const slow = setTimeout(() => {
    if (tab.loadSeq === seq && tab.frame === null) {
      tab.wrapper.replaceChildren(el("div", { className: "web-frame-status" }, "Loading preview…"));
    }
  }, LOADING_DELAY_MS);
  const outcome = await grantPreview.dispatch(tab.path);
  clearTimeout(slow);
  if (tab.loadSeq !== seq || tabs.get(tab.path) !== tab) {
    return;
  }
  if (outcome === null) {
    showError(tab, "The preview could not be loaded.");
    return;
  }
  if (outcome.kind === "refused") {
    showError(tab, outcome.message);
    return;
  }
  const { grant } = outcome;
  // shownPath survives leaving the web view, so it alone cannot mean on screen;
  // a grant landing while the view is hidden takes the hidden branch and its
  // idle clock, because onLeave found no frame to arm.
  const shown = webActive && shownPath === tab.path;
  tab.hint = grant.hint;
  tab.epoch = grant.epoch;
  if (shown) {
    paintRadios(tab);
  }
  mountFrame(tab, grant.url);
  tab.status = "loaded";
  tab.baseline = grant.stamp;
  tab.pending = undefined;
  if (shown) {
    layout();
  } else {
    armIdle(tab);
  }
}

function mountFrame(tab: WebTab, url: string): void {
  if (tab.frame === null) {
    enforceMountCap(tab);
  }
  const frame = document.createElement("iframe");
  frame.title = `Preview of ${basename(tab.path)}`;
  frame.setAttribute("sandbox", SANDBOX);
  frame.src = url;
  // Replacing the element rather than assigning `src` on a connected frame
  // keeps a reload out of the session history.
  if (tab.frame === null) {
    tab.wrapper.replaceChildren(frame);
  } else {
    tab.frame.replaceWith(frame);
  }
  tab.frame = frame;
}

function enforceMountCap(incoming: WebTab): void {
  const mounted = [...tabs.values()].filter((t) => t.frame !== null && t !== incoming);
  if (mounted.length < MAX_MOUNTED) {
    return;
  }
  const hidden = mounted.filter((t) => t.path !== shownPath).sort((a, b) => a.shownAt - b.shownAt);
  const oldest = hidden[0];
  if (oldest !== undefined) {
    clearIdle(oldest);
    unload(oldest);
  }
}

function showError(tab: WebTab, message: string): void {
  tab.frame = null;
  tab.status = "error";
  const retry = el("button", { type: "button", className: "btn btn-small" }, "Retry");
  retry.addEventListener("click", () => {
    void load(tab);
  });
  tab.wrapper.replaceChildren(
    el("div", { className: "web-frame-status", role: "alert" }, el("p", {}, message), retry),
  );
}

function startPoll(tab: WebTab): void {
  stopPolling();
  stopPoll = pollAction(previewStamp, tab.path, {
    interval: POLL_MS,
    pauseWhenHidden: true,
    refreshOnFocus: true,
    onSuccess: (s) => {
      onStamp(tab, s);
    },
  });
}

function stopPolling(): void {
  stopPoll?.();
  stopPoll = null;
}

/** A changed stamp reloads once it reads the same twice (changed, then stable
 *  for one interval); a new epoch means the grant's token died with the
 *  server, so it reloads at once. */
function onStamp(tab: WebTab, s: PreviewStamp): void {
  if (!webActive || shownPath !== tab.path || tab.status !== "loaded") {
    return;
  }
  if (s.epoch !== tab.epoch) {
    void load(tab);
    return;
  }
  if (s.stamp === tab.baseline) {
    tab.pending = undefined;
    return;
  }
  if (s.stamp === tab.pending) {
    void load(tab);
    return;
  }
  tab.pending = s.stamp;
}

function paintPath(tab: WebTab): void {
  const path = $.webPath;
  path.replaceChildren(el("bdi", {}, relToWorkspace(tab.path)));
  path.dataset["tooltip"] = tab.path;
}

function radioSpecs(hint: PreviewHint | undefined): RadioSpec[] {
  const specs: RadioSpec[] = [
    { mode: "fill", label: "Fill", icon: ICON_VIEWPORT_FILL },
    { mode: "phone", label: `Phone, ${String(PRESET_WIDTH.phone)} px`, icon: ICON_VIEWPORT_PHONE },
    {
      mode: "tablet",
      label: `Tablet, ${String(PRESET_WIDTH.tablet)} px`,
      icon: ICON_VIEWPORT_TABLET,
    },
    {
      mode: "desktop",
      label: `Desktop, ${String(PRESET_WIDTH.desktop)} px`,
      icon: ICON_VIEWPORT_DESKTOP,
    },
  ];
  if (hint?.width !== undefined && hint.width > 0) {
    specs.push({
      mode: "page",
      label: `Page width (${String(hint.width)} px)`,
      icon: ICON_VIEWPORT_DESKTOP,
    });
  }
  return specs;
}

function paintRadios(tab: WebTab): void {
  const specs = radioSpecs(tab.hint);
  const sig = specs.map((s) => s.label).join("|");
  if (sig === radiosSig) {
    syncRadios(tab);
    return;
  }
  radiosSig = sig;
  const viewport = $.webViewport;
  const hadFocus = viewport.contains(document.activeElement);
  viewport.replaceChildren(
    ...specs.map((s) => {
      const icon = iconEl(s.icon);
      icon.classList.add("seg-icon");
      return el(
        "button",
        {
          type: "button",
          className: "seg",
          role: "radio",
          "aria-label": s.label,
          "data-tooltip": s.label,
          "data-mode": s.mode,
        },
        icon,
      );
    }),
  );
  syncRadios(tab);
  if (hadFocus) {
    viewport.querySelector<HTMLElement>('[aria-checked="true"]')?.focus();
  }
}

function syncRadios(tab: WebTab): void {
  const mode = resolved(tab).mode;
  for (const radio of $.webViewport.querySelectorAll<HTMLElement>('[role="radio"]')) {
    const on = radio.dataset["mode"] === mode;
    radio.setAttribute("aria-checked", String(on));
    radio.classList.toggle("active", on);
    radio.tabIndex = on ? 0 : -1;
  }
}

function scheduleLayout(): void {
  if (layoutFrame !== 0) {
    return;
  }
  // One frame behind the observer, because a layout write can change the
  // observed box through the stage's scrollbars.
  layoutFrame = requestAnimationFrame(() => {
    layoutFrame = 0;
    layout();
  });
}

function layout(): void {
  const tab = shownTab();
  if (tab === undefined) {
    return;
  }
  const stage = $.webStage;
  const stageW = stage.clientWidth;
  const stageH = stage.clientHeight;
  syncRadios(tab);
  if (stageW === 0 || stageH === 0) {
    return;
  }
  const r = resolved(tab);
  const geo = frameGeometry(stageW, stageH, r.width, r.fit);
  const style = tab.wrapper.style;
  style.setProperty("--web-frame-w", `${String(geo.frameW)}px`);
  style.setProperty("--web-frame-h", `${String(geo.frameH)}px`);
  style.setProperty("--web-scale", String(geo.scale));
  tab.wrapper.dataset["mode"] = r.mode;
  stage.toggleAttribute("data-overflow", geo.overflows);
  const btn = $.webScaleBtn;
  const tooWide = r.width !== null && r.width > stageW;
  btn.disabled = !tooWide;
  btn.setAttribute("aria-pressed", String(r.fit));
  const readout = btn.querySelector(".web-scale-readout");
  if (readout !== null) {
    readout.textContent = scaleReadout(tooWide && r.fit ? geo.scale : 1);
  }
}

/** @internal Test seam: forget every tab and the poll. The listeners stay,
 *  because the toolbar elements they sit on persist. */
export function _resetForTest(): void {
  for (const path of [...tabs.keys()]) {
    releaseWebTab(path);
  }
  stopPolling();
  shownPath = null;
  webActive = false;
  radiosSig = null;
  if (layoutFrame !== 0) {
    cancelAnimationFrame(layoutFrame);
    layoutFrame = 0;
  }
}
