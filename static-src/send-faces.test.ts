// Which face each of the composer's two buttons shows, for every combination of what the box holds, what is
// staged, the turn, whether dictation can run and whether it is running. The expected column is the approved
// rules, row by row: the microphone takes the slot Send or Steer would take exactly when there is nothing to
// send, and while dictating it holds that slot as the stop control.

import { describe, expect, it } from "vitest";
import { sendFaces, type FaceInput, type Faces } from "./send-faces.js";

type Turn = FaceInput["turn"];

const TEXTS = { empty: "", blank: "  \n\t ", words: "hello" } as const;
type TextKind = keyof typeof TEXTS;

/** Rule 1's "nothing to send": an empty or whitespace-only box with no file staged. Every other pair holds a send. */
const NOTHING: readonly (readonly [TextKind, boolean])[] = [
  ["empty", false],
  ["blank", false],
];
const SOMETHING: readonly (readonly [TextKind, boolean])[] = [
  ["words", false],
  ["words", true],
  ["empty", true],
  ["blank", true],
];

/** [turn, holds a send, can dictate, dictating, expected faces], one line per rule case. Error keeps Retry
 *  and never dictates (rule 9: a chat that is starting). */
const TABLE: readonly (readonly [Turn, boolean, boolean, boolean, Faces])[] = [
  ["idle", false, false, false, { main: "send", side: "hidden" }],
  ["idle", false, true, false, { main: "dictate", side: "hidden" }],
  ["idle", false, true, true, { main: "dictate", side: "hidden" }],
  ["idle", true, false, false, { main: "send", side: "hidden" }],
  ["idle", true, true, false, { main: "send", side: "hidden" }],
  ["idle", true, true, true, { main: "dictate", side: "hidden" }],
  ["streaming", false, false, false, { main: "cancel", side: "hidden" }],
  ["streaming", false, true, false, { main: "cancel", side: "dictate" }],
  ["streaming", false, true, true, { main: "cancel", side: "dictate" }],
  ["streaming", true, false, false, { main: "cancel", side: "send" }],
  ["streaming", true, true, false, { main: "cancel", side: "send" }],
  ["streaming", true, true, true, { main: "cancel", side: "dictate" }],
  ["error", false, false, false, { main: "retry", side: "hidden" }],
  ["error", false, true, false, { main: "retry", side: "hidden" }],
  ["error", false, true, true, { main: "retry", side: "hidden" }],
  ["error", true, false, false, { main: "retry", side: "hidden" }],
  ["error", true, true, false, { main: "retry", side: "hidden" }],
  ["error", true, true, true, { main: "retry", side: "hidden" }],
];

const rows = TABLE.flatMap(([turn, holds, canDictate, dictating, faces]) =>
  (holds ? SOMETHING : NOTHING).map(([text, staged]) => ({
    input: { turn, text: TEXTS[text], staged, canDictate, dictating },
    faces,
  })),
);

describe("sendFaces", () => {
  it.each(rows)("$input -> $faces", ({ input, faces }) => {
    expect(sendFaces(input)).toEqual(faces);
  });

  it("shows the microphone in Send's place for an empty box with nothing staged", () => {
    expect(
      sendFaces({ turn: "idle", text: "", staged: false, canDictate: true, dictating: false }),
    ).toEqual({ main: "dictate", side: "hidden" });
  });

  it("counts a whitespace-only box as empty", () => {
    expect(
      sendFaces({ turn: "idle", text: " \n ", staged: false, canDictate: true, dictating: false })
        .main,
    ).toBe("dictate");
  });

  it("shows Send for a staged file with no text, since the file sends alone", () => {
    expect(
      sendFaces({ turn: "idle", text: "", staged: true, canDictate: true, dictating: false }).main,
    ).toBe("send");
  });

  it("puts the microphone in Steer's slot beside Cancel while a turn runs and the box is empty", () => {
    expect(
      sendFaces({ turn: "streaming", text: "", staged: false, canDictate: true, dictating: false }),
    ).toEqual({ main: "cancel", side: "dictate" });
  });

  it("keeps the stop control in its slot after words arrive", () => {
    expect(
      sendFaces({ turn: "idle", text: "hello", staged: false, canDictate: true, dictating: true })
        .main,
    ).toBe("dictate");
    expect(
      sendFaces({
        turn: "streaming",
        text: "hello",
        staged: false,
        canDictate: true,
        dictating: true,
      }).side,
    ).toBe("dictate");
  });

  it("leaves Send exactly as today where dictation cannot run", () => {
    expect(
      sendFaces({ turn: "idle", text: "", staged: false, canDictate: false, dictating: false }),
    ).toEqual({ main: "send", side: "hidden" });
    expect(
      sendFaces({
        turn: "streaming",
        text: "",
        staged: false,
        canDictate: false,
        dictating: false,
      }),
    ).toEqual({ main: "cancel", side: "hidden" });
  });
});
