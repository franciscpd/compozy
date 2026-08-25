-- +goose Up
-- disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- create "new_loop_timetravel_ops" table
CREATE TABLE `new_loop_timetravel_ops` (`workspace_id` text NOT NULL, `op_id` text NOT NULL, `kind` text NOT NULL, `idempotency_key` text NOT NULL DEFAULT '', `request_digest` text NOT NULL, `source_run_id` text NOT NULL, `source_generation` integer NULL, `from_node` text NULL, `item_index` integer NULL, `actor_kind` text NOT NULL, `actor_id` text NOT NULL, `reason` text NULL, `result_run_id` text NOT NULL, `result_generation` integer NULL, `created_at` timestamp NOT NULL, PRIMARY KEY (`workspace_id`, `op_id`), CONSTRAINT `0` FOREIGN KEY (`source_run_id`) REFERENCES `loop_runs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE, CHECK (kind IN ('rerun','fork','nested_recovery')), CHECK (length(request_digest) = 64), CHECK (source_generation IS NULL OR source_generation >= 1), CHECK (item_index IS NULL OR item_index >= 0), CHECK (result_generation IS NULL OR result_generation >= 1), CHECK (
		(kind = 'rerun' AND source_generation IS NOT NULL AND from_node IS NOT NULL
		 AND result_generation IS NOT NULL AND result_run_id = source_run_id)
		OR
		(kind = 'fork' AND source_generation IS NOT NULL AND from_node IS NULL
		 AND item_index IS NULL AND result_generation IS NULL)
		OR
		(kind = 'nested_recovery' AND source_generation IS NOT NULL AND from_node IS NOT NULL
		 AND item_index IS NOT NULL AND result_generation IS NOT NULL AND result_run_id = source_run_id)
	));
-- copy rows from old table "loop_timetravel_ops" to new temporary table "new_loop_timetravel_ops"
INSERT INTO `new_loop_timetravel_ops` (`workspace_id`, `op_id`, `kind`, `idempotency_key`, `request_digest`, `source_run_id`, `source_generation`, `from_node`, `item_index`, `actor_kind`, `actor_id`, `reason`, `result_run_id`, `result_generation`, `created_at`) SELECT `workspace_id`, `op_id`, `kind`, `idempotency_key`, `request_digest`, `source_run_id`, `source_generation`, `from_node`, `item_index`, `actor_kind`, `actor_id`, `reason`, `result_run_id`, `result_generation`, `created_at` FROM `loop_timetravel_ops`;
-- drop trigger "workspace_scope_cleanup_after_delete" before rebuilding a referenced table
DROP TRIGGER IF EXISTS `workspace_scope_cleanup_after_delete`;
-- drop "loop_timetravel_ops" table after copying rows
DROP TABLE `loop_timetravel_ops`;
-- rename temporary table "new_loop_timetravel_ops" to "loop_timetravel_ops"
ALTER TABLE `new_loop_timetravel_ops` RENAME TO `loop_timetravel_ops`;
-- create index "uq_loop_timetravel_ops_idempotency" to table: "loop_timetravel_ops"
CREATE UNIQUE INDEX `uq_loop_timetravel_ops_idempotency` ON `loop_timetravel_ops` (`workspace_id`, `idempotency_key`) WHERE idempotency_key != '';
-- create "new_loop_generations" table
CREATE TABLE `new_loop_generations` (`loop_run_id` text NOT NULL, `generation` integer NOT NULL, `parent_generation` integer NOT NULL DEFAULT 0, `origin` text NOT NULL, `created_at` timestamp NOT NULL, PRIMARY KEY (`loop_run_id`, `generation`), CONSTRAINT `0` FOREIGN KEY (`loop_run_id`) REFERENCES `loop_runs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE, CHECK (generation >= 1), CHECK (parent_generation >= 0 AND parent_generation < generation), CHECK (origin IN (
				'initial','stop_when','reattempt','gate_revise','gate_next_generation',
				'dod_retry','ratchet_restore','requeue','operator_rerun','fork_seed','nested_recovery'
			)));
-- copy rows from old table "loop_generations" to new temporary table "new_loop_generations"
INSERT INTO `new_loop_generations` (`loop_run_id`, `generation`, `parent_generation`, `origin`, `created_at`) SELECT `loop_run_id`, `generation`, `parent_generation`, `origin`, `created_at` FROM `loop_generations`;
-- drop "loop_generations" table after copying rows
DROP TABLE `loop_generations`;
-- rename temporary table "new_loop_generations" to "loop_generations"
ALTER TABLE `new_loop_generations` RENAME TO `loop_generations`;
-- create "loop_nested_recoveries" table
CREATE TABLE `loop_nested_recoveries` (`workspace_id` text NOT NULL, `operation_id` text NOT NULL, `parent_run_id` text NOT NULL, `parent_generation` integer NOT NULL, `parent_node_id` text NOT NULL, `parent_item_index` integer NOT NULL, `child_run_id` text NOT NULL, `child_generation` integer NOT NULL, `child_node_id` text NOT NULL, `child_item_index` integer NOT NULL, `task_id` text NOT NULL, `runtime_json` text NOT NULL, `created_at` timestamp NOT NULL, PRIMARY KEY (`workspace_id`, `operation_id`), CONSTRAINT `0` FOREIGN KEY (`workspace_id`, `operation_id`) REFERENCES `loop_timetravel_ops` (`workspace_id`, `op_id`) ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT `1` FOREIGN KEY (`child_run_id`) REFERENCES `loop_runs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT `2` FOREIGN KEY (`parent_run_id`) REFERENCES `loop_runs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE, CHECK (parent_generation >= 1), CHECK (parent_item_index >= 0), CHECK (child_generation >= 1), CHECK (child_item_index >= 0), CHECK (json_valid(runtime_json)));
-- create index "loop_nested_recoveries_workspace_id_child_run_id_child_generation_child_node_id_child_item_index" to table: "loop_nested_recoveries"
CREATE UNIQUE INDEX `loop_nested_recoveries_workspace_id_child_run_id_child_generation_child_node_id_child_item_index` ON `loop_nested_recoveries` (`workspace_id`, `child_run_id`, `child_generation`, `child_node_id`, `child_item_index`);
-- create index "idx_loop_nested_recoveries_parent" to table: "loop_nested_recoveries"
CREATE INDEX `idx_loop_nested_recoveries_parent` ON `loop_nested_recoveries` (`workspace_id`, `parent_run_id`, `created_at`, `operation_id`);
-- create index "idx_loop_nested_recoveries_child" to table: "loop_nested_recoveries"
CREATE INDEX `idx_loop_nested_recoveries_child` ON `loop_nested_recoveries` (`workspace_id`, `child_run_id`, `created_at`, `operation_id`);
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
	DELETE FROM loop_nested_recoveries WHERE workspace_id = OLD.id;
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
