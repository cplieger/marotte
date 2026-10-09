// Shell panel: one global server-side PTY, rendered by @cplieger/web-terminal-ui's
// createTerminal over the existing WebSocket at /api/shell/ws. The UI package and
// engine own input, rendering, the wire protocol and reconnect; marotte keeps only
// the panel chrome and its lifecycle (device-view persistence, bounded reattach).

import { createTerminal, localScrollbackStorage } from "@cplieger/web-terminal-ui";
import { presetSingle } from "@cplieger/web-terminal-ui/presets/single";
import { mobileToolbar } from "@cplieger/web-terminal-ui/features/mobile-toolbar";
import type { MobileToolbarApi } from "@cplieger/web-terminal-ui/features/mobile-toolbar";
import type { TerminalFeature, TerminalHandle } from "@cplieger/web-terminal-ui";
import { $ } from "./dom.js";
import { getScrollEl } from "./messages.js";
import { shiftScroll } from "./scroll.js";
import { setShellRunCallback } from "./code-blocks.js";
import {
  shellHeight,
  shellOpen as storedShellOpen,
  setShellHeight,
  setShellOpen as recordShellOpen,
} from "./device-view.js";
import {
  SHELL_MIN_H,
  clampShellH,
  releaseStoredShellPanel,
  shellMaxH,
  shellPanelPx,
} from "./shell-height.js";
import { confirm as confirmDialog } from "./confirm.js";
import { restartShell } from "./actions/shell.js";
import { refreshGitStatus } from "./git-status-store.js";
import { attachSplitter, type Splitter } from "./splitter.js";
import { touchCapable } from "./pointer-tier.js";

const SHELL_WS_PATH = "/api/shell/ws";
// Awaited before the first server resize so the PTY is sized on real cell metrics. ONE family — the
// one the width probe measures — declared in css/00-fonts.css, and never --font-mono's stack:
// `@cplieger/web-terminal-ui`'s `fontReady` owns why.
const SHELL_FONT_READY = '14px "Monaspace Neon NF"';

// The scrollback store is built at MODULE load, not inside ensureTerminal, even though the terminal
// itself stays lazy.
const shellScrollback = localScrollbackStorage({ prefix: "marotte.shell-scrollback." });

// The shell terminal recolored to marotte's palette.
const SHELL_THEME: Readonly<Record<string, string>> = {
  // The glyph font FIRST, ahead of the text face: it carries only the codepoints that have to tile,
  // so the browser takes box drawing, blocks, shades, braille and the mosaic blocks from it and
  // everything else from Monaspace behind it.
  "--font-mono": '"Web Terminal Glyphs", "Monaspace Neon NF", monospace',
  "--bg": "var(--c-term-bg)",
  "--text": "var(--c-term-fg)",
  "--accent": "var(--c-accent)",
  "--surface": "var(--c-bg-tertiary)",
  "--border": "var(--c-border)",
  // The library sizes its key grid, scroll button and menu rows at one touch size on every tier.
  "--touch-target": "var(--ctl-h)",
};

const encoder = new TextEncoder();

/** Send bytes to the PTY through the kernel's sanitizing, scroll-snapping funnel (the v4
 *  handle's supported host path). No-op until the terminal is created (first panel open). */
function hostSend(bytes: Uint8Array): void {
  handle?.send(bytes);
}

/** `firstFrame` settles once the terminal has drawn a screen frame. */
let settleFirstFrame: () => void = () => undefined;
const firstFrame = new Promise<void>((resolve) => {
  settleFirstFrame = resolve;
});

/** Resolve `firstFrame` on the first screen frame that carries rows. Bytes sent before the
 *  resume is served are lost or doubled: on a fresh session the engine's ledger-lost drop
 *  empties the outbox, and a send between socket open and the resumeAck is replayed by the
 *  outbox retransmit. */
function firstFrameGate(): TerminalFeature {
  return {
    name: "firstFrameGate",
    setup(ctx) {
      return {
        teardown: ctx.on("wire:screen", (msg) => {
          if (msg.rows.length > 0) {
            settleFirstFrame();
          }
        }),
      };
    },
  };
}

// A session that ends leaves this panel dead until something reattaches, and the panel is the only
// thing that can decide to.

/** How long a session must have run for its end to count as a fresh incident rather than another
 *  failure of the spawn that replaced the last one. Above the ladder's longest step, or a storm
 *  would reset its own backoff. */
