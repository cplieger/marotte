import { describe, it, expect } from "vitest";
import { isHttpURL } from "./url-safety.js";

describe("isHttpURL", () => {
  it("accepts http and https", () => {
    expect(isHttpURL("https://auth.example.com/oauth?code=1")).toBe(true);
    expect(isHttpURL("http://localhost:8080/callback")).toBe(true);
    // URL parsing lowercases the scheme.
    expect(isHttpURL("HTTPS://Example.com")).toBe(true);
  });

  it("rejects non-http(s) schemes and unparseable input", () => {
    for (const u of [
      "file:///etc/passwd",
      "javascript:alert(1)",
      "data:text/html,<script>",
      "ftp://example.com",
      "vscode://open",
      "",
      "not a url",
      "//example.com",
    ]) {
      expect(isHttpURL(u), u).toBe(false);
    }
  });
});
