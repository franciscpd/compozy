package loop

import (
	"errors"
	"testing"

	"github.com/compozy/compozy/internal/loop/dsl"
)

func TestNestedRecoveryPlanShouldSelectFirstRecoverableAwaitedChildInGraphOrder(t *testing.T) {
	t.Parallel()

	parent := Run{ID: "parent", WorkspaceID: "ws-1", Status: StatusFailed, Generation: 2}
	parentGraph := dsl.Graph{Nodes: []dsl.Node{
		{ID: "prepare", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent)},
		{ID: "first_delivery", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunLoop),
			Params: dsl.NodeParams{"loop": "child", "mode": "await"}},
		{ID: "second_delivery", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunLoop),
			Params: dsl.NodeParams{"loop": "child", "mode": "await"}},
	}}
	parentOutputs := []GenerationOutput{
		{Generation: 2, NodeID: "prepare", Status: generationOutputSucceeded},
		{Generation: 2, NodeID: "second_delivery", Status: generationOutputFailed, ChildLoopRunID: "child-2"},
		{Generation: 2, NodeID: "first_delivery", Status: generationOutputFailed, ChildLoopRunID: "child-1"},
	}
	childGraph := dsl.Graph{Nodes: []dsl.Node{
		{ID: "implement", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent)},
	}}
	children := map[RunID]NestedRecoveryChildState{
		"child-1": {
			Run: Run{ID: "child-1", ParentLoopRunID: parent.ID, WorkspaceID: parent.WorkspaceID,
				Status: StatusFailed, Generation: 3},
			Graph: childGraph,
			Outputs: []GenerationOutput{
				{Generation: 3, NodeID: "implement", ItemIndex: 2, Status: generationOutputFailed},
				{Generation: 3, NodeID: "implement", ItemIndex: 1, Status: generationOutputFailed},
			},
		},
		"child-2": {
			Run: Run{ID: "child-2", ParentLoopRunID: parent.ID, WorkspaceID: parent.WorkspaceID,
				Status: StatusFailed, Generation: 1},
			Graph: childGraph,
			Outputs: []GenerationOutput{{
				Generation: 1, NodeID: "implement", ItemIndex: 0, Status: generationOutputFailed,
			}},
		},
	}

	target, err := selectNestedRecoveryTarget(NestedRecoveryLineage{
		WorkspaceID: parent.WorkspaceID,
		Parent:      parent, ParentGraph: parentGraph, ParentOutputs: parentOutputs, Children: children,
	})
	if err != nil {
		t.Fatalf("selectNestedRecoveryTarget() error = %v", err)
	}
	if target.ParentNodeID != "first_delivery" || target.ParentItemIndex != 0 || target.ChildRunID != "child-1" {
		t.Fatalf("target parent lineage = %#v, want first_delivery/0 -> child-1", target)
	}
	if target.ChildNodeID != "implement" || target.ChildItemIndex != 1 {
		t.Fatalf("target child cell = %#v, want first failed implement item in item order", target)
	}
}

func TestNestedRecoveryPlanShouldRejectInvalidLineageWithoutSelectingCallerOwnedCoordinates(t *testing.T) {
	t.Parallel()

	base := validNestedRecoveryLineageFixture()
	tests := []struct {
		name   string
		mutate func(*NestedRecoveryLineage)
	}{
		{name: "Should reject a live parent", mutate: func(in *NestedRecoveryLineage) { in.Parent.Status = StatusRunning }},
		{name: "Should reject a canceled child", mutate: func(in *NestedRecoveryLineage) {
			child := in.Children["child"]
			child.Run.Status = StatusCanceled
			in.Children["child"] = child
		}},
		{name: "Should reject a foreign workspace child", mutate: func(in *NestedRecoveryLineage) {
			child := in.Children["child"]
			child.Run.WorkspaceID = "ws-2"
			in.Children["child"] = child
		}},
		{name: "Should reject a child owned by another parent", mutate: func(in *NestedRecoveryLineage) {
			child := in.Children["child"]
			child.Run.ParentLoopRunID = "other-parent"
			in.Children["child"] = child
		}},
		{name: "Should reject a non run-loop parent node", mutate: func(in *NestedRecoveryLineage) {
			in.ParentGraph.Nodes[0].Kind = string(dsl.ActionRunAgent)
		}},
		{name: "Should reject a detached run-loop parent node", mutate: func(in *NestedRecoveryLineage) {
			in.ParentGraph.Nodes[0].Params["mode"] = "detach"
		}},
		{name: "Should reject a non run-agent failed child cell", mutate: func(in *NestedRecoveryLineage) {
			child := in.Children["child"]
			child.Graph.Nodes[0].Kind = string(dsl.ActionTransform)
			in.Children["child"] = child
		}},
		{name: "Should reject a stale parent child pointer", mutate: func(in *NestedRecoveryLineage) {
			in.ParentOutputs[0].ChildLoopRunID = "missing-child"
		}},
		{name: "Should reject lineage without a failed child item", mutate: func(in *NestedRecoveryLineage) {
			child := in.Children["child"]
			child.Outputs[0].Status = generationOutputSucceeded
			in.Children["child"] = child
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := base.clone()
			test.mutate(&input)
			_, err := selectNestedRecoveryTarget(input)
			if !errors.Is(err, ErrNestedRecoveryTargetNotFound) {
				t.Fatalf("selectNestedRecoveryTarget() error = %v, want ErrNestedRecoveryTargetNotFound", err)
			}
		})
	}
}

