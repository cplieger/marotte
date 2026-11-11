// A hand-written stand-in for the browser's SpeechRecognition, injected at dictation.ts's platform seam. Each
// recognizer records what the controller asked of it; the test drives the events a real one fires.

import type {
  SpeechAvailability,
  SpeechOptions,
  SpeechPlatform,
  SpeechRecognizer,
  SpeechRecognizerClass,
} from "../dictation.js";

/** One result as the test speaks it: the transcript and whether the engine has committed it. */
type Heard = readonly [transcript: string, final: boolean];

class FakeRecognizer extends EventTarget implements SpeechRecognizer {
  lang = "";
  continuous = false;
  interimResults = false;
  processLocally = false;
  started = 0; // deadset:ignore DS1301 -- the fake's call record, read by the dictation tests
  stopped = 0; // deadset:ignore DS1301 -- the fake's call record, read by the dictation tests
  aborted = 0; // deadset:ignore DS1301 -- the fake's call record, read by the dictation tests
  startThrows: Error | null = null;

  start(): void {
    if (this.startThrows !== null) {
      throw this.startThrows;
    }
    this.started++;
  }

  stop(): void {
    this.stopped++;
  }

  abort(): void {
    this.aborted++;
  }

  /** The engine has opened the microphone. */
  begin(): void {
    this.dispatchEvent(new Event("start"));
  }

  /** The engine's whole result list so far, as a `result` event carries it. */
  hear(...results: readonly Heard[]): void {
    const list = results.map(([transcript, isFinal]) => {
      const alt = { transcript, confidence: 0.9 };
      return Object.assign([alt], { isFinal, item: () => alt });
    });
    const e = Object.assign(new Event("result"), {
      resultIndex: 0,
      results: Object.assign(list, { item: (i: number) => list[i] }),
    });
    this.dispatchEvent(e);
  }

  fail(code: string): void {
    this.dispatchEvent(Object.assign(new Event("error"), { error: code, message: "" }));
  }

  finish(): void {
    this.dispatchEvent(new Event("end"));
  }
}

interface FakeSpeechOptions {
  readonly lang?: string;
  /** The cloud service's name in the notice, or null for an on-device-only browser (Firefox). */
  readonly cloud?: string | null;
  /** The answer `available()` gives per `processLocally`; omit for a browser without the static (Safari). */
  readonly available?: (o: SpeechOptions) => Promise<SpeechAvailability>;
  readonly install?: (o: SpeechOptions) => Promise<boolean>;
  /** What every recognizer's `start()` throws, for an engine that refuses synchronously. */
  readonly startThrows?: Error;
  /** Chrome on Android's result lists, which repeat earlier final results inside later ones. */
  readonly cumulative?: boolean;
}

export interface FakeSpeech {
  readonly platform: SpeechPlatform;
  readonly made: FakeRecognizer[];
  readonly availableCalls: SpeechOptions[];
  readonly installCalls: SpeechOptions[];
  /** The newest recognizer; throws when none was made. */
  last(): FakeRecognizer;
}

export function fakeSpeech(opts: FakeSpeechOptions = {}): FakeSpeech {
  const made: FakeRecognizer[] = [];
  const availableCalls: SpeechOptions[] = [];
  const installCalls: SpeechOptions[] = [];
  class Recognizer extends FakeRecognizer {
    constructor() {
      super();
      this.startThrows = opts.startThrows ?? null;
      made.push(this);
    }
  }
  const ctor: SpeechRecognizerClass = Recognizer;
  const { available, install } = opts;
  if (available !== undefined) {
    ctor.available = (o) => {
      availableCalls.push(o);
      return available(o);
    };
  }
  if (install !== undefined) {
    ctor.install = (o) => {
      installCalls.push(o);
      return install(o);
    };
  }
  return {
    platform: {
      Recognition: ctor,
      lang: opts.lang ?? "en-US",
      cloud: opts.cloud === undefined ? "Google's speech service" : opts.cloud,
      cumulative: opts.cumulative ?? false,
    },
    made,
    availableCalls,
    installCalls,
    last() {
      const r = made.at(-1);
      if (r === undefined) {
        throw new Error("no recognizer was made");
      }
      return r;
    },
  };
}

/** Let the controller's availability promises and queued observers run. */
export function settle(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}
