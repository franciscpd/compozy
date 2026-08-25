//go:build integration

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compozy/compozy/internal/acp"
	extensionpkg "github.com/compozy/compozy/internal/extension"
	hookspkg "github.com/compozy/compozy/internal/hooks"
	"github.com/compozy/compozy/internal/resources"
	"github.com/compozy/compozy/internal/session"
	"github.com/compozy/compozy/internal/testutil"
	"github.com/compozy/compozy/internal/toolruntime"
	toolspkg "github.com/compozy/compozy/internal/tools"
	"go.uber.org/goleak"
)

type hookBindingIntegrationHarness struct {
	kernel   *resources.Kernel
	codecs   *resources.CodecRegistry
	store    resources.Store[hookspkg.HookDecl]
	driver   resources.ReconcileDriver
	hooks    *hookspkg.Hooks
	notifier *hooksNotifier
	actor    resources.MutationActor
}

func TestGeneratedRequiredExtensionHookContainsMatchedToolCall(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })

	ctx := testutil.Context(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("filepath.Abs(repo root) error = %v", err)
	}
	failureMarker := filepath.Join(t.TempDir(), "deliberate-hook-failure.json")
	t.Setenv("COMPOZY_REQUIRED_HOOK_MARKER", failureMarker)
	sourceDir := filepath.Join(repoRoot, "internal", "daemon", "testdata", "required-hook-fixture")

	buildCtx, cancelBuild := context.WithTimeout(ctx, time.Minute)
	defer cancelBuild()
	built, err := extensionpkg.BuildBundle(buildCtx, extensionpkg.BuildRequest{
		SourceDir: sourceDir,
		OutputDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildBundle(required hook fixture) error = %v", err)
	}

	database := openDaemonTestGlobalDB(t)
	extensionRegistry := extensionpkg.NewRegistry(database.DB())
	if err := extensionRegistry.Install(built.Manifest, built.GenerationDir, built.GenerationHash); err != nil {
		t.Fatalf("Registry.Install(required hook fixture) error = %v", err)
	}

	processStore := toolruntime.NewMemoryStore()
	processRegistry := toolruntime.NewRegistry(processStore)
	manager := extensionpkg.NewManager(
		extensionRegistry,
		extensionpkg.WithHealthCheckTimeout(100*time.Millisecond),
		extensionpkg.WithSubprocessSignalGrace(25*time.Millisecond),
		extensionpkg.WithProcessRegistry(processRegistry),
	)
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Manager.Start(required hook fixture) error = %v", err)
	}
	managerRunning := true
	t.Cleanup(func() {
		if managerRunning {
			if err := manager.Stop(testutil.Context(t)); err != nil {
				t.Fatalf("Manager.Stop(required hook fixture) cleanup error = %v", err)
			}
		}
	})

	harness := newHookBindingIntegrationHarness(t, nil, processRegistry)
	publisher, err := (&Daemon{}).newHookBindingPublisher(
		&bootState{
			resourceKernel:    harness.kernel,
			resourceCodecs:    harness.codecs,
			resourceReconcile: harness.driver,
			logger:            discardLogger(),
		},
		harness.hooks,
		[]hookBindingDeclarationProvider{
			extensionDeclarationProvider(func() extensionRuntime { return manager }, nil),
		},
	)
	if err != nil {
		t.Fatalf("newHookBindingPublisher() error = %v", err)
	}
	if err := publisher.Sync(ctx); err != nil {
		t.Fatalf("hookBindingSourceSyncer.Sync() error = %v", err)
	}

	var catalog []hookspkg.CatalogEntry
	waitForConditionWithin(t, "generated extension hook binding reconciliation", 5*time.Second, func() bool {
		var catalogErr error
		catalog, catalogErr = harness.hooks.Catalog(hookspkg.CatalogFilter{Event: hookspkg.HookToolPreCall})
		return catalogErr == nil && len(catalog) == 1
	})
	if got, want := len(catalog), 1; got != want {
		t.Fatalf("len(hooks.Catalog(tool.pre_call)) = %d, want %d: %#v", got, want, catalog)
	}
	entry := catalog[0]
	if entry.Name != "publisher-guard" || entry.Event != hookspkg.HookToolPreCall ||
		entry.Mode != hookspkg.HookModeSync || !entry.Required ||
		entry.Source != hookspkg.HookSourceExtension || entry.ExecutorKind != hookspkg.HookExecutorSubprocess ||
		entry.Matcher.AgentName != "batuta-publisher" || entry.Metadata["extension"] != "required-hook-fixture" {
		t.Fatalf("generated hook catalog entry = %#v, want required sync extension hook for batuta-publisher", entry)
	}

	protectedTool := newHookProtectedTool()
	toolRegistry, err := toolspkg.NewRegistry(
		toolspkg.WithProviders(protectedTool),
		toolspkg.WithPolicyInputs(toolspkg.PolicyInputs{
			SystemPermissionMode: toolspkg.PermissionModeApproveAll,
			ExternalDefault:      toolspkg.ExternalDefaultEnabled,
		}, toolspkg.ToolsetCatalog{}),
		toolspkg.WithHookRunner(harness.notifier),
	)
	if err != nil {
		t.Fatalf("tools.NewRegistry(protected publish) error = %v", err)
	}
	matchedResult, matchedErr := toolRegistry.Call(ctx, toolspkg.Scope{
		SessionID: "publisher-session", AgentName: "batuta-publisher",
	}, toolspkg.CallRequest{
		ToolID: "protected_publish", ToolCallID: "publisher-call", TurnID: "publisher-turn",
		Input: json.RawMessage(`{"release":"v1"}`),
	})
	if matchedErr == nil {
		t.Fatalf("RuntimeRegistry.Call(batuta-publisher) error = nil, want required hook failure")
	}
	var toolErr *toolspkg.ToolError
	if !errors.As(matchedErr, &toolErr) || toolErr.Code != toolspkg.ErrorCodeDenied ||
		toolErr.ToolID != "protected_publish" {
		t.Fatalf("RuntimeRegistry.Call(batuta-publisher) error = %#v, want structured tool_denied", matchedErr)
	}
	if toolErr.Err == nil || !strings.Contains(toolErr.Err.Error(), "exit status 23") {
		t.Fatalf("matched hook cause = %v, want deliberate fixture exit status", toolErr.Err)
	}
	failureEvidence, err := os.ReadFile(failureMarker)
	if err != nil {
		t.Fatalf("os.ReadFile(deliberate hook failure marker) error = %v", err)
	}
	if got, want := string(failureEvidence), `{"agent_name":"batuta-publisher","tool_id":"protected_publish"}`; got != want {
		t.Fatalf("deliberate hook failure marker = %s, want %s", got, want)
	}
	if len(matchedResult.Structured) != 0 {
		t.Fatalf("matched tool result = %#v, want empty result on containment", matchedResult)
	}
	if got := protectedTool.handlerCalls.Load(); got != 0 {
		t.Fatalf("protected handler calls after matched failure = %d, want 0", got)
	}

	nonMatchResult, err := toolRegistry.Call(ctx, toolspkg.Scope{
		SessionID: "publisher-session", AgentName: "batuta-reviewer",
	}, toolspkg.CallRequest{
		ToolID: "protected_publish", ToolCallID: "publisher-call", TurnID: "publisher-turn",
		Input: json.RawMessage(`{"release":"v1"}`),
	})
	if err != nil {
		t.Fatalf("RuntimeRegistry.Call(batuta-reviewer) error = %v", err)
	}
	if got := string(nonMatchResult.Structured); got != `{"published":true}` {
		t.Fatalf("non-matching tool result = %s, want published success", got)
	}
	if got := protectedTool.handlerCalls.Load(); got != 1 {
		t.Fatalf("protected handler calls after non-match = %d, want 1", got)
	}

	if err := manager.Stop(ctx); err != nil {
		t.Fatalf("Manager.Stop(required hook fixture) error = %v", err)
	}
	managerRunning = false
	pending, err := processStore.ListProcessRecords(ctx, toolruntime.ProcessQuery{States: []toolruntime.ProcessState{
		toolruntime.ProcessStateRunning,
		toolruntime.ProcessStateInterrupting,
	}})
	if err != nil {
		t.Fatalf("ListProcessRecords(pending) error = %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending fixture processes = %#v, want none", pending)
	}
}

