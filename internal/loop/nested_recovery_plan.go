package loop

import (
	"errors"
	"sort"
	"strings"

	"github.com/compozy/compozy/internal/loop/dsl"
)

// ErrNestedRecoveryTargetNotFound reports that a settled parent no longer owns
// a recoverable failed child cell.
var ErrNestedRecoveryTargetNotFound = errors.New("loop: nested recovery target not found")

// NestedRecoveryChildState is the trusted state for one direct child run.
type NestedRecoveryChildState struct {
	Run     Run
	Graph   dsl.Graph
	Outputs []GenerationOutput
}

// NestedRecoveryLineage is the workspace-scoped state used to derive a target.
type NestedRecoveryLineage struct {
	WorkspaceID   WorkspaceID
	Parent        Run
	ParentGraph   dsl.Graph
	ParentOutputs []GenerationOutput
	Children      map[RunID]NestedRecoveryChildState
}

// NestedRecoveryTarget is a daemon-derived parent/child generation cell pair.
type NestedRecoveryTarget struct {
	ParentNodeID    NodeID
	ParentItemIndex int
	ChildRunID      RunID
	ChildNodeID     NodeID
	ChildItemIndex  int
}

// NestedRecoveryGenerationPlan carries both next-generation snapshots.
type NestedRecoveryGenerationPlan struct {
	ParentIntent  GenerationIntent
	ChildIntent   GenerationIntent
	ParentOutputs []GenerationOutput
	ChildOutputs  []GenerationOutput
}

func selectNestedRecoveryTarget(lineage NestedRecoveryLineage) (NestedRecoveryTarget, error) {
	if strings.TrimSpace(string(lineage.WorkspaceID)) == "" ||
		lineage.Parent.WorkspaceID != lineage.WorkspaceID ||
		!nestedRecoveryStatus(lineage.Parent.Status) {
		return NestedRecoveryTarget{}, ErrNestedRecoveryTargetNotFound
	}
	for _, node := range lineage.ParentGraph.Nodes {
		if node.Class != dsl.NodeClassAction || dsl.ActionKind(node.Kind) != dsl.ActionRunLoop ||
			runLoopMode(node) != dsl.RunLoopAwait {
			continue
		}
		for _, parentOutput := range outputsForNode(lineage.ParentOutputs, string(node.ID)) {
			if parentOutput.Status != generationOutputFailed || parentOutput.ChildLoopRunID == "" {
				continue
			}
			child, ok := lineage.Children[RunID(parentOutput.ChildLoopRunID)]
			if !ok || child.Run.ID != RunID(parentOutput.ChildLoopRunID) ||
				child.Run.WorkspaceID != lineage.WorkspaceID || child.Run.ParentLoopRunID != lineage.Parent.ID ||
				!nestedRecoveryStatus(child.Run.Status) {
				continue
			}
			if target, ok := selectFailedChildCell(node, parentOutput, child); ok {
				return target, nil
			}
		}
	}
	return NestedRecoveryTarget{}, ErrNestedRecoveryTargetNotFound
}

func selectFailedChildCell(
	parentNode dsl.Node,
	parentOutput GenerationOutput,
	child NestedRecoveryChildState,
) (NestedRecoveryTarget, bool) {
	for _, childNode := range child.Graph.Nodes {
		if childNode.Class != dsl.NodeClassAction || dsl.ActionKind(childNode.Kind) != dsl.ActionRunAgent {
			continue
		}
		for _, childOutput := range outputsForNode(child.Outputs, string(childNode.ID)) {
			if childOutput.Status != generationOutputFailed {
				continue
			}
			return NestedRecoveryTarget{
				ParentNodeID:    NodeID(parentNode.ID),
				ParentItemIndex: parentOutput.ItemIndex,
				ChildRunID:      child.Run.ID,
				ChildNodeID:     NodeID(childNode.ID),
				ChildItemIndex:  childOutput.ItemIndex,
			}, true
		}
	}
	return NestedRecoveryTarget{}, false
}

