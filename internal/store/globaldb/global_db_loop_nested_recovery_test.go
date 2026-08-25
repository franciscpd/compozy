package globaldb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	looppkg "github.com/compozy/compozy/internal/loop"
	"github.com/compozy/compozy/internal/loop/dsl"
	"github.com/compozy/compozy/internal/testutil"
)

func TestGlobalDBNestedRecoveryShouldSerializeConcurrentRequests(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		different  bool
		wantReplay int
		wantError  int
	}{
		{name: "Should replay concurrent identical request ids", wantReplay: 1},
		{name: "Should conflict concurrent different request ids for one cell", different: true, wantError: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			db := openLoopTestGlobalDB(t)
			ctx := testutil.Context(t)
			first := nestedRecoveryStoreFixture(t, ctx, db, strings.ReplaceAll(test.name, " ", "-"))
			second := first
			if test.different {
				second.IdempotencyKey += "-other"
				second.Operation.ID += "-other"
				second.Operation.IdempotencyKey = second.IdempotencyKey
				second.Operation.RequestDigest = strings.Repeat("c", 64)
				second.RequestDigest = second.Operation.RequestDigest
			}

			start := make(chan struct{})
			type outcome struct {
				replayed bool
				err      error
			}
			outcomes := make(chan outcome, 2)
			var wg sync.WaitGroup
			for _, request := range []looppkg.NestedRecoveryStoreRequest{first, second} {
				request := request
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, replayed, err := db.CreateNestedRecovery(ctx, request)
					outcomes <- outcome{replayed: replayed, err: err}
				}()
			}
			close(start)
			wg.Wait()
			close(outcomes)

			var replayCount, errorCount int
			for got := range outcomes {
				if got.replayed {
					replayCount++
				}
				if got.err != nil {
					if !errors.Is(got.err, looppkg.ErrNestedRecoveryConflict) {
						t.Fatalf("concurrent CreateNestedRecovery() error = %v", got.err)
					}
					errorCount++
				}
			}
			if replayCount != test.wantReplay || errorCount != test.wantError {
				t.Fatalf("concurrent outcomes replay=%d error=%d, want replay=%d error=%d",
					replayCount, errorCount, test.wantReplay, test.wantError)
			}
			if got := countCoordinatorTaskRunsForLoop(ctx, t, db, first.Parent.ID); got != 2 {
				t.Fatalf("parent coordinator reservations = %d, want initial plus one recovery", got)
			}
			if got := countCoordinatorTaskRunsForLoop(ctx, t, db, first.Child.ID); got != 2 {
				t.Fatalf("child coordinator reservations = %d, want initial plus one recovery", got)
			}
		})
	}
}

