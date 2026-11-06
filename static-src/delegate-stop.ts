// The words for a delegate that did not end its turn cleanly, keyed on the raw stop the server
// carries as `delegate_stop`. Display only: the grade is already the invocation's status.

interface StopWords {
  /** Appended to the card's announced state word. */
  readonly word: string;
  /** The delegate page's "Stopped" fact. */
  readonly sentence: string;
}

const STOP_WORDS = new Map<string, StopWords>([
  [
    "max_turn_requests",
    {
      word: "stopped at its turn limit",
      sentence: "It stopped at its turn limit before it finished, so its work is not done.",
    },
  ],
  ["refusal", { word: "refused", sentence: "The model refused, so it stopped." }],
  [
    "max_tokens",
    { word: "report cut off", sentence: "Its report was cut off at the output limit." },
  ],
]);

const FALLBACK: StopWords = { word: "stopped early", sentence: "It stopped early." };

function wordsFor(raw: string): StopWords {
  return STOP_WORDS.get(raw) ?? FALLBACK;
}

/** The short form for the card's state word; empty when the delegate ended cleanly. */
export function delegateStopWord(raw: string | undefined): string {
  return raw === undefined || raw === "" ? "" : wordsFor(raw).word;
}

/** The sentence for the delegate page; empty when the delegate ended cleanly. */
export function delegateStopSentence(raw: string | undefined): string {
  return raw === undefined || raw === "" ? "" : wordsFor(raw).sentence;
}