func outputsForNode(outputs []GenerationOutput, nodeID string) []GenerationOutput {
	selected := make([]GenerationOutput, 0)
	for _, output := range outputs {
		if output.NodeID == nodeID {
			selected = append(selected, output)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].ItemIndex < selected[j].ItemIndex })
	return selected
}

func runLoopMode(node dsl.Node) dsl.RunLoopMode {
	mode, _ := node.Params["mode"].(string)
	if strings.TrimSpace(mode) == "" {
		return dsl.RunLoopAwait
	}
	return dsl.RunLoopMode(mode)
}

func nestedRecoveryStatus(status Status) bool {
	switch status {
	case StatusFailed, StatusExhausted, StatusStalled:
		return true
	default:
		return false
	}
}

func planNestedRecoveryGenerations(
	lineage NestedRecoveryLineage,
	target NestedRecoveryTarget,
) (NestedRecoveryGenerationPlan, error) {
	child, ok := lineage.Children[target.ChildRunID]
	if !ok {
		return NestedRecoveryGenerationPlan{}, ErrNestedRecoveryTargetNotFound
	}
	childRerun := map[generationOutputKey]struct{}{{
		nodeID: string(target.ChildNodeID), itemIndex: target.ChildItemIndex,
	}: {}}
	addTransitiveDependents(child.Graph, child.Outputs, childRerun)
	parentRerun := map[generationOutputKey]struct{}{{
		nodeID: string(target.ParentNodeID), itemIndex: target.ParentItemIndex,
	}: {}}
	addTransitiveDependents(lineage.ParentGraph, lineage.ParentOutputs, parentRerun)
	delete(parentRerun, generationOutputKey{
		nodeID: string(target.ParentNodeID), itemIndex: target.ParentItemIndex,
	})

	childGeneration := child.Run.Generation + 1
	parentGeneration := lineage.Parent.Generation + 1
	return NestedRecoveryGenerationPlan{
		ChildIntent: GenerationIntent{
			Generation: int64(childGeneration), ParentGeneration: int64(child.Run.Generation),
			Origin: OriginNestedRecovery,
		},
		ParentIntent: GenerationIntent{
			Generation: int64(parentGeneration), ParentGeneration: int64(lineage.Parent.Generation),
			Origin: OriginNestedRecovery,
		},
		ChildOutputs: nestedRecoveryOutputs(child.Graph, child.Outputs, childRerun, childGeneration, nil),
		ParentOutputs: nestedRecoveryOutputs(
			lineage.ParentGraph,
			lineage.ParentOutputs,
			parentRerun,
			parentGeneration,
			&target,
		),
	}, nil
}

func nestedRecoveryOutputs(
	graph dsl.Graph,
	current []GenerationOutput,
	rerun map[generationOutputKey]struct{},
	nextGeneration int,
	parentTarget *NestedRecoveryTarget,
) []GenerationOutput {
	next := make([]GenerationOutput, 0, len(current))
	for _, output := range current {
		node, ok := graphNode(graph, dsl.NodeID(output.NodeID))
		if !ok {
			continue
		}
		key := generationOutputKey{nodeID: output.NodeID, itemIndex: output.ItemIndex}
		if parentTarget != nil && key.nodeID == string(parentTarget.ParentNodeID) &&
			key.itemIndex == parentTarget.ParentItemIndex {
			next = append(next, nestedRecoveryParentBinding(output, nextGeneration))
			continue
		}
		if _, shouldRerun := rerun[key]; shouldRerun {
			next = append(next, reattemptPendingOutput(node, output, nextGeneration))
			continue
		}
		carry := output
		carry.Generation = nextGeneration
		carry.ExpectedEpoch = nil
		next = append(next, carry)
	}
	return next
}

func nestedRecoveryParentBinding(current GenerationOutput, nextGeneration int) GenerationOutput {
	return GenerationOutput{
		Generation:     nextGeneration,
		NodeID:         current.NodeID,
		ItemIndex:      current.ItemIndex,
		Status:         generationOutputAwaitingChild,
		ChildLoopRunID: current.ChildLoopRunID,
		Attempt:        1,
	}
}