func TestGlobalDBNestedRecoveryShouldCommitBothRunsAtomicallyAndReplayWithoutNewWork(t *testing.T) {
	t.Parallel()

	db := openLoopTestGlobalDB(t)
	ctx := testutil.Context(t)
	request := nestedRecoveryStoreFixture(t, ctx, db, "atomic")

	result, replayed, err := db.CreateNestedRecovery(ctx, request)
	if err != nil || replayed {
		t.Fatalf("CreateNestedRecovery() = %#v replayed=%v error=%v", result, replayed, err)
	}
	if result.ParentRunID != request.Parent.ID || result.ParentGeneration != 2 ||
		result.ChildRunID != request.Child.ID || result.ChildGeneration != 2 || result.TaskID != request.TaskID {
		t.Fatalf("CreateNestedRecovery() result = %#v", result)
	}
	assertNestedRecoveryRunState(t, ctx, db, request.Parent.ID, 2, request.Parent.TokensUsed)
	assertNestedRecoveryRunState(t, ctx, db, request.Child.ID, 2, request.Child.TokensUsed)
	if got := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Parent.ID); got != 2 {
		t.Fatalf("parent coordinator reservations = %d, want initial plus recovery", got)
	}
	if got := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Child.ID); got != 2 {
		t.Fatalf("child coordinator reservations = %d, want initial plus recovery", got)
	}
	runtime, found, err := db.GetNestedRecoveryRuntime(ctx, looppkg.NestedRecoveryRuntimeKey{
		WorkspaceID: request.WorkspaceID, RunID: request.Child.ID, Generation: 2,
		NodeID: request.Target.ChildNodeID, ItemIndex: request.Target.ChildItemIndex,
	})
	if err != nil || !found || runtime.Provider != "codex" || runtime.Model != "gpt-5.6" {
		t.Fatalf("GetNestedRecoveryRuntime() = %#v found=%v error=%v", runtime, found, err)
	}
	assertNestedRecoveryOutputFidelity(t, ctx, db, request)
	for _, runID := range []looppkg.RunID{request.Parent.ID, request.Child.ID} {
		recoveries, err := db.ListNestedRecoveries(ctx, request.WorkspaceID, runID)
		if err != nil || len(recoveries) != 1 || recoveries[0].OperationID != result.OperationID ||
			recoveries[0].Runtime.Runtime.Model != "gpt-5.6" {
			t.Fatalf("ListNestedRecoveries(%s) = %#v error=%v", runID, recoveries, err)
		}
	}

	parentReservations := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Parent.ID)
	childReservations := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Child.ID)
	if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET generation = 3, status = 'done'
		WHERE id IN (?, ?)`, request.Parent.ID, request.Child.ID); err != nil {
		t.Fatalf("advance recovered lineage before replay error = %v", err)
	}
	replay, replayed, err := db.CreateNestedRecovery(ctx, request)
	if err != nil || !replayed || replay.OperationID != result.OperationID {
		t.Fatalf("CreateNestedRecovery(replay) = %#v replayed=%v error=%v", replay, replayed, err)
	}
	if countCoordinatorTaskRunsForLoop(ctx, t, db, request.Parent.ID) != parentReservations ||
		countCoordinatorTaskRunsForLoop(ctx, t, db, request.Child.ID) != childReservations {
		t.Fatal("replay reserved additional coordinator work")
	}

	mismatch := request
	mismatch.RequestDigest = strings.Repeat("b", 64)
	if _, _, err := db.CreateNestedRecovery(ctx, mismatch); !errors.Is(err, looppkg.ErrTimeTravelKeyReuse) {
		t.Fatalf("CreateNestedRecovery(key reuse) error = %v, want ErrTimeTravelKeyReuse", err)
	}
}

func TestGlobalDBNestedRecoveryShouldListAuthoritativeHistoryByGeneration(t *testing.T) {
	t.Parallel()

	db := openLoopTestGlobalDB(t)
	ctx := testutil.Context(t)
	request := nestedRecoveryStoreFixture(t, ctx, db, "ordered-history")
	first, _, err := db.CreateNestedRecovery(ctx, request)
	if err != nil {
		t.Fatalf("CreateNestedRecovery() error = %v", err)
	}
	secondOperationID := "loopop-recovery-ordered-history-second"
	earlier := request.At.Add(-time.Hour)
	if _, err := db.db.ExecContext(ctx, `INSERT INTO loop_timetravel_ops (
		workspace_id, op_id, kind, idempotency_key, request_digest, source_run_id,
		source_generation, from_node, item_index, actor_kind, actor_id, result_run_id,
		result_generation, created_at
	) VALUES (?, ?, 'nested_recovery', ?, ?, ?, 2, 'delivery', 0, 'operator',
		'operator:recovery', ?, 3, ?)`,
		request.WorkspaceID, secondOperationID, "recovery-ordered-history-second",
		strings.Repeat("d", 64), request.Parent.ID, request.Parent.ID, earlier,
	); err != nil {
		t.Fatalf("insert second nested recovery operation error = %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO loop_nested_recoveries (
		workspace_id, operation_id, parent_run_id, parent_generation, parent_node_id,
		parent_item_index, child_run_id, child_generation, child_node_id, child_item_index,
		task_id, runtime_json, created_at
	) VALUES (?, ?, ?, 3, 'delivery', 0, ?, 3, 'implement', 1, 'task-1',
		'{"runtime":{"provider":"codex","model":"gpt-5.6"},"source":{}}', ?)`,
		request.WorkspaceID, secondOperationID, request.Parent.ID, request.Child.ID, earlier,
	); err != nil {
		t.Fatalf("insert second nested recovery result error = %v", err)
	}

	recoveries, err := db.ListNestedRecoveries(ctx, request.WorkspaceID, request.Parent.ID)
	if err != nil {
		t.Fatalf("ListNestedRecoveries() error = %v", err)
	}
	if len(recoveries) != 2 || recoveries[0].OperationID != first.OperationID ||
		recoveries[1].OperationID != secondOperationID {
		t.Fatalf("ListNestedRecoveries() = %#v, want parent generations 2 then 3", recoveries)
	}
}

