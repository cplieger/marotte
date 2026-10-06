// Chromium warns about a password field outside a <form>, so `#mcp-remote-headers` (runtime password fields) needs a
// form with a real submit. Read from the markup: mcp-panels.ts resolves fields by id and would work inside a div.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

function parsed(): Document {
  return new DOMParser().parseFromString(indexHtml, "text/html");
}

function remoteForm(doc: Document): HTMLFormElement | null | undefined {
  return doc.querySelector<HTMLElement>("#mcp-remote-headers")?.closest("form");
}

describe("credential inputs in static/index.html", () => {
  it("puts every password field inside a form", () => {
    const doc = parsed();
    for (const field of doc.querySelectorAll<HTMLInputElement>('input[type="password"]')) {
      expect(field.closest("form"), `${field.id} is not in a form`).not.toBeNull();
    }
    expect(remoteForm(doc), "#mcp-remote-headers is not in a form").not.toBeNull();
  });

  it("keeps the server's credentials out of the browser's password manager", () => {
    // A server's credentials, not the reader's login, so the form asks not to be remembered.
    expect(remoteForm(parsed())?.getAttribute("autocomplete")).toBe("off");
  });

  it("gives the credential form a real submit, so Enter reaches Save", () => {
    // A form whose only button is `type="button"` has no submit, so Enter in any field does nothing.
    expect(remoteForm(parsed())?.querySelector('button[type="submit"]')?.id).toBe(
      "mcp-remote-save",
    );
  });

  it("leaves validation to the panel, which renders its own field errors", () => {
    // The URL field is `type="url"`: without novalidate a native bubble fires beside the server's inline marking.
    expect(remoteForm(parsed())?.hasAttribute("novalidate")).toBe(true);
  });
});
