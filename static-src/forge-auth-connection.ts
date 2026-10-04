// ---------------------------------------------------------------------------
// Where a new connection points: the server host and the per-connection
// options, shared by the device sign-in and the token form in one add pane.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import type { ConnectionOptions } from "./actions/forge.js";
import { DEFAULT_HOST, HOST_LOCKED_KINDS } from "./forge-types.js";
import type { ForgeKind } from "./wire/types.gen.js";

export interface ConnectionTarget {
  readonly el: HTMLElement;
  readonly hostInput: HTMLInputElement;
  /** The host the connection id names, trimmed. */
  readonly host: () => string;
  /** The options the user set; an unset one is absent. */
  readonly options: () => ConnectionOptions;
}

/** `kind` is null for a server whose family is not known yet. */
export function renderConnectionTarget(kind: ForgeKind | null): ConnectionTarget {
  const hostInput = el("input", {
    className: "tool-form-input",
    "data-forge-host": "",
    autocomplete: "off",
  }) as HTMLInputElement;
  const defaultHost = kind === null ? "" : DEFAULT_HOST[kind];
  hostInput.value = defaultHost;
  const block = el("div", { className: "forge-add-pane-section" });
  if (kind !== null && HOST_LOCKED_KINDS.includes(kind)) {
    hostInput.type = "hidden";
    block.appendChild(hostInput);
  } else {
    hostInput.type = "text";
    hostInput.placeholder = defaultHost || "your-host.example.com";
    block.appendChild(el("label", { className: "tool-form-label" }, "Server", hostInput));
  }

  const proxy = textInput("proxy", "http://proxy.example.com:3128");
  const privateAddresses = checkbox("private_addresses");
  const plaintext = checkbox("plaintext_http");
  const caPEM = pemInput("ca_pem");
  const certPEM = pemInput("client_cert_pem");
  const keyPEM = pemInput("client_key_pem");

  const chevron = chevronEl();
  chevron.classList.add("forge-options-chevron");
  const details = el(
    "details",
    { className: "forge-options" },
    el("summary", { className: "forge-options-summary" }, chevron, "Connection options"),
    el(
      "div",
      { className: "forge-options-body" },
      toggleRow(
        privateAddresses,
        "Allow private addresses",
        "Lets this connection reach a forge on a private or loopback address. Leave it off unless the forge is on your own network.",
      ),
      fieldRow(
        proxy,
        "Proxy URL",
        "Requests go through this proxy, so the address check applies to the proxy, not to the forge behind it.",
      ),
      toggleRow(
        plaintext,
        "Use plain HTTP",
        "Connects with http:// instead of https://, so the token travels unencrypted. Use it only for a forge on this machine or a network you trust.",
      ),
      fieldRow(
        caPEM,
        "CA certificate (PEM)",
        "Trusts this certificate authority for the forge's TLS certificate.",
      ),
      fieldRow(
        certPEM,
        "Client certificate (PEM)",
        "For a forge that asks for a client certificate.",
      ),
      fieldRow(keyPEM, "Client key (PEM)", "The private key of that client certificate."),
    ),
  );
  block.appendChild(details);

  const host = (): string => hostInput.value.trim();
  return {
    el: block,
    hostInput,
    host,
    options: () => {
      const o: ConnectionOptions = {};
      if (plaintext.checked) {
        o.web_base_url = `http://${host()}`;
        o.plaintext_http = true;
      }
      if (proxy.value.trim() !== "") {
        o.proxy = proxy.value.trim();
      }
      if (privateAddresses.checked) {
        o.private_addresses = true;
      }
      if (caPEM.value.trim() !== "") {
        o.ca_pem = caPEM.value.trim();
      }
      if (certPEM.value.trim() !== "") {
        o.client_cert_pem = certPEM.value.trim();
      }
      if (keyPEM.value.trim() !== "") {
        o.client_key_pem = keyPEM.value.trim();
      }
      return o;
    },
  };
}

function textInput(name: string, placeholder: string): HTMLInputElement {
  return el("input", {
    type: "text",
    name,
    placeholder,
    className: "tool-form-input",
    autocomplete: "off",
  }) as HTMLInputElement;
}

function checkbox(name: string): HTMLInputElement {
  return el("input", { type: "checkbox", name }) as HTMLInputElement;
}

function pemInput(name: string): HTMLTextAreaElement {
  return el("textarea", {
    name,
    rows: 3,
    className: "tool-form-input forge-option-pem",
    autocomplete: "off",
  }) as HTMLTextAreaElement;
}

function fieldRow(input: HTMLElement, label: string, hint: string): HTMLElement {
  return el(
    "label",
    { className: "tool-form-label forge-option" },
    label,
    input,
    el("span", { className: "tool-form-hint" }, hint),
  );
}

function toggleRow(input: HTMLInputElement, label: string, hint: string): HTMLElement {
  return el(
    "div",
    { className: "forge-option" },
    el(
      "label",
      { className: "toggle-inline" },
      el("span", { className: "toggle" }, input, el("span", { className: "toggle-slider" })),
      el("span", { className: "section-option-label" }, label),
    ),
    el("p", { className: "tool-form-hint" }, hint),
  );
}
