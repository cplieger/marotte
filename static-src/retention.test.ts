// Tests for retention.ts: the chat_retention_days a settings payload states; state + listeners; the
// off(0)/forever(-1)/N-days semantics.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { adoptRetention, isRetentionEnabled, onRetentionChange } from "./retention.js";

beforeEach(() => {
  adoptRetention(settingsPayload());
});

describe("retention", () => {
  it("forever (-1) and a day count keep the chat", () => {
    adoptRetention(settingsPayload({ chat_retention_days: -1 }));
    expect(isRetentionEnabled()).toBe(true);
    adoptRetention(settingsPayload({ chat_retention_days: 14 }));
    expect(isRetentionEnabled()).toBe(true);
  });

  it("off (0) is the only disabled state; subscribe fires on 0<->N flips", () => {
    const listener = vi.fn();
    const unsub = onRetentionChange(listener);
    // subscribe() fires immediately with the current value.
    expect(listener).toHaveBeenCalledTimes(1);
    expect(isRetentionEnabled()).toBe(true);

    adoptRetention(settingsPayload({ chat_retention_days: 0 }));
    expect(listener).toHaveBeenCalledTimes(2);
    expect(isRetentionEnabled()).toBe(false);

    adoptRetention(settingsPayload({ chat_retention_days: 14 }));
    expect(listener).toHaveBeenCalledTimes(3);
    expect(isRetentionEnabled()).toBe(true);

    unsub();
    adoptRetention(settingsPayload({ chat_retention_days: 0 }));
    expect(listener).toHaveBeenCalledTimes(3);
  });
});
