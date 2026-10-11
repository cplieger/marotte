// The delegated-work boxes. `buildSubagentCard` is a LEAF with no body (its output is on
// the delegate's own page), so its HEAD opens that page and its tail is pushed in through
// `setTail`. `buildSubagentContainer`'s body holds its stages' cards, so it discloses.
// The state word is an `.sr-only` span only on an INERT head: a screen reader ignores
// `aria-label` on a plain div, while an anchor head carries both in its own label.

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { ToolStatus } from "../types.js";
import { isToolActive } from "../tool-schema.js";
import { iconEl } from "../icon-el.js";
import { chevronEl } from "../chevron.js";
import { ICON_TAB_AGENT, outcomeIcon } from "../icons.js";
import { CHROME_ATTR } from "../chrome-attr.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  earnsTurnFooter,
  type TurnSummaryData,
} from "./turn-footer.js";

/** What both delegated-work boxes answer to. */
interface SubagentBox {
  /** The `.subagent-block` root to insert into the DOM. */
  readonly root: HTMLDivElement;
  /** Update the header status glyph and the announced state word. */
  setStatus(status: ToolStatus): void;
  /** Update the delegate's display name. */
  setName(name: string): void;
  /** Secondary identity beside the name (an inline helper's model and effort);
   *  empty removes it. */
  setDetail(detail: string): void;
  /** Swap the identity glyph (SVG string; roles.ts iconForSubagent). The
   *  spinner still owns the slot while the delegate is active. */
  setIcon(svg: string): void;
  /** Render the footer ledger; a no-op until the data earns a row. */
  setSummary(d: TurnSummaryData): void;
}

/** One delegate's card. No body: its output is on its own page. */
export interface SubagentCard extends SubagentBox {
  /** Replace the rolling tail, oldest line first; a no-op once the delegate settled. */
  setTail(lines: readonly string[]): void;
}

/** A PIPELINE's container. Its `body` hosts its stages' own cards. */
export interface SubagentContainer extends SubagentBox {
  /** The container the composition renders this pipeline's stage cards into. */
  readonly body: HTMLElement;
  /** Whether another element has been posted after this box in the store (only the
   *  dispatcher knows). True FOLDS the box, subject to the carve-outs; false never
   *  opens one. */
  setSuperseded(superseded: boolean): void;
}

/** The way to this delegate's own page, injected because a `fundamentals/` view must not
 *  import the tabs feature. `href` makes the head a real anchor; `open` routes a plain
 *  click through the app. */
export interface SubagentOpener {
  href: string;
  open: () => void;
}

export interface SubagentCardOptions {
  /** What the card's HEAD opens; absent leaves it inert (a DETACHED render, or no chat
   *  to open it in). */
  open?: SubagentOpener;
}

export interface SubagentContainerOptions {
  /** Fired only when the READER flips the disclosure; an automatic fold or open is
   *  silent, or the registry would record the view's decisions as the reader's. */
  onOpenChange?: (open: boolean) => void;
  /** Where the disclosure starts. Default TRUE: a box nothing was posted after renders
   *  expanded, and only composition can say otherwise. */
  startOpen?: boolean;
  /** Whether the reader has ALREADY decided about this box in a previous mount, so
   *  the auto path is off for its whole life. Default false. */
  userDecided?: boolean;
}

/** The announced word for a status. `cancelled` rather than the wire's `aborted`,
 *  matching the enclosing turn card's own footer. */
function stateWord(s: ToolStatus): string {
  return s === "failed"
    ? "failed"
    : s === "aborted"
      ? "cancelled"
      : isToolActive(s)
        ? "running"
        : "succeeded";
}

/** The identity row, the foot and the four setters that write them; each builder
 *  adds its own middle region between them. */
interface Shell {
  root: HTMLDivElement;
  header: HTMLElement;
  foot: HTMLDivElement;
  box: SubagentBox;
}

