//go:build integration && !windows

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/compozy/compozy/internal/api/contract"
	"github.com/compozy/compozy/internal/config"
	"github.com/compozy/compozy/internal/loop/dsl"
	"github.com/compozy/compozy/internal/store/globaldb"
	"github.com/compozy/compozy/internal/testutil/acpmock"
	e2etest "github.com/compozy/compozy/internal/testutil/e2e"
)

func TestDaemonNestedRecoveryShouldReuseLineageAcrossTwoExactRuntimesIntegration(t *testing.T) {
	driverPath := acpmock.RequireDriver(t)
	homePaths := e2etest.NewHomePaths(t)
	fixturePath := mockFixturePath(t, "nested_recovery_fixture.json")
	failDiagnosticsPath := filepath.Join(homePaths.LogsDir, "recovery-fail.jsonl")
	successDiagnosticsPath := filepath.Join(homePaths.LogsDir, "recovery-success.jsonl")
	options := e2etest.RuntimeHarnessOptions{
		HomePaths: homePaths,
		Workspace: e2etest.WorkspaceSeedOptions{Root: filepath.Join(homePaths.HomeDir, "workspace")},
		ConfigSeed: e2etest.ConfigSeedOptions{
			DefaultAgent: "nested-worker", DefaultProvider: acpmock.ProviderName,
			Mutate: func(cfg *config.Config) {
				cfg.Providers["recovery-fail"] = acpmock.ProviderConfig(acpmock.BuildCommand(
					driverPath, fixturePath, "nested_recovery_fail", failDiagnosticsPath,
				))
				cfg.Providers["recovery-success"] = acpmock.ProviderConfig(acpmock.BuildCommand(
					driverPath, fixturePath, "nested_recovery_success", successDiagnosticsPath,
				))
			},
		},
		MockAgents: []e2etest.MockAgentSpec{{
			FixturePath: fixturePath, FixtureAgent: "nested_initial", AgentName: "nested-worker",
		}},
		StartTimeout: 30 * time.Second,
	}
	harness := e2etest.StartRuntimeHarness(t, &options)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	createLoopViaHTTP(t, ctx, harness, nestedRecoveryChildDefinition())
	createLoopViaHTTP(t, ctx, harness, nestedRecoveryParentDefinition())
	putNestedRecoveryHaltConfig(t, ctx, harness, "nested-recovery-child")
	putNestedRecoveryHaltConfig(t, ctx, harness, "nested-recovery-parent")
	parent := runLoopViaHTTP(t, ctx, harness, "nested-recovery-parent")
	childID := waitForNestedRecoveryChildID(t, ctx, harness, parent.ID)
	waitForLoopRunStatus(t, ctx, harness, childID, contract.LoopRunStatusFailed)
	waitForLoopRunStatus(t, ctx, harness, parent.ID, contract.LoopRunStatusFailed)
	initialParent := readLoopRunDetailViaHTTP(t, ctx, harness, parent.ID)
	initialChild := readLoopRunDetailViaHTTP(t, ctx, harness, childID)
	if initialChild.Run.Status != contract.LoopRunStatusFailed || initialChild.Run.ParentLoopRunID != parent.ID {
		t.Fatalf("initial child = %#v, want failed direct child of %s", initialChild.Run, parent.ID)
	}
	parentConfig := nestedRecoveryConfigSnapshot(t, ctx, harness, "nested-recovery-parent")
	childConfig := nestedRecoveryConfigSnapshot(t, ctx, harness, "nested-recovery-child")

	first := recoverNestedViaHTTP(t, ctx, harness, parent.ID, contract.RecoverNestedLoopRequest{
		RequestID: "nested-recovery-1",
		Runtime:   contract.LoopRuntimeSpec{Provider: "recovery-fail", Model: "fail-model"},
	})
	waitForLoopRunStatus(t, ctx, harness, childID, contract.LoopRunStatusFailed)
	waitForLoopRunStatus(t, ctx, harness, parent.ID, contract.LoopRunStatusFailed)
	afterFirstParent := readLoopRunDetailViaHTTP(t, ctx, harness, parent.ID)
	afterFirstChild := readLoopRunDetailViaHTTP(t, ctx, harness, childID)
	if first.ParentRunID != parent.ID || first.ChildRunID != childID || first.ParentGeneration != 2 ||
		first.ChildGeneration != 2 || afterFirstParent.Run.Generation != 2 || afterFirstChild.Run.Generation != 2 {
		t.Fatalf("first recovery = %#v parent=%#v child=%#v", first, afterFirstParent.Run, afterFirstChild.Run)
	}

	second := recoverNestedViaHTTP(t, ctx, harness, parent.ID, contract.RecoverNestedLoopRequest{
		RequestID: "nested-recovery-2",
		Runtime:   contract.LoopRuntimeSpec{Provider: "recovery-success", Model: "success-model"},
	})
	waitForLoopRunStatus(t, ctx, harness, childID, contract.LoopRunStatusDone)
	waitForLoopRunStatus(t, ctx, harness, parent.ID, contract.LoopRunStatusDone)
	finalParent := readLoopRunDetailViaHTTP(t, ctx, harness, parent.ID)
	finalChild := readLoopRunDetailViaHTTP(t, ctx, harness, childID)
	if second.ParentRunID != parent.ID || second.ChildRunID != childID || finalParent.Run.ID != parent.ID ||
		finalChild.Run.ID != childID || finalChild.Run.Status != contract.LoopRunStatusDone {
		t.Fatalf("second recovery = %#v parent=%#v child=%#v", second, finalParent.Run, finalChild.Run)
	}
	assertNestedRecoveryStatusParity(t, finalParent, finalChild, first, second)
	assertNestedRecoveryGenerationFidelity(t, finalChild)
	assertNestedRecoveryTerminalEffects(
		t, ctx, harness, childID, "nested_recovery_failed", map[int64]int{1: 1, 2: 1},
	)
	assertNestedRecoveryTerminalEffects(
		t, ctx, harness, parent.ID, "nested_recovery_parent_failed", map[int64]int{1: 1, 2: 1},
	)
	assertNestedRecoveryTerminalEffects(
		t, ctx, harness, childID, "nested_recovery_done", map[int64]int{3: 1},
	)
	assertNestedRecoveryTerminalEffects(
		t, ctx, harness, parent.ID, "nested_recovery_parent_done", map[int64]int{3: 1},
	)
	assertNestedRecoveryRuntimeEvents(t, ctx, harness, childID)
	if finalParent.Run.DefinitionDigest != initialParent.Run.DefinitionDigest ||
		finalChild.Run.DefinitionDigest != initialChild.Run.DefinitionDigest ||
		finalParent.Run.BudgetTokens != initialParent.Run.BudgetTokens ||
		finalChild.Run.BudgetTokens != initialChild.Run.BudgetTokens ||
		finalParent.Run.IterationCap != initialParent.Run.IterationCap ||
		finalChild.Run.IterationCap != initialChild.Run.IterationCap ||
		finalParent.Run.BudgetWallSec != initialParent.Run.BudgetWallSec ||
		finalChild.Run.BudgetWallSec != initialChild.Run.BudgetWallSec ||
		!finalParent.Run.StartedAt.Equal(initialParent.Run.StartedAt) ||
		!finalChild.Run.StartedAt.Equal(initialChild.Run.StartedAt) ||
		finalParent.Run.TokensUsed < initialParent.Run.TokensUsed || finalChild.Run.TokensUsed < initialChild.Run.TokensUsed {
		t.Fatalf("recovery changed pinned definitions or original budget accounting")
	}
	if got := nestedRecoveryConfigSnapshot(t, ctx, harness, "nested-recovery-parent"); string(got) != string(parentConfig) {
		t.Fatalf("parent config changed: before=%s after=%s", parentConfig, got)
	}
	if got := nestedRecoveryConfigSnapshot(t, ctx, harness, "nested-recovery-child"); string(got) != string(childConfig) {
		t.Fatalf("child config changed: before=%s after=%s", childConfig, got)
	}

	sessionsBeforeReplay := nestedRecoverySessionStartCount(
		t, failDiagnosticsPath, successDiagnosticsPath,
	)
	taskRunsBeforeReplay, coordinatorsBeforeReplay := nestedRecoveryWorkCounts(
		t, ctx, harness.HomePaths.DatabaseFile, parent.ID, childID,
	)
	replay := recoverNestedViaHTTP(t, ctx, harness, parent.ID, contract.RecoverNestedLoopRequest{
		RequestID: "nested-recovery-1",
		Runtime:   contract.LoopRuntimeSpec{Provider: "recovery-fail", Model: "fail-model"},
	})
	if !replay.Replayed || replay.OperationID != first.OperationID || replay.ChildGeneration != 2 {
		t.Fatalf("replay after later recovery = %#v, want original first result", replay)
	}
	afterReplay := readLoopRunDetailViaHTTP(t, ctx, harness, parent.ID)
	if afterReplay.Run.Generation != finalParent.Run.Generation || len(afterReplay.NestedRecoveries) != 2 {
		t.Fatalf("replay started extra work: before=%d/%d after=%d/%d",
			finalParent.Run.Generation, len(finalParent.NestedRecoveries),
			afterReplay.Run.Generation, len(afterReplay.NestedRecoveries))
	}
	if got := nestedRecoverySessionStartCount(t, failDiagnosticsPath, successDiagnosticsPath); got != sessionsBeforeReplay {
		t.Fatalf("replay started an extra ACP session: before=%d after=%d", sessionsBeforeReplay, got)
	}
	if taskRuns, coordinators := nestedRecoveryWorkCounts(
		t, ctx, harness.HomePaths.DatabaseFile, parent.ID, childID,
	); taskRuns != taskRunsBeforeReplay || coordinators != coordinatorsBeforeReplay {
		t.Fatalf("replay started extra task/coordinator work: before=%d/%d after=%d/%d",
			taskRunsBeforeReplay, coordinatorsBeforeReplay, taskRuns, coordinators)
	}
}