func TestRuntimeRegistryRequiredToolMatchersUseTrustedContext(t *testing.T) {
	tests := []struct {
		name                 string
		descriptorReadOnly   bool
		matcherReadOnly      bool
		matcherWorkspace     string
		resolvedWorkspace    string
		wantDenied           bool
		wantHandlerCallCount int32
	}{
		{
			name:               "Should deny matching read-only tool in matching workspace root",
			descriptorReadOnly: true, matcherReadOnly: true,
			matcherWorkspace: "/workspace/alpha", resolvedWorkspace: "/workspace/alpha",
			wantDenied: true, wantHandlerCallCount: 0,
		},
		{
			name:               "Should not let explicit false overmatch read-only tool",
			descriptorReadOnly: true, matcherReadOnly: false,
			matcherWorkspace: "/workspace/alpha", resolvedWorkspace: "/workspace/alpha",
			wantHandlerCallCount: 1,
		},
		{
			name:               "Should deny matching explicit false mutating tool",
			descriptorReadOnly: false, matcherReadOnly: false,
			matcherWorkspace: "/workspace/alpha", resolvedWorkspace: "/workspace/alpha",
			wantDenied: true, wantHandlerCallCount: 0,
		},
		{
			name:               "Should not match a different workspace root",
			descriptorReadOnly: true, matcherReadOnly: true,
			matcherWorkspace: "/workspace/beta", resolvedWorkspace: "/workspace/alpha",
			wantHandlerCallCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hookFailure := errors.New("required matcher hook failed")
			h := newHookBindingIntegrationHarness(t, map[string]hookspkg.Executor{
				"matcher-guard": hookspkg.NewTypedNativeExecutor(
					func(
						context.Context,
						hookspkg.RegisteredHook,
						hookspkg.ToolPreCallPayload,
					) (hookspkg.ToolCallPatch, error) {
						return hookspkg.ToolCallPatch{}, hookFailure
					},
				),
			})
			matcherReadOnly := tt.matcherReadOnly
			h.putBinding(t, "matcher-guard", 0, resources.ResourceScope{
				Kind: resources.ResourceScopeKindUser,
			}, hookspkg.HookDecl{
				Name: "matcher-guard", Event: hookspkg.HookToolPreCall,
				Source: hookspkg.HookSourceNative, Mode: hookspkg.HookModeSync, Required: true,
				ExecutorKind: hookspkg.HookExecutorNative,
				Matcher: hookspkg.HookMatcher{
					ToolReadOnly: &matcherReadOnly, WorkspaceRoot: tt.matcherWorkspace,
				},
			})
			if err := h.driver.RunBoot(testutil.Context(t)); err != nil {
				t.Fatalf("driver.RunBoot() error = %v", err)
			}

			protectedTool := newHookProtectedTool()
			protectedTool.descriptor.ReadOnly = tt.descriptorReadOnly
			if tt.descriptorReadOnly {
				protectedTool.descriptor.Risk = toolspkg.RiskRead
			}
			registry, err := toolspkg.NewRegistry(
				toolspkg.WithProviders(protectedTool),
				toolspkg.WithPolicyInputs(toolspkg.PolicyInputs{
					SystemPermissionMode: toolspkg.PermissionModeApproveAll,
				}, toolspkg.ToolsetCatalog{}),
				toolspkg.WithHookRunner(h.notifier),
				toolspkg.WithTrustedWorkspaceRootResolver(
					func(_ context.Context, workspaceID string) (string, error) {
						if workspaceID != "ws-alpha" {
							t.Fatalf("workspace root resolver id = %q, want ws-alpha", workspaceID)
						}
						return tt.resolvedWorkspace, nil
					},
				),
			)
			if err != nil {
				t.Fatalf("tools.NewRegistry() error = %v", err)
			}
			_, callErr := registry.Call(testutil.Context(t), toolspkg.Scope{
				WorkspaceID: "ws-alpha", AgentName: "batuta-publisher",
			}, toolspkg.CallRequest{
				ToolID: "protected_publish", ReadOnly: !tt.descriptorReadOnly,
				TrustedWorkspaceRoot: "/workspace/beta", Input: json.RawMessage(`{"release":"v1"}`),
			})
			if tt.wantDenied {
				if !errors.Is(callErr, toolspkg.ErrToolDenied) || !errors.Is(callErr, hookFailure) {
					t.Fatalf("RuntimeRegistry.Call() error = %v, want required hook denial", callErr)
				}
			} else if callErr != nil {
				t.Fatalf("RuntimeRegistry.Call() error = %v, want nil", callErr)
			}
			if got := protectedTool.handlerCalls.Load(); got != tt.wantHandlerCallCount {
				t.Fatalf("protected handler calls = %d, want %d", got, tt.wantHandlerCallCount)
			}
		})
	}

	t.Run("Should not match caller workspace root without a workspace identity", func(t *testing.T) {
		hookFailure := errors.New("required matcher hook failed")
		h := newHookBindingIntegrationHarness(t, map[string]hookspkg.Executor{
			"workspace-guard": hookspkg.NewTypedNativeExecutor(
				func(
					context.Context,
					hookspkg.RegisteredHook,
					hookspkg.ToolPreCallPayload,
				) (hookspkg.ToolCallPatch, error) {
					return hookspkg.ToolCallPatch{}, hookFailure
				},
			),
		})
		h.putBinding(t, "workspace-guard", 0, resources.ResourceScope{
			Kind: resources.ResourceScopeKindUser,
		}, hookspkg.HookDecl{
			Name: "workspace-guard", Event: hookspkg.HookToolPreCall,
			Source: hookspkg.HookSourceNative, Mode: hookspkg.HookModeSync, Required: true,
			ExecutorKind: hookspkg.HookExecutorNative,
			Matcher:      hookspkg.HookMatcher{WorkspaceRoot: "/workspace/beta"},
		})
		if err := h.driver.RunBoot(testutil.Context(t)); err != nil {
			t.Fatalf("driver.RunBoot() error = %v", err)
		}

		protectedTool := newHookProtectedTool()
		registry, err := toolspkg.NewRegistry(
			toolspkg.WithProviders(protectedTool),
			toolspkg.WithPolicyInputs(toolspkg.PolicyInputs{
				SystemPermissionMode: toolspkg.PermissionModeApproveAll,
			}, toolspkg.ToolsetCatalog{}),
			toolspkg.WithHookRunner(h.notifier),
		)
		if err != nil {
			t.Fatalf("tools.NewRegistry() error = %v", err)
		}
		_, callErr := registry.Call(testutil.Context(t), toolspkg.Scope{
			AgentName: "batuta-publisher",
		}, toolspkg.CallRequest{
			ToolID: toolspkg.ToolID("protected_publish"), TrustedWorkspaceRoot: "/workspace/beta",
			Input: json.RawMessage(`{"release":"v1"}`),
		})
		if callErr != nil {
			t.Fatalf("RuntimeRegistry.Call() error = %v, want nil", callErr)
		}
		if got := protectedTool.handlerCalls.Load(); got != 1 {
			t.Fatalf("protected handler calls = %d, want 1", got)
		}
	})
}

