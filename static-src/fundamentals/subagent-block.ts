// The delegated-work card: a LEAF with no body (its output is on the delegate's own page),
// so its HEAD opens that page and its tail is pushed in through `setTail`. The state word is an
// `.sr-only` span only on an INERT head: a screen reader ignores `aria-label` on a plain div,
// while an anchor head carries both in its own label.

import { el } from "@cplieger/reactive";
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

/** The card's identity row and foot. */
interface SubagentBox {
  /** The `.subagent-block` root to insert into the DOM. */
  readonly root: HTMLDivElement;
  /** Update the header status glyph and the announced state word. */
  setStatus(status: ToolStatus): void;
  /** Update the delegate's display name. */
  setName(name: string): void;
  /** Secondary identity beside the name (the requested model category); empty removes
   *  it. */
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

/** The identity row, the foot and the setters that write them; the card adds its tail
 *  between them. */
interface Shell {
  root: HTMLDivElement;
  foot: HTMLDivElement;
  box: SubagentBox;
}

function buildShell(name: string, status: ToolStatus, opener?: SubagentOpener): Shell {
  const root = el("div", { className: "subagent-block" }) as HTMLDivElement;
  // `tool-icon` is what the `.tool-icon.is-*` tint selectors match against:
  // without it a settled delegate keeps the running accent forever.
  const icon = el("span", { className: "subagent-icon tool-icon" });
  const nameEl = el("span", { className: "subagent-name" }, name);
  const detailEl = el("span", { className: "subagent-detail" });
  // ONE owner for the state word, chosen by which head was built: an anchor names
  // itself, so building both would announce the word twice.
  const stateEl = opener === undefined ? el("span", { className: "sr-only" }) : null;
  // The NAVIGATION glyph. A span rather than a button: the head itself is the control, so
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
    // The slot empties while active so CSS can spin it as a ring.
    icon.classList.toggle("subagent-spinner", active);
    const mark = failed ? outcomeIcon("fail") : aborted ? outcomeIcon("warn") : iconSvg;
    icon.replaceChildren(...(active ? [] : [iconEl(mark)]));
    refreshName(s);
  };

  applyIcon(status);

  return {
    root,
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
  const shell = buildShell(name, status, opts.open);
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
