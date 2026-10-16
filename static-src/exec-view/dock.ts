// The run dock: one card above the bottom bar's composer holding everything about the execution as
// a whole, so none of it scrolls away. A head (the run's identity, its progress and the verbs), the
// question a step asks, notes, the steps in flight as rows, and one disclosure panel with the run's
// inputs over Steps and Timeline. Names no source: controls and notes are the consumer's.

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { attachClamp, releaseClamp } from "../clamp-text.js";
import { chevronEl } from "../chevron.js";
import { publishViewportHeight } from "../dock-ask.js";
import { wireRowToggle } from "../disclosure-row.js";
import { iconEl } from "../icon-el.js";
import { ICON_SEG_STEPS, ICON_SEG_TIMELINE } from "../icons.js";
import { bindFileLink } from "../linkify.js";
import { fileOpenRoute } from "../preview-page.js";
import { reconcile } from "../reconcile.js";
import { buildPath } from "../route-path.js";
import { initSegmentedBar } from "../segmented-bar.js";
import { formatElapsed } from "../strings.js";
import { servedFileRoute } from "../utils-url.js";
import {
  currentWork,
  elapsed,
  newestStarted,
  type ExecNode,
  type ExecQuestion,
  type ExecRun,
} from "./model.js";
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

type Segment = "steps" | "timeline";

const SEGMENTS: readonly { id: Segment; label: string; icon: string }[] = [
  { id: "steps", label: "Steps", icon: ICON_SEG_STEPS },
  { id: "timeline", label: "Timeline", icon: ICON_SEG_TIMELINE },
];

/** The question's clamp: the line count the STYLESHEET clamps to (`clamp-line-count.test.ts`). */
const ASK_CLAMP = { lines: 4 } as const;

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

interface DockNote {
  text: string;
  failed: boolean;
}

