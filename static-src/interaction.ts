// How a tool call's approval or question was answered. One owner for the card's fact line and the footer's tally.

import type { ToolInteraction } from "./types.js";

/** The answer's class. An approval with an unknown option kind is `answered`: the option id names nothing actionable. */
export type AskBucket = "allowed" | "always_allowed" | "rejected" | "answered" | "skipped";

export function askBucket(i: ToolInteraction): AskBucket {
  if (i.type === "user_input") {
    return i.outcome === "dismissed" ? "skipped" : "answered";
  }
  switch (i.choice) {
    case "allow_once":
      return "allowed";
    case "allow_always":
      return "always_allowed";
    case "reject_once":
    case "reject_always":
      return "rejected";
    default:
      return "answered";
  }
}

/** The bucket's label, in tally order. */
export const ASK_LABEL: Readonly<Record<AskBucket, string>> = {
  allowed: "Allowed",
  always_allowed: "Always allowed",
  rejected: "Rejected",
  answered: "Answered",
  skipped: "Skipped",
};

/** The card's fact line. Only a question's answer carries text: an approval's choice is an option id. */
export function interactionFact(i: ToolInteraction): string {
  const bucket = askBucket(i);
  const choice = i.choice ?? "";
  if (bucket === "answered" && i.type === "user_input" && choice !== "") {
    return `Answered: ${choice}`;
  }
  return ASK_LABEL[bucket];
}
