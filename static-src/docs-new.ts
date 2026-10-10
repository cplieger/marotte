import { el } from "@cplieger/reactive";
import { createPopover, type PopoverController } from "@cplieger/ui-primitives/popover";
import { createDoc, type NewDocRequest } from "./actions/docs.js";
import { iconEl } from "./icon-el.js";
import { ICON_PLUS_UI } from "./icons.js";
import type { DocsTab } from "./route-path.js";

type NewDocTab = "steering" | "skills" | "prompts" | "agents" | "specs" | "hooks";

const NEW_LABEL: Readonly<Record<NewDocTab, string>> = {
  steering: "New steering doc",
  skills: "New skill",
  prompts: "New prompt",
  agents: "New agent",
  specs: "New spec",
  hooks: "New hook",
};

function isCreatable(tab: DocsTab): tab is NewDocTab {
  return Object.hasOwn(NEW_LABEL, tab);
}

interface NewDocForm {
  readonly form: HTMLFormElement;
  readonly focus: HTMLElement;
}

/**
 * Mount the button into `slot`. The returned function follows the active tab: the button is
 * present only on a creatable one, and a tab switch closes its form. `onCreated` gets the new
 * file's path in the inventory rows' spelling; `startSpec` gets the spec chat's prompt and the
 * message shown for it.
 */
export function mountNewDocButton(
  slot: HTMLElement,
  onCreated: (path: string) => void,
  startSpec: (prompt: string, shown: string) => void,
): (tab: DocsTab) => void {
  const btn = el(
    "button",
    { type: "button", className: "icon-btn docs-new-btn", "aria-haspopup": "dialog" },
    iconEl(ICON_PLUS_UI),
  ) as HTMLButtonElement;
  let tab: NewDocTab | null = null;
  let open: PopoverController | null = null;

  const close = (): void => {
    open?.hide();
  };

  btn.addEventListener("click", () => {
    if (open !== null) {
      close();
      return;
    }
    if (tab === null) {
      return;
    }
    const panel = el("div", { className: "docs-new-popover", role: "dialog" });
    panel.setAttribute("aria-label", NEW_LABEL[tab]);
    const built = buildForm(tab, { close, onCreated, startSpec });
    panel.append(el("h2", { className: "docs-new-title" }, NEW_LABEL[tab]), built.form);
    const ctl = createPopover(btn, panel, {
      placement: "bottom",
      align: "end",
      offset: 4,
      margin: 8,
      haspopup: "dialog",
      initialFocus: built.focus,
      returnFocus: btn,
      onClose: () => {
        // Rebuilt per open, so a later open never shows the last attempt's values or error.
        ctl.dispose();
        panel.remove();
        open = null;
      },
    });
    open = ctl;
    ctl.show();
  });

  return (next: DocsTab): void => {
    close();
    tab = isCreatable(next) ? next : null;
    if (tab === null) {
      btn.remove();
      return;
    }
    btn.setAttribute("aria-label", NEW_LABEL[tab]);
    btn.setAttribute("data-tooltip", NEW_LABEL[tab]);
    if (!btn.isConnected) {
      slot.append(btn);
    }
  };
}

interface FormHooks {
  readonly close: () => void;
  readonly onCreated: (path: string) => void;
  readonly startSpec: (prompt: string, shown: string) => void;
}

function buildForm(tab: NewDocTab, hooks: FormHooks): NewDocForm {
  switch (tab) {
    case "steering":
      return steeringForm(hooks);
    case "skills":
      return skillForm(hooks);
    case "prompts":
      return nameOnlyForm(
        hooks,
        "prompt",
        "Letters, numbers, hyphens, and underscores (1-50 characters).",
        50,
      );
    case "agents":
      return nameOnlyForm(hooks, "agent", "Created as a JSON agent in .kiro/agents.");
    case "specs":
      return specForm(hooks);
    case "hooks":
      return hookForm(hooks);
    default: {
      const exhaustive: never = tab;
      return exhaustive;
    }
  }
}

function input(attrs: Record<string, string | boolean>): HTMLInputElement {
  return el("input", {
    className: "tool-form-input",
    autocomplete: "off",
    ...attrs,
  }) as HTMLInputElement;
}

function textarea(attrs: Record<string, string | boolean>): HTMLTextAreaElement {
  return el("textarea", {
    className: "tool-form-input docs-new-text",
    rows: "3",
    ...attrs,
  }) as HTMLTextAreaElement;
}

function select(options: readonly (readonly [string, string])[], value: string): HTMLSelectElement {
  const s = el(
    "select",
    { className: "tool-form-select" },
    ...options.map(([v, label]) => el("option", { value: v }, label)),
  ) as HTMLSelectElement;
  s.value = value;
  return s;
}

function field(label: string, control: HTMLElement, hint?: HTMLElement | string): HTMLLabelElement {
  const hintEl =
    typeof hint === "string" ? el("span", { className: "tool-form-hint" }, hint) : hint;
  return el(
    "label",
    { className: "tool-form-label" },
    label,
    control,
    hintEl ?? null,
  ) as HTMLLabelElement;
}

