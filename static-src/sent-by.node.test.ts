import { describe, it, expect } from "vitest";
import { sentBy } from "./sent-by.js";

describe("sentBy", () => {
  it("names a labelled program", () => {
    expect(sentBy({ label: "Agent Mail" })).toBe("Sent by Agent Mail");
  });

  it("names a program with no label, or a blank one, as an API caller", () => {
    expect(sentBy({})).toBe("Sent by an API caller");
    expect(sentBy({ label: "  " })).toBe("Sent by an API caller");
  });
});
