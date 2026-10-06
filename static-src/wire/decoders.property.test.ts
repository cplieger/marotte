// Property tests over the generated wire surface, from the generator's own table: 14a arbitraries
// and exported decoders are equal sets; 14b every pair round-trips through JSON losslessly; 14c an
// enum vocabulary draws a finite set of strings.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import * as decoders from "./decoders.gen.js";
import { ARBITRARY_BY_TYPE } from "./arbitraries.gen.js";
import type { Decoder } from "../validators.js";

const DECODER_PREFIX = "decode";

const decoderByType = new Map<string, Decoder<unknown>>();
for (const [name, value] of Object.entries(decoders)) {
  if (name.startsWith(DECODER_PREFIX) && typeof value === "function") {
    decoderByType.set(name.slice(DECODER_PREFIX.length), value as Decoder<unknown>);
  }
}

const pairs: { type: string; decoder: Decoder<unknown>; arb: fc.Arbitrary<unknown> }[] = [];
for (const [type, arb] of Object.entries(ARBITRARY_BY_TYPE)) {
  const decoder = decoderByType.get(type);
  if (decoder) {
    pairs.push({ type, decoder, arb });
  }
}

// A table key with no decoder is an enum vocabulary: the generator emits one
// arbitrary per registered type AND per registered enum, and only a type gets a
// decoder. 14a's second direction proves the residue is exactly that.
const enumKeys = Object.keys(ARBITRARY_BY_TYPE).filter((type) => !decoderByType.has(type));

const ENUM_SAMPLES = 200;
const MAX_ENUM_SUPPORT = 64;

describe("wire/arbitraries cover the registry (property 14a)", () => {
  it("every exported decoder has a generated arbitrary", () => {
    const missing = [...decoderByType.keys()].filter((type) => !(type in ARBITRARY_BY_TYPE));
    expect(missing, `decoders with no entry in ARBITRARY_BY_TYPE: ${missing.join(", ")}`).toEqual(
      [],
    );
  });

  it("every generated arbitrary is a decoder's or an enum vocabulary", () => {
    const orphans = enumKeys.filter((type) => {
      const [drawn] = fc.sample(ARBITRARY_BY_TYPE[type]!, { numRuns: 1, seed: 1 });
      return typeof drawn === "object" && drawn !== null;
    });
    expect(
      orphans,
      `arbitraries for a record type with no exported decoder: ${orphans.join(", ")}`,
    ).toEqual([]);
  });

  it("the pairing covers every decoder", () => {
    expect(pairs.length).toBe(decoderByType.size);
    expect(pairs.length).toBeGreaterThan(0);
  });
});

describe("valid-shape inputs round-trip without loss (property 14b)", () => {
  // toEqual treats an absent key and an undefined-valued key as equal, which is
  // the same normalization JSON.stringify applies to the input first.
  for (const { type, decoder, arb } of pairs) {
    it(type, () => {
      fc.assert(
        fc.property(arb, (input) => {
          const json: unknown = JSON.parse(JSON.stringify(input));
          expect(decoder(json)).toEqual(json);
        }),
        { numRuns: 30 },
      );
    });
  }
});

describe("enum vocabularies are finite and string-valued (property 14c)", () => {
  for (const type of enumKeys) {
    it(type, () => {
      const drawn = fc.sample(ARBITRARY_BY_TYPE[type]!, { numRuns: ENUM_SAMPLES, seed: 7 });
      expect(drawn.every((v) => typeof v === "string")).toBe(true);
      expect(new Set(drawn).size).toBeLessThanOrEqual(MAX_ENUM_SUPPORT);
    });
  }
});

describe("arbitrary inputs throw TypeError or succeed — never non-TypeError", () => {
  for (const { type, decoder } of pairs) {
    it(type, () => {
      let runs = 0;
      fc.assert(
        fc.property(fc.anything(), (input) => {
          runs++;
          try {
            decoder(input);
          } catch (e) {
            if (!(e instanceof TypeError)) {
              throw new Error(
                `decode${type} threw non-TypeError: ${e instanceof Error ? e.message : String(e)}`,
                { cause: e },
              );
            }
          }
        }),
        { numRuns: 20 },
      );
      expect(runs).toBeGreaterThan(0);
    });
  }
});