func nestedRecoveryChildDefinition() contract.LoopDefinitionDocument {
	return contract.LoopDefinitionDocument{
		APIVersion: "compozy.loop/v1", Kind: "Loop", Concurrency: "allow",
		Meta: dsl.Meta{Name: "nested-recovery-child", Description: "Nested recovery fan-out child."},
		Contract: dsl.Contract{
			Goal: "Implement both imported tasks.", DefinitionOfDone: "Both task cells succeed.",
			StopWhen: dsl.StopWhenSpec{Expr: "nodes.execute.status == 'succeeded'"}, IterationCap: 5,
			NoProgress: dsl.NoProgress{Window: 3},
			Budget:     dsl.Budget{Tokens: 10000, WallClockSec: 120, OnExceeded: dsl.BudgetExceededHalt},
			ContractLifecycleState: &dsl.ContractLifecycleState{
				TerminalStates: []dsl.TerminalState{dsl.TerminalDone, dsl.TerminalFailed, dsl.TerminalExhausted, dsl.TerminalStalled},
				TerminalEffects: dsl.TerminalEffects{
					OnFailed: []dsl.EffectSpec{{Emit: &dsl.EmitSpec{
						Kind: "nested_recovery_failed", Payload: map[string]any{"generation": "{{ .effect.identity.generation }}"},
					}}},
					OnDone: []dsl.EffectSpec{{Emit: &dsl.EmitSpec{
						Kind: "nested_recovery_done", Payload: map[string]any{"generation": "{{ .effect.identity.generation }}"},
					}}},
				},
			},
		},
		Graph: dsl.Graph{
			Nodes: []dsl.Node{
				{ID: "tasks", Class: dsl.NodeClassAction, Kind: "transform", Params: dsl.NodeParams{
					"map": map[string]any{"tasks": map[string]any{"value": []any{
						map[string]any{"id": "task-a", "title": "Task A", "path": ".compozy/tasks/task-a.md", "type": "backend", "complexity": "medium"},
						map[string]any{"id": "task-b", "title": "Task B", "path": ".compozy/tasks/task-b.md", "type": "backend", "complexity": "high"},
					}}},
				}},
				{ID: "fan", Class: dsl.NodeClassControl, Kind: "fan-out", Collection: "{{ .nodes.tasks.output.tasks }}", BatchSize: 1, MaxParallel: 1, MaxFanOut: 2},
				{ID: "execute", Class: dsl.NodeClassAction, Kind: "run-agent", Retry: &dsl.RetrySpec{MaxAttempts: 1}, Params: dsl.NodeParams{
					"agent": "nested-worker", "prompt": "implement {{ .item.id }}",
					"output_schema": map[string]any{"type": "object", "required": []any{"ok", "task"}, "properties": map[string]any{
						"ok": map[string]any{"type": "boolean"}, "task": map[string]any{"type": "string"},
					}},
				}},
			},
			Edges: []dsl.Edge{{From: "tasks", To: "fan"}, {From: "fan", To: "execute"}},
		},
		DefinitionExtensionState: &dsl.DefinitionExtensionState{Start: []dsl.StartBinding{{Kind: "http"}, {Kind: "uds"}}},
	}
}

