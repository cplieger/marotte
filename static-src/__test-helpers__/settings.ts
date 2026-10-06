import type { EffectiveSettings } from "../wire/types.gen.js";

/** A complete GET /api/settings payload, overridable per field. The values mirror Go's
 *  settings.EffectiveDefaults() by hand, NOT imported: a fixture derived from the code under test
 *  asserts nothing. A drift shows as a test whose stated default stops matching the server's. */
export function settingsPayload(overrides: Partial<EffectiveSettings> = {}): EffectiveSettings {
  return {
    agent_ignore_files: [".gitignore", ".kiroignore"],
    chat_retention_days: 7,
    theme: "",
    fb_path: "",
    last_model: "",
    last_effort_by_model: {},
    last_merge_method: "",
    knowledge_enabled: true,
    content_collection_enabled: false,
    guard_payload_links: true,
    tool_search_enabled: false,
    memory_mode: "learn",
    auto_compaction_enabled: true,
    auto_compact_pct: 80,
    spec_planning: "off",
    spec_planning_ask_first: false,
    inline_agents_enabled: false,
    steering_reminders_enabled: false,
    workflows_enabled: true,
    work_validation: "",
    cloudformation_safety_check: "",
    output_style: "default",
    terminal_command_timeout_ms: 0,
    mcp_wait_for_ready: false,
    notifications_enabled: false,
    notify_agent_finished: true,
    notify_pr_status: false,
    notify_run_outcome: true,
    supervised_default: false,
    scheduled_auto_approve: false,
    debug_logs: false,
    ...overrides,
  };
}
