package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/compozy/compozy/internal/workspaceaccess"
)

// WorkspaceIDResolver canonicalizes workspace references before policy evaluation.
type WorkspaceIDResolver func(context.Context, string) (string, error)

// TrustedWorkspaceRootResolver resolves a canonical workspace ID to its trusted root path.
type TrustedWorkspaceRootResolver func(context.Context, string) (string, error)

// WithWorkspaceIDResolver wires workspace-reference canonicalization into dispatch.
func WithWorkspaceIDResolver(resolver WorkspaceIDResolver) RegistryOption {
	return func(registry *RuntimeRegistry) {
		registry.workspaceIDResolver = resolver
	}
}

// WithTrustedWorkspaceRootResolver wires trusted workspace-root enrichment into dispatch hooks.
func WithTrustedWorkspaceRootResolver(resolver TrustedWorkspaceRootResolver) RegistryOption {
	return func(registry *RuntimeRegistry) {
		registry.workspaceRootResolver = resolver
	}
}

func (r *RuntimeRegistry) normalizeDispatchCallRequest(
	ctx context.Context,
	scope Scope,
	req CallRequest,
) (CallRequest, error) {
	for _, binding := range []struct {
		field    string
		trusted  string
		supplied *string
	}{
		{field: "profile_id", trusted: scope.ProfileID, supplied: &req.ProfileID},
		{field: "session_id", trusted: scope.SessionID, supplied: &req.SessionID},
		{field: "agent_name", trusted: scope.AgentName, supplied: &req.AgentName},
		{field: "actor_kind", trusted: scope.ActorKind, supplied: &req.ActorKind},
	} {
		if err := bindCallScopeField(req.ToolID, binding.field, binding.trusted, binding.supplied); err != nil {
			return CallRequest{}, err
		}
	}
	trustedWorkspaceID := strings.TrimSpace(scope.WorkspaceID)
	requestedWorkspaceID := strings.TrimSpace(req.WorkspaceID)
	if requestedWorkspaceID == "" {
		req.WorkspaceID = trustedWorkspaceID
		return r.enrichTrustedWorkspaceRoot(ctx, req)
	}
	if trustedWorkspaceID != "" && trustedWorkspaceID == requestedWorkspaceID {
		req.WorkspaceID = requestedWorkspaceID
		return r.enrichTrustedWorkspaceRoot(ctx, req)
	}
	if r == nil || r.workspaceIDResolver == nil {
		return CallRequest{}, workspaceAccessDeniedError(req.ToolID)
	}
	canonicalWorkspaceID, err := r.workspaceIDResolver(ctx, requestedWorkspaceID)
	if err != nil || strings.TrimSpace(canonicalWorkspaceID) == "" {
		return CallRequest{}, workspaceAccessDeniedError(req.ToolID)
	}
	requestedWorkspaceID = strings.TrimSpace(canonicalWorkspaceID)
	if trustedWorkspaceID != "" && trustedWorkspaceID == requestedWorkspaceID {
		req.WorkspaceID = requestedWorkspaceID
		return r.enrichTrustedWorkspaceRoot(ctx, req)
	}
	if r == nil || r.workspaceAccess == nil {
		return CallRequest{}, workspaceAccessDeniedError(req.ToolID)
	}
	decision, err := r.workspaceAccess.Authorize(ctx, workspaceaccess.Request{
		Actor:             workspaceAccessActorFromScope(scope),
		TargetWorkspaceID: requestedWorkspaceID,
		Seam:              workspaceaccess.SeamTool,
	})
	if err != nil || !decision.Allowed {
		return CallRequest{}, workspaceAccessDeniedError(req.ToolID)
	}
	req.WorkspaceID = requestedWorkspaceID
	return r.enrichTrustedWorkspaceRoot(ctx, req)
}

func (r *RuntimeRegistry) enrichTrustedWorkspaceRoot(
	ctx context.Context,
	req CallRequest,
) (CallRequest, error) {
	req.TrustedWorkspaceRoot = strings.TrimSpace(req.TrustedWorkspaceRoot)
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID == "" || r == nil || r.workspaceRootResolver == nil {
		return req, nil
	}
	root, err := r.workspaceRootResolver(ctx, workspaceID)
	root = strings.TrimSpace(root)
	if err != nil || root == "" {
		return CallRequest{}, workspaceAccessDeniedError(req.ToolID)
	}
	req.TrustedWorkspaceRoot = root
	return req, nil
}

func workspaceAccessActorFromScope(scope Scope) workspaceaccess.ActorRef {
	kind := workspaceaccess.ActorKind(strings.TrimSpace(scope.ActorKind))
	if kind == "" && strings.TrimSpace(scope.SessionID) != "" {
		kind = workspaceaccess.ActorAgentSession
	}
	if scope.Operator && kind == "" {
		kind = workspaceaccess.ActorHuman
	}
	return workspaceaccess.ActorRef{
		Kind:        kind,
		SessionID:   strings.TrimSpace(scope.SessionID),
		WorkspaceID: strings.TrimSpace(scope.WorkspaceID),
		AgentName:   strings.TrimSpace(scope.AgentName),
		Operator:    scope.Operator,
	}
}

func workspaceAccessDeniedError(id ToolID) error {
	return NewToolError(
		ErrorCodeDenied,
		id,
		fmt.Sprintf("tool %q denied: %s", id, workspaceaccess.DenialHint),
		ErrToolDenied,
		ReasonWorkspaceAccessDenied,
	)
}