export function buildExecDock(opts: ExecDockOpts): ExecDockView {
  publishViewportHeight();
  const id = `ev-dock-${String(++dockIds)}`;
  const head = el("div", { className: "ev-dock-head" });
  const summary = buildExecSummary();
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
  const act = el("span", { className: "ev-dock-act" });
  head.append(
    toggle,
    el("span", { className: "ev-dock-icon", "aria-hidden": "true" }, iconEl(opts.icon)),
    summary.root,
    act,
  );

  // Live regions are never `hidden`: one must already be rendered when its first line arrives.
  const askLive = el("div", { className: "ev-dock-ask-live", role: "status" });
  const askCount = el("p", { className: "ev-dock-ask-count" });
  const askText = el("p", { className: "ev-dock-ask-q", id: `${id}-ask` });
  const askMore = el("button", { type: "button", className: "clamp-more" }) as HTMLButtonElement;
  const ask = el("div", { className: "ev-dock-ask" }, askCount, askText, askMore);
  attachClamp(askText, askMore, ASK_CLAMP);
  const notes = el("ul", { className: "ev-dock-notes", role: "status" });
  // A failure interrupts; anything else waits its turn.
  const urgent = el("ul", { className: "ev-dock-notes", role: "alert" });

  const rows = el("ul", { className: "ev-dock-rows", "aria-label": "Steps in flight" });
  const more = el("button", { type: "button", className: "ev-dock-more" }) as HTMLButtonElement;
  more.hidden = true;
  const live = el("div", { className: "ev-dock-live" }, rows, more);

  const inputs = el("dl", { className: "ev-dock-inputs", "aria-label": "Inputs" });
  const bar = el("nav", { className: "seg-bar ev-dock-seg", "aria-label": "Run detail sections" });
  const panels = new Map<Segment, HTMLElement>();
  const content: Record<Segment, HTMLElement> = { steps: opts.tree, timeline: opts.timeline };
  const body = el("div", { className: "ev-dock-body" });
  for (const seg of SEGMENTS) {
    const glyph = iconEl(seg.icon);
    glyph.classList.add("seg-icon");
    glyph.setAttribute("aria-hidden", "true");
    bar.append(
      el(
        "button",
        { type: "button", className: "seg", "data-ev-seg": seg.id, "data-tooltip": seg.label },
        glyph,
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
  const region = el("div", { className: "ev-dock-panel" }, inputs, bar, body);
  const root = el("div", { className: "ev-dock" }, head, askLive, notes, urgent, live, region);

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
      // One cell of the rows' shared grid: a kind glyph shows only where it says something.
      el("span", { className: "ev-dock-name" }, kind, label),
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
      // Every row here is work, so only a kind other than a step says something.
      row.kind.replaceChildren(...(n.kind === "step" ? [] : [kindGlyph(n.kind)]));
      row.kind.hidden = n.kind === "step";
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

  /** The question region: the shown step's question, else the first, in full up to the clamp. */
  const paintAsk = (run: ExecRun, selected: string): void => {
    const questions = run.questions ?? [];
    const q: ExecQuestion | undefined =
      questions.find((x) => x.path !== undefined && x.path === selected) ?? questions[0];
    if (q === undefined) {
      if (ask.parentElement !== null) {
        ask.remove();
        setText(askText, "");
      }
      return;
    }
    const at = questions.indexOf(q) + 1;
    setText(
      askCount,
      questions.length > 1 ? `${String(at)} of ${String(questions.length)} questions` : "",
    );
    askCount.hidden = questions.length < 2;
    const label = q.step === undefined ? "A step asks" : `${q.step} asks`;
    setAttr(ask, "aria-label", label);
    if (askText.textContent !== q.text) {
      askText.textContent = q.text;
      attachClamp(askText, askMore, ASK_CLAMP).collapse();
    }
    if (ask.parentElement !== askLive) {
      askLive.replaceChildren(ask);
    }
  };

  const paintNotes = (run: ExecRun, extra: readonly string[]): void => {
    const calm: DockNote[] = [];
    const loud: DockNote[] = [];
    const alert = run.alert;
    if (alert !== undefined && alert.kind !== "input") {
      (alert.kind === "failed" ? loud : calm).push({
        text: alert.text,
        failed: alert.kind === "failed",
      });
    }
    if (run.notice !== undefined) {
      calm.push(run.notice);
    }
    for (const text of extra) {
      calm.push({ text, failed: false });
    }
    paintNoteList(notes, calm);
    paintNoteList(urgent, loud);
  };

  /** Rebuilt only when the SET changed, so a reader's focus on an input link survives a repaint. */
  const paintInputs = (run: ExecRun): void => {
    const entries = Object.entries(run.inputs ?? {});
    const sig = entries.map(([k, v]) => `${k}\u0001${v}`).join("\u0002");
    if (inputs.dataset["sig"] === sig) {
      return;
    }
    inputs.dataset["sig"] = sig;
    inputs.hidden = entries.length === 0;
    inputs.replaceChildren(
      ...entries.map(([k, v]) =>
        el("div", { className: "ev-dock-input" }, el("dt", {}, k), el("dd", {}, inputValue(v))),
      ),
    );
  };

  return {
    paint(run, selected, extra) {
      root.dataset["state"] = run.state;
      summary.paint(run);
      paintAsk(run, selected);
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
    dispose() {
      ctl.dispose();
      releaseClamp(askText);
      opts.host.replaceChildren();
    },
  };
}

/** One input's value on one line, its whole text in the tooltip; a workspace path opens its file. */
function inputValue(value: string): HTMLElement {
  const file = servedFileRoute(value);
  if (file === null) {
    return el("span", { className: "ev-dock-input-v", "data-tooltip": value }, value);
  }
  const link = el(
    "a",
    {
      className: "ev-dock-input-v ev-dock-input-path",
      href: buildPath(fileOpenRoute(file.path, file.line)),
      "data-tooltip": value,
    },
    value,
  ) as HTMLAnchorElement;
  bindFileLink(link, file.path, file.line);
  return link;
}

function paintNoteList(list: HTMLElement, items: readonly DockNote[]): void {
  reconcile(list, items, {
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
