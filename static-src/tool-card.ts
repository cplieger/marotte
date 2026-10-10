// The live flow and replay build cards through this one builder, so the two never diverge.

import type { ToolStatus, TextSpan } from "./types.js";
import type { BuildToolCardOpts } from "./tool-card-opts.js";
import { escText, windowOutput, windowSpans } from "./strings.js";
import { renderOutput } from "./output-render.js";
import { fileIcon, toolIcon, outcomeIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { chevronEl } from "./chevron.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { openChange, openAtLine, openCallDiff, openSpec } from "./navigate.js";
import { specDirOf } from "./spec-path.js";
import { lineDiff, windowHunks, stats as diffStats } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";
import { setUserScrolledUp } from "./scroll.js";
import { wireRowToggle } from "./disclosure-row.js";
import { toolCallBulk, type ToolBulk } from "./tool-bulk.js";
import {
  SILENCE_THRESHOLD_MS,
  noteToolActivity,
  silenceLabel,
  silenceMsFor,
} from "./tool-silence.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import {
  renderInfoFor,
  disclosedClaim,
  formatMCPToolName,
  commandTitle,
  toolTitleText,
  toolDepth1,
  hasDepth1,
  isToolActive,
  isToolDone,
  type ToolRenderInfo,
} from "./tool-schema.js";
import type { ToolDenial, ToolInteraction, ToolOffload } from "./types.js";
import { buildPath } from "./route-path.js";
import { interactionFact } from "./interaction.js";
import { el } from "@cplieger/reactive";

/** Build a tool-call element. Does not append it to the DOM. */
export function buildToolCard(opts: BuildToolCardOpts): HTMLDivElement {
  const info = renderInfoFor(opts.title, opts.kind, opts.input, {
    disclosed: opts.disclosed,
    denial: opts.denial,
    sourcePath: opts.sourcePath,
  });
  const depth1 = toolDepth1(info.kind);
  const withToggle = hasDepth1(info.kind);
  const shellTitle = commandTitle(opts.title, opts.kind, opts.input);
  // A disclose_context call names its DOCUMENT, not the tool that fetched it.
  const displayTitle =
    info.disclosed !== null
      ? disclosedClaim(info.disclosed)
      : info.mcp !== null
        ? formatMCPToolName(info.mcp.tool)
        : (shellTitle ?? toolTitleText(opts.title));

  const node = el("div", { className: `tool-call tool-depth1-${depth1}` }) as HTMLDivElement;
  node.dataset["kind"] = info.kind;
  node.dataset["title"] = displayTitle;
  node.dataset["depth1"] = depth1;
  node.dataset["toolId"] = opts.id;
  if (info.disclosed !== null) {
    node.dataset["disclosed"] = info.disclosed.type;
  }
  if (info.denial !== null) {
    // Read back by applyOutcome on the update path, which only has the DOM.
    node.dataset["denied"] = "1";
  }
  if (opts.declined === true) {
    // The DOM is this fact's ONE source, so the stamp lands before `applyOutcome`.
    // Never cleared: the wire field is `omitempty`, so absence means unchanged.
    node.dataset["declined"] = "1";
  }
  if (info.mcp !== null) {
    node.dataset["mcpServer"] = info.mcp.server;
  }
  if (info.filePath !== "") {
    node.dataset["filename"] = info.fileBasename;
    node.dataset["filePath"] = info.filePath;
  }
  if (opts.live && isToolActive(opts.status)) {
    // `data-start-ms` MEANS in flight: `tool-group.ts`'s fold guards read it, and
    // `applyStatusUpdate` drops it on every settle.
    node.dataset["startMs"] = String(Date.now());
    // The create seeds the silence value, or a call that never streams would never
    // get a silence marker.
    noteToolActivity(opts.chatID ?? "", opts.id);
  }

  const summary = el("div", {
    className: withToggle ? "tool-summary has-disclosure" : "tool-summary",
  });
  summary.appendChild(buildHeader(opts, displayTitle, info, withToggle));
  node.appendChild(summary);
  applyOutcome(node, opts.status, displayTitle, info);

  // The subtitle sits in the summary, so the box is one disclosure and one hover
  // target; a card titled by its command carries none.
  if (
    shellTitle === null &&
    (depth1 === "search" || depth1 === "fetch" || depth1 === "generic" || depth1 === "output")
  ) {
    const subtitle = extractSubtitle(opts.input);
    if (subtitle !== "") {
      summary.appendChild(el("div", { className: "tool-subtitle" }, subtitle));
    }
  }

  if (depth1 === "move") {
    const row = moveRow(opts.input);
    if (row !== null) {
      summary.appendChild(row);
    }
  }

  if (withToggle) {
    // The SHELL only: everything with a cost is built on first open by `detailsBody`,
    // because a transcript mounts dozens of collapsed cards.
    node.insertAdjacentHTML("beforeend", detailsShell());
    // Declared once so the load and the `data-disclosable` arms agree.
    const deferred: DeferredCtx = { opts, depth1, info };
    // Born open only where the reader had it open. Expand-on-fail is the live status
    // FLIP's (`expandToolDetails`); deriving it here would open every failed call in a
    // reopened chat.
    const buildOpen = opts.detailsOpen === true;
    wireToggle(node, detailsBody(node, deferred), buildOpen);
    // One arm per thing that can open this card, for `refreshToolDisclosure`: the region
    // is empty until first open, so it cannot answer. `bulkChatID` is the same guard
    // `detailsBody`'s fetch reads, so no chevron sits over a region that can never fill.
    if (
      opts.denial !== undefined ||
      (opts.live && opts.input !== undefined) ||
      (opts.output !== undefined && opts.output.trim() !== "") ||
      (bulkChatID(opts) !== null && DEFERRED_PARTS.some((part) => part.reveals(deferred)))
    ) {
      node.dataset["disclosable"] = "1";
    }
  }

  wireFileLink(node, info.filePath, depth1 === "diff");
  syncOffloadLink(node, opts.offload);
  syncInteractionFact(node, opts.interaction);

  // An edit's diff IS its depth 1, so it is inserted here, not deferred.
  const resting = depth1 === "diff" ? restingDiff(opts, info) : null;
  if (resting !== null) {
    insertDiffPreview(node, resting.path, resting.src);
  }

  refreshToolDisclosure(node);
  return node;
}

