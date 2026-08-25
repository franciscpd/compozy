package globaldb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	looppkg "github.com/compozy/compozy/internal/loop"
)

const loopTimeTravelKindNestedRecovery = "nested_recovery"

var _ looppkg.NestedRecoveryStore = (*LoopRepo)(nil)

// LookupNestedRecoveryReplay returns immutable recovery evidence before lineage replanning.
func (g *LoopRepo) LookupNestedRecoveryReplay(
	ctx context.Context,
	workspaceID looppkg.WorkspaceID,
	idempotencyKey string,
	requestDigest string,
) (looppkg.NestedRecoveryResult, bool, error) {
	if err := g.checkReady(ctx, "lookup nested Loop recovery replay"); err != nil {
		return looppkg.NestedRecoveryResult{}, false, err
	}
	prior, found, err := getTimeTravelReplay(ctx, g.db, workspaceID, strings.TrimSpace(idempotencyKey))
	if err != nil || !found {
		return looppkg.NestedRecoveryResult{}, false, err
	}
	if prior.digest != requestDigest || prior.kind != loopTimeTravelKindNestedRecovery {
		return looppkg.NestedRecoveryResult{}, false, timeTravelKeyReuseError(idempotencyKey)
	}
	result, found, err := getNestedRecoveryResult(ctx, g.db, workspaceID, prior.operationID)
	if err != nil || !found {
		return result, found, err
	}
	result.Replayed = true
	return result, true, nil
}

// CreateNestedRecovery atomically reactivates an existing direct parent/child lineage.
func (g *LoopRepo) CreateNestedRecovery(
	ctx context.Context,
	request looppkg.NestedRecoveryStoreRequest,
) (result looppkg.NestedRecoveryResult, replayed bool, err error) {
	if err := g.checkReady(ctx, "create nested Loop recovery"); err != nil {
		return result, false, err
	}
	if err := validateNestedRecoveryStoreRequest(request); err != nil {
		return result, false, err
	}
	err = g.withTaskImmediateTransaction(ctx, "create nested Loop recovery", func(exec taskSQLExecutor) error {
		return g.createNestedRecoveryWithExecutor(ctx, exec, request, &result, &replayed)
	})
	return result, replayed, err
}

func (g *LoopRepo) createNestedRecoveryWithExecutor(
	ctx context.Context,
	exec taskSQLExecutor,
	request looppkg.NestedRecoveryStoreRequest,
	result *looppkg.NestedRecoveryResult,
	replayed *bool,
) error {
	prior, found, err := getTimeTravelReplay(ctx, exec, request.WorkspaceID, request.IdempotencyKey)
	if err != nil {
		return err
	}
	if found {
		if prior.digest != request.RequestDigest || prior.kind != loopTimeTravelKindNestedRecovery {
			return timeTravelKeyReuseError(request.IdempotencyKey)
		}
		stored, storedFound, err := getNestedRecoveryResult(ctx, exec, request.WorkspaceID, prior.operationID)
		if err != nil {
			return err
		}
		if !storedFound {
			return fmt.Errorf("store: nested recovery replay %q has no result row", prior.operationID)
		}
		stored.Replayed = true
		*result, *replayed = stored, true
		return nil
	}
	parent, child, err := loadAndValidateNestedRecoveryLineage(ctx, exec, request)
	if err != nil {
		return err
	}
	if err := looppkg.ValidateNestedRecoveryBudget(parent, int(request.ParentIntent.Generation), request.At); err != nil {
		return err
	}
	if err := looppkg.ValidateNestedRecoveryBudget(child, int(request.ChildIntent.Generation), request.At); err != nil {
		return err
	}
	if err := insertNestedRecoveryGenerationState(ctx, exec, request); err != nil {
		return err
	}
	if err := g.reactivateNestedRecoveryRun(ctx, exec, &child, request.ChildIntent, request.At); err != nil {
		return err
	}
	if err := g.reactivateNestedRecoveryRun(ctx, exec, &parent, request.ParentIntent, request.At); err != nil {
		return err
	}
	if err := insertTimeTravelOp(ctx, exec, request.WorkspaceID, request.Operation); err != nil {
		return err
	}
	if err := insertNestedRecoveryResult(ctx, exec, request); err != nil {
		return err
	}
	*result = nestedRecoveryResultFromRequest(request)
	return nil
}

