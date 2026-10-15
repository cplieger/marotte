// The run dock: one card above the bottom bar's composer holding everything about the execution as
// a whole, so none of it scrolls away. A head (summary or alert, the alert's action, the verbs),
// notes, the steps in flight as rows, and one disclosure panel with Steps, Timeline and Inputs.
// Names no source: controls, alert actions and notes are the consumer's elements and sentences.

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { attachClamp, releaseClampsIn } from "../clamp-text.js";
import { chevronEl } from "../chevron.js";
import { publishViewportHeight } from "../dock-ask.js";
import { wireRowToggle } from "../disclosure-row.js";
import { iconEl } from "../icon-el.js";
import { reconcile } from "../reconcile.js";
import { initSegmentedBar, setSegmentHidden } from "../segmented-bar.js";
import { formatElapsed } from "../strings.js";
import { currentWork, elapsed, newestStarted, type ExecNode, type ExecRun } from "./model.js";
import { STATE_WORD, inFlight, paintStateMark } from "./status.js";
import { paintLabel } from "./label.js";
import { kindGlyph, nodeAt } from "./tree.js";
import { buildExecSummary } from "./summary.js";

export interface ExecDockView {
  /** Paint the run, its shown step and the consumer's own notes. Safe on every tick. */
  paint(run: ExecRun, selected: string, notes: readonly string[]): void;
  /** Seat the run's verbs in the head; the same element again moves nothing, so focus inside it
   *  survives a repaint. */
  setControls(row: HTMLElement | null): void;
  /** Seat the alert's own action beside the verbs, under `setControls`' contract. */
  setAlertAction(action: HTMLElement | null): void;
  /** Release the panel, the clamps and the host. */
  dispose(): void;
}

interface ExecDockOpts {
  readonly host: HTMLElement;
  /** The head's identity glyph. */
  readonly icon: string;
  /** The page's step tree and timeline, which the panel's segments hold. */
  readonly tree: HTMLElement;
  readonly timeline: HTMLElement;
  /** Show a step, as a tree row click does. */
  readonly select: (path: string) => void;
  /** Bring the step that matters into view in `scroller`, the Steps pane's box. Called once each
   *  time the panel opens on Steps or Steps is picked while it is open, never on a repaint, so a
   *  reader scrolling the tree is never moved. */
  readonly revealSteps: (scroller: HTMLElement) => void;
}

type Segment = "steps" | "timeline" | "inputs";

const SEGMENTS: readonly { id: Segment; label: string }[] = [
  { id: "steps", label: "Steps" },
  { id: "timeline", label: "Timeline" },
  { id: "inputs", label: "Inputs" },
];

/** The instructions clamp: the line count the STYLESHEET clamps to, plus the character threshold
 *  used only while the element is detached. `clamp-line-count.test.ts` holds the first to the
 *  stylesheet's own. */
const CLAMP = { lines: 3, fallbackChars: 220 } as const;

let dockIds = 0;

/** What a reader does to scroll the tree themselves. */
const TAKEOVER = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

/** One step row: a button that shows its step. */
interface LiveRow {
  readonly root: HTMLElement;
  readonly btn: HTMLButtonElement;
  readonly mark: HTMLElement;
  readonly kind: HTMLElement;
  readonly label: HTMLElement;
  readonly tail: HTMLElement;
  readonly dur: HTMLElement;
}