/** Give a card whose output KAS offloaded a link to the full file, after its output
 *  region or on a claim-only card's claim row. Idempotent and one-way, like the field. */
export function syncOffloadLink(card: HTMLElement, offload: ToolOffload | undefined): void {
  if (offload === undefined || card.querySelector(".tool-offload-link") !== null) {
    return;
  }
  const host = card.querySelector(".tool-details") ?? card.querySelector(".tool-summary");
  if (host === null) {
    return;
  }
  const link = el(
    "a",
    {
      className: "tool-offload-link",
      href: buildPath({ kind: "file", path: offload.path }),
      [CHROME_ATTR]: "",
    },
    `Open full output (${offload.total_chars.toLocaleString()} chars)`,
  );
  link.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    // A modified click (new tab or window) is a deliberate escape from routing.
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
      return;
    }
    e.preventDefault();
    openAtLine(offload.path);
  });
  host.appendChild(link);
}

/** Put the answer's fact line on the card's claim row. Rewrites in place; an absent
 *  interaction leaves it standing, like the field. */
export function syncInteractionFact(
  card: HTMLElement,
  interaction: ToolInteraction | undefined,
): void {
  if (interaction === undefined) {
    return;
  }
  const text = interactionFact(interaction);
  const summary = card.querySelector(".tool-summary");
  if (summary === null) {
    return;
  }
  let line = summary.querySelector<HTMLElement>(":scope > .tool-fact");
  if (line === null) {
    line = el("div", { className: "tool-fact" });
    summary.appendChild(line);
  }
  if (line.textContent !== text) {
    line.textContent = text;
  }
}

