// The "Run when…" popup on /docs/workflows: a recipe's launch rules as lines, and the form of a
// rule that launches the recipe when something happens on a forge.

import { el } from "@cplieger/ui";
import { split as splitKey } from "@cplieger/keyenc";
import { scheduleViewOf } from "./launch-rule-types.js";
import type {
  EventFilter,
  EventKind,
  EventSpec,
  Gap,
  Hold,
  LaunchRuleView,
} from "./launch-rule-types.js";
import type { ConfiguredForge, ForgeKind } from "./wire/types.gen.js";
import type { RunDefaults, RunDefaultsPicker } from "./run-defaults-picker.js";
import { buildUnattendedNote, describeSpec, formatStamp, summaryLine } from "./schedule-picker.js";
import { buildRuleInputs, type RuleInputs } from "./rule-inputs.js";
import { formatClock } from "./locale-time.js";
import { usableStatus } from "./forge-auth-status.js";

const KINDS: readonly [EventKind, string][] = [
  ["issue_labeled", "an issue gets a label"],
  ["pr_labeled", "a pull request gets a label"],
  ["issue_opened", "an issue is opened by…"],
  ["pr_opened", "a pull request is opened by…"],
  ["run_concluded", "a CI run finishes"],
];

type Result = NonNullable<EventFilter["result"]>[number];
const RESULTS: readonly Result[] = ["success", "failure", "neutral"];

/** The run triggers a pull request or a comment can start, which a rule takes only on request. */
const OUTSIDE_TRIGGERS = ["pull_request", "other"] as const;
const OWN_TRIGGERS = ["push", "schedule", "manual"] as const;

const labeled = (k: EventKind): boolean => k === "issue_labeled" || k === "pr_labeled";
const opened = (k: EventKind): boolean => k === "issue_opened" || k === "pr_opened";
const onPR = (k: EventKind): boolean => k === "pr_labeled" || k === "pr_opened";

/** The inputs a kind's event sets; internal/launchrule's Kind.eventInputs owns the list. */
function eventInputsOf(kind: EventKind): string[] {
  const common = ["repo", "forge", "trigger_kind", "trigger_rule", "trigger_key", "event_id"];
  if (kind === "run_concluded") {
    return [...common, "run_id", "result", "head_sha", "workflow"];
  }
  return [...common, onPR(kind) ? "pr_number" : "issue_number", labeled(kind) ? "label" : "author"];
}

/** An event key's event, by number or run id: the key holds no forge text, so the row cannot show
 *  a title an author wrote. "" for a key this client cannot read. */
function eventRef(key: string): string {
  let parts: string[];
  try {
    parts = splitKey(key);
  } catch {
    return "";
  }
  const [, , part, id] = parts;
  if (id === undefined) {
    return "";
  }
  if (part === "run") {
    return `run ${id}`;
  }
  return part === "issue" || part === "pr" ? `#${id}` : "";
}

function describeEvent(ev: EventSpec): string {
  const f = ev.filter;
  const thing = onPR(ev.kind) ? "a pull request" : "an issue";
  const branch = f.branch === undefined || f.branch === "" ? "" : ` on ${f.branch}`;
  if (labeled(ev.kind)) {
    return `When ${thing} in ${ev.repo}${branch} gets the label ${f.label ?? ""}`;
  }
  if (opened(ev.kind)) {
    return `When ${(f.authors ?? []).join(", ")} opens ${thing} in ${ev.repo}${branch}`;
  }
  const wf =
    f.workflow === undefined || f.workflow === "" ? "CI run" : (f.workflow.split("/").pop() ?? "");
  return `When ${ev.repo}'s ${wf} finishes with ${(f.result ?? []).join(" or ")}${branch}`;
}

/** One gap of an event rule, kept on its line until the operator dismisses it. */
function gapText(g: Gap): string {
  switch (g.cause) {
    case "outage": {
      const before = formatStamp(g.created_before);
      const unchecked =
        before === ""
          ? ""
          : `; runs created before ${before} that ended in that time were not checked`;
      return `gap: no complete check between ${formatStamp(g.from)} and ${formatStamp(g.to)}${unchecked}`;
    }
    case "aged_out":
      return g.queue_full === true
        ? `run ${String(g.run_id ?? "")} ended while the queue was full and was not launched`
        : `run ${String(g.run_id ?? "")} was still running at its last check and was not seen to end`;
    case "result_window":
      return `the forge did not list at least ${String(g.omitted_at_least ?? 0)} runs created since ${formatStamp(g.from)}`;
  }
}