export function buildExecDock(opts: ExecDockOpts): ExecDockView {
  publishViewportHeight();
  const id = `ev-dock-${String(++dockIds)}`;
  const head = el("div", { className: "ev-dock-head" });
  const summary = buildExecSummary({ washHost: head, live: true });
  summary.root.id = `${id}-sum`;
  const toggle = el(
    "button",
    {
      type: "button",
      className: "ev-box-toggle",
      "aria-label": "Run details",
      "aria-describedby": summary.root.id,
    },
    chevronEl(),
  );
  const alertAct = el("span", { className: "ev-dock-act ev-dock-alert-act" });
  const act = el("span", { className: "ev-dock-act" });
  head.append(
    toggle,
    el("span", { className: "ev-dock-icon", "aria-hidden": "true" }, iconEl(opts.icon)),
    summary.root,
    alertAct,
    act,
  );

  // Never `hidden`: a live region must already be rendered when its first line arrives.
  const notes = el("ul", { className: "ev-dock-notes", role: "status" });

  const rows = el("ul", { className: "ev-dock-rows", "aria-label": "Steps in flight" });
  const more = el("button", { type: "button", className: "ev-dock-more" }) as HTMLButtonElement;
  more.hidden = true;
  const live = el("div", { className: "ev-dock-live" }, rows, more);

  const inputs = el("dl", { className: "ev-inputs" });
  const bar = el("nav", { className: "seg-bar ev-dock-seg", "aria-label": "Run detail sections" });
  const panels = new Map<Segment, HTMLElement>();
  const content: Record<Segment, HTMLElement> = {
    steps: opts.tree,
    timeline: opts.timeline,
    inputs,
  };
  const body = el("div", { className: "ev-dock-body" });
  for (const seg of SEGMENTS) {
    bar.append(
      el(
        "button",
        { type: "button", className: "seg", "data-ev-seg": seg.id },
        el("span", { className: "seg-label" }, seg.label),
      ),
    );
    const panel = el(
      "div",
      {
        className: "ev-dock-pane",
        id: `${id}-panel-${seg.id}`,
        role: "tabpanel",
        "aria-labelledby": `${id}-tab-${seg.id}`,
      },
      content[seg.id],
    );
    panels.set(seg.id, panel);
    body.append(panel);
  }
  const region = el("div", { className: "ev-dock-panel" }, bar, body);
  const root = el("div", { className: "ev-dock" }, head, notes, live, region);

  let active: Segment = "steps";
  const paintBar = initSegmentedBar(bar, {
    attr: "data-ev-seg",
    idPrefix: id,
    tabs: SEGMENTS,
    onSelect: (seg) => {
      const moved = seg !== active;
      show(seg);
      if (moved && seg === "steps" && ctl.isOpen) {
        reveal();
      }
    },
  });
  function show(seg: Segment): void {
    active = seg;
    paintBar(seg);
    for (const [key, panel] of panels) {
      panel.hidden = key !== seg;
    }
  }
  show(active);

  const ctl = createDisclosure(toggle, region, {
    onToggle: (open) => {
      root.toggleAttribute("data-open", open);
      if (open && active === "steps") {
        reveal();
      }
    },
  });

  let revealGen = 0;
  /** The panel's height opens from 0 and the pane shrinks with it, so a row centred now would be
   *  centred in nothing: the reveal waits for the open to settle, and a reader who touches the tree
   *  meanwhile, or a close or a segment change, calls it off. */
  function reveal(): void {
    const gen = ++revealGen;
    const opening = region.getAnimations().map((a) => a.finished);
    if (opening.length === 0) {
      opts.revealSteps(body);
      return;
    }
    const off = (): void => {
      revealGen++;
    };
    for (const type of TAKEOVER) {
      body.addEventListener(type, off, { once: true, passive: true });
    }
    void Promise.allSettled(opening).then(() => {
      for (const type of TAKEOVER) {
        body.removeEventListener(type, off);
      }
      if (gen === revealGen && ctl.isOpen && active === "steps") {
        opts.revealSteps(body);
      }
    });
  }
  wireRowToggle(head, toggle);
  more.addEventListener("click", () => {
    show("steps");
    if (!ctl.isOpen) {
      ctl.open();
    }
  });
  opts.host.replaceChildren(root);

  const rowParts = new WeakMap<HTMLElement, LiveRow>();
  const buildRow = (path: string): LiveRow => {
    const mark = el("span", { className: "ev-state", "aria-hidden": "true" });
    const kind = el("span", { className: "ev-kind", "aria-hidden": "true" });
    const label = el("span", { className: "ev-dock-label" });
    const tail = el("span", { className: "ev-dock-tail" });
    const dur = el("span", { className: "ev-dock-dur" });
    const btn = el(
      "button",
      { type: "button", className: "ev-dock-row" },
      mark,
      kind,
      label,
      tail,
      dur,
    ) as HTMLButtonElement;
    btn.addEventListener("click", () => {
      opts.select(path);
    });
    const row = {
      root: el("li", { className: "ev-dock-li" }, btn),
      btn,
      mark,
      kind,
      label,
      tail,
      dur,
    };
    rowParts.set(row.root, row);
    return row;
  };
  const paintRow = (row: LiveRow, n: ExecNode, shown: boolean): void => {
    if (row.btn.dataset["state"] !== n.state) {
      paintStateMark(row.mark, n.state);
      row.btn.dataset["state"] = n.state;
    }
    if (row.btn.dataset["kind"] !== n.kind) {
      row.kind.replaceChildren(kindGlyph(n.kind));
      row.btn.dataset["kind"] = n.kind;
    }
    paintLabel(row.label, n);
    setText(row.tail, n.tail ?? "");
    const ms = elapsed(n.start, n.end);
    const time = ms > 0 ? formatElapsed(ms) : "";
    setText(row.dur, time);
    setAttr(
      row.btn,
      "aria-label",
      [n.label, STATE_WORD[n.state], time].filter((s) => s !== "").join(", "),
    );
    if (shown) {
      row.btn.setAttribute("aria-current", "true");
    } else {
      row.btn.removeAttribute("aria-current");
    }
  };

  /** The steps in flight, else the newest started one, plus the shown step wherever it stands. A
   *  list cut to one row keeps only the lead (the shown step's row, else the first), so the step
   *  the transcript belongs to is named on screen at every width and `+N` is the way to the rest. */
  const liveSteps = (run: ExecRun, selected: string): ExecNode[] => {
    const work = currentWork(run.nodes);
    const flying = work.filter((n) => inFlight(n.state));
    const out =
      flying.length > 0 ? flying : [newestStarted(run.nodes) ?? work[0]].flatMap((n) => n ?? []);
    const shown = nodeAt(run.nodes, selected);
    if (shown?.children.length === 0 && !out.includes(shown)) {
      out.push(shown);
    }
    return out;
  };

  const paintNotes = (run: ExecRun, extra: readonly string[]): void => {
    const list: { text: string; failed: boolean }[] = [];
    if (run.alert?.kind === "stopped") {
      list.push({ text: run.alert.text, failed: false });
    }
    if (run.notice !== undefined) {
      list.push(run.notice);
    }
    for (const text of extra) {
      list.push({ text, failed: false });
    }
    reconcile(notes, list, {
      key: (n) => n.text,
      mount: (n) => {
        const li = el(
          "li",
          { className: "ev-dock-note" },
          el("span", { className: "ev-dock-note-text" }, n.text),
        );
        li.toggleAttribute("data-failed", n.failed);
        return li;
      },
      update: (li, n) => {
        li.toggleAttribute("data-failed", n.failed);
      },
    });
  };

  /** Rebuilt only when the SET changed: an unguarded rebuild on every invalidation would discard
   *  the reader's show-more expansion. */
  const paintInputs = (run: ExecRun): void => {
    const entries = Object.entries(run.inputs ?? {});
    setSegmentHidden(bar, "data-ev-seg", "inputs", entries.length === 0);
    if (entries.length === 0 && active === "inputs") {
      show("steps");
    }
    const sig = entries.map(([k, v]) => `${k}\u0001${v}`).join("\u0002");
    if (inputs.dataset["sig"] === sig) {
      return;
    }
    inputs.dataset["sig"] = sig;
    // Before the discard, or the leaving rows keep their clamp observations on detached elements.
    releaseClampsIn(inputs);
    inputs.replaceChildren(
      ...entries.flatMap(([k, v]) => {
        const text = el("div", { className: "ev-in-text" }, v);
        const opener = el("button", {
          type: "button",
          className: "ev-in-more",
        }) as HTMLButtonElement;
        attachClamp(text, opener, CLAMP);
        return [
          el("dt", { className: "ev-in-k" }, k),
          el("dd", { className: "ev-in-v" }, text, opener),
        ];
      }),
    );
  };

  return {
    paint(run, selected, extra) {
      root.dataset["state"] = run.state;
      summary.paint(run);
      paintNotes(run, extra);
      const steps = liveSteps(run, selected);
      const lead = (steps.find((n) => n.path === selected) ?? steps[0])?.path;
      reconcile(rows, steps, {
        key: (n) => n.path,
        mount: (n) => {
          const row = buildRow(n.path);
          paintRow(row, n, n.path === selected);
          row.root.toggleAttribute("data-lead", n.path === lead);
          return row.root;
        },
        update: (rowRoot, n) => {
          const row = rowParts.get(rowRoot);
          if (row !== undefined) {
            paintRow(row, n, n.path === selected);
          }
          rowRoot.toggleAttribute("data-lead", n.path === lead);
        },
      });
      more.hidden = steps.length <= 1;
      setText(more, `+${String(steps.length - 1)}`);
      const hidden = steps.length - 1;
      setAttr(more, "aria-label", `Show ${String(hidden)} more ${hidden === 1 ? "step" : "steps"}`);
      paintInputs(run);
    },
    setControls(row) {
      seat(act, row);
    },
    setAlertAction(action) {
      seat(alertAct, action);
    },
    dispose() {
      ctl.dispose();
      releaseClampsIn(inputs);
      opts.host.replaceChildren();
    },
  };
}

/** Make `child` the host's only child, or empty it; the same element again moves nothing, since a
 *  re-insertion blurs whatever is focused inside and this runs once per progress frame. */
function seat(host: HTMLElement, child: HTMLElement | null): void {
  if (child === null) {
    host.replaceChildren();
  } else if (host.firstChild !== child || host.childNodes.length !== 1) {
    host.replaceChildren(child);
  }
}

function setText(target: HTMLElement, text: string): void {
  if (target.textContent !== text) {
    target.textContent = text;
  }
}

function setAttr(target: HTMLElement, name: string, value: string): void {
  if (target.getAttribute(name) !== value) {
    target.setAttribute(name, value);
  }
}