/** The diff a card draws at REST, or null: the call's own ToolDiff, else the pair its
 *  INPUT carries. The deferred table's diff member is its negation, so a preview is
 *  never inserted twice. */
function restingDiff(
  opts: BuildToolCardOpts,
  info: ToolRenderInfo,
): { path: string; src: { oldText: string; newText: string } } | null {
  const d = opts.diffs?.[0];
  if (d !== undefined) {
    return { path: d.path, src: { oldText: d.old_text ?? "", newText: d.new_text } };
  }
  if (info.writesFile && info.diffSources !== null) {
    return { path: info.filePath, src: info.diffSources };
  }
  return null;
}

/** Two facts a move's claim line cannot carry. */
function moveRow(input: Record<string, unknown> | undefined): HTMLElement | null {
  const from = typeof input?.["sourcePath"] === "string" ? input["sourcePath"] : "";
  const to = typeof input?.["destinationPath"] === "string" ? input["destinationPath"] : "";
  if (from === "" || to === "") {
    return null;
  }
  return el(
    "div",
    { className: "tool-move-row" },
    el("span", { className: "tool-move-from" }, from),
    el(
      "span",
      { className: "tool-move-arrow", "aria-hidden": "true", [CHROME_ATTR]: "" },
      "\u2192",
    ),
    el("span", { className: "tool-move-to" }, to),
  );
}

/** A one-line subtitle from tool input. */
export function extractSubtitle(input: Record<string, unknown> | undefined): string {
  if (input === undefined) {
    return "";
  }
  for (const key of ["query", "pattern", "command", "url", "path", "explanation"]) {
    const val = input[key];
    if (typeof val === "string" && val !== "") {
      return val.length > 120 ? val.slice(0, 117) + "\u2026" : val;
    }
  }
  return "";
}

// --- HTML fragments ---

function buildHeader(
  opts: BuildToolCardOpts,
  displayTitle: string,
  info: ToolRenderInfo,
  withToggle: boolean,
): HTMLDivElement {
  const header = el("div", {
    className: "tool-header",
    title: displayTitle,
  }) as HTMLDivElement;

  const iconSpan = el("span", { className: "tool-icon" }, iconEl(toolIcon(info.kind, opts.title)));
  header.appendChild(iconSpan);

  const titleSpan = el("span", { className: "tool-title" }, displayTitle);
  header.appendChild(titleSpan);

  if (info.mcp !== null) {
    const badge = el(
      "span",
      {
        className: "tool-mcp-badge",
        title: `From the ${info.mcp.server} MCP integration`,
        [CHROME_ATTR]: "",
      },
      info.mcp.server,
    );
    badge.style.setProperty("--mcp-hue", String(mcpHue(info.mcp.server)));
    header.appendChild(badge);
  }

  if (info.filePath !== "") {
    // The filename IS the link to the change. There used to be a second
    // "View diff" button beside the stats; depth 2 is a click on the SUBJECT,
    // and a generic button next to it was a second affordance for one intent.
    const btn = el(
      "button",
      {
        className: "tool-file-link",
        "data-path": info.filePath,
        // The chip shows the BASENAME, so the tooltip carries THE PATH ALONE and the
        // action lives in the accessible name: a leading action line would leave the
        // path one of `--tooltip-lines`, clipping long paths.
        "data-tooltip": info.filePath,
        "aria-label": fileLinkLabel(info),
      },
      el("span", { className: "tool-file-icon" }, iconEl(fileIcon(info.fileBasename, false))),
      el("span", { className: "tool-file-name" }, info.fileBasename),
    );
    header.appendChild(btn);
    // A path under a spec directory gets its own door onto the spec page; the chip's
    // click stays the file's diff.
    const specDir = specDirOf(info.filePath);
    if (specDir !== null) {
      const open = el(
        "button",
        {
          type: "button",
          className: "tool-spec-link btn-small",
          "data-spec-dir": specDir,
          "data-tooltip": `Open the ${specDir.split("/").pop() ?? specDir} spec`,
          [CHROME_ATTR]: "",
        },
        "Open spec",
      );
      open.addEventListener("click", (e: Event) => {
        e.stopPropagation();
        void openSpec(specDir, opts.chatID);
      });
      header.appendChild(open);
    }
  }

  if (opts.live && isToolActive(opts.status)) {
    const spinner = el("span", { className: "tool-spinner" });
    header.appendChild(spinner);
  }

  // No status word: the row carries ONE mark (applyOutcome).

  // A card that goes bare LATER is `refreshToolDisclosure`'s.
  if (withToggle) {
    header.appendChild(
      el(
        "button",
        {
          className: "tool-disclosure",
          "aria-expanded": "false",
          "aria-label": "Toggle tool details",
        },
        chevronEl(),
      ),
    );
  }

  return header;
}

