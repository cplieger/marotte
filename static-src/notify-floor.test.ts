import { describe, it, expect } from "vitest";
import { chatTarget } from "./push-subject.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
import indexHtml from "../static/index.html?raw";
import notifySrc from "./notify.ts?raw";
import wireTypesSrc from "./wire/types.gen.ts?raw";
import domSrc from "./dom.ts?raw";
import turnHandlerSrc from "./handlers/turn.ts?raw";

describe("the permission notification has no off switch", () => {
  const html = indexHtml;

  it("has no permission toggle in the markup", () => {
    expect(html).not.toContain("notify-permission-toggle");
  });

  it("says the channel is always on and names the real relaxation", () => {
    const section = html.slice(html.indexOf("notify-sub-options"));
    expect(section).toContain("Always on");
    expect(section).toContain("waits until you answer");
    expect(section).toContain("Permissions");
  });

  it("keeps the agent-finished toggle, which IS a preference", () => {
    expect(html).toContain("notify-finished-toggle");
  });

  it("offers the security profile as the control that replaced it", () => {
    expect(html).toContain("security-profile-list");
  });
});

// The authored markup is the settings panel's first frame: a `checked` that
// disagrees with the server's default flickers on every open.
describe("each keyed toggle's authored default matches the server's", () => {
  function inputTag(id: string): string {
    const at = indexHtml.indexOf(`id="${id}"`);
    expect(at, `${id} is not in static/index.html`).toBeGreaterThan(-1);
    const open = indexHtml.lastIndexOf("<input", at);
    return indexHtml.slice(open, indexHtml.indexOf(">", at) + 1);
  }

  it("leaves pull-request checks unchecked, and its two siblings checked", () => {
    expect(inputTag("notify-pr-status-toggle")).not.toContain("checked");
    expect(inputTag("notify-finished-toggle")).toContain("checked");
    expect(inputTag("notify-run-outcome-toggle")).toContain("checked");
  });
});

describe("no client code can address the removed setting", () => {
  it("notify.ts exports no per-kind permission getter or setter", () => {
    expect(notifySrc).not.toContain("isPermissionNeededEnabled");
    expect(notifySrc).not.toContain("setPermissionNeededEnabled");
  });

  it("notify.ts never reads notify_permission from the settings payload", () => {
    // The property read, not the bare word: a comment may name the removed key.
    expect(notifySrc).not.toMatch(/\.notify_permission\b/);
    expect(notifySrc).not.toMatch(/^\s*notify_permission\?/m);
  });

  it("the generated settings type declares no field for the key", () => {
    expect(wireTypesSrc).not.toMatch(/^\s*notify_permission\??:/m);
  });

  it("the DOM registry has no getter for the removed toggle", () => {
    expect(domSrc).not.toContain("notifyPermissionToggle");
  });

  // Structural claim on the source; the behavioural half is handlers/turn.test.ts.
  it("the ask handlers carry no per-kind gate", () => {
    expect(turnHandlerSrc).not.toContain("isPermissionNeededEnabled");
  });
});

describe("the master switch still governs everything", () => {
  it("notifyIfHidden gates on the master enabled flag", async () => {
    const notify = await import("./notify.js");
    expect(notify.areNotificationsEnabled()).toBe(false);
    expect(notify.notifyIfHidden("Marotte", "Permission needed", chatTarget(""))).toBe(false);

    notify.restoreNotifications(
      settingsPayload({
        notifications_enabled: false,
        notify_agent_finished: true,
      }),
    );
    expect(notify.areNotificationsEnabled()).toBe(false);
  });
});