func nestedRecoveryParentDefinition() contract.LoopDefinitionDocument {
	return contract.LoopDefinitionDocument{
		APIVersion: "compozy.loop/v1", Kind: "Loop", Concurrency: "allow",
		Meta: dsl.Meta{Name: "nested-recovery-parent", Description: "Nested recovery awaited parent."},
		Contract: dsl.Contract{
			Goal: "Await the child.", DefinitionOfDone: "The child succeeds.",
			StopWhen: dsl.StopWhenSpec{Expr: "nodes.child.status == 'succeeded'"}, IterationCap: 5,
			NoProgress: dsl.NoProgress{Window: 3},
			Budget:     dsl.Budget{Tokens: 10000, WallClockSec: 120, OnExceeded: dsl.BudgetExceededHalt},
			ContractLifecycleState: &dsl.ContractLifecycleState{
				TerminalStates: []dsl.TerminalState{
					dsl.TerminalDone, dsl.TerminalFailed, dsl.TerminalExhausted, dsl.TerminalStalled,
				},
				TerminalEffects: dsl.TerminalEffects{
					OnFailed: []dsl.EffectSpec{{Emit: &dsl.EmitSpec{
						Kind: "nested_recovery_parent_failed", Payload: map[string]any{"generation": "{{ .effect.identity.generation }}"},
					}}},
					OnDone: []dsl.EffectSpec{{Emit: &dsl.EmitSpec{
						Kind: "nested_recovery_parent_done", Payload: map[string]any{"generation": "{{ .effect.identity.generation }}"},
					}}},
				},
			},
		},
		Graph: dsl.Graph{Nodes: []dsl.Node{{
			ID: "child", Class: dsl.NodeClassAction, Kind: "run-loop",
			Params: dsl.NodeParams{"loop": "nested-recovery-child", "mode": "await"},
		}}},
		DefinitionExtensionState: &dsl.DefinitionExtensionState{Start: []dsl.StartBinding{{Kind: "http"}, {Kind: "uds"}}},
	}
}