func TestHookBindingResourceReconcileFiresToolHookThroughSessionNotifier(t *testing.T) {
	toolPayloads := make(chan hookspkg.ToolPreCallPayload, 1)
	h := newHookBindingIntegrationHarness(t, map[string]hookspkg.Executor{
		"tool-hook": hookspkg.NewTypedNativeExecutor(
			func(_ context.Context, _ hookspkg.RegisteredHook, payload hookspkg.ToolPreCallPayload) (hookspkg.ToolCallPatch, error) {
				select {
				case toolPayloads <- payload:
				default:
				}
				return hookspkg.ToolCallPatch{}, nil
			},
		),
	})

	record := h.putBinding(t, "tool-hook", 0, resources.ResourceScope{
		Kind: resources.ResourceScopeKindWorkspace,
		ID:   "ws-1",
	}, hookspkg.HookDecl{
		Name:         "tool-hook",
		Event:        hookspkg.HookToolPreCall,
		Source:       hookspkg.HookSourceNative,
		Mode:         hookspkg.HookModeSync,
		ExecutorKind: hookspkg.HookExecutorNative,
		Matcher: hookspkg.HookMatcher{
			AgentName: "codex",
			ToolID:    "Read",
		},
	})
	if record.Version <= 0 {
		t.Fatalf("record.Version = %d, want positive", record.Version)
	}
	if err := h.driver.RunBoot(testutil.Context(t)); err != nil {
		t.Fatalf("driver.RunBoot() error = %v", err)
	}

	h.notifier.OnAgentEventForSession(testutil.Context(t), integrationSession(), acp.AgentEvent{
		Type:       acp.EventTypeToolCall,
		SessionID:  "acp-session-1",
		TurnID:     "turn-1",
		ToolCallID: "tool-1",
		Raw:        mustMarshalJSON(t, toolEventRaw("tool_call", "", nil)),
	})

	select {
	case payload := <-toolPayloads:
		if payload.SessionID != "sess-1" || payload.WorkspaceID != "ws-1" {
			t.Fatalf("payload.SessionContext = %#v, want session metadata", payload.SessionContext)
		}
		if payload.ToolID != "Read" {
			t.Fatalf("payload.ToolID = %q, want %q", payload.ToolID, "Read")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resource-backed tool.pre_call hook")
	}
}

func TestHookBindingResourceReconcileFiresPermissionHooksThroughSessionNotifier(t *testing.T) {
	requests := make(chan hookspkg.PermissionRequestPayload, 1)
	resolved := make(chan hookspkg.PermissionResolvedPayload, 1)
	denied := make(chan hookspkg.PermissionDeniedPayload, 1)

	h := newHookBindingIntegrationHarness(t, map[string]hookspkg.Executor{
		"perm-request": hookspkg.NewTypedNativeExecutor(
			func(_ context.Context, _ hookspkg.RegisteredHook, payload hookspkg.PermissionRequestPayload) (hookspkg.PermissionRequestPatch, error) {
				requests <- payload
				return hookspkg.PermissionRequestPatch{}, nil
			},
		),
		"perm-resolved": hookspkg.NewTypedNativeExecutor(
			func(_ context.Context, _ hookspkg.RegisteredHook, payload hookspkg.PermissionResolvedPayload) (hookspkg.PermissionResolvedPatch, error) {
				resolved <- payload
				return hookspkg.PermissionResolvedPatch{}, nil
			},
		),
		"perm-denied": hookspkg.NewTypedNativeExecutor(
			func(_ context.Context, _ hookspkg.RegisteredHook, payload hookspkg.PermissionDeniedPayload) (hookspkg.PermissionDeniedPatch, error) {
				denied <- payload
				return hookspkg.PermissionDeniedPatch{}, nil
			},
		),
	})

	h.putBinding(t, "perm-request", 0, resources.ResourceScope{
		Kind: resources.ResourceScopeKindWorkspace,
		ID:   "ws-1",
	}, hookspkg.HookDecl{
		Name:         "perm-request",
		Event:        hookspkg.HookPermissionRequest,
		Source:       hookspkg.HookSourceNative,
		Mode:         hookspkg.HookModeSync,
		ExecutorKind: hookspkg.HookExecutorNative,
		Matcher: hookspkg.HookMatcher{
			AgentName: "codex",
			ToolName:  "Read",
		},
	})
	h.putBinding(t, "perm-resolved", 0, resources.ResourceScope{
		Kind: resources.ResourceScopeKindWorkspace,
		ID:   "ws-1",
	}, hookspkg.HookDecl{
		Name:         "perm-resolved",
		Event:        hookspkg.HookPermissionResolved,
		Source:       hookspkg.HookSourceNative,
		ExecutorKind: hookspkg.HookExecutorNative,
		Matcher: hookspkg.HookMatcher{
			AgentName: "codex",
			ToolName:  "Read",
		},
	})
	h.putBinding(t, "perm-denied", 0, resources.ResourceScope{
		Kind: resources.ResourceScopeKindWorkspace,
		ID:   "ws-1",
	}, hookspkg.HookDecl{
		Name:         "perm-denied",
		Event:        hookspkg.HookPermissionDenied,
		Source:       hookspkg.HookSourceNative,
		ExecutorKind: hookspkg.HookExecutorNative,
		Matcher: hookspkg.HookMatcher{
			AgentName: "codex",
			ToolName:  "Read",
		},
	})
	if err := h.driver.RunBoot(testutil.Context(t)); err != nil {
		t.Fatalf("driver.RunBoot() error = %v", err)
	}

	sessionValue := integrationSession()
	h.notifier.OnAgentEventForSession(testutil.Context(t), sessionValue, (acp.AgentEvent{
		Type:      acp.EventTypePermission,
		SessionID: "acp-session-1",
		TurnID:    "turn-1",
		Action:    "session/request_permission",
		Resource:  "/tmp/secret.txt",
		Raw:       mustMarshalJSON(t, permissionEventRaw("perm-1", "", "Read")),
	}).WithRequestID("perm-1"))
	h.notifier.OnAgentEventForSession(testutil.Context(t), sessionValue, (acp.AgentEvent{
		Type:      acp.EventTypePermission,
		SessionID: "acp-session-1",
		TurnID:    "turn-1",
		Action:    "session/request_permission",
		Resource:  "/tmp/secret.txt",
		Decision:  "allow",
		Raw:       mustMarshalJSON(t, permissionEventRaw("perm-1", "allow", "Read")),
	}).WithRequestID("perm-1"))
	h.notifier.OnAgentEventForSession(testutil.Context(t), sessionValue, (acp.AgentEvent{
		Type:      acp.EventTypePermission,
		SessionID: "acp-session-1",
		TurnID:    "turn-1",
		Action:    "session/request_permission",
		Resource:  "/tmp/secret.txt",
		Decision:  "deny",
		Raw:       mustMarshalJSON(t, permissionEventRaw("perm-1", "deny", "Read")),
	}).WithRequestID("perm-1"))

	select {
	case payload := <-requests:
		if payload.SessionID != "sess-1" || payload.ToolCall.Kind != "Read" {
			t.Fatalf("permission.request payload = %#v, want sess-1 and Read tool", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for permission.request hook")
	}

	select {
	case payload := <-resolved:
		if payload.Decision != "allow" || payload.DecisionClass != "resolved" {
			t.Fatalf("permission.resolved payload = %#v, want allow/resolved", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for permission.resolved hook")
	}

	select {
	case payload := <-denied:
		if payload.Decision != "deny" || payload.DecisionClass != "denied" {
			t.Fatalf("permission.denied payload = %#v, want deny/denied", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for permission.denied hook")
	}
}

func TestHookBindingResourceReconcileFiresTaskRunHookThroughDaemonBridge(t *testing.T) {
	taskRunPayloads := make(chan hookspkg.TaskRunEnqueuedPayload, 1)
	h := newHookBindingIntegrationHarness(t, map[string]hookspkg.Executor{
		"task-run-hook": hookspkg.NewTypedNativeExecutor(
			func(
				_ context.Context,
				_ hookspkg.RegisteredHook,
				payload hookspkg.TaskRunEnqueuedPayload,
			) (hookspkg.TaskRunObservationPatch, error) {
				select {
				case taskRunPayloads <- payload:
				default:
				}
				return hookspkg.TaskRunObservationPatch{}, nil
			},
		),
	})

	h.putBinding(t, "task-run-hook", 0, resources.ResourceScope{
		Kind: resources.ResourceScopeKindWorkspace,
		ID:   "ws-1",
	}, hookspkg.HookDecl{
		Name:         "task-run-hook",
		Event:        hookspkg.HookTaskRunEnqueued,
		Source:       hookspkg.HookSourceNative,
		Mode:         hookspkg.HookModeSync,
		ExecutorKind: hookspkg.HookExecutorNative,
		Matcher: hookspkg.HookMatcher{
			WorkspaceID: "ws-1",
			Autonomy: &hookspkg.AutonomyMatcher{
				TaskID:               "task-1",
				ParticipationChannel: "coord-ch-1",
			},
		},
	})
	if err := h.driver.RunBoot(testutil.Context(t)); err != nil {
		t.Fatalf("driver.RunBoot() error = %v", err)
	}

	if _, err := h.notifier.DispatchTaskRunEnqueued(testutil.Context(t), hookspkg.TaskRunEnqueuedPayload{
		PayloadBase: hookspkg.PayloadBase{
			Event:     hookspkg.HookTaskRunEnqueued,
			Timestamp: time.Date(2026, 4, 15, 11, 59, 0, 0, time.UTC),
		},
		TaskRunContext: hookspkg.TaskRunContext{
			TaskID:                       "task-1",
			RunID:                        "run-mismatched-channel",
			WorkspaceID:                  "ws-1",
			ResolvedNetworkParticipation: daemonTestLiveParticipationPtr("ws-1", "other-channel"),
		},
	}); err != nil {
		t.Fatalf("DispatchTaskRunEnqueued(mismatched channel) error = %v", err)
	}
	select {
	case payload := <-taskRunPayloads:
		t.Fatalf("mismatched participation dispatched payload %#v", payload.TaskRunContext)
	default:
	}

	if _, err := h.notifier.DispatchTaskRunEnqueued(testutil.Context(t), hookspkg.TaskRunEnqueuedPayload{
		PayloadBase: hookspkg.PayloadBase{
			Event:     hookspkg.HookTaskRunEnqueued,
			Timestamp: time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		},
		TaskRunContext: hookspkg.TaskRunContext{
			TaskID:                       "task-1",
			RunID:                        "run-1",
			WorkspaceID:                  "ws-1",
			ResolvedNetworkParticipation: daemonTestLiveParticipationPtr("ws-1", "coord-ch-1"),
		},
	}); err != nil {
		t.Fatalf("DispatchTaskRunEnqueued() error = %v", err)
	}

	select {
	case payload := <-taskRunPayloads:
		if payload.RunID != "run-1" ||
			payload.ResolvedNetworkParticipation == nil ||
			payload.ResolvedNetworkParticipation.WorkspaceID != "ws-1" ||
			resolvedParticipationChannelID(payload.ResolvedNetworkParticipation) != "coord-ch-1" {
			t.Fatalf("task-run payload = %#v, want run and participation channel metadata", payload.TaskRunContext)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resource-backed task.run.enqueued hook")
	}
}

func TestHookBindingResourceReconcileFiresTaskStatusChangedHookThroughDaemonBridge(t *testing.T) {
	t.Parallel()

	t.Run("Should deliver async config task status payload through daemon bridge", func(t *testing.T) {
		t.Parallel()

		capturePath := filepath.Join(t.TempDir(), "task-status-payload.json")
		h := newHookBindingIntegrationHarness(t, nil)

		h.putBinding(t, "task-status-hook", 0, resources.ResourceScope{
			Kind: resources.ResourceScopeKindWorkspace,
			ID:   "ws-1",
		}, hookspkg.HookDecl{
			Name:         "task-status-hook",
			Event:        hookspkg.HookTaskStatusChanged,
			Source:       hookspkg.HookSourceConfig,
			Mode:         hookspkg.HookModeAsync,
			ExecutorKind: hookspkg.HookExecutorSubprocess,
			Command:      "/bin/sh",
			Args:         []string{"-c", `payload=$(cat); printf '%s' "$payload" > "$HOOK_CAPTURE"; printf '{}'`},
			Env:          map[string]string{"HOOK_CAPTURE": capturePath},
			Matcher: hookspkg.HookMatcher{
				WorkspaceID: "ws-1",
				Autonomy: &hookspkg.AutonomyMatcher{
					TaskID: "task-1",
				},
			},
		})
		if err := h.driver.RunBoot(testutil.Context(t)); err != nil {
			t.Fatalf("driver.RunBoot() error = %v", err)
		}

		if _, err := h.notifier.DispatchTaskStatusChanged(testutil.Context(t), hookspkg.TaskStatusChangedPayload{
			PayloadBase: hookspkg.PayloadBase{
				Event:     hookspkg.HookTaskStatusChanged,
				Timestamp: time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
			},
			TaskContext: hookspkg.TaskContext{
				TaskID:       "task-1",
				ParentTaskID: "task-parent",
				WorkspaceID:  "ws-1",
			},
			FromStatus: "ready",
			ToStatus:   "blocked",
		}); err != nil {
			t.Fatalf("DispatchTaskStatusChanged() error = %v", err)
		}

		payload := waitForCapturedTaskStatusChangedPayload(t, capturePath)
		if payload.TaskID != "task-1" ||
			payload.ParentTaskID != "task-parent" ||
			payload.WorkspaceID != "ws-1" ||
			payload.FromStatus != "ready" ||
			payload.ToStatus != "blocked" {
			t.Fatalf("task status payload = %#v, want status transition and task correlation fields", payload)
		}
	})
}

func TestHookBindingResourceReconcileFailurePreservesAppliedRuntimeState(t *testing.T) {
	toolPayloads := make(chan hookspkg.ToolPreCallPayload, 2)

	h := newHookBindingIntegrationHarness(t, map[string]hookspkg.Executor{
		"tool-stable": hookspkg.NewTypedNativeExecutor(
			func(_ context.Context, _ hookspkg.RegisteredHook, payload hookspkg.ToolPreCallPayload) (hookspkg.ToolCallPatch, error) {
				toolPayloads <- payload
				return hookspkg.ToolCallPatch{}, nil
			},
		),
	})

	record := h.putBinding(t, "tool-hook", 0, resources.ResourceScope{
		Kind: resources.ResourceScopeKindWorkspace,
		ID:   "ws-1",
	}, hookspkg.HookDecl{
		Name:         "tool-stable",
		Event:        hookspkg.HookToolPreCall,
		Source:       hookspkg.HookSourceNative,
		Mode:         hookspkg.HookModeSync,
		ExecutorKind: hookspkg.HookExecutorNative,
		Matcher: hookspkg.HookMatcher{
			AgentName: "codex",
			ToolID:    "Read",
		},
	})
	if err := h.driver.RunBoot(testutil.Context(t)); err != nil {
		t.Fatalf("initial driver.RunBoot() error = %v", err)
	}
	h.notifier.OnAgentEventForSession(testutil.Context(t), integrationSession(), acp.AgentEvent{
		Type:       acp.EventTypeToolCall,
		SessionID:  "acp-session-1",
		TurnID:     "turn-1",
		ToolCallID: "tool-1",
		Raw:        mustMarshalJSON(t, toolEventRaw("tool_call", "", nil)),
	})
	select {
	case <-toolPayloads:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial stable hook dispatch")
	}

	updated := h.putBinding(t, "tool-hook", record.Version, resources.ResourceScope{
		Kind: resources.ResourceScopeKindWorkspace,
		ID:   "ws-1",
	}, hookspkg.HookDecl{
		Name:         "tool-missing",
		Event:        hookspkg.HookToolPreCall,
		Source:       hookspkg.HookSourceNative,
		Mode:         hookspkg.HookModeSync,
		ExecutorKind: hookspkg.HookExecutorNative,
		Matcher: hookspkg.HookMatcher{
			AgentName: "codex",
			ToolID:    "Read",
		},
	})
	if updated.Version <= record.Version {
		t.Fatalf("updated.Version = %d, want greater than %d", updated.Version, record.Version)
	}
	if err := h.driver.RunBoot(testutil.Context(t)); err == nil {
		t.Fatal("driver.RunBoot() error = nil, want missing executor failure")
	}

	h.notifier.OnAgentEventForSession(testutil.Context(t), integrationSession(), acp.AgentEvent{
		Type:       acp.EventTypeToolCall,
		SessionID:  "acp-session-1",
		TurnID:     "turn-1",
		ToolCallID: "tool-1",
		Raw:        mustMarshalJSON(t, toolEventRaw("tool_call", "", nil)),
	})
	select {
	case payload := <-toolPayloads:
		if payload.ToolID != "Read" || payload.SessionID != "sess-1" {
			t.Fatalf("post-failure payload = %#v, want stable hook payload", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for preserved stable hook after projector failure")
	}
}

func waitForCapturedTaskStatusChangedPayload(t *testing.T, capturePath string) hookspkg.TaskStatusChangedPayload {
	t.Helper()

	deadline := time.After(time.Second)
	for {
		data, err := os.ReadFile(capturePath)
		switch {
		case err == nil && len(data) > 0:
			var payload hookspkg.TaskStatusChangedPayload
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("json.Unmarshal(captured task status payload) error = %v", err)
			}
			return payload
		case err != nil && !os.IsNotExist(err):
			t.Fatalf("os.ReadFile(%q) error = %v", capturePath, err)
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for resource-backed task.status_changed hook")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func newHookBindingIntegrationHarness(
	t *testing.T,
	nativeExecutors map[string]hookspkg.Executor,
	processRegistries ...*toolruntime.Registry,
) *hookBindingIntegrationHarness {
	t.Helper()

	db := openDaemonTestGlobalDB(t)
	kernel, err := resources.NewKernel(db.DB())
	if err != nil {
		t.Fatalf("resources.NewKernel() error = %v", err)
	}
	codec, err := newHookBindingCodec()
	if err != nil {
		t.Fatalf("newHookBindingCodec() error = %v", err)
	}
	codecs := resources.NewCodecRegistry()
	if err := resources.RegisterCodec(codecs, codec); err != nil {
		t.Fatalf("resources.RegisterCodec(hook binding) error = %v", err)
	}
	store, err := newHookBindingStore(kernel, codec)
	if err != nil {
		t.Fatalf("newHookBindingStore() error = %v", err)
	}
	hooks := hookspkg.NewHooks(
		hookspkg.WithLogger(discardLogger()),
		hookspkg.WithExecutorResolver(daemonExecutorResolverWithSecrets(
			nativeExecutors,
			nil,
			processRegistries...,
		)),
	)
	t.Cleanup(hooks.Close)

	registration, err := resources.NewTypedProjectorRegistration(codec, newHookBindingProjector(hooks))
	if err != nil {
		t.Fatalf("NewTypedProjectorRegistration() error = %v", err)
	}
	driver, err := resources.NewReconcileDriver(
		kernel,
		resources.MutationActor{
			Kind:     resources.MutationActorKindDaemon,
			ID:       "integration-control",
			Source:   resources.ResourceSource{Kind: resources.ResourceSourceKind("daemon"), ID: "integration"},
			MaxScope: resources.ResourceScope{Kind: resources.ResourceScopeKindUser},
		},
		[]resources.ProjectorRegistration{registration},
		resources.WithReconcileLogger(discardLogger()),
	)
	if err != nil {
		t.Fatalf("resources.NewReconcileDriver() error = %v", err)
	}
	t.Cleanup(func() {
		if err := driver.Close(testutil.Context(t)); err != nil {
			t.Fatalf("driver.Close() error = %v", err)
		}
	})

	notifier := newHooksNotifier(discardLogger(), func() time.Time {
		return time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	})
	notifier.setRuntime(hooks, nil)

	return &hookBindingIntegrationHarness{
		kernel:   kernel,
		codecs:   codecs,
		store:    store,
		driver:   driver,
		hooks:    hooks,
		notifier: notifier,
		actor: resources.MutationActor{
			Kind:     resources.MutationActorKindDaemon,
			ID:       "integration-writer",
			Source:   resources.ResourceSource{Kind: resources.ResourceSourceKind("daemon"), ID: "integration"},
			MaxScope: resources.ResourceScope{Kind: resources.ResourceScopeKindUser},
		},
	}
}

type hookProtectedTool struct {
	descriptor   toolspkg.Descriptor
	handlerCalls atomic.Int32
}

var _ toolspkg.Provider = (*hookProtectedTool)(nil)
var _ toolspkg.Handle = (*hookProtectedTool)(nil)

func newHookProtectedTool() *hookProtectedTool {
	return &hookProtectedTool{descriptor: toolspkg.Descriptor{
		ID:               "protected_publish",
		ToolPresentation: toolspkg.NewToolPresentation("Protected Publish", "", ""),
		Description:      "Publish one protected release",
		InputSchema:      json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"release":{"type":"string"}},"required":["release"]}`),
		OutputSchema:     json.RawMessage(`{"type":"object"}`),
		Backend: toolspkg.BackendRef{
			Kind:       toolspkg.BackendNativeGo,
			NativeName: "protected_publish",
		},
		Source: toolspkg.SourceRef{
			Kind:  toolspkg.SourceBuiltin,
			Owner: "daemon",
		},
		Visibility:      toolspkg.VisibilityModel,
		Risk:            toolspkg.RiskMutating,
		ConcurrencySafe: true,
		MaxResultBytes:  1024,
	}}
}

func (p *hookProtectedTool) ID() toolspkg.SourceRef { return p.descriptor.Source }

func (p *hookProtectedTool) List(context.Context, toolspkg.Scope) ([]toolspkg.Descriptor, error) {
	return []toolspkg.Descriptor{p.descriptor}, nil
}

func (p *hookProtectedTool) Resolve(
	_ context.Context,
	_ toolspkg.Scope,
	id toolspkg.ToolID,
) (toolspkg.Handle, bool, error) {
	return p, id == p.descriptor.ID, nil
}

func (p *hookProtectedTool) Descriptor() toolspkg.Descriptor { return p.descriptor }

func (*hookProtectedTool) Availability(context.Context, toolspkg.Scope) toolspkg.Availability {
	return toolspkg.Availability{
		Registered: true, Enabled: true, Available: true, Authorized: true, Executable: true,
	}
}

func (p *hookProtectedTool) Call(context.Context, toolspkg.CallRequest) (toolspkg.ToolResult, error) {
	p.handlerCalls.Add(1)
	return toolspkg.ToolResult{Structured: json.RawMessage(`{"published":true}`)}, nil
}

func (h *hookBindingIntegrationHarness) putBinding(
	t *testing.T,
	id string,
	expectedVersion int64,
	scope resources.ResourceScope,
	decl hookspkg.HookDecl,
) resources.Record[hookspkg.HookDecl] {
	t.Helper()

	spec, err := validateHookBindingSpec(testutil.Context(t), scope, decl)
	if err != nil {
		t.Fatalf("validateHookBindingSpec() error = %v", err)
	}
	record, err := h.store.Put(testutil.Context(t), h.actor, resources.Draft[hookspkg.HookDecl]{
		ID:              id,
		Scope:           scope,
		ExpectedVersion: expectedVersion,
		Spec:            spec,
	})
	if err != nil {
		t.Fatalf("store.Put(%q) error = %v", id, err)
	}
	return record
}

func integrationSession() *session.Session {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	return &session.Session{
		ID:          "sess-1",
		Name:        "demo",
		AgentName:   "codex",
		WorkspaceID: "ws-1",
		Workspace:   "/tmp/ws-1",
		Type:        session.SessionTypeUser,
		State:       session.StateActive,
		CreatedAt:   now.Add(-time.Minute),
		UpdatedAt:   now,
	}
}
