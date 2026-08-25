package loop

import (
	"context"
	"reflect"
	"testing"

	"github.com/compozy/compozy/internal/loop/dsl"
	"github.com/compozy/compozy/internal/task"
)

func TestCoordinatorShouldLoadRecoveryRuntimeOnlyForTheExactGenerationCell(t *testing.T) {
	t.Parallel()

	wantKey := NestedRecoveryRuntimeKey{
		WorkspaceID: "ws-1", RunID: "child", Generation: 3, NodeID: "implement", ItemIndex: 2,
	}
	reader := &recordingNestedRecoveryRuntimeReader{
		key:     wantKey,
		runtime: RuntimeSpec{Provider: "codex", Model: "gpt-5.6", Reasoning: "high"},
	}
	runner := &CoordinatorRunner{recoveryRuntimes: reader}

	for _, test := range []struct {
		name string
		key  NestedRecoveryRuntimeKey
		want RuntimeSpec
	}{
		{name: "Should apply the exact cell", key: wantKey, want: reader.runtime},
		{name: "Should not leak across items", key: withRecoveryItem(wantKey, 1)},
		{name: "Should not leak across generations", key: withRecoveryGeneration(wantKey, 4)},
		{name: "Should not leak across runs", key: withRecoveryRun(wantKey, "other-child")},
		{name: "Should not leak across workspaces", key: withRecoveryWorkspace(wantKey, "ws-2")},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := ActionExecutionInput{}
			actionCtx := coordinatorActionRunContext{
				loopRun: Run{ID: test.key.RunID, WorkspaceID: test.key.WorkspaceID},
				node:    dsl.Node{ID: dsl.NodeID(test.key.NodeID)},
				meta: coordinatorActionRunMetadata{
					Generation: test.key.Generation, NodeID: string(test.key.NodeID),
					ItemIndex: test.key.ItemIndex, Attempt: 1,
				},
			}
			if err := runner.configureActionExecutionInput(
				context.Background(), task.Run{}, &actionCtx, &input,
			); err != nil {
				t.Fatalf("configureActionExecutionInput() error = %v", err)
			}
			if got := input.RuntimeSelectionOrZero().Recovery; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("recovery runtime = %#v, want %#v", got, test.want)
			}
		})
	}
}

type recordingNestedRecoveryRuntimeReader struct {
	key     NestedRecoveryRuntimeKey
	runtime RuntimeSpec
}

func (r *recordingNestedRecoveryRuntimeReader) GetNestedRecoveryRuntime(
	_ context.Context,
	key NestedRecoveryRuntimeKey,
) (RuntimeSpec, bool, error) {
	if key == r.key {
		return r.runtime, true, nil
	}
	return RuntimeSpec{}, false, nil
}

func withRecoveryItem(key NestedRecoveryRuntimeKey, item int) NestedRecoveryRuntimeKey {
	key.ItemIndex = item
	return key
}

func withRecoveryGeneration(key NestedRecoveryRuntimeKey, generation int) NestedRecoveryRuntimeKey {
	key.Generation = generation
	return key
}

func withRecoveryRun(key NestedRecoveryRuntimeKey, runID RunID) NestedRecoveryRuntimeKey {
	key.RunID = runID
	return key
}

func withRecoveryWorkspace(key NestedRecoveryRuntimeKey, workspaceID WorkspaceID) NestedRecoveryRuntimeKey {
	key.WorkspaceID = workspaceID
	return key
}