/** `submit` returns the error to show, or null when the form is done. */
function formShell(
  fields: readonly HTMLElement[],
  label: string,
  submit: () => Promise<string | null>,
): { form: HTMLFormElement; button: HTMLButtonElement } {
  const error = el("p", { className: "docs-new-error", role: "alert" });
  const button = el(
    "button",
    { type: "submit", className: "btn-small primary" },
    label,
  ) as HTMLButtonElement;
  const form = el(
    "form",
    { className: "docs-new-form" },
    ...fields,
    error,
    button,
  ) as HTMLFormElement;
  let busy = false;
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    // A disabled submitter does not stop requestSubmit(), so the flag is the guard.
    if (busy) {
      return;
    }
    busy = true;
    button.disabled = true;
    error.textContent = "";
    void submit().then((message) => {
      busy = false;
      button.disabled = false;
      if (message !== null) {
        error.textContent = message;
      }
    });
  });
  return { form, button };
}

async function create(req: NewDocRequest, hooks: FormHooks): Promise<string | null> {
  const out = await createDoc.dispatch(req).outcome;
  if (out.status === "success") {
    hooks.close();
    hooks.onCreated(out.value.path);
    return null;
  }
  return out.status === "error" ? out.error.message : "The create was cancelled before it ran.";
}

const STEERING_KINDS = [
  ["always", "Agent steering"],
  ["manual", "Manual steering (slash command)"],
] as const;

const STEERING_HINT: Readonly<Record<"always" | "manual", string>> = {
  always: "Applies only within this specific workspace.",
  manual: "Author a steering file invoked on demand as /<filename>.",
};

function steeringForm(hooks: FormHooks): NewDocForm {
  const kind = select(STEERING_KINDS, "always");
  const kindHint = el("span", { className: "tool-form-hint" }, STEERING_HINT.always);
  kind.addEventListener("change", () => {
    kindHint.textContent = STEERING_HINT[kind.value === "manual" ? "manual" : "always"];
  });
  const name = input({ required: true, placeholder: "Enter the name for the steering document" });
  const { form } = formShell([field("Type", kind, kindHint), field("Name", name)], "Create", () =>
    create(
      {
        category: "steering",
        name: name.value,
        inclusion: kind.value === "manual" ? "manual" : "always",
      },
      hooks,
    ),
  );
  return { form, focus: name };
}

function skillForm(hooks: FormHooks): NewDocForm {
  const name = input({ required: true, maxlength: "64", placeholder: "pr-review" });
  const description = input({ required: true, maxlength: "1024" });
  const { form } = formShell(
    [
      field("Name", name, "Lowercase letters, numbers, and hyphens only (max 64 characters)."),
      field(
        "Description",
        description,
        "When to use this skill. Kiro matches this against your requests.",
      ),
    ],
    "Create",
    () => create({ category: "skill", name: name.value, description: description.value }, hooks),
  );
  return { form, focus: name };
}

function nameOnlyForm(
  hooks: FormHooks,
  category: "prompt" | "agent",
  hint: string,
  maxLength?: number,
): NewDocForm {
  const name = input(
    maxLength === undefined ? { required: true } : { required: true, maxlength: String(maxLength) },
  );
  const { form } = formShell([field("Name", name, hint)], "Create", () =>
    create({ category, name: name.value }, hooks),
  );
  return { form, focus: name };
}

/** The IDE's spec-name rule. */
const SPEC_NAME = /^[a-zA-Z0-9_-]+$/;

/** kiro-cli's `/spec new` prompt, verbatim. */
function newSpecPrompt(name: string, description: string): string {
  return (
    `Start a new spec called "${name}". Create the .kiro/specs/${name}/ directory and draft the initial requirements document.\n\n` +
    `The user described what this spec should cover \u2014 treat this as the ground truth for the requirements:\n${description}`
  );
}

function specForm(hooks: FormHooks): NewDocForm {
  const name = input({ required: true, placeholder: "feature-name" });
  const description = textarea({
    required: true,
    placeholder: "Enter your idea to generate requirement, design, and task specs.",
  });
  const { form } = formShell(
    [
      field("Name", name, "Letters, numbers, hyphens, and underscores."),
      field("Description", description, "Opens a spec chat that drafts the requirements first."),
    ],
    "Start spec",
    () => {
      const n = name.value.trim();
      const d = description.value.trim();
      if (!SPEC_NAME.test(n)) {
        return Promise.resolve("Name can only contain letters, numbers, hyphens, and underscores.");
      }
      if (d === "") {
        return Promise.resolve("Describe the spec in words first.");
      }
      hooks.close();
      hooks.startSpec(newSpecPrompt(n, d), d);
      return Promise.resolve(null);
    },
  );
  return { form, focus: name };
}

