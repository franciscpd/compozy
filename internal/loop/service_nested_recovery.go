package loop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/compozy/compozy/internal/loop/dsl"
)

// RecoverNestedLoop derives and atomically reactivates one failed direct child item.
func (s *service) RecoverNestedLoop(
	ctx context.Context,
	input NestedRecoveryInput,
) (NestedRecoveryResult, error) {
	if err := validateNestedRecoveryInput(input); err != nil {
		return NestedRecoveryResult{}, err
	}
	store, ok := s.store.(NestedRecoveryStore)
	if !ok {
		return NestedRecoveryResult{}, fmt.Errorf(
			"%w: nested recovery store is unavailable",
			ErrActionDependencyMissing,
		)
	}
	canonicalRuntime := RuntimeSpec{
		Provider:  strings.TrimSpace(input.Runtime.Provider),
		Model:     strings.TrimSpace(input.Runtime.Model),
		Reasoning: strings.TrimSpace(input.Runtime.Reasoning),
		Speed:     input.Runtime.Speed,
	}
	digest, err := nestedRecoveryRequestDigest(input.ParentRunID, canonicalRuntime)
	if err != nil {
		return NestedRecoveryResult{}, err
	}
	if replay, found, err := store.LookupNestedRecoveryReplay(
		ctx, input.WorkspaceID, strings.TrimSpace(input.RequestID), digest,
	); err != nil {
		return NestedRecoveryResult{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	parent, err := s.store.GetLoopRun(ctx, input.WorkspaceID, input.ParentRunID)
	if err != nil {
		return NestedRecoveryResult{}, err
	}
	lineage, err := s.loadNestedRecoveryLineage(ctx, parent)
	if err != nil {
		return NestedRecoveryResult{}, err
	}
	target, err := selectNestedRecoveryTarget(lineage)
	if err != nil {
		return NestedRecoveryResult{}, nestedRecoveryTargetConflict(err)
	}
	plan, err := planNestedRecoveryGenerations(lineage, target)
	if err != nil {
		return NestedRecoveryResult{}, nestedRecoveryTargetConflict(err)
	}
	child := lineage.Children[target.ChildRunID]
	taskID, err := s.nestedRecoveryTaskID(ctx, child, target)
	if err != nil {
		return NestedRecoveryResult{}, err
	}
	runtime, err := s.validateNestedRecoveryRuntime(ctx, input.WorkspaceID, taskID, canonicalRuntime)
	if err != nil {
		return NestedRecoveryResult{}, err
	}
	at := s.now().UTC()
	parentGeneration := plan.ParentIntent.Generation
	parentItemIndex := target.ParentItemIndex
	op, err := newTimeTravelOp(
		timeTravelKindNestedRecovery,
		input.RequestID,
		digest,
		parent,
		input.Actor,
		"",
		target.ParentNodeID,
		&parentItemIndex,
		parent.ID,
		&parentGeneration,
		at,
	)
	if err != nil {
		return NestedRecoveryResult{}, err
	}
	result, replayed, err := store.CreateNestedRecovery(ctx, NestedRecoveryStoreRequest{
		WorkspaceID:    input.WorkspaceID,
		Parent:         &lineage.Parent,
		Child:          &child.Run,
		ParentIntent:   plan.ParentIntent,
		ChildIntent:    plan.ChildIntent,
		ParentOutputs:  plan.ParentOutputs,
		ChildOutputs:   plan.ChildOutputs,
		Target:         target,
		TaskID:         taskID,
		Runtime:        runtime,
		Operation:      op,
		RequestDigest:  digest,
		IdempotencyKey: strings.TrimSpace(input.RequestID),
		At:             at,
	})
	if err != nil {
		return NestedRecoveryResult{}, err
	}
	result.Replayed = replayed
	return result, nil
}

func validateNestedRecoveryInput(input NestedRecoveryInput) error {
	if strings.TrimSpace(string(input.WorkspaceID)) == "" || strings.TrimSpace(string(input.ParentRunID)) == "" {
		return fmt.Errorf("%w: nested recovery workspace and parent run are required", ErrValidation)
	}
	if strings.TrimSpace(input.RequestID) == "" {
		return fmt.Errorf("%w: nested recovery request_id is required", ErrValidation)
	}
	if strings.TrimSpace(input.Runtime.Provider) == "" {
		return NewRuntimeValidationError(RuntimeValidationItem{
			Field: runtimeFieldProvider, Value: input.Runtime.Provider, Reason: "exact_runtime_required",
		})
	}
	if strings.TrimSpace(input.Runtime.Model) == "" {
		return NewRuntimeValidationError(RuntimeValidationItem{
			Field: runtimeFieldModel, Value: input.Runtime.Model, Reason: "exact_runtime_required",
		})
	}
	return nil
}

func nestedRecoveryTargetConflict(err error) error {
	if !errors.Is(err, ErrNestedRecoveryTargetNotFound) {
		return err
	}
	return &ReasonError{
		Code: ReasonCodeNestedRecoveryConflict,
		Err:  fmt.Errorf("%w: %w", ErrNestedRecoveryConflict, err),
	}
}

func (s *service) loadNestedRecoveryLineage(
	ctx context.Context,
	parent Run,
) (NestedRecoveryLineage, error) {
	parentGraph, err := s.executedRunGraph(ctx, parent)
	if err != nil {
		return NestedRecoveryLineage{}, err
	}
	parentOutputs, err := requireTimeTravelOutputs(
		ctx, s.store, parent, parent.Generation,
		ReasonCodeNestedRecoveryConflict, ErrNestedRecoveryConflict,
	)
	if err != nil {
		return NestedRecoveryLineage{}, err
	}
	lineage := NestedRecoveryLineage{
		WorkspaceID: parent.WorkspaceID,
		Parent:      parent, ParentGraph: parentGraph, ParentOutputs: parentOutputs,
		Children: make(map[RunID]NestedRecoveryChildState),
	}
	for _, output := range parentOutputs {
		childID := RunID(strings.TrimSpace(output.ChildLoopRunID))
		if childID == "" {
			continue
		}
		if _, loaded := lineage.Children[childID]; loaded {
			continue
		}
		child, err := s.store.GetLoopRun(ctx, parent.WorkspaceID, childID)
		if err != nil {
			continue
		}
		graph, err := s.executedRunGraph(ctx, child)
		if err != nil {
			return NestedRecoveryLineage{}, err
		}
		outputs, err := requireTimeTravelOutputs(
			ctx, s.store, child, child.Generation,
			ReasonCodeNestedRecoveryConflict, ErrNestedRecoveryConflict,
		)
		if err != nil {
			return NestedRecoveryLineage{}, err
		}
		lineage.Children[childID] = NestedRecoveryChildState{Run: child, Graph: graph, Outputs: outputs}
	}
	return lineage, nil
}

func (s *service) executedRunGraph(ctx context.Context, run Run) (dsl.Graph, error) {
	snapshot, err := s.store.GetLoopDefinitionSnapshot(ctx, run.WorkspaceID, run.DefinitionDigest)
	if err != nil {
		return dsl.Graph{}, err
	}
	resolved, err := LoadExecutedDefinitionSnapshot(snapshot.Definition, run.DefinitionDigest)
	if err != nil {
		return dsl.Graph{}, err
	}
	return resolved.Definition.Graph, nil
}

func (s *service) nestedRecoveryTaskID(
	ctx context.Context,
	child NestedRecoveryChildState,
	target NestedRecoveryTarget,
) (string, error) {
	reader, ok := s.store.(GenerationOutputReader)
	if !ok {
		return "", fmt.Errorf("%w: generation output payload reader is unavailable", ErrActionDependencyMissing)
	}
	outputs, err := generationOutputRuntimeView(ctx, reader, generationOutputRuntimeScope{
		workspaceID: child.Run.WorkspaceID, runID: child.Run.ID, generation: child.Run.Generation,
	}, child.Outputs)
	if err != nil {
		return "", err
	}
	namespace, err := runtimeNamespace(
		child.Run, child.Run.Generation, child.Graph, newControlTopology(child.Graph),
		outputs, dsl.NodeID(target.ChildNodeID), target.ChildItemIndex,
	)
	if err != nil {
		return "", err
	}
	node, ok := graphNode(child.Graph, dsl.NodeID(target.ChildNodeID))
	if !ok {
		return "", nestedRecoveryTargetConflict(ErrNestedRecoveryTargetNotFound)
	}
	item, err := ItemRuntimeFromNamespace(namespace, node.Params, RuntimeSpec{})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(item.TaskID) == "" {
		return "", fmt.Errorf("%w: recovered child item has no imported task identity", ErrValidation)
	}
	return item.TaskID, nil
}

func (s *service) validateNestedRecoveryRuntime(
	ctx context.Context,
	workspaceID WorkspaceID,
	taskID string,
	runtime RuntimeSpec,
) (ResolvedRuntime, error) {
	if s.runtimeCatalog == nil {
		return ResolvedRuntime{}, fmt.Errorf("%w: runtime catalog is unavailable", ErrActionDependencyMissing)
	}
	catalog, err := s.runtimeCatalog.ForWorkspace(ctx, workspaceID)
	if err != nil {
		return ResolvedRuntime{}, err
	}
	resolved := ResolvedRuntime{}
	applyRuntime(&resolved, runtime, RuntimeSourceRecovery)
	return ValidateResolvedRuntime(ctx, catalog, taskID, resolved)
}

func nestedRecoveryRequestDigest(parentRunID RunID, runtime RuntimeSpec) (string, error) {
	return timeTravelRequestDigest(struct {
		Kind        string      `json:"kind"`
		ParentRunID RunID       `json:"parent_run_id"`
		Runtime     RuntimeSpec `json:"runtime"`
	}{timeTravelKindNestedRecovery, parentRunID, runtime})
}
