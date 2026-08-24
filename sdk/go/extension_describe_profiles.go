package compozysdk

import (
	"slices"
	"strconv"
	"strings"

	"github.com/compozy/compozy/sdk/go/contracts"
)

func normalizeDescribeResourcePaths(resources []contracts.DescribeResourcePath) []contracts.DescribeResourcePath {
	normalized := make([]contracts.DescribeResourcePath, 0, len(resources))
	for _, resource := range resources {
		normalized = append(normalized, contracts.DescribeResourcePath{
			Path: strings.TrimSpace(resource.Path), Profile: strings.TrimSpace(resource.Profile),
		})
	}
	slices.SortFunc(normalized, func(left, right contracts.DescribeResourcePath) int {
		if compared := strings.Compare(left.Path, right.Path); compared != 0 {
			return compared
		}
		return strings.Compare(left.Profile, right.Profile)
	})
	return slices.Compact(normalized)
}

func normalizeDescribeHookEvents(events []contracts.DescribeHookEvent) []contracts.DescribeHookEvent {
	normalized := make([]contracts.DescribeHookEvent, 0, len(events))
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		normalizedEvent := normalizeDescribeHookEvent(event)
		key := describeHookEventKey(normalizedEvent)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, normalizedEvent)
	}
	slices.SortFunc(normalized, func(left, right contracts.DescribeHookEvent) int {
		if compared := strings.Compare(left.Profile, right.Profile); compared != 0 {
			return compared
		}
		if compared := strings.Compare(string(left.Event), string(right.Event)); compared != 0 {
			return compared
		}
		if compared := strings.Compare(left.Name, right.Name); compared != 0 {
			return compared
		}
		return strings.Compare(describeHookEventKey(left), describeHookEventKey(right))
	})
	return normalized
}