func recoverNestedViaHTTP(
	t *testing.T, ctx context.Context, harness *e2etest.RuntimeHarness, parentRunID string,
	request contract.RecoverNestedLoopRequest,
) contract.RecoverNestedLoopResponse {
	t.Helper()
	var response contract.RecoverNestedLoopResponse
	path := "/api/workspaces/" + url.PathEscape(harness.WorkspaceID) + "/loop-runs/" +
		url.PathEscape(parentRunID) + "/recover-nested"
	if err := harness.HTTPJSON(ctx, http.MethodPost, path, request, &response); err != nil {
		t.Fatalf("HTTP recover nested Loop error = %v", err)
	}
	return response
}

func putNestedRecoveryHaltConfig(
	t *testing.T, ctx context.Context, harness *e2etest.RuntimeHarness, name string,
) {
	t.Helper()
	strategy := contract.LoopReattemptHalt
	request := contract.PutLoopConfigRequest{
		Config: contract.LoopConfig{ReattemptStrategy: &strategy},
	}
	var response contract.LoopConfigResponse
	path := "/api/workspaces/" + url.PathEscape(harness.WorkspaceID) + "/loops/" + url.PathEscape(name) + "/config"
	if err := harness.HTTPJSON(ctx, http.MethodPut, path, request, &response); err != nil {
		t.Fatalf("HTTP put Loop config %s error = %v", name, err)
	}
	if response.Config == nil || response.Config.ReattemptStrategy == nil ||
		*response.Config.ReattemptStrategy != contract.LoopReattemptHalt {
		t.Fatalf("Loop config %s = %#v, want reattempt_strategy halt", name, response)
	}
}