function trimDot(s: string): string {
  return s.endsWith(".") ? s.slice(0, -1) : s;
}

/** internal/trigger's queueFullPrefix: a full queue leaves events at the forge, so the recipe row shows it. */
const QUEUE_FULL = "queue full: ";

/** An event rule's state segment, and whether it is an error the recipe row should surface. */
export function eventState(v: LaunchRuleView): { text: string; error: boolean } {
  const st = v.status;
  if (v.error !== undefined) {
    return { text: `blocked: ${trimDot(v.error)}. Edit the rule`, error: true };
  }
  if (!v.enabled) {
    return { text: "paused", error: false };
  }
  if (st?.blocked !== undefined) {
    return { text: `blocked: ${trimDot(st.blocked)}. Edit the rule`, error: true };
  }
  if (v.cursor?.blocked !== undefined) {
    return { text: v.cursor.blocked, error: true };
  }
  const now = st?.paused ?? st?.row_text;
  if (now !== undefined) {
    return { text: now, error: now.startsWith(QUEUE_FULL) };
  }
  if (v.cursor?.baselined !== true) {
    const stop = st?.baseline_stop;
    return {
      text: stop === undefined ? "taking a baseline" : `taking a baseline; ${stop}`,
      error: false,
    };
  }
  const at = st?.checked_at === undefined ? Number.NaN : Date.parse(st.checked_at);
  return {
    text: Number.isNaN(at) ? "watching" : `watching, checked ${formatClock(at)}`,
    error: false,
  };
}

function lastFired(v: LaunchRuleView): string {
  const st = v.status;
  const when = formatStamp(st?.last_run_at);
  if (when === "") {
    return "";
  }
  const ref = eventRef(st?.last_event ?? "");
  let seg = `last fired ${when}${ref === "" ? "" : ` (${ref})`}`;
  const reason =
    st?.last_reason === undefined || st.last_reason === "" ? "" : `: ${st.last_reason}`;
  switch (st?.last_status) {
    case "completed":
      seg += ", completed";
      break;
    case "failed":
      seg += `, failed${reason}`;
      break;
    case "unknown":
      seg += `, outcome unknown${reason}`;
      break;
    case "skipped":
      seg += `, skipped${reason}`;
      break;
    case "started":
    case undefined:
      break;
  }
  return seg;
}

interface LineContext {
  autoApprove: boolean;
  /** The recipe's declared input names; undefined while the recipe list has not answered. */
  declared: readonly string[] | undefined;
}

/** A rule's line: what starts the run, what last happened, then the state, middot-joined. */
export function ruleLine(v: LaunchRuleView, ctx: LineContext): string {
  return composeLine(v, ctx, false);
}

/** ruleLine for the recipe row, whose width cuts the line: an event rule's state leads. */
export function ruleRowLine(v: LaunchRuleView, ctx: LineContext): string {
  return composeLine(v, ctx, true);
}

function composeLine(v: LaunchRuleView, ctx: LineContext, stateFirst: boolean): string {
  const clock = v.when.clock;
  if (clock !== undefined) {
    const view = scheduleViewOf(v);
    return v.enabled ? summaryLine(view ?? undefined) : `${describeSpec(clock)} · paused`;
  }
  const ev = v.when.event;
  if (ev === undefined) {
    return "";
  }
  const head = [describeEvent(ev), lastFired(v)];
  const state = eventState(v).text;
  const segs = stateFirst ? [state, ...head] : [...head, state];
  segs.push(...(v.cursor?.notes ?? []), ...(v.status?.gaps ?? []).map(gapText));
  if (ctx.autoApprove) {
    segs.push("auto-approve on");
  }
  if (ctx.declared !== undefined && !ctx.declared.includes("event_id")) {
    segs.push("no event_id");
  }
  return segs.filter((s) => s !== "").join(" · ");
}

/** The first error a rule's line shows, or "" for none; the recipe row shows it over the rule. */
export function ruleError(v: LaunchRuleView): string {
  if (v.when.event === undefined) {
    return v.error === undefined ? "" : `blocked: ${trimDot(v.error)}. Edit the rule`;
  }
  const s = eventState(v);
  if (s.error) {
    return s.text;
  }
  const gap = v.status?.gaps?.[0];
  return gap === undefined ? "" : gapText(gap);
}