func describedHookEventNames(events []contracts.DescribeHookEvent) []string {
	seen := make(map[string]struct{}, len(events))
	names := make([]string, 0, len(events))
	for _, event := range events {
		name := strings.TrimSpace(string(event.Event))
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func normalizeDescribeHookEvent(event contracts.DescribeHookEvent) contracts.DescribeHookEvent {
	return contracts.DescribeHookEvent{
		Name:     strings.TrimSpace(event.Name),
		Event:    contracts.HookEvent(strings.TrimSpace(string(event.Event))),
		Profile:  strings.TrimSpace(event.Profile),
		Mode:     contracts.HookMode(strings.TrimSpace(string(event.Mode))),
		Matcher:  cloneDescribeHookMatcher(event.Matcher),
		Required: event.Required,
	}
}

func cloneDescribeHookMatcher(matcher contracts.HookMatcher) contracts.HookMatcher {
	cloned := contracts.HookMatcher{
		AgentName:           strings.TrimSpace(matcher.AgentName),
		AgentType:           strings.TrimSpace(matcher.AgentType),
		WorkspaceID:         strings.TrimSpace(matcher.WorkspaceID),
		WorktreeID:          strings.TrimSpace(matcher.WorktreeID),
		WorkspaceRoot:       strings.TrimSpace(matcher.WorkspaceRoot),
		SessionType:         strings.TrimSpace(matcher.SessionType),
		SandboxID:           strings.TrimSpace(matcher.SandboxID),
		SandboxBackend:      strings.TrimSpace(matcher.SandboxBackend),
		SandboxProfile:      strings.TrimSpace(matcher.SandboxProfile),
		SyncDirection:       strings.TrimSpace(matcher.SyncDirection),
		InputClass:          strings.TrimSpace(matcher.InputClass),
		ACPEventType:        strings.TrimSpace(matcher.ACPEventType),
		TurnID:              strings.TrimSpace(matcher.TurnID),
		ToolID:              strings.TrimSpace(matcher.ToolID),
		ToolName:            strings.TrimSpace(matcher.ToolName),
		ToolReadOnly:        cloneOptionalBool(matcher.ToolReadOnly),
		DecisionClass:       strings.TrimSpace(matcher.DecisionClass),
		MessageRole:         strings.TrimSpace(matcher.MessageRole),
		MessageDeltaType:    strings.TrimSpace(matcher.MessageDeltaType),
		Channel:             strings.TrimSpace(matcher.Channel),
		Surface:             strings.TrimSpace(matcher.Surface),
		Kind:                strings.TrimSpace(matcher.Kind),
		Direction:           strings.TrimSpace(matcher.Direction),
		WorkState:           strings.TrimSpace(matcher.WorkState),
		ParticipationMode:   strings.TrimSpace(matcher.ParticipationMode),
		ParticipationSource: strings.TrimSpace(matcher.ParticipationSource),
		Reason:              strings.TrimSpace(matcher.Reason),
		Strategy:            strings.TrimSpace(matcher.Strategy),
	}
	if matcher.Autonomy != nil {
		cloned.Autonomy = &contracts.AutonomyMatcher{
			TaskID:               strings.TrimSpace(matcher.Autonomy.TaskID),
			RunID:                strings.TrimSpace(matcher.Autonomy.RunID),
			LoopRunID:            strings.TrimSpace(matcher.Autonomy.LoopRunID),
			LoopName:             strings.TrimSpace(matcher.Autonomy.LoopName),
			NodeID:               strings.TrimSpace(matcher.Autonomy.NodeID),
			WorkflowID:           strings.TrimSpace(matcher.Autonomy.WorkflowID),
			ParticipationChannel: strings.TrimSpace(matcher.Autonomy.ParticipationChannel),
			CoordinatorSessionID: strings.TrimSpace(matcher.Autonomy.CoordinatorSessionID),
			ParentSessionID:      strings.TrimSpace(matcher.Autonomy.ParentSessionID),
			RootSessionID:        strings.TrimSpace(matcher.Autonomy.RootSessionID),
			ChildSessionID:       strings.TrimSpace(matcher.Autonomy.ChildSessionID),
			SpawnRole:            strings.TrimSpace(matcher.Autonomy.SpawnRole),
			ReleaseReason:        strings.TrimSpace(matcher.Autonomy.ReleaseReason),
		}
	}
	return cloned
}

func describeHookEventKey(event contracts.DescribeHookEvent) string {
	var builder strings.Builder
	appendDescribeKeyPart(&builder, event.Profile)
	appendDescribeKeyPart(&builder, string(event.Event))
	appendDescribeKeyPart(&builder, event.Name)
	appendDescribeKeyPart(&builder, string(event.Mode))
	appendDescribeHookMatcherKey(&builder, event.Matcher)
	if event.Required {
		builder.WriteByte('1')
	} else {
		builder.WriteByte('0')
	}
	return builder.String()
}

func appendDescribeHookMatcherKey(builder *strings.Builder, matcher contracts.HookMatcher) {
	for _, value := range []string{
		matcher.AgentName, matcher.AgentType, matcher.WorkspaceID, matcher.WorktreeID,
		matcher.WorkspaceRoot, matcher.SessionType, matcher.SandboxID, matcher.SandboxBackend,
		matcher.SandboxProfile, matcher.SyncDirection, matcher.InputClass, matcher.ACPEventType,
		matcher.TurnID, matcher.ToolID, matcher.ToolName,
	} {
		appendDescribeKeyPart(builder, value)
	}
	switch {
	case matcher.ToolReadOnly == nil:
		builder.WriteByte('n')
	case *matcher.ToolReadOnly:
		builder.WriteByte('t')
	default:
		builder.WriteByte('f')
	}
	for _, value := range []string{
		matcher.DecisionClass, matcher.MessageRole, matcher.MessageDeltaType, matcher.Channel,
		matcher.Surface, matcher.Kind, matcher.Direction, matcher.WorkState, matcher.ParticipationMode,
		matcher.ParticipationSource, matcher.Reason, matcher.Strategy,
	} {
		appendDescribeKeyPart(builder, value)
	}
	if matcher.Autonomy == nil {
		builder.WriteByte('n')
		return
	}
	builder.WriteByte('v')
	for _, value := range []string{
		matcher.Autonomy.TaskID, matcher.Autonomy.RunID, matcher.Autonomy.LoopRunID,
		matcher.Autonomy.LoopName, matcher.Autonomy.NodeID, matcher.Autonomy.WorkflowID,
		matcher.Autonomy.ParticipationChannel, matcher.Autonomy.CoordinatorSessionID,
		matcher.Autonomy.ParentSessionID, matcher.Autonomy.RootSessionID,
		matcher.Autonomy.ChildSessionID, matcher.Autonomy.SpawnRole, matcher.Autonomy.ReleaseReason,
	} {
		appendDescribeKeyPart(builder, value)
	}
}

func appendDescribeKeyPart(builder *strings.Builder, value string) {
	builder.WriteString(strconv.Itoa(len(value)))
	builder.WriteByte(':')
	builder.WriteString(value)
}

func normalizeDescribeProfiles(profiles []contracts.DescribeProfile) []contracts.DescribeProfile {
	normalized := make([]contracts.DescribeProfile, 0, len(profiles))
	for _, profile := range profiles {
		credentials := make([]contracts.DescribeProfileCredential, 0, len(profile.Credentials))
		for _, credential := range profile.Credentials {
			credentials = append(credentials, contracts.DescribeProfileCredential{
				Provider: strings.TrimSpace(credential.Provider), Slot: strings.TrimSpace(credential.Slot),
			})
		}
		slices.SortFunc(credentials, func(left, right contracts.DescribeProfileCredential) int {
			if compared := strings.Compare(left.Provider, right.Provider); compared != 0 {
				return compared
			}
			return strings.Compare(left.Slot, right.Slot)
		})
		normalized = append(normalized, contracts.DescribeProfile{
			Name: strings.TrimSpace(profile.Name), Color: strings.TrimSpace(profile.Color),
			Icon: strings.TrimSpace(profile.Icon), Emoji: strings.TrimSpace(profile.Emoji),
			Defaults: contracts.DescribeProfileDefaults{
				Agent: strings.TrimSpace(profile.Defaults.Agent), Provider: strings.TrimSpace(profile.Defaults.Provider),
				Sandbox: strings.TrimSpace(profile.Defaults.Sandbox),
			},
			Credentials: slices.Compact(credentials),
		})
	}
	slices.SortFunc(normalized, func(left, right contracts.DescribeProfile) int {
		return strings.Compare(left.Name, right.Name)
	})
	return normalized
}