func TestGlobalDBNestedRecoveryShouldRejectChangedLineageAndBudgetsBeforeEitherReactivation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(context.Context, *testing.T, *GlobalDB, looppkg.NestedRecoveryStoreRequest)
		want   error
	}{
		{name: "Should reject a parent generation changed after planning", mutate: func(
			ctx context.Context, t *testing.T, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
		) {
			if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET generation = generation + 1 WHERE id = ?`, request.Parent.ID); err != nil {
				t.Fatalf("advance parent generation error = %v", err)
			}
		}, want: looppkg.ErrNestedRecoveryConflict},
		{name: "Should reject a child ownership change after planning", mutate: func(
			ctx context.Context, t *testing.T, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
		) {
			if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET parent_loop_run_id = NULL WHERE id = ?`, request.Child.ID); err != nil {
				t.Fatalf("detach child error = %v", err)
			}
		}, want: looppkg.ErrNestedRecoveryConflict},
		{name: "Should reject exhausted child tokens", mutate: func(
			ctx context.Context, t *testing.T, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
		) {
			if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET budget_tokens = 10, tokens_used = 10 WHERE id = ?`, request.Child.ID); err != nil {
				t.Fatalf("exhaust child budget error = %v", err)
			}
		}, want: looppkg.ErrNestedRecoveryBudgetExhausted},
		{name: "Should reject exhausted parent iteration cap", mutate: func(
			ctx context.Context, t *testing.T, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
		) {
			if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET iteration_cap = generation WHERE id = ?`, request.Parent.ID); err != nil {
				t.Fatalf("exhaust parent iteration cap error = %v", err)
			}
		}, want: looppkg.ErrNestedRecoveryBudgetExhausted},
		{name: "Should reject expired child deadline", mutate: func(
			ctx context.Context, t *testing.T, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
		) {
			startedAt := request.At.Add(-time.Duration(request.Child.BudgetWallSec) * time.Second)
			if _, err := db.db.ExecContext(
				ctx,
				`UPDATE loop_runs SET started_at = ? WHERE id = ?`,
				startedAt.Format(time.RFC3339Nano),
				request.Child.ID,
			); err != nil {
				t.Fatalf("expire child deadline error = %v", err)
			}
		}, want: looppkg.ErrNestedRecoveryBudgetExhausted},
		{name: "Should reject a paused parent", mutate: func(
			ctx context.Context, t *testing.T, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
		) {
			if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET pause_requested = 1 WHERE id = ?`, request.Parent.ID); err != nil {
				t.Fatalf("pause parent error = %v", err)
			}
		}, want: looppkg.ErrNestedRecoveryConflict},
		{name: "Should reject a canceled child", mutate: func(
			ctx context.Context, t *testing.T, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
		) {
			if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET cancel_requested = 1 WHERE id = ?`, request.Child.ID); err != nil {
				t.Fatalf("cancel child error = %v", err)
			}
		}, want: looppkg.ErrNestedRecoveryConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			db := openLoopTestGlobalDB(t)
			ctx := testutil.Context(t)
			request := nestedRecoveryStoreFixture(t, ctx, db, strings.ReplaceAll(test.name, " ", "-"))
			test.mutate(ctx, t, db, request)
			beforeParent, err := db.GetLoopRunByID(ctx, request.Parent.ID)
			if err != nil {
				t.Fatalf("GetLoopRunByID(parent before) error = %v", err)
			}
			beforeChild, err := db.GetLoopRunByID(ctx, request.Child.ID)
			if err != nil {
				t.Fatalf("GetLoopRunByID(child before) error = %v", err)
			}
			beforeParentRuns := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Parent.ID)
			beforeChildRuns := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Child.ID)
			_, _, err = db.CreateNestedRecovery(ctx, request)
			if !errors.Is(err, test.want) {
				t.Fatalf("CreateNestedRecovery() error = %v, want %v", err, test.want)
			}
			assertNoNestedRecoveryMutation(
				t, ctx, db, request, beforeParent, beforeChild, beforeParentRuns, beforeChildRuns,
			)
		})
	}
}

