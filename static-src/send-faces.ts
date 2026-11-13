import { isSendable } from "./composer-value.js";

export interface FaceInput {
  readonly turn: "idle" | "streaming" | "error";
  readonly text: string;
  readonly staged: boolean;
  /** Whether a press could start dictation now (`Dictation.ready`). */
  readonly canDictate: boolean;
  readonly dictating: boolean;
}

/** `main` is `#send-btn`, `side` is the mid-turn pill (`#midturn-send`) in the slot beside Cancel. */
export interface Faces {
  readonly main: "send" | "dictate" | "cancel" | "retry";
  readonly side: "hidden" | "send" | "dictate";
}

/** The microphone takes the slot Send or Steer would take exactly when the composer holds nothing to send, and holds
 *  it as the stop control while dictating. The error face keeps Retry: there is nothing to dictate TO. */
export function sendFaces(f: FaceInput): Faces {
  if (f.turn === "error") {
    return { main: "retry", side: "hidden" };
  }
  const holds = isSendable(f.text, f.staged);
  const mic = f.dictating || (f.canDictate && !holds);
  if (f.turn === "idle") {
    return { main: mic ? "dictate" : "send", side: "hidden" };
  }
  return { main: "cancel", side: mic ? "dictate" : holds ? "send" : "hidden" };
}
