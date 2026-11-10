import { describe, it, expect } from "vitest";
import { senderWords, sentBy } from "./sent-by.js";

describe("sentBy", () => {
  it("names a labelled program", () => {
    expect(sentBy({ label: "Agent Mail" })).toBe("Sent by Agent Mail");
  });

  it("names a program with no label, or a blank one, as an API caller", () => {
    expect(sentBy({})).toBe("Sent by an API caller");
    expect(sentBy({ label: "  " })).toBe("Sent by an API caller");
  });
});

describe("senderWords", () => {
  it("names marotte for a prompt it sent on its own", () => {
    expect(senderWords({ automatic: true })).toBe("Sent by marotte");
  });

  it("names an API caller's prompt by the caller", () => {
    expect(senderWords({ caller: { label: "Agent Mail" } })).toBe("Sent by Agent Mail");
  });

  it("names no one for the reader's own prompt", () => {
    expect(senderWords({})).toBeUndefined();
    expect(senderWords({ automatic: false })).toBeUndefined();
    expect(senderWords(undefined)).toBeUndefined();
  });
});
