package globaldb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	looppkg "github.com/compozy/compozy/internal/loop"
	"github.com/compozy/compozy/internal/store/globaldb/sqlcgen"
)

type loopConfigPatchFlags struct {
	HumanGate        bool
	Reattempt        bool
	EnabledChecks    bool
	IterationCap     bool
	BudgetTokens     bool
	BudgetWallSec    bool
	BudgetOnExceeded bool
	NoProgressWindow bool
	FanOutWidth      bool
	GateMaxRevisions bool
	RuntimeDefaults  bool
	RuntimeRules     bool
	Environment      bool
}

func loopConfigPatchFlagsForStore(original looppkg.LoopConfig, normalized looppkg.LoopConfig) loopConfigPatchFlags {
	return loopConfigPatchFlags{
		HumanGate:        normalized.HumanGateEnabled != nil,
		Reattempt:        normalized.ReattemptStrategy != nil,
		EnabledChecks:    len(original.EnabledChecks) > 0,
		IterationCap:     normalized.IterationCap != nil,
		BudgetTokens:     normalized.BudgetTokens != nil,
		BudgetWallSec:    normalized.BudgetWallSec != nil,
		BudgetOnExceeded: normalized.BudgetOnExceeded != nil,
		NoProgressWindow: normalized.NoProgressWindow != nil,
		FanOutWidth:      normalized.FanOutWidth != nil,
		GateMaxRevisions: normalized.GateMaxRevisions != nil,
		RuntimeDefaults:  normalized.RuntimeDefaults != nil,
		RuntimeRules:     original.RuntimeRules != nil,
		Environment:      normalized.Environment != nil,
	}
}

func upsertLoopConfigWithExecutor(
	ctx context.Context,
	exec globalSQLExecutor,
	workspaceID string,
	loopName string,
	original looppkg.LoopConfig,
	normalized looppkg.LoopConfig,
) error {
	_, err := mutateLoopConfigWithExecutor(ctx, exec, workspaceID, loopName, original, normalized, nil)
	return err
}

func mutateLoopConfigWithExecutor(
	ctx context.Context,
	exec globalSQLExecutor,
	workspaceID string,
	loopName string,
	original looppkg.LoopConfig,
	normalized looppkg.LoopConfig,
	expectedRevision *int64,
) (looppkg.StoredLoopConfigSnapshot, error) {
	queries := sqlcgen.New(exec)
	current, err := storedLoopConfigSnapshotWithQueries(ctx, queries, workspaceID, loopName)
	if err != nil {
		return looppkg.StoredLoopConfigSnapshot{}, err
	}
	if expectedRevision != nil && *expectedRevision != current.Revision {
		return looppkg.StoredLoopConfigSnapshot{}, &looppkg.ConfigRevisionConflictError{
			Expected: *expectedRevision,
			Current:  current.Revision,
		}
	}
	next := mergeLoopConfigPatch(current.Config, original, normalized)
	next, err = normalizeCompleteLoopConfigForStore(next)
	if err != nil {
		return looppkg.StoredLoopConfigSnapshot{}, err
	}
	if current.Config != nil && loopConfigsCanonicallyEqual(*current.Config, next) {
		return current, nil
	}

	revision := current.Revision + 1
	params, err := loopConfigInsertParams(workspaceID, loopName, next, revision)
	if err != nil {
		return looppkg.StoredLoopConfigSnapshot{}, err
	}
	if current.Config == nil {
		err = queries.InsertLoopConfig(ctx, params)
	} else {
		err = queries.ReplaceLoopConfig(ctx, loopConfigReplaceParams(params))
	}
	if err != nil {
		return looppkg.StoredLoopConfigSnapshot{}, err
	}
	return looppkg.StoredLoopConfigSnapshot{Config: &next, Revision: revision}, nil
}

func storedLoopConfigSnapshotWithQueries(
	ctx context.Context,
	queries *sqlcgen.Queries,
	workspaceID string,
	loopName string,
) (looppkg.StoredLoopConfigSnapshot, error) {
	row, err := queries.GetLoopConfig(ctx, sqlcgen.GetLoopConfigParams{WorkspaceID: workspaceID, LoopName: loopName})
	if errors.Is(err, sql.ErrNoRows) {
		return looppkg.StoredLoopConfigSnapshot{}, nil
	}
	if err != nil {
		return looppkg.StoredLoopConfigSnapshot{}, err
	}
	cfg, err := loopConfigFromGenerated(row)
	if err != nil {
		return looppkg.StoredLoopConfigSnapshot{}, err
	}
	return looppkg.StoredLoopConfigSnapshot{Config: &cfg, Revision: row.Revision}, nil
}

