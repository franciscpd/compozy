package loop_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	loop "github.com/compozy/compozy/internal/loop"
	"github.com/compozy/compozy/internal/loop/dsl"
	"github.com/compozy/compozy/internal/task"
)

func TestServiceRecoverNestedLoopShouldDeriveTargetTaskAndValidateExactRuntime(t *testing.T) {
	t.Parallel()

	store := &nestedRecoveryFakeStore{timeTravelFakeStore: newTimeTravelFakeStore()}
	seedNestedRecoveryServiceFixture(t, store)
	svc := newTestServiceWithOptions(
		t,
		store,
		validDefinition(),
		loop.WithRuntimeCatalog(rejectingServiceRuntimeCatalogFactory{}),
	)
	timeTravel, ok := svc.(loop.TimeTravelService)
	if !ok {
		t.Fatal("Loop service does not implement TimeTravelService")
	}

	result, err := timeTravel.RecoverNestedLoop(context.Background(), loop.NestedRecoveryInput{
		WorkspaceID: "ws-1", ParentRunID: "parent", RequestID: "request-1",
		Runtime: loop.RuntimeSpec{Provider: "codex", Model: "gpt-5.6", Reasoning: "high"},
		Actor:   humanActor(t),
	})
	if err != nil {
		t.Fatalf("RecoverNestedLoop() error = %v", err)
	}
	if result.ParentRunID != "parent" || result.ChildRunID != "child" || result.TaskID != "task-b" {
		t.Fatalf("RecoverNestedLoop() result = %#v", result)
	}
	request := store.request
	if request == nil {
		t.Fatal("CreateNestedRecovery() was not called")
	}
	if request.Target.ParentNodeID != "delivery" || request.Target.ChildNodeID != "implement" ||
		request.Target.ChildItemIndex != 1 || request.TaskID != "task-b" {
		t.Fatalf("daemon-derived target = %#v task=%q", request.Target, request.TaskID)
	}
	if request.Runtime.Runtime.Provider != "codex" || request.Runtime.Runtime.Model != "gpt-5.6" ||
		request.Runtime.Source.Provider != loop.RuntimeSourceRecovery ||
		request.Runtime.Source.Model != loop.RuntimeSourceRecovery {
		t.Fatalf("validated recovery runtime = %#v", request.Runtime)
	}
}

func TestServiceRecoverNestedLoopShouldRejectInvalidPublicInputsBeforeStoreMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		request loop.NestedRecoveryInput
		field   string
	}{
		{name: "Should require request id", request: loop.NestedRecoveryInput{
			WorkspaceID: "ws-1", ParentRunID: "parent",
			Runtime: loop.RuntimeSpec{Provider: "codex", Model: "gpt-5.6"},
		}},
		{name: "Should require exact provider", field: "provider", request: loop.NestedRecoveryInput{
			WorkspaceID: "ws-1", ParentRunID: "parent", RequestID: "request-1",
			Runtime: loop.RuntimeSpec{Model: "gpt-5.6"},
		}},
		{name: "Should require exact model", field: "model", request: loop.NestedRecoveryInput{
			WorkspaceID: "ws-1", ParentRunID: "parent", RequestID: "request-1",
			Runtime: loop.RuntimeSpec{Provider: "codex"},
		}},
		{name: "Should reject catalog-invalid provider", request: loop.NestedRecoveryInput{
			WorkspaceID: "ws-1", ParentRunID: "parent", RequestID: "request-1",
			Runtime: loop.RuntimeSpec{Provider: "flarp", Model: "unknown"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := &nestedRecoveryFakeStore{timeTravelFakeStore: newTimeTravelFakeStore()}
			seedNestedRecoveryServiceFixture(t, store)
			svc := newTestServiceWithOptions(
				t, store, validDefinition(),
				loop.WithRuntimeCatalog(rejectingServiceRuntimeCatalogFactory{}),
			).(loop.TimeTravelService)
			_, err := svc.RecoverNestedLoop(context.Background(), test.request)
			if err == nil {
				t.Fatal("RecoverNestedLoop() error = nil, want rejection")
			}
			if test.field != "" {
				var validation *loop.RuntimeValidationError
				if !errors.As(err, &validation) || len(validation.Items) != 1 ||
					validation.Items[0].Field != test.field {
					t.Fatalf("RecoverNestedLoop() error = %#v, want runtime field %q", err, test.field)
				}
			}
			if store.request != nil {
				t.Fatal("rejected recovery reached the atomic store")
			}
		})
	}
}

