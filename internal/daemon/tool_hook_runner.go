package daemon

import (
	"context"
	"encoding/json"

	hookspkg "github.com/compozy/compozy/internal/hooks"
	toolspkg "github.com/compozy/compozy/internal/tools"
)

const daemonToolHookAllowed = "allowed"

var _ toolspkg.HookRunner = (*hooksNotifier)(nil)

func (n *hooksNotifier) PreCall(
	ctx context.Context,
	call toolspkg.CallRequest,
) (toolspkg.CallRequest, toolspkg.EffectiveToolDecision, error) {
	payload, err := n.DispatchToolPreCall(ctx, hookspkg.ToolPreCallPayload{
		PayloadBase:    n.toolHookPayloadBase(hookspkg.HookToolPreCall),
		SessionContext: toolHookSessionContext(call),
		TurnContext:    hookspkg.TurnContext{TurnID: call.TurnID},
		ToolCallRef:    toolHookCallRef(call),
		ToolInput:      cloneToolHookJSON(call.Input),
	})
	if err != nil {
		return call, toolHookAllowedDecision(), err
	}
	call.ToolID = toolspkg.ToolID(payload.ToolID)
	call.Input = cloneToolHookJSON(payload.ToolInput)
	return call, toolHookAllowedDecision(), nil
}

func (n *hooksNotifier) PostCall(
	ctx context.Context,
	call toolspkg.CallRequest,
	result toolspkg.ToolResult,
) (toolspkg.ToolResult, error) {
	payload, err := n.DispatchToolPostCall(ctx, hookspkg.ToolPostCallPayload{
		PayloadBase:    n.toolHookPayloadBase(hookspkg.HookToolPostCall),
		SessionContext: toolHookSessionContext(call),
		TurnContext:    hookspkg.TurnContext{TurnID: call.TurnID},
		ToolCallRef:    toolHookCallRef(call),
		ToolInput:      cloneToolHookJSON(call.Input),
		ToolResult:     cloneToolHookJSON(result.Structured),
	})
	if err != nil {
		return result, err
	}
	result.Structured = cloneToolHookJSON(payload.ToolResult)
	return result, nil
}

func (n *hooksNotifier) PostError(
	ctx context.Context,
	call toolspkg.CallRequest,
	callErr error,
) error {
	errorText := ""
	if callErr != nil {
		errorText = callErr.Error()
	}
	_, err := n.DispatchToolPostError(ctx, hookspkg.ToolPostErrorPayload{
		PayloadBase:    n.toolHookPayloadBase(hookspkg.HookToolPostError),
		SessionContext: toolHookSessionContext(call),
		TurnContext:    hookspkg.TurnContext{TurnID: call.TurnID},
		ToolCallRef:    toolHookCallRef(call),
		ToolInput:      cloneToolHookJSON(call.Input),
		Error:          errorText,
	})
	return err
}

func (n *hooksNotifier) toolHookPayloadBase(event hookspkg.HookEvent) hookspkg.PayloadBase {
	return hookspkg.PayloadBase{Event: event, Timestamp: n.timestamp()}
}

func toolHookSessionContext(call toolspkg.CallRequest) hookspkg.SessionContext {
	return hookspkg.SessionContext{
		ProfileID:   call.ProfileID,
		SessionID:   call.SessionID,
		AgentName:   call.AgentName,
		WorkspaceID: call.WorkspaceID,
		Workspace:   call.TrustedWorkspaceRoot,
	}
}

func toolHookCallRef(call toolspkg.CallRequest) hookspkg.ToolCallRef {
	return hookspkg.ToolCallRef{
		ToolCallID: call.ToolCallID,
		ToolID:     call.ToolID.String(),
		ReadOnly:   call.ReadOnly,
	}
}

func toolHookAllowedDecision() toolspkg.EffectiveToolDecision {
	return toolspkg.EffectiveToolDecision{Callable: true, HookResult: daemonToolHookAllowed}
}

func cloneToolHookJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