/** What a caller may state; the History page states a run's verdict through the same
 *  writer, so `aborted` serves both. */
type OutcomeStatus = ToolStatus;

/** The verdicts the vocabulary paints: `pending` and `in_progress` are one thing to a
 *  reader, and a refusal is its own state. */
type OutcomeState = "ok" | "fail" | "warn" | "declined" | "denied" | "running";

/** What a `.tool-icon` slot holds: the glyph it was BUILT with, and the state painted. */
interface IconMark {
  identity: Element | null;
  painted: OutcomeState | null;
}
const iconMarks = new WeakMap<HTMLElement, IconMark>();

/** Paint a row's ONE outcome mark and give `nameTarget` its accessible name ("Edited
 *  auth.go, succeeded"). `ok`/`running` keep the identity glyph and move its tint; the
 *  other states swap in an `outcomeIcon` silhouette, so hue is never the only channel.
 *  The identity glyph must be in the slot before the first call: it is CAPTURED, never
 *  recomputed, because a History row's glyph is not a `toolIcon`. Idempotent.
 *  `tool-group.ts` and `subagent-block.ts` paint through the same `icons.ts` glyph set
 *  without calling this. */
export function applyOutcome(
  node: HTMLElement,
  status: OutcomeStatus,
  displayTitle: string,
  info: ToolRenderInfo,
  nameTarget: HTMLElement = node,
): void {
  const icon = node.querySelector<HTMLElement>(".tool-icon");
  // A policy refusal is its OWN state, not a failure. Read from the dataset too, for
  // the update path, which only has the DOM.
  const denied = info.denial !== null || node.dataset["denied"] === "1";
  // A DECLINED call ran correctly and refused: neither success nor failure. The DOM is
  // its one source. `denied` outranks it, because that command never ran.
  const declined = node.dataset["declined"] === "1";
  const state: OutcomeState = denied
    ? "denied"
    : declined
      ? "declined"
      : status === "aborted"
        ? "warn"
        : isToolDone(status)
          ? status === "failed"
            ? "fail"
            : "ok"
          : "running";
  node.dataset["outcome"] = state;
  if (icon !== null) {
    icon.classList.remove("is-ok", "is-fail", "is-warn", "is-running", "is-declined", "is-denied");
    icon.classList.add(`is-${state}`);
    icon.setAttribute("aria-hidden", "true");
    let mark = iconMarks.get(icon);
    if (mark === undefined) {
      const built = icon.firstElementChild;
      mark = {
        identity: built === null ? null : (built.cloneNode(true) as Element),
        painted: null,
      };
      iconMarks.set(icon, mark);
    }
    if (mark.painted !== state) {
      const keepIdentity = state === "ok" || state === "running";
      const wanted = keepIdentity
        ? (mark.identity?.cloneNode(true) ?? null)
        : iconEl(outcomeIcon(state));
      // `painted` is recorded only on a write, so it describes what the slot HOLDS.
      if (wanted !== null) {
        icon.replaceChildren(wanted);
        mark.painted = state;
      } else if (mark.painted !== null) {
        // No identity glyph: clear the silhouette THIS function wrote. A slot it never
        // wrote is left alone.
        icon.replaceChildren();
        mark.painted = state;
      }
    }
  }
  const subject = info.fileBasename !== "" ? `${displayTitle} ${info.fileBasename}` : displayTitle;
  nameTarget.setAttribute("aria-label", `${subject}, ${outcomeWord(state)}`);
}

