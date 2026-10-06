import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { applyLoginOptions } from "./modals.js";

let host: HTMLDivElement;

beforeEach(() => {
  host = document.createElement("div");
  host.innerHTML =
    '<button id="modal-login-free"></button>' +
    '<button id="modal-login-sso"></button>' +
    '<div id="modal-sso-form" class="hidden">' +
    '<input id="modal-provider"><input id="modal-region"></div>';
  document.body.appendChild(host);
});

afterEach(() => {
  host.remove();
});

function hidden(id: string): boolean {
  return document.getElementById(id)?.classList.contains("hidden") ?? false;
}

function value(id: string): string {
  return (document.getElementById(id) as HTMLInputElement).value;
}

describe("applyLoginOptions", () => {
  it("hides a sign-in door the administrator denies", () => {
    applyLoginOptions({ builder_id: false, idc: true });
    expect(hidden("modal-login-free")).toBe(true);
    expect(hidden("modal-login-sso")).toBe(false);
  });

  it("opens the organization form at once when it is the only door", () => {
    applyLoginOptions({ builder_id: false, idc: true });
    expect(hidden("modal-sso-form")).toBe(false);
  });

  it("pre-fills the organization's start URL and region", () => {
    applyLoginOptions({
      builder_id: true,
      idc: true,
      idc_start_url: "https://example.awsapps.com/start",
      idc_region: "eu-west-1",
    });
    expect(value("modal-provider")).toBe("https://example.awsapps.com/start");
    expect(value("modal-region")).toBe("eu-west-1");
    expect(hidden("modal-sso-form")).toBe(true);
  });

  it("keeps what the reader already typed", () => {
    (document.getElementById("modal-provider") as HTMLInputElement).value =
      "https://mine.example/start";
    applyLoginOptions({
      builder_id: true,
      idc: true,
      idc_start_url: "https://example.awsapps.com/start",
    });
    expect(value("modal-provider")).toBe("https://mine.example/start");
  });
});
