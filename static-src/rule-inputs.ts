// The inputs block of the launch-rule forms: one field per declared input, and for an event rule
// the inputs its event sets, listed read-only.

import { el } from "@cplieger/ui";

export interface RuleInputs {
  readonly el: HTMLElement;
  /** The typed values, empty ones dropped; undefined when none is set. */
  value(): Record<string, string> | undefined;
}

interface RuleInputsOptions {
  /** The recipe's declared inputs, name to placeholder. */
  declared: Readonly<Record<string, string>> | undefined;
  /** The names the rule's kind sets, or null for a clock rule, which sets none. */
  setByEvent: readonly string[] | null;
  initial: Readonly<Record<string, string>> | undefined;
}

export function buildRuleInputs(opts: RuleInputsOptions): RuleInputs {
  const names = Object.keys(opts.declared ?? {});
  const setBy = new Set(opts.setByEvent ?? []);
  const fields = new Map<string, HTMLInputElement>();
  const block = el("div", { className: "rw-inputs" });
  for (const name of names.filter((n) => !setBy.has(n))) {
    const input = el("input", {
      type: "text",
      className: "recipe-input",
      placeholder: opts.declared?.[name] ?? "string",
      value: opts.initial?.[name] ?? "",
      "aria-label": `Input ${name}`,
    }) as HTMLInputElement;
    fields.set(name, input);
    block.appendChild(el("label", { className: "recipe-input-label" }, `${name}: `, input));
  }
  if (opts.setByEvent !== null) {
    const set = names.filter((n) => setBy.has(n));
    if (set.length > 0) {
      block.appendChild(
        el("div", { className: "rw-event-inputs" }, `Set by the event: ${set.join(", ")}`),
      );
    }
    if (!names.includes("event_id")) {
      block.appendChild(
        el(
          "div",
          { className: "rw-no-event-id" },
          "This workflow does not declare event_id, so it cannot skip a repeated event",
        ),
      );
    }
    block.appendChild(
      el(
        "div",
        { className: "sched-hint rw-idempotent" },
        "A run can start twice for one event after a restart. Declare event_id, record it only once the work it must not repeat is done, and end a run that finds it recorded.",
      ),
    );
  }
  return {
    el: block,
    value: () => {
      // Until the recipe list answers, a stored value has no field and is kept; once it has, a
      // name the recipe dropped goes, since a save naming an undeclared input is refused.
      const out: Record<string, string> =
        opts.declared === undefined
          ? Object.fromEntries(Object.entries(opts.initial ?? {}).filter(([n]) => !setBy.has(n)))
          : {};
      for (const [name, f] of fields) {
        if (f.value !== "") {
          out[name] = f.value;
        }
      }
      return Object.keys(out).length === 0 ? undefined : out;
    },
  };
}
