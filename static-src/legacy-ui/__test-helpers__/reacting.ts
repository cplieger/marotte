// Elements that run caller code from their own reactions, for the named
// re-entry regressions: a custom element's attribute, connected and focus
// callbacks run synchronously inside the library's write.

type Reaction = (name: string, value: string | null) => void;

const WATCHED = ["class", "style", "tabindex", "aria-expanded", "hidden", "open"];

class ReactingElement extends HTMLElement {
  static readonly observedAttributes = WATCHED;
  onAttr: Reaction | null = null;
  onConnect: (() => void) | null = null;
  attributeChangedCallback(name: string, _old: string | null, value: string | null): void {
    this.onAttr?.(name, value);
  }
  connectedCallback(): void {
    this.onConnect?.();
  }
}

class ReactingDialog extends HTMLDialogElement {
  static readonly observedAttributes = WATCHED;
  onAttr: Reaction | null = null;
  attributeChangedCallback(name: string, _old: string | null, value: string | null): void {
    this.onAttr?.(name, value);
  }
}

class ReactingBody extends HTMLBodyElement {
  static readonly observedAttributes = WATCHED;
  onAttr: Reaction | null = null;
  attributeChangedCallback(name: string, _old: string | null, value: string | null): void {
    this.onAttr?.(name, value);
  }
}

function define(): void {
  if (customElements.get("uip-test-reacting") === undefined) {
    customElements.define("uip-test-reacting", ReactingElement);
    customElements.define("uip-test-reacting-dialog", ReactingDialog, { extends: "dialog" });
    customElements.define("uip-test-reacting-body", ReactingBody, { extends: "body" });
  }
}

/** A custom element whose `onAttr` runs inside every watched attribute write. */
export function reactingElement(): ReactingElement {
  define();
  return document.createElement("uip-test-reacting") as ReactingElement;
}

/** A customized `<dialog>` whose `onAttr` runs inside every watched attribute write. */
export function reactingDialog(): ReactingDialog {
  define();
  return document.createElement("dialog", { is: "uip-test-reacting-dialog" }) as ReactingDialog;
}

/** Whether a `class` attribute value carries `token`. */
export function classHas(value: string | null, token: string): boolean {
  return value?.split(/\s+/).includes(token) === true;
}

/** Swap a customized `<body>` in for the document's; returns it and the restore. */
export function reactingBody(): { body: ReactingBody; restore: () => void } {
  define();
  const original = document.body;
  const body = document.createElement("body", { is: "uip-test-reacting-body" }) as ReactingBody;
  document.documentElement.replaceChild(body, original);
  return {
    body,
    restore: () => {
      document.documentElement.replaceChild(original, body);
    },
  };
}
