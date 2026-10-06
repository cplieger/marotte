// KAS treats `agent://<name>` as a one-step recipe, so an agent launched here gets
// its own run tab like any other workflow run.

import { el } from "@cplieger/reactive";
import { createPopup } from "@cplieger/ui-primitives/popup";
import { launchRun } from "./actions/runs.js";
import { iconEl } from "./icon-el.js";
import { ICON_PLAY } from "./icons.js";

const AGENT_NAME = /^[A-Za-z0-9._-]+$/;

type Popup = ReturnType<typeof createPopup>;

/** A Run button for the named agent, or null when KAS could not address it. */
export function agentRunButton(name: string): HTMLButtonElement | null {
  if (!AGENT_NAME.test(name)) {
    return null;
  }
  const btn = el(
    "button",
    {
      type: "button",
      className: "icon-btn docs-agent-run",
      "aria-label": `Run ${name}`,
      "aria-haspopup": "dialog",
      "data-tooltip": "Run",
    },
    iconEl(ICON_PLAY),
  ) as HTMLButtonElement;
  let popup: Popup | null = null;
  btn.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    popup ??= buildPopup(name, btn);
    popup.toggle();
  });
  return btn;
}

function buildPopup(name: string, btn: HTMLButtonElement): Popup {
  const panel = promptForm(name, () => {
    popup.hide();
  });
  const popup = createPopup(panel, {
    trigger: btn,
    group: "agent-run",
    haspopup: "dialog",
    initialFocus: panel.querySelector("textarea"),
    // Safari does not focus a clicked button, so capturing the active element at
    // open would restore focus to the page rather than to the trigger.
    returnFocus: btn,
    onClose: () => {
      // The Agents tab rebuilds its rows, and a panel whose button is gone would
      // otherwise stay parked in the page.
      if (!btn.isConnected) {
        popup.dispose();
        panel.remove();
      }
    },
  });
  return popup;
}

/** A blank prompt is refused here because KAS refuses it too, and the refusal
 *  would otherwise cost a round trip. */
function promptForm(name: string, close: () => void): HTMLElement {
  const field = el("textarea", {
    className: "tool-form-input agent-run-prompt",
    rows: "4",
    required: true,
    placeholder: "What should the agent do...",
    "aria-label": `Prompt for ${name}`,
  }) as HTMLTextAreaElement;
  const form = el(
    "form",
    { className: "agent-run-form" },
    field,
    el("button", { type: "submit", className: "btn-small primary" }, "Launch"),
  );
  field.addEventListener("input", () => {
    field.setCustomValidity("");
  });
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    const prompt = field.value.trim();
    if (prompt === "") {
      field.setCustomValidity("Write a prompt first.");
      field.reportValidity();
      return;
    }
    field.value = "";
    close();
    void launchRun.dispatch(
      { source: `agent://${name}`, inputs: { prompt } },
      {
        onSuccess: (d) => {
          // Lazy: the run view's graph is the run store and the exec page, which the
          // configuration browser that hosts this button has no other use for.
          void import("./run-view.js").then(({ openRunView }) =>
            openRunView(d.workflow_id, d.name),
          );
        },
      },
    );
  });
  return el("div", { className: "sched-popup", role: "dialog", "aria-label": `Run ${name}` }, form);
}
