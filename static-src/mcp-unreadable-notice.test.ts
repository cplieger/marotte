import { afterEach, describe, expect, it } from "vitest";
import { bindUnreadableNotice } from "./mcp-ui.js";
import { unreadableEntries } from "./mcp-state.js";

describe("the unreadable-record notice", () => {
  afterEach(() => {
    unreadableEntries.value = 0;
  });

  it("is hidden while every entry reads, and counts the ones that do not", () => {
    const host = document.createElement("p");
    const stop = bindUnreadableNotice(host);
    expect(host.hidden).toBe(true);

    unreadableEntries.value = 1;
    expect(host.hidden).toBe(false);
    expect(host.textContent).toMatch(/^1 saved integration could not be read/);
    expect(host.textContent).toContain(
      "restart marotte right after editing the file, before changing anything in this section",
    );
    expect(host.textContent).toContain("the agent keeps its last settings if it had them");

    unreadableEntries.value = 3;
    expect(host.textContent).toMatch(/^3 saved integrations could not be read/);

    unreadableEntries.value = 0;
    expect(host.hidden).toBe(true);
    stop();
  });
});