const REATTACH_STABLE_MS = 3000;
/** Consecutive reattaches before the panel gives up and leaves "Session ended" standing. A shell
 *  that dies as fast as it is spawned (a login file that exits, a missing interpreter) must not
 *  be respawned forever; four attempts across ~2s ride out a transient and stop well short of a
 *  hot loop. */
const REATTACH_MAX = 4;
/** First backoff step; each later attempt doubles it (0, 250, 500, 1000ms). */
const REATTACH_BASE_MS = 250;

let reattachAttempts = 0;
/** When the last reattach actually ran (0 = never), for the stability window. */
let reattachedAt = 0;
/** Counts reattaches so an awaiting caller can tell whether one landed while it was waiting. The
 *  restart POST and the ended close race each other, and this is what keeps the winner from
 *  being reconnected over. */
let reattachSeq = 0;
let reattachTimer: ReturnType<typeof setTimeout> | null = null;

/** Queue ONE reattach, backing off across consecutive failures. Idempotent: a second call while
 *  a reattach is already pending is the two triggers (the ended close and the restart response)
 *  naming the same incident, not two incidents. The FIRST attempt runs synchronously, because by
 *  the time either trigger fires the server has already installed the replacement handler — the
 *  swap precedes the kill that closes the socket, and the restart response comes after both — so
 *  there is nothing to wait for. */
function scheduleReattach(reason: "ended" | "restart"): void {
  // No terminal means no connection to reattach; the first panel open connects.
  if (handle === null || reattachTimer !== null) {
    return;
  }
  if (reason === "restart" || Date.now() - reattachedAt > REATTACH_STABLE_MS) {
    reattachAttempts = 0;
  }
  if (reattachAttempts >= REATTACH_MAX) {
    return;
  }
  const delay = reattachAttempts === 0 ? 0 : REATTACH_BASE_MS * 2 ** (reattachAttempts - 1);
  reattachAttempts++;
  if (delay === 0) {
    reattachShell();
    return;
  }
  reattachTimer = setTimeout(() => {
    reattachTimer = null;
    reattachShell();
  }, delay);
}

/** Attach to the PTY the server now serves. */
function reattachShell(): void {
  reattachedAt = Date.now();
  reattachSeq++;
  handle?.reattach();
}

/** Kill the PTY and get a fresh one. Confirmed, because unlike the clear it destroys whatever is
 *  running. */
async function hostRestart(): Promise<void> {
  const ok = await confirmDialog(
    "Restart the shell? Anything running in it is killed.",
    "Restart",
    "normal",
  );
  if (!ok) {
    return;
  }
  const seq = reattachSeq;
  if ((await restartShell.dispatch()) === null) {
    return; // the action framework toasts the failure
  }
  if (reattachSeq === seq) {
    scheduleReattach("restart");
  }
}

let handle: TerminalHandle | null = null;
let initialized = false;

// The on-screen key grid (Tab/Esc/arrows/Enter/sticky-Ctrl).
let keys: TerminalFeature<MobileToolbarApi> | null = null;

/** Show or hide the key grid, and write the trigger's pressed state. ONE writer, because a face
 *  left behind is the whole failure mode of a toggle whose panel lives somewhere else. */
function toggleKeys(): void {
  const api = keys?.api;
  if (api === undefined) {
    return; // no terminal yet: the panel's first open builds it
  }
  api.toggle();
  $.shellKeysBtn.setAttribute("aria-pressed", api.isOpen() ? "true" : "false");
}

/** The toggle's two faces, both Lucide (`maximize` / `minimize`) flattened into one path: four
 *  corners pointing OUT to enter full screen, the same four pointing IN to leave. */
const FS_ICON_ENTER =
  "M8 3H5a2 2 0 0 0-2 2v3M21 8V5a2 2 0 0 0-2-2h-3M3 16v3a2 2 0 0 0 2 2h3M16 21h3a2 2 0 0 0 2-2v-3";
const FS_ICON_LEAVE =
  "M8 3v3a2 2 0 0 1-2 2H3M21 8h-3a2 2 0 0 1-2-2V3M3 16h3a2 2 0 0 1 2 2v3M16 21v-3a2 2 0 0 1 2-2h3";

/** Write every part of the toggle that depends on whether the panel is full screen: the pressed
 *  flag, the label, and the glyph. */
function setFullscreenBtnState(on: boolean): void {
  const fsBtn = $.shellFullscreenBtn;
  fsBtn.setAttribute("aria-pressed", on ? "true" : "false");
  const label = on ? "Exit full screen" : "Full screen";
  fsBtn.setAttribute("data-tooltip", label);
  fsBtn.setAttribute("aria-label", label);
  // Optional chained: the panel's glyph is markup this module does not build, and a missing one is
  // no reason to drop the rest of the state.
  fsBtn.querySelector("path")?.setAttribute("d", on ? FS_ICON_LEAVE : FS_ICON_ENTER);
}