func TestServiceRecoverNestedLoopShouldReturnTypedConflictWhenNoRecoverableTargetExists(t *testing.T) {
	t.Parallel()

	store := &nestedRecoveryFakeStore{timeTravelFakeStore: newTimeTravelFakeStore()}
	seedNestedRecoveryServiceFixture(t, store)
	parent := store.runs["parent"]
	parent.Status = loop.StatusRunning
	store.runs["parent"] = parent
	svc := newTestServiceWithOptions(
		t, store, validDefinition(), loop.WithRuntimeCatalog(rejectingServiceRuntimeCatalogFactory{}),
	).(loop.TimeTravelService)

	_, err := svc.RecoverNestedLoop(context.Background(), loop.NestedRecoveryInput{
		WorkspaceID: "ws-1", ParentRunID: "parent", RequestID: "request-no-target",
		Runtime: loop.RuntimeSpec{Provider: "codex", Model: "gpt-5.6"}, Actor: humanActor(t),
	})
	if !errors.Is(err, loop.ErrNestedRecoveryConflict) ||
		!errors.Is(err, loop.ErrNestedRecoveryTargetNotFound) {
		t.Fatalf("RecoverNestedLoop() error = %v, want typed target conflict", err)
	}
	var reason *loop.ReasonError
	if !errors.As(err, &reason) || reason.Code != loop.ReasonCodeNestedRecoveryConflict {
		t.Fatalf("RecoverNestedLoop() reason = %#v, want nested_recovery_conflict", reason)
	}
	if store.request != nil {
		t.Fatal("no-target recovery reached the atomic store")
	}
}

func TestServiceRecoverNestedLoopShouldReplayOriginalResultAfterLineageAdvances(t *testing.T) {
	t.Parallel()

	store := &nestedRecoveryFakeStore{timeTravelFakeStore: newTimeTravelFakeStore()}
	seedNestedRecoveryServiceFixture(t, store)
	svc := newTestServiceWithOptions(
		t, store, validDefinition(), loop.WithRuntimeCatalog(rejectingServiceRuntimeCatalogFactory{}),
	).(loop.TimeTravelService)
	input := loop.NestedRecoveryInput{
		WorkspaceID: "ws-1", ParentRunID: "parent", RequestID: "request-replay",
		Runtime: loop.RuntimeSpec{Provider: "codex", Model: "gpt-5.6", Reasoning: "high"},
		Actor:   humanActor(t),
	}
	first, err := svc.RecoverNestedLoop(context.Background(), input)
	if err != nil {
		t.Fatalf("RecoverNestedLoop(first) error = %v", err)
	}
	parent := store.runs["parent"]
	parent.Generation, parent.Status = 3, loop.StatusDone
	store.runs["parent"] = parent
	child := store.runs["child"]
	child.Generation, child.Status = 3, loop.StatusDone
	store.runs["child"] = child

	replay, err := svc.RecoverNestedLoop(context.Background(), input)
	if err != nil {
		t.Fatalf("RecoverNestedLoop(replay after lineage advances) error = %v", err)
	}
	if !replay.Replayed || replay.OperationID != first.OperationID || replay.ChildGeneration != first.ChildGeneration {
		t.Fatalf("replay = %#v, first = %#v", replay, first)
	}
	if store.createCalls != 1 {
		t.Fatalf("CreateNestedRecovery calls = %d, want 1", store.createCalls)
	}
}

type nestedRecoveryFakeStore struct {
	*timeTravelFakeStore
	request     *loop.NestedRecoveryStoreRequest
	createCalls int
	replay      *loop.NestedRecoveryResult
}

func (s *nestedRecoveryFakeStore) CreateNestedRecovery(
	_ context.Context,
	request loop.NestedRecoveryStoreRequest,
) (loop.NestedRecoveryResult, bool, error) {
	cloned := request
	s.request = &cloned
	s.createCalls++
	result := loop.NestedRecoveryResult{
		OperationID: request.Operation.ID,
		ParentRunID: request.Parent.ID, ParentGeneration: request.ParentIntent.Generation,
		ChildRunID: request.Child.ID, ChildGeneration: request.ChildIntent.Generation,
		TaskID: request.TaskID, Runtime: request.Runtime,
	}
	s.replay = &result
	return result, false, nil
}

func (s *nestedRecoveryFakeStore) LookupNestedRecoveryReplay(
	_ context.Context,
	_ loop.WorkspaceID,
	_ string,
	_ string,
) (loop.NestedRecoveryResult, bool, error) {
	if s.replay == nil {
		return loop.NestedRecoveryResult{}, false, nil
	}
	result := *s.replay
	result.Replayed = true
	return result, true, nil
}

func (s *nestedRecoveryFakeStore) ListNestedRecoveries(
	context.Context,
	loop.WorkspaceID,
	loop.RunID,
) ([]loop.NestedRecoveryResult, error) {
	return nil, nil
}