func mergeLoopConfigPatch(
	current *looppkg.LoopConfig,
	original looppkg.LoopConfig,
	normalized looppkg.LoopConfig,
) looppkg.LoopConfig {
	var merged looppkg.LoopConfig
	if current != nil {
		merged = *current
	}
	flags := loopConfigPatchFlagsForStore(original, normalized)
	if flags.HumanGate {
		merged.HumanGateEnabled = normalized.HumanGateEnabled
	}
	if flags.Reattempt {
		merged.ReattemptStrategy = normalized.ReattemptStrategy
	}
	if flags.EnabledChecks {
		merged.EnabledChecks = normalized.EnabledChecks
	}
	if flags.IterationCap {
		merged.IterationCap = normalized.IterationCap
	}
	if flags.BudgetTokens {
		merged.BudgetTokens = normalized.BudgetTokens
	}
	if flags.BudgetWallSec {
		merged.BudgetWallSec = normalized.BudgetWallSec
	}
	if flags.BudgetOnExceeded {
		merged.BudgetOnExceeded = normalized.BudgetOnExceeded
	}
	if flags.NoProgressWindow {
		merged.NoProgressWindow = normalized.NoProgressWindow
	}
	if flags.FanOutWidth {
		merged.FanOutWidth = normalized.FanOutWidth
	}
	if flags.GateMaxRevisions {
		merged.GateMaxRevisions = normalized.GateMaxRevisions
	}
	if flags.RuntimeDefaults {
		merged.RuntimeDefaults = normalized.RuntimeDefaults
	}
	if flags.RuntimeRules {
		merged.RuntimeRules = normalized.RuntimeRules
	}
	if flags.Environment {
		merged.Environment = normalized.Environment
	}
	return merged
}

func normalizeCompleteLoopConfigForStore(cfg looppkg.LoopConfig) (looppkg.LoopConfig, error) {
	normalized, err := normalizeLoopConfigForStore(cfg)
	if err != nil {
		return looppkg.LoopConfig{}, err
	}
	if normalized.HumanGateEnabled == nil {
		normalized.HumanGateEnabled = new(false)
	}
	// These fields are not part of the legacy loop_config storage contract.
	normalized.Lifecycle = nil
	normalized.RequestExpireAfter = nil
	return normalized, nil
}

func loopConfigsCanonicallyEqual(left looppkg.LoopConfig, right looppkg.LoopConfig) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func loopConfigInsertParams(
	workspaceID string,
	loopName string,
	normalized looppkg.LoopConfig,
	revision int64,
) (sqlcgen.InsertLoopConfigParams, error) {
	runtimeDefaultsJSON, err := nullableLoopConfigJSON(normalized.RuntimeDefaults)
	if err != nil {
		return sqlcgen.InsertLoopConfigParams{}, err
	}
	var runtimeRulesValue any
	if normalized.RuntimeRules != nil {
		runtimeRulesValue = normalized.RuntimeRules
	}
	runtimeRulesJSON, err := nullableLoopConfigJSON(runtimeRulesValue)
	if err != nil {
		return sqlcgen.InsertLoopConfigParams{}, err
	}
	environmentJSON, err := nullableLoopConfigJSON(normalized.Environment)
	if err != nil {
		return sqlcgen.InsertLoopConfigParams{}, err
	}
	return sqlcgen.InsertLoopConfigParams{
		WorkspaceID:         workspaceID,
		LoopName:            loopName,
		HumanGateEnabled:    int64(boolPtrToInt(normalized.HumanGateEnabled)),
		ReattemptStrategy:   nullStringPtr(normalized.ReattemptStrategy),
		EnabledChecksJson:   enabledChecksForStore(normalized.EnabledChecks),
		IterationCap:        nullIntPtr(normalized.IterationCap),
		BudgetTokens:        nullIntPtr(normalized.BudgetTokens),
		BudgetWallSec:       nullIntPtr(normalized.BudgetWallSec),
		BudgetOnExceeded:    nullStringPtr(normalized.BudgetOnExceeded),
		NoProgressWindow:    nullIntPtr(normalized.NoProgressWindow),
		FanOutWidth:         nullIntPtr(normalized.FanOutWidth),
		GateMaxRevisions:    nullIntPtr(normalized.GateMaxRevisions),
		RuntimeDefaultsJson: runtimeDefaultsJSON,
		RuntimeRulesJson:    runtimeRulesJSON,
		EnvironmentJson:     environmentJSON,
		Revision:            revision,
	}, nil
}

func loopConfigReplaceParams(insert sqlcgen.InsertLoopConfigParams) sqlcgen.ReplaceLoopConfigParams {
	return sqlcgen.ReplaceLoopConfigParams{
		HumanGateEnabled: insert.HumanGateEnabled, ReattemptStrategy: insert.ReattemptStrategy,
		EnabledChecksJson: insert.EnabledChecksJson, IterationCap: insert.IterationCap,
		BudgetTokens: insert.BudgetTokens, BudgetWallSec: insert.BudgetWallSec,
		BudgetOnExceeded: insert.BudgetOnExceeded, NoProgressWindow: insert.NoProgressWindow,
		FanOutWidth: insert.FanOutWidth, GateMaxRevisions: insert.GateMaxRevisions,
		RuntimeDefaultsJson: insert.RuntimeDefaultsJson, RuntimeRulesJson: insert.RuntimeRulesJson,
		EnvironmentJson: insert.EnvironmentJson, Revision: insert.Revision,
		WorkspaceID: insert.WorkspaceID, LoopName: insert.LoopName,
	}
}
