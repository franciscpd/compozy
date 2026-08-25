package globaldb

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/compozy/compozy/internal/store"
	"github.com/compozy/compozy/internal/testutil"
)

func TestGlobalDBNestedRecoveryMigrationShouldPreserveHistory(t *testing.T) {
	t.Parallel()
	testNestedRecoveryMigrationPreservesHistory(t)
}

func testNestedRecoveryMigrationPreservesHistory(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), GlobalDatabaseName)
	prefixDB, err := openGlobalMigrationPrefixDatabase(
		t, path, globalMigrationPrefixBefore(t, "00091_nested_loop_recovery.sql"),
	)
	if err != nil {
		t.Fatalf("openGlobalMigrationPrefixDatabase() error = %v", err)
	}
	ctx := testutil.Context(t)
	now := time.Date(2026, time.August, 25, 10, 0, 0, 0, time.UTC)
	prefixGlobalDB := &GlobalDB{db: prefixDB, path: path, now: func() time.Time { return now }}
	prefixGlobalDB.initializeRepositories(openConfig{})
	workspaceID := registerWorkspaceForGlobalTests(t, prefixGlobalDB, "nested-recovery-upgrade", t.TempDir())
	for _, runID := range []string{"run-source-v90", "run-fork-v90"} {
		if _, err := prefixDB.ExecContext(ctx, `INSERT INTO loop_runs (
			id, workspace_id, profile_id, loop_name, status, generation, reattempt_strategy,
			last_progress_at, inputs_json, created_at, started_at
		) VALUES (?, ?, ?, 'delivery', 'failed', 2, 'failed_only', ?, '{}', ?, ?)`,
			runID, workspaceID, store.DefaultProfileID, now, now, now); err != nil {
			t.Fatalf("insert v90 loop run %s error = %v", runID, err)
		}
	}
	generationRows := []struct {
		runID, origin      string
		generation, parent int
	}{
		{runID: "run-source-v90", origin: "initial", generation: 1},
		{runID: "run-source-v90", origin: "operator_rerun", generation: 2, parent: 1},
		{runID: "run-fork-v90", origin: "fork_seed", generation: 1},
		{runID: "run-fork-v90", origin: "initial", generation: 2, parent: 1},
	}
	for _, row := range generationRows {
		if _, err := prefixDB.ExecContext(ctx, `INSERT INTO loop_generations
			(loop_run_id, generation, parent_generation, origin, created_at) VALUES (?, ?, ?, ?, ?)`,
			row.runID, row.generation, row.parent, row.origin, now); err != nil {
			t.Fatalf("insert v90 generation %#v error = %v", row, err)
		}
	}
	digest := strings.Repeat("a", 64)
	if _, err := prefixDB.ExecContext(ctx, `INSERT INTO loop_timetravel_ops (
		workspace_id, op_id, kind, idempotency_key, request_digest, source_run_id,
		source_generation, from_node, item_index, actor_kind, actor_id, reason,
		result_run_id, result_generation, created_at
	) VALUES
		(?, 'op-rerun-v90', 'rerun', 'key-rerun-v90', ?, 'run-source-v90', 1,
		 'verify', 0, 'operator', 'qa', 'retry', 'run-source-v90', 2, ?),
		(?, 'op-fork-v90', 'fork', 'key-fork-v90', ?, 'run-source-v90', 2,
		 NULL, NULL, 'operator', 'qa', 'branch', 'run-fork-v90', NULL, ?)`,
		workspaceID, digest, now, workspaceID, strings.Repeat("b", 64), now); err != nil {
		t.Fatalf("insert v90 time-travel rows error = %v", err)
	}
	operationQuery := `SELECT json_object(
		'workspace_id', workspace_id, 'op_id', op_id, 'kind', kind,
		'idempotency_key', idempotency_key, 'request_digest', request_digest,
		'source_run_id', source_run_id, 'source_generation', source_generation,
		'from_node', from_node, 'item_index', item_index, 'actor_kind', actor_kind,
		'actor_id', actor_id, 'reason', reason, 'result_run_id', result_run_id,
		'result_generation', result_generation, 'created_at', created_at)
		FROM loop_timetravel_ops WHERE workspace_id = ? ORDER BY op_id`
	generationQuery := `SELECT json_object('loop_run_id', loop_run_id, 'generation', generation,
		'parent_generation', parent_generation, 'origin', origin, 'created_at', created_at)
		FROM loop_generations WHERE loop_run_id IN ('run-source-v90','run-fork-v90')
		ORDER BY loop_run_id, generation`
	beforeOps := readLoopMigrationRows(t, prefixDB, operationQuery, workspaceID)
	beforeGenerations := readLoopMigrationRows(t, prefixDB, generationQuery)
	if err := prefixDB.Close(); err != nil {
		t.Fatalf("Close(prefixDB) error = %v", err)
	}

	upgraded, err := openGlobalMigrationUpgrade(t, path)
	if err != nil {
		t.Fatalf("openGlobalMigrationUpgrade() error = %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close(testutil.Context(t)) })
	afterOps := readLoopMigrationRows(t, upgraded.db, operationQuery, workspaceID)
	afterGenerations := readLoopMigrationRows(t, upgraded.db, generationQuery)
	if !reflect.DeepEqual(afterOps, beforeOps) || !reflect.DeepEqual(afterGenerations, beforeGenerations) {
		t.Fatalf("migration changed durable history\nops before=%v after=%v\ngenerations before=%v after=%v",
			beforeOps, afterOps, beforeGenerations, afterGenerations)
	}
	var indexSQL string
	if err := upgraded.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index'
		AND name = 'uq_loop_timetravel_ops_idempotency'`).Scan(&indexSQL); err != nil {
		t.Fatalf("read idempotency index error = %v", err)
	}
	if !strings.Contains(indexSQL, "WHERE idempotency_key != ''") {
		t.Fatalf("idempotency index SQL = %q", indexSQL)
	}
}

func readLoopMigrationRows(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(testutil.Context(t), query, args...)
	if err != nil {
		t.Fatalf("read migration rows error = %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close migration rows error = %v", err)
		}
	}()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan migration row error = %v", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration rows error = %v", err)
	}
	return values
}