func TestGlobalDBNestedRecoveryShouldRollbackEveryDurableWriteStage(t *testing.T) {
	t.Parallel()

	stages := []struct {
		name    string
		trigger string
	}{
		{name: "generation intent", trigger: `CREATE TRIGGER fail_nested_generation
			BEFORE INSERT ON loop_generations WHEN NEW.generation = 2 BEGIN SELECT RAISE(ABORT, 'fault generation'); END`},
		{name: "generation output", trigger: `CREATE TRIGGER fail_nested_output
			BEFORE INSERT ON loop_generation_outputs WHEN NEW.generation = 2 BEGIN SELECT RAISE(ABORT, 'fault output'); END`},
		{name: "run reactivation", trigger: `CREATE TRIGGER fail_nested_reactivation
			BEFORE UPDATE OF generation ON loop_runs WHEN NEW.generation = 2 BEGIN SELECT RAISE(ABORT, 'fault run'); END`},
		{name: "generation event", trigger: `CREATE TRIGGER fail_nested_event
			BEFORE INSERT ON loop_run_events WHEN NEW.kind = 'generation_started' BEGIN SELECT RAISE(ABORT, 'fault event'); END`},
		{name: "coordinator reservation", trigger: `CREATE TRIGGER fail_nested_reservation
			BEFORE INSERT ON task_runs WHEN NEW.run_kind = 'coordinator' BEGIN SELECT RAISE(ABORT, 'fault reservation'); END`},
		{name: "time travel operation", trigger: `CREATE TRIGGER fail_nested_operation
			BEFORE INSERT ON loop_timetravel_ops WHEN NEW.kind = 'nested_recovery' BEGIN SELECT RAISE(ABORT, 'fault operation'); END`},
		{name: "recovery result", trigger: `CREATE TRIGGER fail_nested_result
			BEFORE INSERT ON loop_nested_recoveries BEGIN SELECT RAISE(ABORT, 'fault result'); END`},
	}
	for _, stage := range stages {
		stage := stage
		t.Run(stage.name, func(t *testing.T) {
			t.Parallel()
			db := openLoopTestGlobalDB(t)
			ctx := testutil.Context(t)
			request := nestedRecoveryStoreFixture(t, ctx, db, "rollback-"+strings.ReplaceAll(stage.name, " ", "-"))
			beforeParent, err := db.GetLoopRunByID(ctx, request.Parent.ID)
			if err != nil {
				t.Fatalf("GetLoopRunByID(parent before) error = %v", err)
			}
			beforeChild, err := db.GetLoopRunByID(ctx, request.Child.ID)
			if err != nil {
				t.Fatalf("GetLoopRunByID(child before) error = %v", err)
			}
			beforeParentRuns := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Parent.ID)
			beforeChildRuns := countCoordinatorTaskRunsForLoop(ctx, t, db, request.Child.ID)
			if _, err := db.db.ExecContext(ctx, stage.trigger); err != nil {
				t.Fatalf("install %s fault trigger error = %v", stage.name, err)
			}

			if _, _, err := db.CreateNestedRecovery(ctx, request); err == nil {
				t.Fatalf("CreateNestedRecovery(%s fault) error = nil", stage.name)
			}
			assertNoNestedRecoveryMutation(
				t, ctx, db, request, beforeParent, beforeChild, beforeParentRuns, beforeChildRuns,
			)
		})
	}
}

