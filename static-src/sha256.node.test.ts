import { describe, it, expect } from "vitest";
import { createHash } from "node:crypto";
import fc from "fast-check";

import { sha256Hex } from "./sha256.js";

const enc = (s: string): Uint8Array => new TextEncoder().encode(s);

describe("sha256Hex", () => {
  it.each([
    ["", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"],
    ["abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"],
    [
      "abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
      "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1",
    ],
  ])("digests %j to the FIPS 180-4 vector", (input, want) => {
    expect(sha256Hex(enc(input))).toBe(want);
  });

  it("agrees with node:crypto on every length around the block padding boundaries", () => {
    fc.assert(
      fc.property(fc.uint8Array({ minLength: 0, maxLength: 300 }), (bytes) => {
        expect(sha256Hex(bytes)).toBe(createHash("sha256").update(bytes).digest("hex"));
      }),
      { numRuns: 300 },
    );
  });

  it("agrees with node:crypto on a 2 MiB input", () => {
    const big = new Uint8Array(2 << 20).map((_, i) => (i * 31) & 0xff);
    expect(sha256Hex(big)).toBe(createHash("sha256").update(big).digest("hex"));
  });
});