func validateNestedRecoveryStoreRequest(request looppkg.NestedRecoveryStoreRequest) error {
	if request.Parent == nil || request.Child == nil || request.WorkspaceID == "" {
		return fmt.Errorf("%w: nested recovery parent, child, and workspace are required", looppkg.ErrValidation)
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" || len(strings.TrimSpace(request.RequestDigest)) != 64 {
		return fmt.Errorf("%w: nested recovery request identity is invalid", looppkg.ErrValidation)
	}
	if request.Operation.Kind != loopTimeTravelKindNestedRecovery ||
		request.ParentIntent.Origin != looppkg.OriginNestedRecovery ||
		request.ChildIntent.Origin != looppkg.OriginNestedRecovery {
		return fmt.Errorf("%w: nested recovery provenance is invalid", looppkg.ErrValidation)
	}
	if err := request.ParentIntent.Validate(); err != nil {
		return err
	}
	if err := request.ChildIntent.Validate(); err != nil {
		return err
	}
	if request.Target.ChildRunID != request.Child.ID || strings.TrimSpace(request.TaskID) == "" ||
		strings.TrimSpace(request.Runtime.Runtime.Provider) == "" || strings.TrimSpace(request.Runtime.Runtime.Model) == "" {
		return fmt.Errorf("%w: nested recovery target, task, and exact runtime are required", looppkg.ErrValidation)
	}
	return nil
}

func loadAndValidateNestedRecoveryLineage(
	ctx context.Context,
	exec taskSQLExecutor,
	request looppkg.NestedRecoveryStoreRequest,
) (looppkg.Run, looppkg.Run, error) {
	parent, err := getLoopRunByIDWithExecutor(ctx, exec, request.Parent.ID)
	if err != nil {
		return parent, looppkg.Run{}, err
	}
	child, err := getLoopRunByIDWithExecutor(ctx, exec, request.Child.ID)
	if err != nil {
		return parent, child, err
	}
	if parent.WorkspaceID != request.WorkspaceID || child.WorkspaceID != request.WorkspaceID ||
		child.ParentLoopRunID != parent.ID || parent.Generation != request.Parent.Generation ||
		child.Generation != request.Child.Generation || !nestedRecoveryStoreStatus(parent.Status) ||
		!nestedRecoveryStoreStatus(child.Status) {
		return parent, child, nestedRecoveryStoreConflict("lineage changed after planning")
	}
	if int64(parent.Generation) != request.ParentIntent.ParentGeneration ||
		int64(child.Generation) != request.ChildIntent.ParentGeneration {
		return parent, child, nestedRecoveryStoreConflict("source generation changed after planning")
	}
	var status string
	var childRunID sql.NullString
	err = exec.QueryRowContext(ctx, `SELECT status, child_loop_run_id FROM loop_generation_outputs
		WHERE loop_run_id = ? AND generation = ? AND node_id = ? AND item_index = ?`,
		parent.ID, parent.Generation, request.Target.ParentNodeID, request.Target.ParentItemIndex,
	).Scan(&status, &childRunID)
	if err != nil || status != "failed" || !childRunID.Valid || childRunID.String != string(child.ID) {
		return parent, child, nestedRecoveryStoreConflict("parent no longer points to the failed child")
	}
	return parent, child, nil
}

func nestedRecoveryStoreStatus(status looppkg.Status) bool {
	switch status {
	case looppkg.StatusFailed, looppkg.StatusExhausted, looppkg.StatusStalled:
		return true
	default:
		return false
	}
}

func nestedRecoveryStoreConflict(detail string) error {
	return &looppkg.ReasonError{
		Code: looppkg.ReasonCodeNestedRecoveryConflict,
		Err:  fmt.Errorf("%w: %s", looppkg.ErrNestedRecoveryConflict, detail),
	}
}