func seedNestedRecoveryServiceFixture(t *testing.T, store *nestedRecoveryFakeStore) {
	t.Helper()
	now := time.Date(2026, time.July, 4, 11, 0, 0, 0, time.UTC)
	parentGraph := dsl.Graph{Nodes: []dsl.Node{{
		ID: "delivery", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunLoop),
		Params: dsl.NodeParams{"loop": "child", "mode": "await"},
	}}}
	childGraph := dsl.Graph{
		Nodes: []dsl.Node{
			{ID: "load", Class: dsl.NodeClassAction, Kind: string(dsl.ActionTransform), Params: dsl.NodeParams{
				"map": map[string]any{"items": map[string]any{"value": []any{}}},
			}},
			{ID: "fan", Class: dsl.NodeClassControl, Kind: string(dsl.ControlFanOut),
				Collection: "{{ .nodes.load.output.items }}", MaxFanOut: 2},
			{ID: "implement", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent),
				Params: dsl.NodeParams{"agent": "codex", "prompt": "Implement task"}},
		},
		Edges: []dsl.Edge{{From: "load", To: "fan"}, {From: "fan", To: "implement"}},
	}
	parent := nestedRecoveryServiceRun(t, store, "parent", parentGraph, now)
	child := nestedRecoveryServiceRun(t, store, "child", childGraph, now)
	child.ParentLoopRunID = parent.ID
	store.seed(parent)
	store.seed(child)
	items := json.RawMessage(`{
		"kind":"fan_out","branches":2,"batch_size":1,"max_parallel":1,
		"chunks":[
			[{"index":0,"item":{"id":"task-a","path":".compozy/tasks/task-a.md","type":"backend","complexity":"medium"}}],
			[{"index":1,"item":{"id":"task-b","path":".compozy/tasks/task-b.md","type":"backend","complexity":"high"}}]
		]
	}`)
	ref := loop.OutputRefForPayload(items)
	store.payloads[ref] = items
	store.generationOutputs[parent.ID] = []loop.GenerationOutput{{
		Generation: 1, NodeID: "delivery", Status: "failed", ChildLoopRunID: string(child.ID),
	}}
	store.generationOutputs[child.ID] = []loop.GenerationOutput{
		{Generation: 1, NodeID: "fan", Status: "succeeded", OutputRef: ref},
		{Generation: 1, NodeID: "implement", ItemIndex: 0, Status: "succeeded", OutputRef: "carried"},
		{Generation: 1, NodeID: "implement", ItemIndex: 1, Status: "failed"},
	}
}

func nestedRecoveryServiceRun(
	t *testing.T,
	store *nestedRecoveryFakeStore,
	id loop.RunID,
	graph dsl.Graph,
	at time.Time,
) loop.Run {
	t.Helper()
	definition := dsl.Definition{
		APIVersion: dsl.APIVersion,
		Kind:       dsl.KindLoop,
		Meta:       dsl.Meta{Name: string(id), Version: 1},
		Contract: dsl.Contract{
			Goal: "Recover", DefinitionOfDone: "Recovered", IterationCap: 5,
			NoProgress: dsl.NoProgress{Window: 2},
			Budget:     dsl.Budget{Tokens: 1000, WallClockSec: 3600, OnExceeded: dsl.BudgetExceededHalt},
		},
		Graph: graph,
	}
	resolved := compileDefinition(t, definition)
	effective, err := loop.ResolveEffectiveConfig(
		resolved, loop.DefaultLoopDefaults(), nil, loop.LoopConfig{},
	)
	if err != nil {
		t.Fatalf("ResolveEffectiveConfig() error = %v", err)
	}
	snapshot, digest, err := loop.BuildExecutedDefinitionSnapshot(resolved, effective)
	if err != nil {
		t.Fatalf("BuildExecutedDefinitionSnapshot() error = %v", err)
	}
	store.snapshots["ws-1/"+digest] = loop.DefinitionSnapshot{
		WorkspaceID: "ws-1", Digest: digest, Version: 1, Definition: snapshot,
	}
	return loop.Run{
		ID: id, WorkspaceID: "ws-1", LoopName: string(id), Status: loop.StatusFailed,
		Generation: 1, DefinitionDigest: digest, IterationCap: 5, BudgetTokens: 1000,
		BudgetWallSec: 3600, BudgetOnExceeded: dsl.BudgetExceededHalt,
		CreatedAt: at, StartedAt: at, LastProgressAt: at,
		Inputs: map[string]any{}, StartedBy: task.ActorIdentity{Kind: task.ActorKindHuman, Ref: "test"},
	}
}