interface RuleListOptions {
  rules: readonly LaunchRuleView[];
  autoApprove: boolean;
  declared: readonly string[] | undefined;
  onToggle: (v: LaunchRuleView, enabled: boolean) => void;
  onEdit: (v: LaunchRuleView) => void;
  onDelete: (v: LaunchRuleView) => void;
  /** Resolves the rule as it reads once dismissed, or null when the dismiss failed. */
  onDismissGap: (v: LaunchRuleView) => Promise<LaunchRuleView | null>;
  onAddClock: () => void;
  onAddEvent: () => void;
}

/** The popup's first view: one line per rule with its controls, then "Add a rule". */
export function buildRuleList(opts: RuleListOptions): HTMLElement {
  const ctx = { autoApprove: opts.autoApprove, declared: opts.declared };
  const row = (v: LaunchRuleView): HTMLElement => {
    const line = el("div", { className: "rw-line" }, ruleLine(v, ctx));
    line.classList.toggle("entry-sub-error", ruleError(v) !== "");
    const on = el("input", {
      type: "checkbox",
      "aria-label": "Run on this rule",
    }) as HTMLInputElement;
    on.checked = v.enabled;
    on.addEventListener("change", () => {
      opts.onToggle(v, on.checked);
    });
    const button = (label: string, cls: string, fn: () => void): HTMLButtonElement => {
      const b = el(
        "button",
        { type: "button", className: `btn-small ${cls}` },
        label,
      ) as HTMLButtonElement;
      // A view swap detaches the button before the click reaches the popup's outside-click check.
      b.addEventListener("click", (e: MouseEvent) => {
        e.stopPropagation();
        fn();
      });
      return b;
    };
    const controls = el(
      "div",
      { className: "rw-controls" },
      el(
        "label",
        { className: "toggle toggle-inline" },
        on,
        el("span", { className: "toggle-slider" }),
      ),
      button("Edit", "rw-edit", () => {
        opts.onEdit(v);
      }),
      button("Delete", "btn-danger rw-delete", () => {
        opts.onDelete(v);
      }),
    );
    if ((v.status?.gaps ?? []).length > 0) {
      const dismiss = button("Dismiss gap", "rw-dismiss-gap", () => {
        dismiss.disabled = true;
        void opts.onDismissGap(v).then((next) => {
          if (next === null) {
            dismiss.disabled = false;
            return;
          }
          r.replaceWith(row(next));
        });
      });
      controls.appendChild(dismiss);
    }
    const r = el("div", { className: "rw-rule", "data-rule-id": v.id }, line, controls);
    return r;
  };
  const list = el("div", { className: "rw-rules" });
  if (opts.rules.length === 0) {
    list.appendChild(el("div", { className: "sched-hint" }, "No rules yet."));
  }
  for (const v of opts.rules) {
    list.appendChild(row(v));
  }
  const add = (label: string, fn: () => void): HTMLButtonElement => {
    const b = el("button", { type: "button", className: "btn-small" }, label) as HTMLButtonElement;
    b.addEventListener("click", (e: MouseEvent) => {
      e.stopPropagation();
      fn();
    });
    return b;
  };
  return el(
    "div",
    { className: "rw-popup" },
    list,
    el(
      "div",
      { className: "rw-add" },
      el("div", { className: "rw-add-title" }, "Add a rule"),
      add("On a schedule", opts.onAddClock),
      add("When something happens on a forge", opts.onAddEvent),
    ),
  );
}

/** What the event form saves: the rule's own fields beside the run-defaults picks. */
interface EventRuleDraft {
  enabled: boolean;
  event: EventSpec;
  inputs?: Record<string, string>;
}

interface EventFormOptions {
  /** The rule being edited; absent for a new one. */
  rule?: LaunchRuleView;
  /** The Sources tab's connections. */
  forges: readonly ConfiguredForge[];
  declared: Readonly<Record<string, string>> | undefined;
  autoApprove: boolean;
  picks: RunDefaultsPicker;
  /** Resolves null once saved, which the caller answers by closing the form, or the refusal's
   *  sentence, which the form shows while it stays open; "" keeps it open with none. */
  onSave: (draft: EventRuleDraft, defaults: RunDefaults) => Promise<string | null>;
  onClose: () => void;
  onOpenPermissions: () => void;
  onOpenSources: () => void;
}

function connLabel(f: ConfiguredForge): string {
  return `${f.host} · ${f.username ?? f.account}`;
}