func insertNestedRecoveryGenerationState(
	ctx context.Context,
	exec taskSQLExecutor,
	request looppkg.NestedRecoveryStoreRequest,
) error {
	if err := insertLoopGenerationWithExecutor(ctx, exec, request.Child.ID, request.ChildIntent, request.At); err != nil {
		return err
	}
	if err := insertNestedRecoveryOutputs(ctx, exec, request.Child.ID, request.ChildOutputs); err != nil {
		return err
	}
	if err := insertLoopGenerationWithExecutor(ctx, exec, request.Parent.ID, request.ParentIntent, request.At); err != nil {
		return err
	}
	return insertNestedRecoveryOutputs(ctx, exec, request.Parent.ID, request.ParentOutputs)
}

func insertNestedRecoveryOutputs(
	ctx context.Context,
	exec taskSQLExecutor,
	runID looppkg.RunID,
	outputs []looppkg.GenerationOutput,
) error {
	for _, output := range outputs {
		var resolved any
		if output.ResolvedRuntime != nil {
			raw, err := json.Marshal(output.ResolvedRuntime)
			if err != nil {
				return fmt.Errorf("store: encode nested recovery output runtime: %w", err)
			}
			resolved = string(raw)
		}
		_, err := exec.ExecContext(ctx, `INSERT INTO loop_generation_outputs (
			loop_run_id, generation, node_id, item_index, output_id, artifact_name, status,
			output_ref, task_run_id, child_loop_run_id, resolved_runtime_json, attempt,
			next_attempt_at, first_scheduled_at, epoch
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, output.Generation, output.NodeID, output.ItemIndex,
			nullTrimmed(output.OutputID), nullTrimmed(output.ArtifactName), output.Status,
			nullTrimmed(output.OutputRef), nullTrimmed(output.TaskRunID), nullTrimmed(output.ChildLoopRunID),
			resolved, maxInt(output.Attempt, 1), output.NextAttemptAt, output.FirstScheduledAt, output.Epoch,
		)
		if err != nil {
			return fmt.Errorf("store: insert nested recovery output %s/%d: %w", output.NodeID, output.ItemIndex, err)
		}
	}
	return nil
}

func (g *LoopRepo) reactivateNestedRecoveryRun(
	ctx context.Context,
	exec taskSQLExecutor,
	run *looppkg.Run,
	intent looppkg.GenerationIntent,
	at time.Time,
) error {
	fromStatus := run.Status
	result, err := exec.ExecContext(ctx, `UPDATE loop_runs SET
		status = 'running', completion_state = 'complete', active_gate_id = '',
		active_human_criteria_json = '[]', generation = ?, last_progress_at = ?
		WHERE id = ? AND workspace_id = ? AND generation = ? AND status = ?
			AND pause_requested = 0 AND cancel_requested = 0`,
		intent.Generation, at.UTC(), run.ID, run.WorkspaceID, run.Generation, run.Status)
	if err != nil {
		return fmt.Errorf("store: reactivate nested recovery run %q: %w", run.ID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return nestedRecoveryStoreConflict("run changed during reactivation")
	}
	if err := appendLoopGenerationStartedEventWithExecutor(ctx, exec, *run, intent, at); err != nil {
		return err
	}
	run.Status, run.Generation = looppkg.StatusRunning, int(intent.Generation)
	if err := g.repairLoopCoordinatorTaskWithExecutor(
		ctx, exec, *run, loopCoordinatorTaskID(run.ID), at,
	); err != nil && !errorsIsTaskNotFound(err) {
		return err
	}
	_, _, err = g.reserveLoopCoordinatorRunWithExecutor(
		ctx, exec, *run, loopCoordinatorStartOrigin(), at,
		loopCoordinatorRunID(run.ID, run.Generation), loopCoordinatorIdempotencyKey(run.ID, run.Generation),
	)
	if err != nil {
		return err
	}
	return appendLoopRunStatusEvent(
		ctx, exec, run.ID, run.WorkspaceID, fromStatus, looppkg.StatusRunning,
		looppkg.TransitionCauseNestedRecovery, at,
	)
}

func insertNestedRecoveryResult(
	ctx context.Context,
	exec taskSQLExecutor,
	request looppkg.NestedRecoveryStoreRequest,
) error {
	runtimeJSON, err := json.Marshal(request.Runtime)
	if err != nil {
		return fmt.Errorf("store: encode nested recovery runtime: %w", err)
	}
	_, err = exec.ExecContext(ctx, `INSERT INTO loop_nested_recoveries (
		workspace_id, operation_id, parent_run_id, parent_generation, parent_node_id,
		parent_item_index, child_run_id, child_generation, child_node_id, child_item_index,
		task_id, runtime_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.WorkspaceID, request.Operation.ID, request.Parent.ID, request.ParentIntent.Generation,
		request.Target.ParentNodeID, request.Target.ParentItemIndex, request.Child.ID,
		request.ChildIntent.Generation, request.Target.ChildNodeID, request.Target.ChildItemIndex,
		request.TaskID, string(runtimeJSON), request.At.UTC(),
	)
	if err != nil {
		return fmt.Errorf("store: insert nested recovery result: %w", err)
	}
	return nil
}

func nestedRecoveryResultFromRequest(request looppkg.NestedRecoveryStoreRequest) looppkg.NestedRecoveryResult {
	return looppkg.NestedRecoveryResult{
		OperationID: request.Operation.ID,
		ParentRunID: request.Parent.ID, ParentGeneration: request.ParentIntent.Generation,
		ChildRunID: request.Child.ID, ChildGeneration: request.ChildIntent.Generation,
		TaskID: request.TaskID, Runtime: request.Runtime,
	}
}

func getNestedRecoveryResult(
	ctx context.Context,
	exec taskSQLExecutor,
	workspaceID looppkg.WorkspaceID,
	operationID string,
) (looppkg.NestedRecoveryResult, bool, error) {
	var result looppkg.NestedRecoveryResult
	var runtimeJSON string
	err := exec.QueryRowContext(ctx, `SELECT operation_id, parent_run_id, parent_generation,
		child_run_id, child_generation, task_id, runtime_json FROM loop_nested_recoveries
		WHERE workspace_id = ? AND operation_id = ?`, workspaceID, operationID).Scan(
		&result.OperationID, &result.ParentRunID, &result.ParentGeneration,
		&result.ChildRunID, &result.ChildGeneration, &result.TaskID, &runtimeJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, fmt.Errorf("store: get nested recovery result: %w", err)
	}
	if err := json.Unmarshal([]byte(runtimeJSON), &result.Runtime); err != nil {
		return result, false, fmt.Errorf("store: decode nested recovery result runtime: %w", err)
	}
	return result, true, nil
}

// ListNestedRecoveries lists ordered recovery evidence for either side of a lineage.
func (g *LoopRepo) ListNestedRecoveries(
	ctx context.Context,
	workspaceID looppkg.WorkspaceID,
	runID looppkg.RunID,
) (results []looppkg.NestedRecoveryResult, err error) {
	if err := g.checkReady(ctx, "list nested Loop recoveries"); err != nil {
		return nil, err
	}
	rows, err := g.db.QueryContext(ctx, `SELECT operation_id, parent_run_id, parent_generation,
		child_run_id, child_generation, task_id, runtime_json FROM loop_nested_recoveries
		WHERE workspace_id = ? AND (parent_run_id = ? OR child_run_id = ?)
		ORDER BY CASE WHEN parent_run_id = ? THEN parent_generation ELSE child_generation END ASC,
			created_at ASC, operation_id ASC`, workspaceID, runID, runID, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list nested recoveries: %w", err)
	}
	defer func() { err = joinRowsCloseError(rows, err, "nested recoveries") }()
	for rows.Next() {
		var result looppkg.NestedRecoveryResult
		var runtimeJSON string
		if err := rows.Scan(
			&result.OperationID, &result.ParentRunID, &result.ParentGeneration,
			&result.ChildRunID, &result.ChildGeneration, &result.TaskID, &runtimeJSON,
		); err != nil {
			return nil, fmt.Errorf("store: scan nested recovery: %w", err)
		}
		if err := json.Unmarshal([]byte(runtimeJSON), &result.Runtime); err != nil {
			return nil, fmt.Errorf("store: decode nested recovery runtime: %w", err)
		}
		results = append(results, result)
	}
	return results, rows.Err()
}