const HOOK_TRIGGER_GROUPS = [
  [
    "File Hooks",
    [
      ["PostFileCreate", "File Created"],
      ["PostFileSave", "File Saved"],
      ["PostFileDelete", "File Deleted"],
    ],
  ],
  [
    "Tool Hooks",
    [
      ["PreToolUse", "Pre Tool Use"],
      ["PostToolUse", "Post Tool Use"],
    ],
  ],
  [
    "Prompt & Lifecycle Hooks",
    [
      ["UserPromptSubmit", "Prompt Submit"],
      ["SessionStart", "Session Start"],
      ["Stop", "Agent Stop"],
    ],
  ],
  [
    "Task Hooks",
    [
      ["PreTaskExec", "Pre Task Execution"],
      ["PostTaskExec", "Post Task Execution"],
    ],
  ],
] as const;

interface MatcherCopy {
  readonly label: string;
  readonly placeholder: string;
  readonly help: string;
}

const TOOL_MATCHER: MatcherCopy = {
  label: "Tool name pattern",
  placeholder: "e.g. fs_write|str_replace",
  help: "Regular expression matched against the tool name. Leave blank to match every tool.",
};

const FILE_MATCHER: MatcherCopy = {
  label: "File path pattern",
  placeholder: "e.g. \\.tsx?$",
  help: "Regular expression matched against the file path. Leave blank to match every file.",
};

function matcherCopy(trigger: string): MatcherCopy | null {
  if (trigger === "PreToolUse" || trigger === "PostToolUse") {
    return TOOL_MATCHER;
  }
  if (trigger === "PostFileCreate" || trigger === "PostFileSave" || trigger === "PostFileDelete") {
    return FILE_MATCHER;
  }
  return null;
}

const ACTION_COPY = {
  command: {
    label: "Command to execute",
    help: "Executes a shell command. The hook event payload is provided as JSON on stdin.",
  },
  agent: {
    label: "Instructions for Kiro agent",
    help: "Appends instructions to the agent prompt when the hook fires.",
  },
} as const;

function hookForm(hooks: FormHooks): NewDocForm {
  const title = input({ required: true, placeholder: "Title" });
  const description = textarea({ placeholder: "Description" });
  const trigger = el(
    "select",
    { className: "tool-form-select" },
    ...HOOK_TRIGGER_GROUPS.map(([group, options]) =>
      el(
        "optgroup",
        { label: group },
        ...options.map(([v, label]) => el("option", { value: v }, label)),
      ),
    ),
  ) as HTMLSelectElement;
  trigger.value = "PostFileSave";
  const matcher = input({});
  const matcherLabel = el("span", {});
  const matcherHint = el("span", { className: "tool-form-hint" });
  const matcherField = el(
    "label",
    { className: "tool-form-label" },
    matcherLabel,
    matcher,
    matcherHint,
  );
  const action = select(
    [
      ["command", "Run Command"],
      ["agent", "Ask Kiro"],
    ],
    "command",
  );
  const actionHint = el("span", { className: "tool-form-hint" });
  const content = textarea({ required: true });
  const contentLabel = el("span", {});
  const contentField = el("label", { className: "tool-form-label" }, contentLabel, content);
  const timeout = input({
    type: "number",
    min: "0",
    step: "1",
    placeholder: "60",
    inputmode: "numeric",
  });
  const timeoutField = field(
    "Timeout (seconds)",
    timeout,
    "Maximum execution time for the command. Default is 60s. Set to 0 to disable.",
  );

  const actionField = field("Action", action, actionHint);
  const fields = [
    field("Title", title),
    field("Description", description),
    field("Trigger", trigger),
    matcherField,
    actionField,
    contentField,
    timeoutField,
  ];

  const { form, button } = formShell(fields, "Create Hook", () => {
    const pattern = matcherCopy(trigger.value) === null ? "" : matcher.value.trim();
    const isCommand = action.value === "command";
    const req: NewDocRequest = {
      category: "hook",
      name: title.value,
      description: description.value,
      trigger: trigger.value,
      action: isCommand ? "command" : "agent",
      content: content.value,
    };
    if (pattern !== "") {
      req.matcher = pattern;
    }
    if (isCommand && timeout.value !== "") {
      req.timeout = Number(timeout.value);
    }
    return create(req, hooks);
  });

  const paint = (): void => {
    const copy = matcherCopy(trigger.value);
    if (copy === null) {
      matcherField.remove();
    } else {
      matcherLabel.textContent = copy.label;
      matcher.placeholder = copy.placeholder;
      matcherHint.textContent = copy.help;
      if (!matcherField.isConnected) {
        actionField.before(matcherField);
      }
    }
    const isCommand = action.value === "command";
    const act = ACTION_COPY[isCommand ? "command" : "agent"];
    actionHint.textContent = act.help;
    contentLabel.textContent = act.label;
    content.placeholder = isCommand ? "Command to execute" : "Instructions";
    if (isCommand && !timeoutField.isConnected) {
      contentField.after(timeoutField);
    } else if (!isCommand) {
      timeoutField.remove();
    }
  };
  const gate = (): void => {
    button.disabled = title.value.trim() === "";
  };
  trigger.addEventListener("change", paint);
  action.addEventListener("change", paint);
  title.addEventListener("input", gate);
  paint();
  gate();
  return { form, focus: title };
}