/** Bring one card's SILENCE marker up to date: shown past the threshold, removed
 *  otherwise, only while `data-start-ms` says it is in flight. Idempotent. The value is
 *  TRACKED, so a caller inside an effect repaints while the call stays quiet. */
export function syncSilenceMarker(card: HTMLElement, chatID: string, toolID: string): void {
  const header = card.querySelector<HTMLElement>(".tool-header");
  if (header === null) {
    return;
  }
  const existing = header.querySelector<HTMLElement>(".tool-silence");
  const ms = silenceMsFor(chatID, toolID);
  if (card.dataset["startMs"] === undefined || ms === undefined || ms < SILENCE_THRESHOLD_MS) {
    existing?.remove();
    return;
  }
  const words = silenceLabel(ms);
  if (existing !== null) {
    existing.textContent = words;
    return;
  }
  // CHROME, so find-in-chat does not count it; ahead of the trailing controls.
  const marker = el("span", { className: "tool-silence", [CHROME_ATTR]: "" }, words);
  header.insertBefore(
    marker,
    header.querySelector(".tool-spinner") ?? header.querySelector(".tool-disclosure"),
  );
}

/** The word the accessible name uses. */
function outcomeWord(state: OutcomeState): string {
  switch (state) {
    case "ok":
      return "succeeded";
    case "fail":
      return "failed";
    case "warn":
      return "aborted";
    case "declined":
      return "declined";
    case "denied":
      return "blocked by security policy";
    default:
      return "running";
  }
}

// mcpHue derives a stable hue in [0,360) from the server name (an FNV-1a fold); two
// servers may share a hue.
export function mcpHue(server: string): number {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < server.length; i++) {
    h ^= server.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return h % 360;
}

/** The empty details region, mounted with the card. `.tool-output` is in the SHELL
 *  because the live update path streams into it before the card is ever opened. The
 *  disclosure controller owns the collapse state. */
function detailsShell(): string {
  return `<div class="tool-details"><div class="tool-output"></div></div>`;
}

/** What a deferred piece of content is decided and applied against. */
interface DeferredCtx {
  readonly opts: BuildToolCardOpts;
  readonly depth1: string;
  readonly info: ToolRenderInfo;
}

/** One piece of a card's content the transcript dropped, put back from the bulk when the
 *  reader OPENS the card, never on mount. For `output` the predicates differ: a previewed
 *  output arrives without spans, so the bulk is APPLIED to restore its colour, but only a
 *  CUT output REVEALS anything. */
interface DeferredPart {
  /** Should the bulk be applied for this piece on open. */
  pending(ctx: DeferredCtx): boolean;
  /** Does this piece make the card openable: an arm of `data-disclosable`. */
  reveals(ctx: DeferredCtx): boolean;
  apply(node: HTMLDivElement, bulk: ToolBulk, ctx: DeferredCtx): void;
}

/** A ToolDiff is dropped whole or not at all, so its two predicates coincide. */
function diffDeferred({ opts, depth1, info }: DeferredCtx): boolean {
  return depth1 === "diff" && opts.hasFull === true && restingDiff(opts, info) === null;
}

/** The chat this card's bulk is keyed on, or `null`: the ONE owner of "can this card
 *  reach its bulk", read by the `data-disclosable` union and `detailsBody`'s fetch. */
function bulkChatID(opts: BuildToolCardOpts): string | null {
  const id = opts.chatID ?? "";
  return id === "" ? null : id;
}