/** Why a connection the Sources tab lists cannot back a rule, as a sentence. */
function lapsedText(f: ConfiguredForge): string {
  return f.status === "reconnect"
    ? `${connLabel(f)} needs a new sign-in. Reconnect it in Sources.`
    : `${connLabel(f)} cannot read the forge now. Sources says why.`;
}

function lapsedNote(f: ConfiguredForge): string {
  return f.status === "reconnect" ? "sign in again in Sources" : "see Sources";
}

function trustText(kind: EventKind): string {
  if (labeled(kind)) {
    return "Fires on any account that adds this label. On GitHub that takes Triage access, which includes bots and workflows you have given it. Keep this label out of your issue templates.";
  }
  return opened(kind)
    ? "Only these accounts. Remove someone here when they leave."
    : "Your repository's own CI.";
}

function windowText(kind: EventKind): string {
  return kind === "run_concluded"
    ? "A re-run is caught when its run started within 24 hours of the last check. A run is watched for up to 72 hours after it starts."
    : "Events on items closed while marotte was down are not caught.";
}

function workflowHint(kind: ForgeKind | undefined): string {
  return kind === "github"
    ? "The workflow file's path, e.g. .github/workflows/weekly.yaml. Empty means any."
    : "The workflow file's name, e.g. weekly.yaml. Empty means any.";
}

function defaultHold(kind: EventKind): Hold {
  return kind === "run_concluded" ? { policy: "newest", cap: 10 } : { policy: "fifo", cap: 20 };
}

function textField(cls: string, label: string, value: string, placeholder = ""): HTMLInputElement {
  return el("input", {
    type: "text",
    className: cls,
    value,
    placeholder,
    "aria-label": label,
  }) as HTMLInputElement;
}

function numberField(
  cls: string,
  label: string,
  value: number,
  min: number,
  max: number,
): HTMLInputElement {
  return el("input", {
    type: "number",
    className: `sched-num ${cls}`,
    min: String(min),
    max: String(max),
    value: String(value),
    "aria-label": label,
  }) as HTMLInputElement;
}

/** The lapsed connections' sentences and the way to Sources, or null when none is lapsed. */
function lapsedBlock(
  lapsed: readonly ConfiguredForge[],
  onOpenSources: () => void,
): HTMLElement | null {
  if (lapsed.length === 0) {
    return null;
  }
  return el(
    "div",
    { className: "rw-lapsed" },
    ...lapsed.map((f) => el("div", { className: "sched-hint" }, lapsedText(f))),
    sourcesButton(onOpenSources),
  );
}

function sourcesButton(onOpenSources: () => void): HTMLButtonElement {
  const b = el(
    "button",
    { type: "button", className: "btn-small rw-open-sources" },
    "Open Sources",
  ) as HTMLButtonElement;
  b.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    onOpenSources();
  });
  return b;
}