func nestedRecoveryStoreFixture(
	t *testing.T,
	ctx context.Context,
	db *GlobalDB,
	suffix string,
) looppkg.NestedRecoveryStoreRequest {
	t.Helper()
	now := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	parent := testLoopRun("looprun-parent-"+suffix, now, looppkg.StatusRunning)
	parent.IterationCap, parent.BudgetTokens, parent.BudgetWallSec = 5, 1000, 3600
	createdParent, err := db.CreateLoopRunForStart(ctx, parent, dsl.ConcurrencyAllow)
	if err != nil {
		t.Fatalf("CreateLoopRunForStart(parent) error = %v", err)
	}
	child := testLoopRun("looprun-child-"+suffix, now.Add(time.Second), looppkg.StatusRunning)
	child.ParentLoopRunID = createdParent.ID
	child.IterationCap, child.BudgetTokens, child.BudgetWallSec = 5, 1000, 3600
	createdChild, err := db.CreateLoopRunForStart(ctx, child, dsl.ConcurrencyAllow)
	if err != nil {
		t.Fatalf("CreateLoopRunForStart(child) error = %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO loop_generation_outputs (
		loop_run_id, generation, node_id, item_index, status, child_loop_run_id, task_run_id, attempt, epoch
	) VALUES (?, 1, 'delivery', 0, 'failed', ?, 'task-parent-old', 2, 4)`,
		createdParent.ID, createdChild.ID); err != nil {
		t.Fatalf("insert parent output error = %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO loop_generation_outputs (
		loop_run_id, generation, node_id, item_index, output_id, artifact_name, status,
		output_ref, task_run_id, resolved_runtime_json, attempt, epoch
	) VALUES
		(?, 1, 'implement', 0, 'sibling', 'result.json', 'succeeded', 'sha256:sibling',
		 'task-sibling', '{"runtime":{"provider":"claude","model":"opus"},"source":{}}', 2, 7),
		(?, 1, 'implement', 1, NULL, NULL, 'failed', 'sha256:failed', 'task-failed', NULL, 3, 8),
		(?, 1, 'implement', 2, NULL, NULL, 'failed', 'sha256:still-failed', 'task-still-failed', NULL, 2, 6)`,
		createdChild.ID, createdChild.ID, createdChild.ID); err != nil {
		t.Fatalf("insert child outputs error = %v", err)
	}
	for _, runID := range []looppkg.RunID{createdParent.ID, createdChild.ID} {
		if _, err := db.db.ExecContext(ctx, `UPDATE loop_runs SET status = 'failed', generation = 1,
			tokens_used = 25 WHERE id = ?`, runID); err != nil {
			t.Fatalf("settle run %q error = %v", runID, err)
		}
		if _, err := db.db.ExecContext(ctx, `UPDATE task_runs SET status = 'completed', ended_at = ?
			WHERE loop_run_id = ? AND run_kind = 'coordinator'`, now.Add(time.Minute), runID); err != nil {
			t.Fatalf("complete coordinator %q error = %v", runID, err)
		}
	}
	createdParent.Status, createdParent.Generation, createdParent.TokensUsed = looppkg.StatusFailed, 1, 25
	createdChild.Status, createdChild.Generation, createdChild.TokensUsed = looppkg.StatusFailed, 1, 25
	parentGeneration := int64(2)
	parentItemIndex := 0
	sourceGeneration := int64(1)
	digest := strings.Repeat("a", 64)
	return looppkg.NestedRecoveryStoreRequest{
		WorkspaceID: createdParent.WorkspaceID,
		Parent:      &createdParent,
		Child:       &createdChild,
		ParentIntent: looppkg.GenerationIntent{
			Generation: 2, ParentGeneration: 1, Origin: looppkg.OriginNestedRecovery,
		},
		ChildIntent: looppkg.GenerationIntent{
			Generation: 2, ParentGeneration: 1, Origin: looppkg.OriginNestedRecovery,
		},
		ParentOutputs: []looppkg.GenerationOutput{
			{Generation: 2, NodeID: "delivery", Status: "awaiting_child", ChildLoopRunID: string(createdChild.ID), Attempt: 1},
		},
		ChildOutputs: []looppkg.GenerationOutput{
			{Generation: 2, NodeID: "implement", ItemIndex: 0, OutputID: "sibling", ArtifactName: "result.json",
				Status: "succeeded", OutputRef: "sha256:sibling", TaskRunID: "task-sibling",
				ResolvedRuntime: &looppkg.ResolvedRuntime{Runtime: looppkg.RuntimeSpec{Provider: "claude", Model: "opus"}},
				Attempt:         2, Epoch: 7},
			{Generation: 2, NodeID: "implement", ItemIndex: 1, Status: "pending", Attempt: 1},
			{Generation: 2, NodeID: "implement", ItemIndex: 2, Status: "failed",
				OutputRef: "sha256:still-failed", TaskRunID: "task-still-failed", Attempt: 2, Epoch: 6},
		},
		Target: looppkg.NestedRecoveryTarget{
			ParentNodeID: "delivery", ChildRunID: createdChild.ID,
			ChildNodeID: "implement", ChildItemIndex: 1,
		},
		TaskID: "task-1",
		Runtime: looppkg.ResolvedRuntime{
			Runtime: looppkg.RuntimeSpec{Provider: "codex", Model: "gpt-5.6", Reasoning: "high"},
			Source: looppkg.RuntimeProvenance{
				Provider: looppkg.RuntimeSourceRecovery, Model: looppkg.RuntimeSourceRecovery,
				Reasoning: looppkg.RuntimeSourceRecovery,
			},
		},
		Operation: looppkg.TimeTravelOp{
			ID: "loopop-recovery-" + suffix, Kind: "nested_recovery", IdempotencyKey: "recovery-" + suffix,
			RequestDigest: digest, SourceRunID: createdParent.ID, SourceGeneration: &sourceGeneration,
			FromNode: "delivery", Actor: operatorActorContextForTest("operator:recovery"),
			ItemIndex: &parentItemIndex, ResultRunID: createdParent.ID,
			ResultGeneration: &parentGeneration, CreatedAt: now.Add(2 * time.Minute),
		},
		RequestDigest:  digest,
		IdempotencyKey: "recovery-" + suffix,
		At:             now.Add(2 * time.Minute),
	}
}