const DEFERRED_PARTS: readonly DeferredPart[] = [
  {
    pending: ({ opts }) => opts.hasFull === true,
    reveals: ({ opts }) => opts.hasFull === true && (opts.outputBytes ?? 0) > 0,
    apply: (node, bulk, { depth1 }) => {
      if (bulk.output.trim() === "") {
        return;
      }
      const out = node.querySelector(".tool-output");
      if (out === null) {
        return;
      }
      out.replaceChildren();
      appendOutput(node, bulk.output, bulk.outputSpans, depth1 === "output");
    },
  },
  {
    pending: diffDeferred,
    reveals: diffDeferred,
    apply: (node, bulk, { info }) => {
      // After an await, a `tool_call_update` may have inserted the preview already
      // (`applyDiffUpdate` checks the same from its side).
      if (node.querySelector(".tool-diff-preview") !== null) {
        return;
      }
      const d = bulk.diffs[0];
      if (d === undefined) {
        return;
      }
      insertDiffPreview(node, d.path === "" ? info.filePath : d.path, {
        oldText: d.old_text ?? "",
        newText: d.new_text,
      });
    },
  },
];

/** The details body's builder, run at most once, on first open. Registered BEFORE the
 *  disclosure controller's listener, which measures `scrollHeight` to animate; a region
 *  filled after that would animate to zero and jump. A previewed card paints its preview,
 *  then fetches its bulk in ONE request and repaints; a `null` bulk is not retried. */
function detailsBody(node: HTMLDivElement, ctx: DeferredCtx): () => void {
  const { opts, depth1 } = ctx;
  let built = false;
  return () => {
    if (built) {
      return;
    }
    built = true;
    const details = node.querySelector<HTMLElement>(".tool-details");
    if (details === null) {
      return;
    }
    // The command this prints is ALSO in `.tool-subtitle`, so a reader of both (a
    // rolling tail) has to dedupe them.
    const inputBlock =
      opts.live && opts.input !== undefined
        ? `<pre class="tool-input">${escText(JSON.stringify(opts.input, null, 2))}</pre>`
        : "";
    const head = denialBlock(opts.denial) + inputBlock;
    if (head !== "") {
      details.insertAdjacentHTML("afterbegin", head);
    }
    if (opts.output !== undefined && opts.output.trim() !== "") {
      appendOutput(node, opts.output, opts.outputSpans ?? [], depth1 === "output");
    }
    const pending = DEFERRED_PARTS.filter((part) => part.pending(ctx));
    const chatID = bulkChatID(opts);
    if (pending.length === 0 || chatID === null) {
      return;
    }
    void toolCallBulk(chatID, opts.id).then((bulk) => {
      if (bulk === null) {
        return;
      }
      for (const part of pending) {
        part.apply(node, bulk, ctx);
      }
    });
  };
}

/** The rule that refused the call, and where it lives: the user owns the policy. */
function denialBlock(d: ToolDenial | undefined): string {
  if (d === undefined) {
    return "";
  }
  const rows: string[] = [
    `<div class="tool-denial-row"><span>Capability</span><code>${escText(d.capability)}</code></div>`,
  ];
  if (d.resource !== "") {
    rows.push(
      `<div class="tool-denial-row"><span>Resource</span><code>${escText(d.resource)}</code></div>`,
    );
  }
  if (d.rule !== undefined) {
    const patterns = [
      ...(d.rule.match ?? []).map((m) => escText(m)),
      ...(d.rule.exclude ?? []).map((m) => `!${escText(m)}`),
    ].join(", ");
    rows.push(
      `<div class="tool-denial-row"><span>Rule</span><code>${escText(d.rule.effect)} ${escText(d.rule.capability)}${patterns === "" ? "" : ` (${patterns})`}</code></div>`,
    );
  }
  if (d.source !== "") {
    rows.push(
      `<div class="tool-denial-row"><span>From</span><code>${escText(d.scope)}: ${escText(d.source)}</code></div>`,
    );
  }
  return `<div class="tool-denial" ${CHROME_ATTR}>${rows.join("")}</div>`;
}

// --- Wiring ---