func TestRecoveryGenerationPlanShouldCarrySiblingsAndRebindParentWithoutRerunningRunLoop(t *testing.T) {
	t.Parallel()

	lineage := validNestedRecoveryLineageFixture()
	child := lineage.Children["child"]
	child.Graph.Nodes = append([]dsl.Node{{
		ID: "fan", Class: dsl.NodeClassControl, Kind: string(dsl.ControlFanOut),
	}}, child.Graph.Nodes...)
	child.Graph.Nodes = append(child.Graph.Nodes,
		dsl.Node{ID: "verify", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent)},
		dsl.Node{ID: "archive", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent)},
	)
	child.Graph.Edges = []dsl.Edge{
		{From: "fan", To: "implement"},
		{From: "implement", To: "verify"},
		{From: "verify", To: "archive"},
	}
	child.Outputs = []GenerationOutput{
		{Generation: 4, NodeID: "implement", ItemIndex: 0, Status: generationOutputSucceeded,
			OutputID: "out-sibling", ArtifactName: "result", OutputRef: "sha256:sibling", TaskRunID: "task-sibling",
			Attempt: 2, Epoch: 4},
		{Generation: 4, NodeID: "implement", ItemIndex: 1, Status: generationOutputFailed,
			OutputRef: "sha256:failed", TaskRunID: "task-failed", Attempt: 3, Epoch: 7},
		{Generation: 4, NodeID: "implement", ItemIndex: 2, Status: generationOutputFailed,
			OutputRef: "sha256:still-failed", TaskRunID: "task-still-failed", Attempt: 2, Epoch: 6},
		{Generation: 4, NodeID: "verify", ItemIndex: 1, Status: generationOutputSucceeded,
			OutputRef: "sha256:verify", TaskRunID: "task-verify"},
		{Generation: 4, NodeID: "archive", ItemIndex: 0, Status: generationOutputSucceeded,
			OutputRef: "sha256:archive", TaskRunID: "task-archive"},
	}
	child.Run.Generation = 4
	lineage.Children["child"] = child
	lineage.Parent.Generation = 5
	lineage.ParentOutputs = []GenerationOutput{
		{Generation: 5, NodeID: "delivery", Status: generationOutputFailed,
			OutputRef: "sha256:child-result", ChildLoopRunID: "child", TaskRunID: "task-parent-loop"},
		{Generation: 5, NodeID: "publish", Status: generationOutputFailed,
			OutputRef: "sha256:publish", TaskRunID: "task-publish"},
		{Generation: 5, NodeID: "unrelated", Status: generationOutputSucceeded,
			OutputRef: "sha256:unrelated", TaskRunID: "task-unrelated"},
	}
	lineage.ParentGraph.Nodes = append(lineage.ParentGraph.Nodes,
		dsl.Node{ID: "publish", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent)},
		dsl.Node{ID: "unrelated", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent)},
	)
	lineage.ParentGraph.Edges = []dsl.Edge{{From: "delivery", To: "publish"}}

	target, err := selectNestedRecoveryTarget(lineage)
	if err != nil {
		t.Fatalf("selectNestedRecoveryTarget() error = %v", err)
	}
	plan, err := planNestedRecoveryGenerations(lineage, target)
	if err != nil {
		t.Fatalf("planNestedRecoveryGenerations() error = %v", err)
	}
	if plan.ChildIntent.Generation != 5 || plan.ChildIntent.ParentGeneration != 4 ||
		plan.ChildIntent.Origin != OriginNestedRecovery {
		t.Fatalf("child intent = %#v", plan.ChildIntent)
	}
	if plan.ParentIntent.Generation != 6 || plan.ParentIntent.ParentGeneration != 5 ||
		plan.ParentIntent.Origin != OriginNestedRecovery {
		t.Fatalf("parent intent = %#v", plan.ParentIntent)
	}

	childOutputs := generationOutputMap(plan.ChildOutputs)
	if got := childOutputs[generationOutputKey{nodeID: "implement", itemIndex: 0}]; got.Status != generationOutputSucceeded || got.OutputRef != "sha256:sibling" ||
		got.TaskRunID != "task-sibling" || got.Attempt != 2 || got.Epoch != 4 {
		t.Fatalf("carried child sibling = %#v, want full fidelity", got)
	}
	for _, key := range []generationOutputKey{
		{nodeID: "implement", itemIndex: 1},
		{nodeID: "verify", itemIndex: 1},
	} {
		if got := childOutputs[key]; got.Status != generationOutputPending || got.OutputRef != "" || got.TaskRunID != "" {
			t.Fatalf("rerun child output %v = %#v, want clean pending", key, got)
		}
	}
	if got := childOutputs[generationOutputKey{nodeID: "implement", itemIndex: 2}]; got.Status != generationOutputFailed || got.OutputRef != "sha256:still-failed" ||
		got.TaskRunID != "task-still-failed" || got.Attempt != 2 || got.Epoch != 6 {
		t.Fatalf("unselected failed child sibling = %#v, want failed with full fidelity", got)
	}
	if got := childOutputs[generationOutputKey{nodeID: "archive", itemIndex: 0}]; got.Status != generationOutputSucceeded || got.OutputRef != "sha256:archive" {
		t.Fatalf("unrelated child output = %#v, want carried", got)
	}

	parentOutputs := generationOutputMap(plan.ParentOutputs)
	delivery := parentOutputs[generationOutputKey{nodeID: "delivery", itemIndex: 0}]
	if delivery.Status != generationOutputAwaitingChild || delivery.ChildLoopRunID != "child" ||
		delivery.TaskRunID != "" {
		t.Fatalf("parent delivery output = %#v, want awaiting same child without a rerun task", delivery)
	}
	if got := parentOutputs[generationOutputKey{nodeID: "publish", itemIndex: 0}]; got.Status != generationOutputPending || got.OutputRef != "" || got.TaskRunID != "" {
		t.Fatalf("parent dependent = %#v, want clean pending", got)
	}
	if got := parentOutputs[generationOutputKey{nodeID: "unrelated", itemIndex: 0}]; got.Status != generationOutputSucceeded || got.OutputRef != "sha256:unrelated" {
		t.Fatalf("parent unrelated output = %#v, want carried", got)
	}
}