/** Wire the panel's full-screen toggle. aria-pressed mirrors the panel class so the button reads
 *  as a toggle; setShellOpen resets both when the panel closes. */
function wireFullscreenToggle(): void {
  const fsBtn = $.shellFullscreenBtn;
  setFullscreenBtnState(false);
  fsBtn.addEventListener("click", () => {
    const panel = $.shellPanel;
    if (!panel.classList.contains("shell-fullscreen")) {
      panel.classList.add("shell-fullscreen");
      setFullscreenBtnState(true);
      return;
    }
    // Without it the class drop is instantaneous and there is nothing to animate — CSS cannot
    // transition an element out of a state it has already left.
    panel.classList.add("shell-fullscreen-leaving");
    setFullscreenBtnState(false);
    const done = (): void => {
      panel.classList.remove("shell-fullscreen", "shell-fullscreen-leaving");
    };
    panel.addEventListener("animationend", done, { once: true });
    // Belt and braces: reduced-motion zeroes the animation, and a suppressed renderer may never
    // fire animationend, which would strand the panel fullscreen with the button already reading
    // "off".
    setTimeout(done, 250);
  });
}

export function revealShellKeys(): void {
  $.shellKeysBtn.classList.remove("hidden");
}

/** Wire the shell panel's host controls. Called once from app.ts on boot. The terminal itself is
 *  NOT created here — it is built lazily on first open (see ensureTerminal), so a session that
 *  never opens the shell opens no WebSocket. */
export function initShellPanel(): void {
  if (initialized) {
    return;
  }
  initialized = true;

  // Toolbar button opens/toggles the panel; the header X closes it.
  $.shellBtn.addEventListener("click", () => {
    setShellOpen(!shellOpen);
  });
  $.shellToggleBtn.addEventListener("click", () => {
    setShellOpen(false);
  });
  // Restart button kills the PTY and gets a fresh one.
  $.shellRestartBtn.addEventListener("click", () => {
    void hostRestart();
  });
  // Key-toolbar button drives the grid; the library draws no toggle for it.
  $.shellKeysBtn.classList.toggle("hidden", !touchCapable());
  $.shellKeysBtn.addEventListener("click", () => {
    toggleKeys();
  });

  wireFullscreenToggle();

  // The command and its "\r" ride ONE buffer: the sticky Ctrl transform rewrites a one-character
  // payload, so a separate CR send becomes a CSI-u sequence.
  setShellRunCallback((cmd: string) => {
    if (!shellOpen) {
      setShellOpen(true, { focus: false });
    }
    void firstFrame.then(() => {
      hostSend(encoder.encode(`${cmd}\r`));
    });
  });

  initShellResize();
}

/** Keyboard resize step (2rem) per ArrowUp/ArrowDown on the handle. */
const SHELL_KEY_STEP = 32;

/** The panel's height while nothing has applied one: the CSS default in `.shell-panel { height:
 *  var(--shell-h, 16rem) }` (21-shell-panel.css). Shadowed here rather than read back, because
 *  neither reading is available when it is needed: a CLOSED panel measures 0 (the closed state
 *  pins height 0) and a computed read during the open transition measures the tween. */
const SHELL_DEFAULT_H = 256;

let splitter: Splitter | undefined;

/** Clamp (and round) a panel height, then apply it via the --shell-h custom property the panel's
 *  `height` consumes. Returns the applied value. */
function applyShellH(h: number): number {
  const clamped = shellPanelPx(h);
  $.shellPanel.style.setProperty("--shell-h", `${String(clamped)}px`);
  splitter?.syncAria(clamped);
  return clamped;
}

/** Drag-to-resize on the panel's top edge handle (bottom-docked, so dragging up grows the
 *  panel). The height lands in --shell-h and persists per-device in device-view (shell_h; 0 =
 *  CSS default). The handle is also a keyboard separator: ArrowUp grows, ArrowDown shrinks (WCAG
 *  2.1.1). */