/** The filename opens the CHANGE on a card that made one (vs HEAD: the working tree is
 *  the after state), and the FILE on a card that only read it. The `+N -M` link uses the
 *  card's own pair instead. */
function wireFileLink(el: HTMLElement, filePath: string, isChange: boolean): void {
  if (filePath === "") {
    return;
  }
  el.querySelector(".tool-file-link")?.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    if (isChange) {
      openChange(filePath);
    } else {
      openAtLine(filePath);
    }
  });
}

/** The chip's accessible name: what its click opens, per `wireFileLink`. */
function fileLinkLabel(info: ToolRenderInfo): string {
  if (toolDepth1(info.kind) === "diff") {
    return `Open the diff for ${info.fileBasename}`;
  }
  if (info.kind === "hook") {
    return `Open the hook file ${info.fileBasename}`;
  }
  return `Open ${info.fileBasename}`;
}

const detailCtls = new WeakMap<HTMLElement, DisclosureController>();

// Per-card deferred body builders: `expandToolDetails` opens WITHOUT a click, so it runs
// the builder itself before the failure path reads the output back.
const detailBuilders = new WeakMap<HTMLElement, () => void>();

/** Wire a card's details region. `initialOpen` creates the controller ALREADY OPEN, the
 *  only silent way to mount an open region: opening after creation animates even in the
 *  same task. `open: true` needs no measurement, so the card may still be detached. */
function wireToggle(el: HTMLElement, buildBody: () => void, initialOpen: boolean): void {
  const toggle = el.querySelector<HTMLElement>(".tool-disclosure");
  const details = el.querySelector<HTMLElement>(".tool-details");
  if (toggle === null || details === null) {
    return;
  }
  if (initialOpen) {
    buildBody();
  }
  // BEFORE createDisclosure's own click handler (registration order), so the region is
  // filled before the controller measures it. `wireRowToggle` forwards row clicks here.
  toggle.addEventListener("click", buildBody);
  detailBuilders.set(el, buildBody);
  const summary = el.querySelector<HTMLElement>(".tool-summary");
  // The primitive owns the ARIA, activation and the animated height; marotte keeps only
  // the scroll-freeze on a user collapse. Chevron direction is CSS's, off `aria-expanded`.
  const ctl = createDisclosure(toggle, details, {
    open: initialOpen,
    onToggle: (open, source) => {
      if (!open && source === "user") {
        setUserScrolledUp(true);
      }
    },
  });
  // The whole summary activates the chevron. Wired HERE, past the early return, so a
  // claim-only card's summary never becomes clickable.
  if (summary !== null) {
    wireRowToggle(summary, toggle);
  }
  detailCtls.set(el, ctl);
}

/** Force-open a card's details (a failed tool's error output). A BARE card is refused,
 *  since it has no chevron to close it again; the body is built BEFORE the open. */
export function expandToolDetails(card: HTMLElement): void {
  if (card.querySelector(".tool-disclosure") === null) {
    return;
  }
  detailBuilders.get(card)?.();
  detailCtls.get(card)?.open();
}

// Chevrons taken off bare cards, held so re-attaching keeps the controller's listeners.
const detachedToggles = new WeakMap<HTMLElement, HTMLElement>();

/** Whether the details region holds anything a reader can SEE, or will once opened.
 *  Emptiness is a property of the region; the wire status cannot answer it. */
function isDisclosable(card: HTMLElement): boolean {
  if (card.dataset["disclosable"] === "1") {
    return true;
  }
  const out = card.querySelector(".tool-output");
  return out !== null && out.textContent.trim() !== "";
}

/** Give a card its disclosure, or take it away: the ONE writer of bare-ness. Idempotent
 *  both ways. The chevron is DETACHED, not hidden, so a bare card carries no
 *  `aria-expanded`, like a claim-only one. */