func validNestedRecoveryLineageFixture() NestedRecoveryLineage {
	parent := Run{ID: "parent", WorkspaceID: "ws-1", Status: StatusFailed, Generation: 1}
	return NestedRecoveryLineage{
		WorkspaceID: parent.WorkspaceID,
		Parent:      parent,
		ParentGraph: dsl.Graph{Nodes: []dsl.Node{{
			ID: "delivery", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunLoop),
			Params: dsl.NodeParams{"loop": "child", "mode": "await"},
		}}},
		ParentOutputs: []GenerationOutput{{
			Generation: 1, NodeID: "delivery", Status: generationOutputFailed, ChildLoopRunID: "child",
		}},
		Children: map[RunID]NestedRecoveryChildState{
			"child": {
				Run: Run{ID: "child", ParentLoopRunID: parent.ID, WorkspaceID: parent.WorkspaceID,
					Status: StatusFailed, Generation: 1},
				Graph: dsl.Graph{Nodes: []dsl.Node{{
					ID: "implement", Class: dsl.NodeClassAction, Kind: string(dsl.ActionRunAgent),
				}}},
				Outputs: []GenerationOutput{{
					Generation: 1, NodeID: "implement", Status: generationOutputFailed,
				}},
			},
		},
	}
}

func (in NestedRecoveryLineage) clone() NestedRecoveryLineage {
	cloned := in
	cloned.ParentGraph.Nodes = append([]dsl.Node(nil), in.ParentGraph.Nodes...)
	for index := range cloned.ParentGraph.Nodes {
		cloned.ParentGraph.Nodes[index].Params = cloneNestedRecoveryNodeParams(cloned.ParentGraph.Nodes[index].Params)
	}
	cloned.ParentOutputs = append([]GenerationOutput(nil), in.ParentOutputs...)
	cloned.Children = make(map[RunID]NestedRecoveryChildState, len(in.Children))
	for key, child := range in.Children {
		child.Graph.Nodes = append([]dsl.Node(nil), child.Graph.Nodes...)
		for index := range child.Graph.Nodes {
			child.Graph.Nodes[index].Params = cloneNestedRecoveryNodeParams(child.Graph.Nodes[index].Params)
		}
		child.Outputs = append([]GenerationOutput(nil), child.Outputs...)
		cloned.Children[key] = child
	}
	return cloned
}

func cloneNestedRecoveryNodeParams(params dsl.NodeParams) dsl.NodeParams {
	cloned := make(dsl.NodeParams, len(params))
	for key, value := range params {
		cloned[key] = value
	}
	return cloned
}