function initShellResize(): void {
  splitter = attachSplitter({
    handle: $.shellResize,
    axis: "y",
    direction: -1,
    orientation: "horizontal",
    label: "Resize shell",
    keys: { grow: "ArrowUp", shrink: "ArrowDown" },
    step: () => SHELL_KEY_STEP,
    measure: () => $.shellPanel.getBoundingClientRect().height,
    limits: () => ({ min: SHELL_MIN_H, max: shellMaxH() }),
    apply: applyShellH,
    commit: setShellHeight,
    frame: "immediate",
    // Suspend the panel's height transition so it tracks the pointer 1:1 instead of easing 200ms
    // behind it (see .shell-panel.resizing).
    onDragStart: () => {
      $.shellPanel.classList.add("resizing");
    },
    onDragEnd: () => {
      $.shellPanel.classList.remove("resizing");
    },
  });

  // Restore the persisted height (re-clamped: the viewport may have changed since it was saved).
  // Harmless while closed — the closed state pins height 0; the value takes effect on open.
  const saved = shellHeight();
  if (saved > 0) {
    applyShellH(saved);
    return;
  }
  // Nothing stored, so the panel is on the CSS default and the separator still needs a value.
  splitter.syncAria(clampShellH(SHELL_DEFAULT_H));
}

/** Build the terminal exactly once, into the (empty) #shell-terminal root. The server PTY
 *  persists across WS reconnects, so this is safe to call on the first open and never again;
 *  reopening the panel reuses the same terminal and its live connection. */
function ensureTerminal(): void {
  if (handle !== null) {
    return;
  }
  handle = createTerminal($.shellTerminal, {
    features: () => {
      keys = mobileToolbar({ externalToggle: true });
      return [...presetSingle(), firstFrameGate(), keys];
    },
    layout: "container",
    wsPath: SHELL_WS_PATH,
    fontReady: SHELL_FONT_READY,
    theme: SHELL_THEME,
    // The shell died and nothing is retrying. On this server that is recoverable (the next connect
    // gets a fresh PTY), so typing `exit` or wedging a child ends with a new prompt rather than a
    // dead panel.
    onSessionEnded: () => {
      scheduleReattach("ended");
    },
    // Restore the shell's scrollback from this device rather than pulling it back over the wire.
    persistScrollback: shellScrollback,
  });
}

let shellOpen = false;

/** Open or close the shell slider. Open: build the terminal on first use (opening its
 *  WebSocket), remove `shell-closed` (CSS slides the panel up, or keeps it where prepaint.js
 *  already painted it open) and release the pre-paint state, mark the toolbar button active,
 *  then focus the terminal via its handle (skipped with focus:false — the boot-time restore must
 *  not steal focus from the prompt input). */
function setShellOpen(open: boolean, opts: { focus?: boolean } = {}): void {
  shellOpen = open;
  if (open) {
    // Capture the chat scroll-area height before the panel steals vertical space, so the same
    // content stays in view once it opens.
    const prevHeight = getScrollEl().clientHeight;
    ensureTerminal();
    // Same task as the class removal, so the pre-paint rule hands over to the base rule at an equal
    // height and no transition starts.
    $.shellPanel.classList.remove("shell-closed");
    releaseStoredShellPanel();
    $.shellBtn.classList.add("active");
    requestAnimationFrame(() => {
      if (opts.focus !== false) {
        handle?.focus();
      }
      const shrunk = prevHeight - getScrollEl().clientHeight;
      if (shrunk > 0) {
        shiftScroll(shrunk);
      }
    });
  } else {
    // Leave fullscreen before closing: the fullscreen rule pins height with !important
    // (un-animatable collapse), and a persisted class would make the next open start fullscreen.
    $.shellPanel.classList.remove("shell-fullscreen");
    setFullscreenBtnState(false);
    // The panel usually holds focus (the terminal's hidden textarea); hiding it would drop focus to
    // <body> and restart Tab order from the document top (WCAG 2.4.3). Hand focus back to the
    // toolbar button instead.
    if ($.shellPanel.contains(document.activeElement)) {
      $.shellBtn.focus({ preventScroll: true });
    }
    releaseStoredShellPanel();
    $.shellPanel.classList.add("shell-closed");
    $.shellBtn.classList.remove("active");
    // Whatever the user typed in here may have written to the tree — a `git commit`, a build, an
    // editor invoked at the prompt — and this panel is the one writer that can name nothing about
    // it.
    void refreshGitStatus();
  }
  recordShellOpen(open);
}

/** Restore the shell panel on page load when this device left it open. Runs the full open path
 *  (build terminal; no slide, since prepaint.js already painted the panel open), so the
 *  WebSocket only opens when the shell was actually left open — but without focusing the
 *  terminal, so boot doesn't steal focus from the prompt input. */
export function restoreShell(): void {
  if (storedShellOpen()) {
    setShellOpen(true, { focus: false });
  }
}
