package daemon

import (
	"testing"

	"github.com/compozy/compozy/internal/loop"
)

func TestLoopNestedRecoveryPayloadsShouldPreserveDurableOrderAndHideReplayState(t *testing.T) {
	t.Parallel()

	results := []loop.NestedRecoveryResult{
		{
			OperationID: "op-1", ParentRunID: "parent-1", ParentGeneration: 2,
			ChildRunID: "child-1", ChildGeneration: 4, TaskID: "task-b",
			Runtime: loop.ResolvedRuntime{Runtime: loop.RuntimeSpec{Provider: "openai", Model: "gpt-5"},
				Source: loop.RuntimeProvenance{Provider: loop.RuntimeSourceRecovery, Model: loop.RuntimeSourceRecovery}},
			Replayed: true,
		},
		{
			OperationID: "op-2", ParentRunID: "parent-1", ParentGeneration: 3,
			ChildRunID: "child-1", ChildGeneration: 5, TaskID: "task-b",
		},
	}

	payloads := loopNestedRecoveryPayloads(results)
	if len(payloads) != 2 || payloads[0].OperationID != "op-1" || payloads[1].OperationID != "op-2" {
		t.Fatalf("payload order = %#v", payloads)
	}
	if payloads[0].Runtime.Provider != "openai" || payloads[0].Runtime.Source.Provider != "recovery" {
		t.Fatalf("runtime payload = %#v", payloads[0].Runtime)
	}
}