/** The event rule's form. The caller anchors it and owns dismissal. */
export function buildEventForm(opts: EventFormOptions): HTMLElement {
  const ev = opts.rule?.when.event;
  const conns = opts.forges.filter((f) => usableStatus(f.status));
  const lapsed = opts.forges.filter((f) => !usableStatus(f.status));
  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");
  cancel.addEventListener("click", opts.onClose);
  if (conns.length === 0 && ev === undefined) {
    return el(
      "div",
      { className: "sched-picker rw-form" },
      lapsedBlock(lapsed, opts.onOpenSources) ??
        el(
          "div",
          { className: "rw-lapsed" },
          el("div", { className: "sched-hint" }, "Connect a forge in Sources first"),
          sourcesButton(opts.onOpenSources),
        ),
      el("div", { className: "sched-actions" }, cancel),
    );
  }

  const connSel = el("select", {
    className: "rw-conn",
    "aria-label": "Connection",
  }) as HTMLSelectElement;
  for (const f of conns) {
    connSel.appendChild(el("option", { value: f.id }, connLabel(f)));
  }
  // Listed so the reader sees it, and off unless the rule being edited already names it.
  for (const f of lapsed) {
    const o = el(
      "option",
      { value: f.id },
      `${connLabel(f)} (${lapsedNote(f)})`,
    ) as HTMLOptionElement;
    o.disabled = f.id !== ev?.connection;
    connSel.appendChild(o);
  }
  if (ev !== undefined && !opts.forges.some((f) => f.id === ev.connection)) {
    connSel.appendChild(el("option", { value: ev.connection }, `${ev.connection} (signed out)`));
  }
  connSel.value = ev?.connection ?? conns[0]?.id ?? "";
  const forgeKind = (): ForgeKind | undefined =>
    opts.forges.find((f) => f.id === connSel.value)?.kind;

  const repo = textField("rw-repo", "Repository", ev?.repo ?? "", "owner/name");
  const kindSel = el("select", { className: "rw-kind", "aria-label": "When" }) as HTMLSelectElement;
  for (const [value, label] of KINDS) {
    kindSel.appendChild(el("option", { value }, label));
  }
  kindSel.value = ev?.kind ?? "run_concluded";
  const kind = (): EventKind => kindSel.value as EventKind;

  const f = ev?.filter ?? {};
  const label = textField("rw-label", "Label", f.label ?? "");
  const authors = el("textarea", {
    className: "rw-authors",
    rows: "3",
    "aria-label": "Author logins, one per line",
  }) as HTMLTextAreaElement;
  authors.value = (f.authors ?? []).join("\n");
  const workflow = textField("rw-workflow", "Workflow file", f.workflow ?? "");
  const branch = textField("rw-branch", "Branch", f.branch ?? "", "any branch");
  const results = RESULTS.map((r) => {
    const box = el("input", {
      type: "checkbox",
      className: "rw-result",
      value: r,
    }) as HTMLInputElement;
    box.checked = ev === undefined ? r === "success" : (f.result ?? []).includes(r);
    return box;
  });
  const outside = el("input", { type: "checkbox", className: "rw-run-prs" }) as HTMLInputElement;
  outside.checked = OUTSIDE_TRIGGERS.some((t) => (f.run_triggers ?? []).includes(t));

  const kindFields = el("div", { className: "rw-kind-fields" });
  const renderKindFields = (): void => {
    const k = kind();
    const row = (...nodes: (Node | string)[]): HTMLElement =>
      el("div", { className: "sched-row" }, ...nodes);
    const nodes: HTMLElement[] = [];
    if (labeled(k)) {
      nodes.push(row(el("span", { className: "sched-label" }, "Label"), label));
    }
    if (opened(k)) {
      nodes.push(
        row(
          el("span", { className: "sched-label" }, "Opened by"),
          authors,
          el("div", { className: "sched-hint" }, "One login per line, such as renovate[bot]"),
        ),
      );
    }
    if (k === "run_concluded") {
      nodes.push(
        forgeKind() === "gitlab"
          ? el(
              "div",
              { className: "sched-hint" },
              "GitLab runs one pipeline configuration per project.",
            )
          : row(
              el("span", { className: "sched-label" }, "Workflow"),
              workflow,
              el("div", { className: "sched-hint" }, workflowHint(forgeKind())),
            ),
        row(
          el("span", { className: "sched-label" }, "Result"),
          ...results.map((b) =>
            el("label", { className: "sched-enabled" }, b, el("span", {}, b.value)),
          ),
        ),
        el(
          "label",
          { className: "sched-enabled" },
          outside,
          el("span", {}, "Also runs started by pull requests or comments"),
        ),
        el(
          "div",
          { className: "sched-hint" },
          "Runs that an outside pull request or comment can start.",
        ),
      );
      if (forgeKind() === "codeberg") {
        nodes.push(
          el(
            "div",
            { className: "sched-hint" },
            "On Forgejo a pull request's run names the pull request's number as its branch, so a branch filter never matches it.",
          ),
        );
      }
    }
    if (k === "run_concluded" || onPR(k)) {
      nodes.push(row(el("span", { className: "sched-label" }, "Branch"), branch));
    }
    kindFields.replaceChildren(...nodes);
  };

  const interval = numberField(
    "rw-interval",
    "Check every N minutes",
    ev?.interval_min ?? 5,
    2,
    60,
  );
  const hold = ev?.hold ?? defaultHold(kind());
  const policy = el("select", {
    className: "rw-hold-policy",
    "aria-label": "Hold",
  }) as HTMLSelectElement;
  policy.append(
    el("option", { value: "newest" }, "Only the newest"),
    el("option", { value: "fifo" }, "Each one, in order"),
  );
  policy.value = hold.policy;
  const cap = numberField("rw-hold-cap", "How many to hold", hold.cap, 1, 50);
  let holdTouched = ev?.hold !== undefined;
  for (const c of [policy, cap]) {
    c.addEventListener("change", () => {
      holdTouched = true;
    });
  }
  const more = el(
    "details",
    { className: "rw-more" },
    el("summary", { className: "rw-more-summary" }, "More"),
    el(
      "div",
      { className: "sched-row" },
      el("span", { className: "sched-label" }, "While busy, hold"),
      policy,
    ),
    el(
      "div",
      { className: "sched-row" },
      el("span", { className: "sched-label" }, "At most"),
      cap,
      el("span", { className: "rw-unit" }, "events"),
    ),
  );

  const inputsSlot = el("div", {});
  let inputs: RuleInputs | null = null;
  const renderInputs = (): void => {
    inputs = buildRuleInputs({
      declared: opts.declared,
      setByEvent: eventInputsOf(kind()),
      initial: inputs === null ? opts.rule?.inputs : (inputs.value() ?? {}),
    });
    inputsSlot.replaceChildren(inputs.el);
  };

  const trust = el("div", { className: "sched-hint rw-trust" });
  const windowLine = el("div", { className: "sched-hint rw-window" });
  const paintKind = (): void => {
    renderKindFields();
    renderInputs();
    trust.textContent = trustText(kind());
    windowLine.textContent = windowText(kind());
    if (!holdTouched) {
      const d = defaultHold(kind());
      policy.value = d.policy;
      cap.value = String(d.cap);
    }
  };
  kindSel.addEventListener("change", paintKind);
  connSel.addEventListener("change", renderKindFields);

  const error = el("div", { className: "entry-sub-error rw-error hidden", role: "alert" });
  const draft = (): EventRuleDraft => {
    const k = kind();
    const filter: EventFilter = {};
    if (labeled(k)) {
      filter.label = label.value.trim();
    }
    if (opened(k)) {
      filter.authors = authors.value
        .split("\n")
        .map((a) => a.trim())
        .filter((a) => a !== "");
    }
    if (k === "run_concluded") {
      if (workflow.value.trim() !== "" && forgeKind() !== "gitlab") {
        filter.workflow = workflow.value.trim();
      }
      filter.result = results.filter((b) => b.checked).map((b) => b.value as Result);
      const kept = (f.run_triggers ?? []).filter(
        (t) => !(OUTSIDE_TRIGGERS as readonly string[]).includes(t),
      );
      if (outside.checked) {
        filter.run_triggers = [...(kept.length === 0 ? OWN_TRIGGERS : kept), ...OUTSIDE_TRIGGERS];
      } else if (f.run_triggers !== undefined) {
        filter.run_triggers = kept;
      }
    }
    if ((k === "run_concluded" || onPR(k)) && branch.value.trim() !== "") {
      filter.branch = branch.value.trim();
    }
    const event: EventSpec = {
      connection: connSel.value,
      repo: repo.value.trim(),
      kind: k,
      filter,
      interval_min: Number(interval.value),
    };
    if (holdTouched) {
      event.hold = { policy: policy.value as Hold["policy"], cap: Number(cap.value) };
    }
    const values = inputs?.value();
    return {
      enabled: opts.rule?.enabled ?? true,
      event,
      ...(values === undefined ? {} : { inputs: values }),
    };
  };

  const save = el(
    "button",
    { type: "button", className: "btn-small btn-primary rw-save" },
    "Save",
  ) as HTMLButtonElement;
  save.addEventListener("click", () => {
    save.disabled = true;
    void opts.onSave(draft(), opts.picks.value()).then((refusal) => {
      save.disabled = false;
      error.textContent = refusal ?? "";
      error.classList.toggle("hidden", refusal === null || refusal === "");
    });
  });

  paintKind();
  return el(
    "div",
    { className: "sched-picker rw-form" },
    el(
      "div",
      { className: "sched-row" },
      el("span", { className: "sched-label" }, "Connection"),
      connSel,
    ),
    lapsedBlock(lapsed, opts.onOpenSources),
    el(
      "div",
      { className: "sched-row" },
      el("span", { className: "sched-label" }, "Repository"),
      repo,
    ),
    el(
      "div",
      { className: "sched-row" },
      el("span", { className: "sched-label" }, "When"),
      kindSel,
    ),
    kindFields,
    trust,
    windowLine,
    el(
      "div",
      { className: "sched-row" },
      el("span", { className: "sched-label" }, "Check every"),
      interval,
      el("span", { className: "rw-unit" }, "minutes"),
    ),
    more,
    inputsSlot,
    el("div", { className: "sched-row" }, opts.picks.el),
    buildUnattendedNote(opts.autoApprove, opts.onOpenPermissions, true),
    error,
    el("div", { className: "sched-actions" }, save, cancel),
  );
}