func assertNestedRecoveryRunState(
	t *testing.T, ctx context.Context, db *GlobalDB, runID looppkg.RunID, generation int, tokens int64,
) {
	t.Helper()
	run, err := db.GetLoopRunByID(ctx, runID)
	if err != nil || run.Status != looppkg.StatusRunning || run.Generation != generation || run.TokensUsed != tokens {
		t.Fatalf("recovered run %q = %#v error=%v", runID, run, err)
	}
}

func assertNestedRecoveryOutputFidelity(
	t *testing.T, ctx context.Context, db *GlobalDB, request looppkg.NestedRecoveryStoreRequest,
) {
	t.Helper()
	var outputID, artifactName, outputRef, taskRunID, runtimeJSON string
	var attempt int
	var epoch int64
	if err := db.db.QueryRowContext(ctx, `SELECT output_id, artifact_name, output_ref, task_run_id,
		resolved_runtime_json, attempt, epoch FROM loop_generation_outputs
		WHERE loop_run_id = ? AND generation = 2 AND node_id = 'implement' AND item_index = 0`,
		request.Child.ID).Scan(&outputID, &artifactName, &outputRef, &taskRunID, &runtimeJSON, &attempt, &epoch); err != nil {
		t.Fatalf("read carried child output error = %v", err)
	}
	if outputID != "sibling" || artifactName != "result.json" || outputRef != "sha256:sibling" ||
		taskRunID != "task-sibling" || !strings.Contains(runtimeJSON, "claude") || attempt != 2 || epoch != 7 {
		t.Fatalf("carried child output lost fidelity: %q %q %q %q %q %d %d",
			outputID, artifactName, outputRef, taskRunID, runtimeJSON, attempt, epoch)
	}
	var childID string
	if err := db.db.QueryRowContext(ctx, `SELECT child_loop_run_id FROM loop_generation_outputs
		WHERE loop_run_id = ? AND generation = 2 AND node_id = 'delivery'`, request.Parent.ID).Scan(&childID); err != nil {
		t.Fatalf("read parent child binding error = %v", err)
	}
	if childID != string(request.Child.ID) {
		t.Fatalf("parent child binding = %q, want %q", childID, request.Child.ID)
	}
	var failedStatus, failedOutputRef, failedTaskRunID string
	if err := db.db.QueryRowContext(ctx, `SELECT status, output_ref, task_run_id
		FROM loop_generation_outputs WHERE loop_run_id = ? AND generation = 2
		AND node_id = 'implement' AND item_index = 2`, request.Child.ID).Scan(
		&failedStatus, &failedOutputRef, &failedTaskRunID,
	); err != nil {
		t.Fatalf("read unselected failed child output error = %v", err)
	}
	if failedStatus != "failed" || failedOutputRef != "sha256:still-failed" ||
		failedTaskRunID != "task-still-failed" {
		t.Fatalf("unselected failed child output = %q/%q/%q, want preserved failure",
			failedStatus, failedOutputRef, failedTaskRunID)
	}
}

