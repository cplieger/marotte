import { describe, it, expect } from "vitest";
import { rowStatus, usableStatus } from "./forge-auth-status.js";
import { forgeRow } from "./__test-helpers__/forge-rows.js";
import type { ForgeStatus } from "./wire/types.gen.js";

const T = 1_700_000_000_000;

describe("what each status says and offers", () => {
  it("says nothing on a connected row and offers Check", () => {
    expect(rowStatus(forgeRow(), T)).toEqual({ line: "", tone: "none", control: "check" });
  });

  it("says muted that a retrying row runs again, and keeps Check", () => {
    const st = rowStatus(
      forgeRow({ status: "retrying", error: { message: "no route to host", kind: "transient" } }),
      T,
    );
    expect(st).toEqual({
      line: "The last check hit a temporary problem and runs again automatically. no route to host",
      tone: "muted",
      control: "check",
    });
  });

  it("gives a refused row the forge's reason and Check, for after it is fixed on the forge", () => {
    expect(
      rowStatus(forgeRow({ status: "refused", error: { message: "IP not allowed" } }), T),
    ).toEqual({ line: "IP not allowed", tone: "error", control: "check" });
  });

  it("names the missing permission on a scope refusal", () => {
    const st = rowStatus(
      forgeRow({
        status: "refused",
        error: { message: "[User: Read]", code: "scope_insufficient" },
      }),
      T,
    );
    expect(st.line).toBe(
      "The token is missing a permission this needs. Add it to the token on the forge, then check again. [User: Read]",
    );
    expect(st.control).toBe("check");
  });

  it("offers a reconnect row only Reconnect, with the server's reason", () => {
    expect(
      rowStatus(forgeRow({ status: "reconnect", error: { message: "no stored credential." } }), T),
    ).toEqual({
      line: "This account needs a new sign-in: no stored credential.",
      tone: "error",
      control: "reconnect",
    });
  });

  it("offers an unusable row no request at all", () => {
    expect(
      rowStatus(forgeRow({ status: "unusable", error: { message: "store mode 0750" } }), T),
    ).toEqual({ line: "store mode 0750", tone: "error", control: "none" });
  });
});

describe("a rate limit's wait", () => {
  const limited = (retryAt?: number): ReturnType<typeof forgeRow> =>
    forgeRow({
      status: "retrying",
      checked_at: T - 3_600_000,
      error: {
        message: "429",
        kind: "rate_limited",
        ...(retryAt === undefined ? {} : { retry_at: retryAt }),
      },
    });

  // Counted to the forge's deadline, never from the check's time, so a renewed limit restarts the count.
  it("counts down to the deadline, then says it can be checked, and restarts on a renewed one", () => {
    expect(rowStatus(limited(T + 60_000), T).line).toBe(
      "The forge is rate limiting this account. Try again in 60 seconds.",
    );
    expect(rowStatus(limited(T + 60_000), T + 61_000).line).toBe(
      "The forge is rate limiting this account. It can be checked again now.",
    );
    expect(rowStatus(limited(T + 121_000), T + 61_000).line).toBe(
      "The forge is rate limiting this account. Try again in 60 seconds.",
    );
  });

  it("says one second in the singular", () => {
    expect(rowStatus(limited(T + 1_000), T).line).toBe(
      "The forge is rate limiting this account. Try again in 1 second.",
    );
  });

  it("asks for later when the forge named no wait", () => {
    expect(rowStatus(limited(), T).line).toBe(
      "The forge is rate limiting this account. Try again later.",
    );
  });
});

describe("the usable class", () => {
  it.each<[ForgeStatus, boolean]>([
    ["connected", true],
    ["retrying", true],
    ["refused", false],
    ["reconnect", false],
    ["unusable", false],
  ])("%s is usable: %s", (status, usable) => {
    expect(usableStatus(status)).toBe(usable);
  });
});