func nestedRecoveryChildRunID(t *testing.T, detail contract.LoopRunResponse) string {
	t.Helper()
	for index := len(detail.Generations) - 1; index >= 0; index-- {
		for _, output := range detail.Generations[index].Outputs {
			if output.NodeID == "child" && output.ChildLoopRunID != "" {
				return output.ChildLoopRunID
			}
		}
	}
	t.Fatal("parent status has no nested child binding")
	return ""
}

func waitForNestedRecoveryChildID(
	t *testing.T, ctx context.Context, harness *e2etest.RuntimeHarness, parentRunID string,
) string {
	t.Helper()
	var childID string
	waitForRuntimeCondition(t, "nested recovery child binding", 20*time.Second, func() bool {
		detail := readLoopRunDetailViaHTTP(t, ctx, harness, parentRunID)
		for _, generation := range detail.Generations {
			for _, output := range generation.Outputs {
				if output.NodeID == "child" && output.ChildLoopRunID != "" {
					childID = output.ChildLoopRunID
					return true
				}
			}
		}
		return false
	})
	return childID
}

func nestedRecoveryConfigSnapshot(
	t *testing.T, ctx context.Context, harness *e2etest.RuntimeHarness, name string,
) json.RawMessage {
	t.Helper()
	var response json.RawMessage
	path := "/api/workspaces/" + url.PathEscape(harness.WorkspaceID) + "/loops/" + url.PathEscape(name) + "/config"
	if err := harness.HTTPJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		t.Fatalf("HTTP get Loop config %s error = %v", name, err)
	}
	return append(json.RawMessage(nil), response...)
}

func assertNestedRecoveryStatusParity(
	t *testing.T, parent contract.LoopRunResponse, child contract.LoopRunResponse,
	first contract.RecoverNestedLoopResponse, second contract.RecoverNestedLoopResponse,
) {
	t.Helper()
	for _, detail := range []contract.LoopRunResponse{parent, child} {
		if len(detail.NestedRecoveries) != 2 || detail.NestedRecoveries[0].OperationID != first.OperationID ||
			detail.NestedRecoveries[1].OperationID != second.OperationID ||
			detail.NestedRecoveries[0].Runtime.Provider != "recovery-fail" ||
			detail.NestedRecoveries[1].Runtime.Provider != "recovery-success" {
			t.Fatalf("nested recovery status = %#v", detail.NestedRecoveries)
		}
	}
}

func assertNestedRecoveryGenerationFidelity(t *testing.T, detail contract.LoopRunResponse) {
	t.Helper()
	if len(detail.Generations) != 3 {
		t.Fatalf("child generations = %d, want 3", len(detail.Generations))
	}
	var carriedTaskRunID string
	for _, generation := range detail.Generations {
		for _, output := range generation.Outputs {
			if output.NodeID != "execute" {
				continue
			}
			if output.ItemIndex == 0 {
				if carriedTaskRunID == "" {
					carriedTaskRunID = output.TaskRunID
				} else if output.TaskRunID != carriedTaskRunID || output.Status != "succeeded" {
					t.Fatalf("successful sibling was not carried: %#v", output)
				}
			}
			if output.ItemIndex == 1 && generation.Generation >= 2 {
				want := "recovery-fail"
				if generation.Generation == 3 {
					want = "recovery-success"
				}
				if output.ResolvedRuntime == nil || output.ResolvedRuntime.Provider != want ||
					output.ResolvedRuntime.Source.Provider != "recovery" {
					t.Fatalf("generation %d recovered output runtime = %#v", generation.Generation, output.ResolvedRuntime)
				}
			}
		}
	}
}

func assertNestedRecoveryTerminalEffects(
	t *testing.T,
	ctx context.Context,
	harness *e2etest.RuntimeHarness,
	runID string,
	authoredKind string,
	want map[int64]int,
) {
	t.Helper()
	events := readLoopRunSSEUntil(
		t,
		ctx,
		harness,
		loopRunEventsPath(harness.WorkspaceID, runID, 0),
		func(events []loopRunSSEEvent) bool {
			return len(nestedRecoveryEffectGenerations(t, events, authoredKind)) >= len(want)
		},
	)
	got := nestedRecoveryEffectGenerations(t, events, authoredKind)
	if len(got) != len(want) {
		t.Fatalf("%s effect generations = %#v, want %#v", authoredKind, got, want)
	}
	for generation, count := range want {
		if got[generation] != count {
			t.Fatalf("%s effect generation %d count = %d, want %d", authoredKind, generation, got[generation], count)
		}
	}
}

