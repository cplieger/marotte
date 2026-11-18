// The rule facts at the client's decoders: the Bot's setup reads each rule's spec and status and the engine's
// in-memory facts from `GET /api/bots/{id}`, Test this rule its answer, and the editor the catalogue.

import { describe, it, expect } from "vitest";

import { decodeBotSetup, decodeRuleCatalog, decodeRuleTestAnswer } from "./decoders.gen.js";

const rule = {
  id: "R1",
  spec: {
    name: "Tidy",
    prompt: "Check the open pull requests.",
    enabled: true,
    triggers: [{ id: "T1", kind: "every", every: { minutes: 15, anchor: "idle" } }],
    condition: {
      op: "any",
      children: [
        { leaf: { kind: "bot_busy", not: true } },
        {
          leaf: {
            kind: "time_window",
            time: { from: "09:00", to: "18:00", weekdays: [1, 2, 3, 4, 5] },
          },
        },
      ],
    },
  },
  status: {
    last_fire: {
      trigger: "T1",
      event_id: "every:T1:1",
      summary: "every 15 min",
      at: 1_700_000_100_000,
    },
    triggers: { T1: { armed_at: 1_700_000_000_000, last_at: 1_700_000_100_000 } },
    queue: [
      {
        stage: "fired",
        trigger: "T1",
        event_id: "every:T1:2",
        data: { trigger: "every" },
        seen_at: 1,
        depth: 1,
        tries: 0,
      },
    ],
    dropped: 2,
  },
};

const setup = {
  bot: { goal: "Keep it tidy.", spec_version: 3, memory: false, rules: [rule] },
  engine: { state: "write_failing", since: 1_700_000_600_000 },
  name: "Tidier",
  model: "auto",
  effort: "high",
  supervised: false,
};

describe("Bot rule wire facts", () => {
  it("keeps a rule's spec, its nested condition and its status on the setup", () => {
    const got = decodeBotSetup(structuredClone(setup));
    expect(got.bot?.rules?.[0]).toEqual(rule);
    expect(got.engine).toEqual({ state: "write_failing", since: 1_700_000_600_000 });
  });

  it("refuses an engine state outside the vocabulary", () => {
    expect(() =>
      decodeBotSetup({ ...structuredClone(setup), engine: { state: "sleeping" } }),
    ).toThrow(/state/);
  });

  it("refuses a trigger kind outside the vocabulary", () => {
    const bad = structuredClone(setup);
    bad.bot.rules[0]!.spec.triggers[0]!.kind = "cron";
    expect(() => decodeBotSetup(bad)).toThrow(/kind/);
  });

  it("keeps a dry run's outcome, leaves, wait and text", () => {
    const answer = {
      outcome: "fire",
      leaves: [{ path: "condition.children[0]", value: "unknown" }],
      wait: { reason: "gap", until: 1_700_000_060_000 },
      text: "Rule: Tidy\n\nCheck the open pull requests.",
    };
    expect(decodeRuleTestAnswer(structuredClone(answer))).toEqual(answer);
    expect(() => decodeRuleTestAnswer({ ...answer, outcome: "maybe" })).toThrow(/outcome/);
  });

  it("keeps the catalogue's kinds with their bounds", () => {
    const catalog = {
      words: {
        days: ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"],
        days_plural: [
          "Sundays",
          "Mondays",
          "Tuesdays",
          "Wednesdays",
          "Thursdays",
          "Fridays",
          "Saturdays",
        ],
        minute: "min",
        hour: "h",
        day: "d",
        weekdays: "weekdays",
        every_day: "every day",
        last_day: "the last day",
      },
      rule: {
        max_rules: 20,
        max_triggers: 8,
        max_name_runes: 80,
        max_prompt_bytes: 8192,
        max_leaves: 40,
        max_group_depth: 4,
        max_group_children: 12,
        max_one_of_values: 32,
        max_value_bytes: 256,
      },
      kinds: [
        {
          phrases: { last: "every {minutes}" },
          sample: { trigger: "every" },
          kind: "every",
          group: "time",
          label: "On a schedule",
          fields: ["due_at"],
          params: [{ name: "minutes", min: 5, max: 10080 }],
          time: true,
        },
      ],
      leaves: [
        { phrases: { is: "this Bot is working" }, kind: "bot_busy", group: "bot", params: [] },
      ],
      ops: [{ op: "is", phrase: "is", negative: "is not", value: true, list: false }],
      common_fields: ["trigger", "rule"],
      limits: {
        holds: ["fifo", "newest"],
        min_per_hour: 1,
        max_per_hour: 60,
        default_per_hour: 12,
        max_per_day: 1440,
        min_hold_cap: 1,
        max_hold_cap: 50,
        default_hold_cap: 10,
        gap_minutes: 1,
        bot_max_per_hour: 60,
      },
    };
    expect(decodeRuleCatalog(structuredClone(catalog))).toEqual(catalog);
  });
});
