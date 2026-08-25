-- +goose Up
-- disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- create "new_loop_config" table
CREATE TABLE `new_loop_config` (`workspace_id` text NOT NULL, `loop_name` text NOT NULL, `human_gate_enabled` integer NOT NULL DEFAULT 0, `reattempt_strategy` text NULL, `enabled_checks_json` text NOT NULL DEFAULT '{}', `iteration_cap` integer NULL, `budget_tokens` integer NULL, `budget_wall_sec` integer NULL, `budget_on_exceeded` text NULL, `no_progress_window` integer NULL, `fan_out_width` integer NULL, `gate_max_revisions` integer NULL, `runtime_defaults_json` text NULL, `runtime_rules_json` text NULL, `environment_json` text NULL, `revision` integer NOT NULL DEFAULT 1, PRIMARY KEY (`workspace_id`, `loop_name`), CHECK (runtime_defaults_json IS NULL OR json_valid(runtime_defaults_json)), CHECK (runtime_rules_json IS NULL OR json_valid(runtime_rules_json)), CHECK (environment_json IS NULL OR json_valid(environment_json)), CHECK (revision >= 1));
-- copy rows from old table "loop_config" to new temporary table "new_loop_config"
INSERT INTO `new_loop_config` (`workspace_id`, `loop_name`, `human_gate_enabled`, `reattempt_strategy`, `enabled_checks_json`, `iteration_cap`, `budget_tokens`, `budget_wall_sec`, `budget_on_exceeded`, `no_progress_window`, `fan_out_width`, `gate_max_revisions`, `runtime_defaults_json`, `runtime_rules_json`, `environment_json`) SELECT `workspace_id`, `loop_name`, `human_gate_enabled`, `reattempt_strategy`, `enabled_checks_json`, `iteration_cap`, `budget_tokens`, `budget_wall_sec`, `budget_on_exceeded`, `no_progress_window`, `fan_out_width`, `gate_max_revisions`, `runtime_defaults_json`, `runtime_rules_json`, `environment_json` FROM `loop_config`;
-- drop trigger "workspace_scope_cleanup_after_delete" before rebuilding a referenced table
DROP TRIGGER IF EXISTS `workspace_scope_cleanup_after_delete`;
-- drop "loop_config" table after copying rows
DROP TABLE `loop_config`;
-- rename temporary table "new_loop_config" to "loop_config"
ALTER TABLE `new_loop_config` RENAME TO `loop_config`;
-- enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
-- recreate trigger "workspace_scope_cleanup_after_delete" after rebuilding table "workspaces"
-- +goose StatementBegin
CREATE TRIGGER workspace_scope_cleanup_after_delete
AFTER DELETE ON workspaces
BEGIN
	DELETE FROM network_wake_events WHERE workspace_id = OLD.id;
	DELETE FROM network_wake_sources WHERE workspace_id = OLD.id;
	DELETE FROM network_message_dispositions WHERE workspace_id = OLD.id;
	DELETE FROM network_live_wakes WHERE workspace_id = OLD.id;
	DELETE FROM network_participation_budgets WHERE workspace_id = OLD.id;
	DELETE FROM network_task_status_projections WHERE workspace_id = OLD.id;
	DELETE FROM network_task_thread_origins WHERE workspace_id = OLD.id;
	DELETE FROM network_thread_session_token_stats WHERE workspace_id = OLD.id;
	DELETE FROM network_thread_participants WHERE workspace_id = OLD.id;
	DELETE FROM network_subscriptions WHERE workspace_id = OLD.id;
	DELETE FROM network_work WHERE workspace_id = OLD.id;
	DELETE FROM network_direct_rooms WHERE workspace_id = OLD.id;
	DELETE FROM network_threads WHERE workspace_id = OLD.id;
	DELETE FROM network_channel_participants WHERE workspace_id = OLD.id;
	DELETE FROM network_channel_kind_counts WHERE workspace_id = OLD.id;
	DELETE FROM network_channel_stats WHERE workspace_id = OLD.id;
	DELETE FROM network_timeline_log WHERE workspace_id = OLD.id;
	DELETE FROM network_channels WHERE workspace_id = OLD.id;
	DELETE FROM network_audit_log WHERE workspace_id = OLD.id;
	DELETE FROM network_coordination_invitations WHERE workspace_id = OLD.id;
	DELETE FROM task_network_coordination WHERE workspace_id = OLD.id;
	DELETE FROM loop_ui_annotations WHERE workspace_id = OLD.id;
	DELETE FROM loop_session_bindings WHERE workspace_id = OLD.id;
	DELETE FROM loop_run_events WHERE workspace_id = OLD.id;
	DELETE FROM loop_runs WHERE workspace_id = OLD.id;
	DELETE FROM loop_goal_session_outbox WHERE workspace_id = OLD.id;
	DELETE FROM loop_goal_session_cleanup WHERE workspace_id = OLD.id;
	DELETE FROM loop_admission_claims WHERE workspace_id = OLD.id;
	DELETE FROM loop_node_lane_pauses WHERE workspace_id = OLD.id;
	DELETE FROM loop_node_amendments WHERE workspace_id = OLD.id;
	DELETE FROM loop_timetravel_ops WHERE workspace_id = OLD.id;
	DELETE FROM loop_requests WHERE workspace_id = OLD.id;
	DELETE FROM loop_gate_decisions WHERE workspace_id = OLD.id;
	DELETE FROM loop_definition_snapshots WHERE workspace_id = OLD.id;
	DELETE FROM loop_config WHERE workspace_id = OLD.id;
	DELETE FROM agent_heartbeat_wake_events WHERE workspace_id = OLD.id;
	DELETE FROM agent_heartbeat_wake_state WHERE workspace_id = OLD.id;
	DELETE FROM agent_heartbeat_revisions WHERE workspace_id = OLD.id;
	DELETE FROM agent_heartbeat_snapshots WHERE workspace_id = OLD.id;
	DELETE FROM agent_soul_revisions WHERE workspace_id = OLD.id;
	DELETE FROM agent_soul_snapshots WHERE workspace_id = OLD.id;
	DELETE FROM session_health WHERE workspace_id = OLD.id;
	DELETE FROM sessions WHERE workspace_id = OLD.id;
	DELETE FROM token_usage_daily WHERE workspace_id = OLD.id;
	DELETE FROM event_summaries WHERE workspace_id = OLD.id;
	DELETE FROM tool_approval_grants WHERE workspace_id = OLD.id;
	DELETE FROM dead_entities WHERE workspace_id = OLD.id;
	DELETE FROM notification_cursors WHERE workspace_id = OLD.id;
END;
-- +goose StatementEnd