export function refreshToolDisclosure(card: HTMLElement): void {
  // A claim-only card has no details region.
  if (card.querySelector(".tool-details") === null) {
    return;
  }
  const summary = card.querySelector<HTMLElement>(".tool-summary");
  if (isDisclosable(card)) {
    const held = detachedToggles.get(card);
    if (held !== undefined) {
      card.querySelector(".tool-header")?.appendChild(held);
      detachedToggles.delete(card);
    }
    summary?.classList.add("has-disclosure");
    return;
  }
  // Close through the CONTROLLER while the button is connected, or the region is left
  // open and exposed.
  detailCtls.get(card)?.close();
  const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
  if (toggle !== null) {
    // No FOCUSED chevron reaches this: every detach left runs inside `buildToolCard`, before
    // the card is in the document. Reintroduce an in-document one and focus falls to <body>;
    // the fallback is the card at `tabindex="-1"` or `.tool-group-header`.
    detachedToggles.set(card, toggle);
    toggle.remove();
  }
  summary?.classList.remove("has-disclosure");
}

/** Fill a card's output region. When `windowed`, the first and last N lines show and a
 *  control reveals the rest IN PLACE. Never routed to the shell panel: that live PTY
 *  would interleave historical bytes with its current stream. */
function appendOutput(
  node: HTMLElement,
  output: string,
  spans: readonly TextSpan[],
  windowed: boolean,
): void {
  const out = node.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  const pre = el("pre");
  const paint = (text: string, s: readonly TextSpan[]): void => {
    renderOutput(pre, text, s);
  };
  if (!windowed) {
    paint(output, spans);
    out.appendChild(pre);
    return;
  }
  const win = windowOutput(output);
  paint(win.text, windowSpans(spans, win.kept));
  out.appendChild(pre);
  if (win.elided === 0) {
    return;
  }
  const reveal = el(
    "button",
    { type: "button", className: "tool-output-reveal", [CHROME_ATTR]: "" },
    `Show ${String(win.elided)} more line${win.elided === 1 ? "" : "s"}`,
  );
  reveal.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    paint(output, spans);
    reveal.remove();
  });
  out.appendChild(reveal);
}

// --- Inline diff preview for file-writing tools ---

export function insertDiffPreview(
  node: HTMLDivElement,
  filePath: string,
  src: { oldText: string; newText: string },
): void {
  const diff = lineDiff(src.oldText, src.newText);
  const s = diffStats(diff);
  if (s.adds === 0 && s.dels === 0) {
    return;
  }

  const wrap = el("div", { className: "tool-diff-preview" });

  // `+N -M` opens the same diff, scrolled to the first hunk.
  const statBtn = el(
    "button",
    {
      type: "button",
      className: "tool-diff-stats",
      "data-tooltip": "Open the diff",
      [CHROME_ATTR]: "",
    },
    el("span", { className: "diff-add-count" }, `+${String(s.adds)}`),
    el("span", { className: "diff-del-count" }, `-${String(s.dels)}`),
  );
  const openDiff = (e: Event): void => {
    e.stopPropagation();
    openCallDiff(filePath, src.oldText, src.newText);
  };
  statBtn.addEventListener("click", openDiff);
  wrap.appendChild(statBtn);

  // Line numbers ON, so a reader carries their place into the real document.
  const win = windowHunks(diff, { maxRows: 24, context: 2 });
  const mini = renderDiffPane(win.lines, {
    unified: true,
    lineNumbers: true,
    syncScroll: false,
    lang: filePath,
  });
  mini.classList.add("tool-diff-mini");
  wrap.appendChild(mini);

  // The omitted-hunk count opens the same full pair the stats do.
  if (win.hunksOmitted > 0) {
    const more = el(
      "button",
      {
        type: "button",
        className: "tool-diff-more",
        "data-tooltip": "Open the diff",
        [CHROME_ATTR]: "",
      },
      `+${String(win.hunksOmitted)} more hunk${win.hunksOmitted === 1 ? "" : "s"}`,
    );
    more.addEventListener("click", openDiff);
    wrap.appendChild(more);
  }

  node.insertBefore(wrap, node.querySelector(".tool-details"));
}