function buildShell(
  name: string,
  status: ToolStatus,
  isContainer: boolean,
  opener?: SubagentOpener,
): Shell {
  const root = el("div", { className: "subagent-block" }) as HTMLDivElement;
  // `tool-icon` is what the `.tool-icon.is-*` tint selectors match against:
  // without it a settled delegate keeps the running accent forever.
  const icon = el("span", { className: "subagent-icon tool-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  const detailEl = el("span", { className: "subagent-detail" });
  // ONE owner for the state word, chosen by which head was built: an anchor names
  // itself, so building both would announce the word twice.
  const stateEl = opener === undefined ? el("span", { className: "sr-only" }) : null;
  // The leaf's NAVIGATION glyph, in the slot the container's disclosure toggle
  // occupies. A span rather than a button: the head itself is the control, so
  // anything interactive inside it is axe's `nested-interactive`.
  const chevron =
    opener === undefined
      ? null
      : el("span", { className: "subagent-head-chevron", "aria-hidden": "true" }, chevronEl());
  const header = el(
    opener === undefined ? "div" : "a",
    {
      className: "subagent-header",
      [CHROME_ATTR]: "",
      ...(opener === undefined ? {} : { href: opener.href }),
    },
    icon,
    nameEl,
    detailEl,
    stateEl,
    chevron,
  );
  let headLink: HTMLAnchorElement | null = null;
  if (opener !== undefined) {
    headLink = header as HTMLAnchorElement;
    headLink.addEventListener("click", (e) => {
      // A modified click (new tab/window) is a deliberate escape from routing.
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
        return;
      }
      e.preventDefault();
      opener.open();
    });
  }

  // Created eagerly since `setSummary` withholds the footer until there is
  // something to show.
  const foot = el("div", { className: "subagent-foot", [CHROME_ATTR]: "" }) as HTMLDivElement;
  root.append(header, foot);

  let footer: HTMLDivElement | null = null;
  let lastSummary: TurnSummaryData = {};
  let iconSvg = ICON_TAB_AGENT;
  let lastStatus = status;
  let displayName = name;
  let displayDetail = "";

  /** Write the name and its state word into whichever channel this head has. */
  const refreshName = (s: ToolStatus): void => {
    if (headLink === null) {
      if (stateEl !== null) {
        stateEl.textContent = stateWord(s);
      }
      return;
    }
    // `<thing>, <state word>`: the link role already says the head opens something.
    const parts = displayDetail === "" ? [displayName] : [displayName, displayDetail];
    headLink.setAttribute("aria-label", `${parts.join(", ")}, ${stateWord(s)}`);
  };

  const applyIcon = (s: ToolStatus): void => {
    const failed = s === "failed";
    // A delegate the reader STOPPED is neither success nor failure: the yellow `warn` mark.
    const aborted = s === "aborted";
    const active = isToolActive(s);
    icon.classList.toggle("is-fail", failed);
    icon.classList.toggle("is-warn", aborted);
    icon.classList.toggle("is-ok", !failed && !aborted && !active);
    icon.classList.toggle("is-running", active);
    root.classList.toggle("running", active);
    // A CARD empties the slot while active so CSS can spin it as a ring; a CONTAINER
    // keeps its identity glyph, because its stages carry the rings.
    const ring = active && !isContainer;
    icon.classList.toggle("subagent-spinner", ring);
    const mark = failed ? outcomeIcon("fail") : aborted ? outcomeIcon("warn") : iconSvg;
    icon.replaceChildren(...(ring ? [] : [iconEl(mark)]));
    refreshName(s);
  };

  applyIcon(status);

  return {
    root,
    header,
    foot,
    box: {
      root,
      setStatus(s: ToolStatus): void {
        lastStatus = s;
        applyIcon(s);
      },
      setName(n: string): void {
        displayName = n;
        nameEl.textContent = n;
        refreshName(lastStatus);
      },
      setDetail(d: string): void {
        displayDetail = d;
        detailEl.textContent = d;
        refreshName(lastStatus);
      },
      setIcon(svg: string): void {
        if (svg === iconSvg) {
          return;
        }
        iconSvg = svg;
        applyIcon(lastStatus);
      },
      setSummary(d: TurnSummaryData): void {
        lastSummary = d;
        // No extras: Rewind and the turn actions are turn-card affordances.
        if (!earnsTurnFooter(d)) {
          return;
        }
        if (footer === null) {
          footer = buildTurnFooter(d);
          footer.classList.add("subagent-footer");
          foot.appendChild(footer);
        }
        updateTurnFooter(footer, lastSummary);
      },
    },
  };
}

/** Build one delegate's card: identity row, rolling tail, foot. */
export function buildSubagentCard(
  name: string,
  status: ToolStatus,
  opts: SubagentCardOptions = {},
): SubagentCard {
  const shell = buildShell(name, status, false, opts.open);
  const tail = el("div", { className: "subagent-tail", "aria-hidden": "true", [CHROME_ATTR]: "" });
  shell.root.insertBefore(tail, shell.foot);
  let live = isToolActive(status);
  if (!live) {
    tail.remove();
  }
  return {
    ...shell.box,
    setStatus(s: ToolStatus): void {
      shell.box.setStatus(s);
      // Settled: removed, and `live` stops a late tail write putting it back.
      if (live && !isToolActive(s)) {
        live = false;
        tail.remove();
      }
    },
    setTail(lines: readonly string[]): void {
      if (!live) {
        return;
      }
      // No lines leaves the tail EMPTY, which its `:empty` resting state keys on.
      tail.replaceChildren(
        ...(lines.length === 0
          ? []
          : [
              el(
                "div",
                { className: "subagent-tail-window" },
                ...lines.map((l) => el("div", { className: "subagent-tail-line" }, l)),
              ),
            ]),
      );
    },
  };
}

/** Build a pipeline's container: identity row with activity dots, a disclosed
 *  body for its stages, foot.
 *
 *  IT RENDERS EXPANDED WHILE IT IS THE NEWEST TOP-LEVEL ELEMENT, and folds when the
 *  next element is posted after it — the same rule and the same carve-outs as
 *  `tool-group.ts`, driven by the dispatcher through `setSuperseded` because only it
 *  knows where this box sits in the store. The expanded state goes to the box the
 *  reader is currently being shown; being superseded is what earns the fold.
 *
 *  Its carve-outs, all refusals to COLLAPSE: a still-running stage, a failure (which
 *  also RE-OPENS a box that folded before it), a reader who has decided, and an EMPTY
 *  body — the inverted form of the group's bare carve-out, since an empty body has
 *  already withdrawn the whole control and there would be no chevron to close it
 *  again. */
export function buildSubagentContainer(
  name: string,
  status: ToolStatus,
  opts: SubagentContainerOptions = {},
): SubagentContainer {
  const shell = buildShell(name, status, true);
  const startOpen = opts.startOpen ?? true;
  // Built in its FULL form; `syncDisclosure` withdraws the control while the body is empty.
  shell.root.classList.add("subagent-container", "has-disclosure");
  shell.root.classList.toggle("collapsed", !startOpen);
  // A span, not a button: inside the `role="button"` header a `<button>` is axe's
  // `nested-interactive`, which aria-hidden + tabindex="-1" does not clear.
  const chevron = el("span", { className: "subagent-toggle", "aria-hidden": "true" }, chevronEl());
  // A disclosure chevron LEADS; the leaf's navigation chevron trails, so a collapsed
  // container and a leaf card never look alike (chevron.ts owns the rule).
  shell.header.prepend(chevron);
  shell.header.append(
    // Shown only while collapsed and running (14-tools.css).
    el(
      "span",
      { className: "subagent-busy activity-dots", "aria-hidden": "true" },
      el("span", { className: "activity-dot" }),
      el("span", { className: "activity-dot" }),
      el("span", { className: "activity-dot" }),
    ),
  );
  shell.header.setAttribute("role", "button");
  shell.header.setAttribute("tabindex", "0");
  const body = el("div", { className: "subagent-body" });
  shell.root.insertBefore(body, shell.foot);
  // A reader's toggle outranks every automatic path. Read off the toggle's `source`, not
  // a click: a withdrawn disclosure has no trigger, so a click there is not the reader.
  let userToggled = opts.userDecided ?? false;
  let lastStatus = status;
  let superseded = false;
  const onToggle = (open: boolean, source: "user" | "api"): void => {
    shell.root.classList.toggle("collapsed", !open);
    if (source !== "user") {
      return;
    }
    userToggled = true;
    opts.onOpenChange?.(open);
  };
  let ctl = createDisclosure(shell.header, body, { open: startOpen, onToggle });
  let wired = true;
  let pendingAutoOpen = false;

  /** The newest-element fold, and the only place the carve-outs are spelled.
   *  COLLAPSE-ONLY, so idempotent: the superseded verdict is monotone. */
  const applyAutoCollapse = (): void => {
    if (
      !superseded ||
      userToggled ||
      isToolActive(lastStatus) ||
      lastStatus === "failed" ||
      body.firstElementChild === null ||
      !ctl.isOpen
    ) {
      return;
    }
    ctl.close();
  };

  /** The disclosure's ONE writer. An EMPTY body gets the primitive's region-only mode: no
   *  `aria-expanded` over an empty region, no tab stop, no chevron. */
  const syncDisclosure = (): void => {
    const populated = body.firstElementChild !== null;
    if (populated !== wired) {
      wired = populated;
      ctl.dispose();
      if (populated) {
        // Re-created, so the primitive re-installs the `role` and `tabindex` the
        // withdrawal removed. Seeded from `startOpen`, NOT `ctl.isOpen`: the withdrawn
        // controller is `open: false`, so reading it would born-collapse every box filled
        // a task late. A populated body never empties and refills, because
        // `pruneEmptyContainers` removes the box in the same pass that drops its last stage.
        ctl = createDisclosure(shell.header, body, { open: startOpen, onToggle });
        // PREPEND: a trailing chevron reads as the leaf card's navigation glyph.
        shell.header.prepend(chevron);
        shell.root.classList.add("has-disclosure");
        shell.root.classList.toggle("collapsed", !startOpen);
      } else {
        shell.header.removeAttribute("aria-expanded");
        shell.header.removeAttribute("aria-controls");
        shell.header.removeAttribute("tabindex");
        shell.header.removeAttribute("role");
        chevron.remove();
        shell.root.classList.add("collapsed");
        shell.root.classList.remove("has-disclosure");
        ctl = createDisclosure(null, body, { open: false });
      }
    }
    if (populated && pendingAutoOpen && !userToggled) {
      pendingAutoOpen = false;
      // Through `openBody` rather than a bare `ctl.open()`, so the held ask lands
      // through the one enforcement point.
      openBody();
    }
    // A supersede refused while the body was empty becomes answerable now.
    applyAutoCollapse();
  };

  /** The failure auto-open's one enforcement point. An empty body has no chevron to
   *  close it again, so the ask is HELD until the body gains its first stage. */
  const openBody = (): void => {
    if (body.firstElementChild === null) {
      pendingAutoOpen = true;
      return;
    }
    if (ctl.isOpen) {
      return;
    }
    ctl.open();
  };

  if (status === "failed") {
    openBody();
  }
  // A microtask lands before paint, so withdrawing a box built empty is invisible where
  // a task-late wiring would pop the chevron in.
  queueMicrotask(syncDisclosure);
  // A stage can arrive after the driver's status frame, or be re-parented in by
  // `pipelineBoxFor`.
  new MutationObserver(syncDisclosure).observe(body, { childList: true });

  return {
    ...shell.box,
    body,
    setStatus(s: ToolStatus): void {
      lastStatus = s;
      shell.box.setStatus(s);
      if (s === "failed" && !userToggled) {
        // A failure BLOCKS a fold and RE-OPENS a box that folded before it settled.
        openBody();
        return;
      }
      // A settle folds nothing itself; it releases the still-running refusal.
      applyAutoCollapse();
    },
    setSuperseded(next: boolean): void {
      superseded = next;
      applyAutoCollapse();
    },
  };
}
