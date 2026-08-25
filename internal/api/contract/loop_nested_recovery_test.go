package contract

import (
	"encoding/json"
	"testing"
)

func TestRecoverNestedLoopContractShouldPreserveZeroGenerationsOnTheWire(t *testing.T) {
	t.Parallel()

	response := RecoverNestedLoopResponse{
		OperationID:      "op-1",
		ParentRunID:      "parent-1",
		ParentGeneration: 0,
		ChildRunID:       "child-1",
		ChildGeneration:  0,
		TaskID:           "task-b",
		Runtime: LoopResolvedRuntime{
			Provider: "openai",
			Model:    "gpt-5",
			Source:   LoopRuntimeProvenance{Provider: "recovery", Model: "recovery"},
		},
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if wire["parent_generation"] != float64(0) || wire["child_generation"] != float64(0) {
		t.Fatalf("generation zero omitted or changed: %s", encoded)
	}
	if _, ok := wire["replayed"]; ok {
		t.Fatalf("false replayed must remain omitted: %s", encoded)
	}
}

func TestRecoverNestedLoopRequestShouldRejectUnknownRuntimeFields(t *testing.T) {
	t.Parallel()

	var request RecoverNestedLoopRequest
	err := json.Unmarshal([]byte(`{"request_id":"req-1","runtime":{"provider":"openai","model":"gpt-5","extra":true}}`), &request)
	if err == nil {
		t.Fatal("Unmarshal() error = nil, want closed runtime object rejection")
	}
}