func assertNoNestedRecoveryMutation(
	t *testing.T,
	ctx context.Context,
	db *GlobalDB,
	request looppkg.NestedRecoveryStoreRequest,
	beforeParent looppkg.Run,
	beforeChild looppkg.Run,
	beforeParentRuns int,
	beforeChildRuns int,
) {
	t.Helper()
	for _, before := range []looppkg.Run{beforeParent, beforeChild} {
		runID := before.ID
		run, err := db.GetLoopRunByID(ctx, runID)
		if err != nil || !reflect.DeepEqual(run, before) {
			t.Fatalf("rejected recovery mutated run %q: %#v error=%v", runID, run, err)
		}
		wantRuns := beforeChildRuns
		if runID == request.Parent.ID {
			wantRuns = beforeParentRuns
		}
		if countCoordinatorTaskRunsForLoop(ctx, t, db, runID) != wantRuns {
			t.Fatalf("rejected recovery reserved coordinator for %q", runID)
		}
	}
	var operationCount int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM loop_timetravel_ops
		WHERE workspace_id = ? AND op_id = ?`, request.WorkspaceID, request.Operation.ID).Scan(&operationCount); err != nil {
		t.Fatalf("count rejected recovery operation error = %v", err)
	}
	var recoveryCount int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM loop_nested_recoveries
		WHERE workspace_id = ? AND operation_id = ?`, request.WorkspaceID, request.Operation.ID).Scan(&recoveryCount); err != nil {
		t.Fatalf("count rejected recovery result error = %v", err)
	}
	if operationCount != 0 || recoveryCount != 0 {
		t.Fatalf("rejected recovery persisted operation=%d recovery=%d", operationCount, recoveryCount)
	}
}
