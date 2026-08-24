package extensionpkg

import (
	"errors"
	"fmt"

	"slices"
	"strings"

	hookspkg "github.com/compozy/compozy/internal/hooks"
)

type hookConfigExecutorFields struct {
	command   string
	args      []string
	env       map[string]string
	secretEnv map[string]string
	kind      hookspkg.HookExecutorKind
}

func resolveHookConfigExecutorFields(cfg *HookConfig) (hookConfigExecutorFields, error) {
	if cfg == nil {
		return hookConfigExecutorFields{}, errors.New("hook config is required")
	}
	fields := hookConfigExecutorFields{
		command:   strings.TrimSpace(cfg.Command),
		args:      slices.Clone(cfg.Args),
		env:       cloneStringMap(cfg.Env),
		secretEnv: cloneStringMap(cfg.SecretEnv),
		kind:      hookspkg.HookExecutorKind(strings.TrimSpace(cfg.Executor.Kind)),
	}
	rootSpecified := fields.command != "" || len(fields.args) > 0 || len(fields.env) > 0 ||
		len(fields.secretEnv) > 0
	nestedSpecified := strings.TrimSpace(cfg.Executor.Command) != "" || len(cfg.Executor.Args) > 0 ||
		len(cfg.Executor.Env) > 0 || len(cfg.Executor.SecretEnv) > 0
	if rootSpecified && nestedSpecified {
		return hookConfigExecutorFields{}, errors.New(
			"hook executor fields must be declared either at the top level or under executor, not both",
		)
	}
	if nestedSpecified {
		fields.command = strings.TrimSpace(cfg.Executor.Command)
		fields.args = slices.Clone(cfg.Executor.Args)
		fields.env = cloneStringMap(cfg.Executor.Env)
		fields.secretEnv = cloneStringMap(cfg.Executor.SecretEnv)
	}
	return fields, nil
}

func hookConfigMatcher(cfg HookMatcherConfig) hookspkg.HookMatcher {
	matcher := hookspkg.HookMatcher{
		AgentName:        strings.TrimSpace(cfg.AgentName),
		AgentType:        strings.TrimSpace(cfg.AgentType),
		WorkspaceID:      strings.TrimSpace(cfg.WorkspaceID),
		WorkspaceRoot:    strings.TrimSpace(cfg.WorkspaceRoot),
		SessionType:      strings.TrimSpace(cfg.SessionType),
		InputClass:       strings.TrimSpace(cfg.InputClass),
		ACPEventType:     strings.TrimSpace(cfg.ACPEventType),
		TurnID:           strings.TrimSpace(cfg.TurnID),
		ToolID:           strings.TrimSpace(cfg.ToolID),
		ToolName:         strings.TrimSpace(cfg.ToolName),
		DecisionClass:    strings.TrimSpace(cfg.DecisionClass),
		MessageRole:      strings.TrimSpace(cfg.MessageRole),
		MessageDeltaType: strings.TrimSpace(cfg.MessageDeltaType),
	}
	matcher.NetworkMatcher = &hookspkg.NetworkMatcher{
		Channel:   strings.TrimSpace(cfg.Channel),
		Surface:   strings.TrimSpace(cfg.Surface),
		Kind:      strings.TrimSpace(cfg.Kind),
		Direction: strings.TrimSpace(cfg.Direction),
		WorkState: strings.TrimSpace(cfg.WorkState),
	}
	matcher.CompactionMatcher = &hookspkg.CompactionMatcher{
		Reason:   strings.TrimSpace(cfg.CompactionReason),
		Strategy: strings.TrimSpace(cfg.CompactionStrategy),
	}
	if cfg.ToolReadOnly != nil {
		value := *cfg.ToolReadOnly
		matcher.ToolReadOnly = &value
	}
	return matcher
}

func hookMatcherConfigFromHookMatcher(matcher hookspkg.HookMatcher) HookMatcherConfig {
	config := HookMatcherConfig{
		AgentName:        strings.TrimSpace(matcher.AgentName),
		AgentType:        strings.TrimSpace(matcher.AgentType),
		WorkspaceID:      strings.TrimSpace(matcher.WorkspaceID),
		WorkspaceRoot:    strings.TrimSpace(matcher.WorkspaceRoot),
		SessionType:      strings.TrimSpace(matcher.SessionType),
		InputClass:       strings.TrimSpace(matcher.InputClass),
		ACPEventType:     strings.TrimSpace(matcher.ACPEventType),
		TurnID:           strings.TrimSpace(matcher.TurnID),
		ToolID:           strings.TrimSpace(matcher.ToolID),
		ToolName:         strings.TrimSpace(matcher.ToolName),
		DecisionClass:    strings.TrimSpace(matcher.DecisionClass),
		MessageRole:      strings.TrimSpace(matcher.MessageRole),
		MessageDeltaType: strings.TrimSpace(matcher.MessageDeltaType),
	}
	if matcher.ToolReadOnly != nil {
		value := *matcher.ToolReadOnly
		config.ToolReadOnly = &value
	}
	if matcher.NetworkMatcher != nil {
		config.Channel = strings.TrimSpace(matcher.Channel)
		config.Surface = strings.TrimSpace(matcher.Surface)
		config.Kind = strings.TrimSpace(matcher.Kind)
		config.Direction = strings.TrimSpace(matcher.Direction)
		config.WorkState = strings.TrimSpace(matcher.WorkState)
	}
	if matcher.CompactionMatcher != nil {
		config.CompactionReason = strings.TrimSpace(matcher.Reason)
		config.CompactionStrategy = strings.TrimSpace(matcher.Strategy)
	}
	return config
}

func validateHookMatcherConfigRepresentable(matcher hookspkg.HookMatcher) error {
	fields := make([]string, 0, 8)
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "worktree_id", value: matcher.WorktreeID},
		{name: "sandbox_id", value: matcher.SandboxID},
		{name: "sandbox_backend", value: matcher.SandboxBackend},
		{name: "sandbox_profile", value: matcher.SandboxProfile},
		{name: "sync_direction", value: matcher.SyncDirection},
	} {
		if field.value != "" {
			fields = append(fields, field.name)
		}
	}
	if matcher.NetworkMatcher != nil {
		if matcher.ParticipationMode != "" {
			fields = append(fields, "participation_mode")
		}
		if matcher.ParticipationSource != "" {
			fields = append(fields, "participation_source")
		}
	}
	if matcher.Autonomy != nil {
		fields = append(fields, "autonomy")
	}
	if len(fields) == 0 {
		return nil
	}
	slices.Sort(fields)
	return fmt.Errorf(
		"hook matcher fields [%s] cannot be represented by an extension manifest",
		strings.Join(fields, ", "),
	)
}
