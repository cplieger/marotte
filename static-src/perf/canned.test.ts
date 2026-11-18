// The canned set tests/perf/record-canned.sh writes: every recorded answer decodes with its
// route's generated decoder, so a wire change that leaves a stale recording fails here until the
// set is re-recorded, and the set stays under the recorder's cap.
import { describe, expect, it } from "vitest";

import recorder from "../../tests/perf/measure/canned.py?raw";
import * as decoders from "../wire/decoders.gen.js";
import { asObject, decodeArray, reqStr, type Decoder } from "../wire/validators.gen.js";
import { EVENT_DECODERS } from "../wire/events.gen.js";
import {
  CANNED_MAX_BYTES,
  type CannedAnswer,
  type CannedConnect,
  type CannedDecoder,
  type CannedFrames,
  type CannedPayloadFrame,
  type CannedStream,
  UNDECODED_EVENTS,
} from "../__test-helpers__/canned.js";

const raw = import.meta.glob<string>("./canned/**/*.json", {
  query: "?raw",
  import: "default",
  eager: true,
});
const answers = Object.entries(raw).filter(([f]) => f.startsWith("./canned/answers/"));
const frameFiles = Object.entries(raw).filter(([f]) => f.startsWith("./canned/frames/"));
const streamFiles = frameFiles.filter(([f]) => f.startsWith("./canned/frames/stream."));

function decoderOf(name: string): Decoder<unknown> {
  const many = name.endsWith("[]");
  const fn = `decode${many ? name.slice(0, -2) : name}`;
  const own = (decoders as Record<string, unknown>)[fn];
  if (typeof own !== "function") {
    throw new Error(`no ${fn} in wire/decoders.gen.ts`);
  }
  const one = own as Decoder<unknown>;
  return many ? (v) => decodeArray(v, one) : one;
}

const isWireEvent = (type: string): boolean =>
  Object.hasOwn(EVENT_DECODERS, type) || UNDECODED_EVENTS.has(type);

function check(decoder: CannedDecoder, body: unknown): void {
  if (body === null && decoder === null) {
    return;
  }
  if (typeof decoder === "string") {
    decoderOf(decoder)(body);
    return;
  }
  const o = asObject(body);
  for (const [key, name] of Object.entries(decoder ?? {})) {
    const field = key.endsWith("?") ? key.slice(0, -1) : key;
    if (field === key || o[field] !== undefined) {
      decoderOf(name)(o[field]);
    }
  }
}

describe("the canned set", () => {
  it("holds recorded answers and stream frames", () => {
    expect(answers.length).toBeGreaterThan(0);
    expect(frameFiles.length).toBeGreaterThan(0);
  });

  it("stays under the recorder's cap", () => {
    const bytes = Object.values(raw).reduce(
      (n, text) => n + new TextEncoder().encode(text).length,
      0,
    );
    expect(bytes).toBeLessThanOrEqual(CANNED_MAX_BYTES);
  });

  it("caps the set at the recorder's own cap", () => {
    const product = /^MAX_BYTES = (\d+(?: \* \d+)*)$/m.exec(recorder)?.[1];
    expect(product, "canned.py MAX_BYTES").toBeDefined();
    const cap = (product ?? "").split(" * ").reduce((n, f) => n * Number(f), 1);
    expect(cap).toBe(CANNED_MAX_BYTES);
  });

  it.each(answers)("%s decodes with its route's decoder", (_file, text) => {
    const a = JSON.parse(text) as CannedAnswer;
    expect(a.route).toMatch(/^(GET|POST|PUT|PATCH|DELETE) \/(api\/|healthz$)/);
    expect(() => {
      check(a.decoder, a.body);
    }).not.toThrow();
  });

  it("holds the measured turn's frames as wire events with their sizes, in arrival order", () => {
    const { frames } = JSON.parse(
      raw["./canned/frames/chat-stream-live.json"] ?? "{}",
    ) as CannedFrames;
    let last = 0;
    for (const f of frames) {
      expect(isWireEvent(f.event), f.event).toBe(true);
      expect(Number.isInteger(f.bytes) && f.bytes > 0).toBe(true);
      expect(f.at).toBeGreaterThanOrEqual(last);
      last = f.at;
    }
  });

  it("holds the measured turn's frames, the connect, and a stream per driver", () => {
    const names = frameFiles.map(([f]) => f);
    expect(names).toContain("./canned/frames/chat-stream-live.json");
    expect(names).toContain("./canned/frames/connect.json");
    const live = JSON.parse(
      raw["./canned/frames/chat-stream-live.json"] ?? "{}",
    ) as Partial<CannedFrames>;
    expect(live.frames?.length).toBeGreaterThan(0);
    expect(streamFiles.length).toBeGreaterThan(0);
  });

  it("holds a hello that names its epoch", () => {
    const { hello } = JSON.parse(raw["./canned/frames/connect.json"] ?? "{}") as CannedConnect;
    expect(() => reqStr(asObject(hello), "epoch")).not.toThrow();
  });

  it.each([...streamFiles, ["./canned/frames/connect.json", raw["./canned/frames/connect.json"]]])(
    "%s carries wire envelopes their events' decoders take, in arrival order",
    (_file, text) => {
      const { frames } = JSON.parse(text ?? "{}") as { frames: readonly CannedPayloadFrame[] };
      expect(Array.isArray(frames)).toBe(true);
      let last = 0;
      for (const f of frames) {
        const o = asObject(f.envelope);
        const type = reqStr(o, "type");
        expect(isWireEvent(type), type).toBe(true);
        const decode = (EVENT_DECODERS as Record<string, Decoder<unknown> | undefined>)[type];
        if (decode !== undefined && o["payload"] !== undefined) {
          expect(() => decode(o["payload"]), type).not.toThrow();
        }
        if (o["subject"] !== undefined && o["subject"] !== null) {
          decoders.decodeSubjectStamp(o["subject"]);
        }
        expect(f.at).toBeGreaterThanOrEqual(last);
        last = f.at;
      }
    },
  );

  it.each(streamFiles)("%s names its driver, window and scope", (file, text) => {
    const s = JSON.parse(text) as CannedStream;
    expect(file).toBe(`./canned/frames/stream.${s.driver}.${s.scenario}-${s.window}.json`);
    expect(["live", "away"]).toContain(s.window);
    expect(s.topics === null || Array.isArray(s.topics)).toBe(true);
  });
});