func nestedRecoveryEffectGenerations(
	t *testing.T,
	events []loopRunSSEEvent,
	authoredKind string,
) map[int64]int {
	t.Helper()
	result := make(map[int64]int)
	for _, event := range events {
		if event.Kind != contract.LoopRunEventCustomEvent {
			continue
		}
		var payload struct {
			AuthoredKind string `json:"authored_kind"`
			Generation   int64  `json:"generation"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode custom_event payload %s error = %v", event.Payload, err)
		}
		if payload.AuthoredKind == authoredKind {
			result[payload.Generation]++
		}
	}
	return result
}

func assertNestedRecoveryRuntimeEvents(
	t *testing.T,
	ctx context.Context,
	harness *e2etest.RuntimeHarness,
	runID string,
) {
	t.Helper()
	events := readLoopRunSSEUntil(
		t,
		ctx,
		harness,
		loopRunEventsPath(harness.WorkspaceID, runID, 0),
		func(events []loopRunSSEEvent) bool {
			return len(nestedRecoveryAppliedRuntimes(t, events)) >= 2
		},
	)
	got := nestedRecoveryAppliedRuntimes(t, events)
	want := map[int64]string{2: "recovery-fail", 3: "recovery-success"}
	if len(got) != len(want) {
		t.Fatalf("recovery runtime events = %#v, want %#v", got, want)
	}
	for generation, provider := range want {
		runtime, ok := got[generation]
		if !ok || runtime.Provider != provider || runtime.Source.Provider != "recovery" {
			t.Fatalf("generation %d runtime_applied = %#v, want provider %s with recovery provenance", generation, runtime, provider)
		}
	}
}

func nestedRecoveryAppliedRuntimes(
	t *testing.T,
	events []loopRunSSEEvent,
) map[int64]contract.LoopResolvedRuntime {
	t.Helper()
	result := make(map[int64]contract.LoopResolvedRuntime)
	for _, event := range events {
		if event.Kind != contract.LoopRunEventRuntimeApplied {
			continue
		}
		var payload struct {
			Generation      int64                        `json:"generation"`
			ItemIndex       int                          `json:"item_index"`
			NodeID          string                       `json:"node_id"`
			ResolvedRuntime contract.LoopResolvedRuntime `json:"resolved_runtime"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode runtime_applied payload %s error = %v", event.Payload, err)
		}
		if payload.Generation >= 2 && payload.NodeID == "execute" && payload.ItemIndex == 1 {
			result[payload.Generation] = payload.ResolvedRuntime
		}
	}
	return result
}

func nestedRecoverySessionStartCount(t *testing.T, paths ...string) int {
	t.Helper()
	count := 0
	for _, path := range paths {
		records, err := acpmock.ReadDiagnostics(path)
		if err != nil {
			t.Fatalf("read nested recovery diagnostics %s error = %v", path, err)
		}
		for _, record := range records {
			if record.LifecycleEvent == "session_new" {
				count++
			}
		}
	}
	return count
}

func nestedRecoveryWorkCounts(
	t *testing.T,
	ctx context.Context,
	databaseFile string,
	parentRunID string,
	childRunID string,
) (taskRuns int, coordinators int) {
	t.Helper()
	db, err := globaldb.OpenGlobalDB(ctx, databaseFile)
	if err != nil {
		t.Fatalf("OpenGlobalDB(inspect nested recovery work) error = %v", err)
	}
	defer func() {
		if err := db.Close(ctx); err != nil {
			t.Fatalf("Close(inspect nested recovery work DB) error = %v", err)
		}
	}()
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN run_kind = 'coordinator' THEN 1 ELSE 0 END), 0)
		FROM task_runs WHERE loop_run_id IN (?, ?)`, parentRunID, childRunID).Scan(
		&taskRuns, &coordinators,
	); err != nil {
		t.Fatalf("count nested recovery task/coordinator work error = %v", err)
	}
	return taskRuns, coordinators
}
