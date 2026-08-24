import {
  SDK_MIN_COMPOZY_VERSION,
  SDK_NAME,
  type ExtensionDescribeProcess,
} from "./extension-contract.js";
import { normalizeHostMethodList, normalizeStringList } from "./extension-runtime.js";
import type {
  AutonomyMatcher,
  CmdPaletteConfig,
  DescribePayload,
  DescribeHookEvent,
  DescribeProfile,
  DescribeResourcePath,
  ExtensionDefinition,
  ExtensionCommandGroupSpec,
  ExtensionToolRuntimeDescriptor,
  HookMatcher,
} from "./types.js";
import type { RegisteredTool } from "./extension-contract.js";

interface ExtensionDescribeInput {
  definition: ExtensionDefinition;
  tools: ExtensionToolRuntimeDescriptor[];
  commandGroups: ExtensionCommandGroupSpec[];
  watchSourceKinds: string[];
  sdkVersion: string;
}

const UTF8_ENCODER = new TextEncoder();

export function compareUTF8Strings(left: string, right: string): number {
  if (left === right) {
    return 0;
  }
  const leftBytes = UTF8_ENCODER.encode(left);
  const rightBytes = UTF8_ENCODER.encode(right);
  const sharedLength = Math.min(leftBytes.length, rightBytes.length);
  for (let index = 0; index < sharedLength; index += 1) {
    const compared = (leftBytes[index] ?? 0) - (rightBytes[index] ?? 0);
    if (compared !== 0) {
      return compared;
    }
  }
  return leftBytes.length - rightBytes.length;
}

export function defaultExtensionDescribeProcess(): ExtensionDescribeProcess {
  return {
    argv: process.argv,
    stdout: process.stdout,
    setExitCode: code => {
      process.exitCode = code;
    },
  };
}

export function runExtensionDescribeMode(
  describeProcess: ExtensionDescribeProcess,
  describe: () => DescribePayload
): boolean {
  if (describeProcess.argv.at(-1)?.trim() !== "__describe") {
    return false;
  }
  describeProcess.stdout.write(`${JSON.stringify(describe())}\n`);
  describeProcess.setExitCode(0);
  return true;
}

export function cloneExtensionToolDescriptors(
  tools: Iterable<RegisteredTool>
): ExtensionToolRuntimeDescriptor[] {
  return [...tools].map(tool => ({
    ...tool.descriptor,
    ...(tool.descriptor.input_schema === undefined
      ? {}
      : { input_schema: structuredClone(tool.descriptor.input_schema) }),
    ...(tool.descriptor.output_schema === undefined
      ? {}
      : { output_schema: structuredClone(tool.descriptor.output_schema) }),
    capabilities: [...(tool.descriptor.capabilities ?? [])],
    ...(tool.descriptor.command === undefined
      ? {}
      : { command: structuredClone(tool.descriptor.command) }),
  }));
}

export function cmdPaletteViewIDs(config: CmdPaletteConfig | undefined): string[] {
  return (config?.views ?? []).map(view => view.id.trim()).filter(id => id.length > 0);
}

export function buildExtensionDescribePayload(input: ExtensionDescribeInput): DescribePayload {
  const command = input.definition.subprocess?.command.trim() ?? "";
  if (!command) {
    throw new Error("subprocess command is required");
  }
  const viewIDs = cmdPaletteViewIDs(input.definition.resources?.cmd_palette);

  return {
    name: input.definition.name.trim(),
    version: input.definition.version.trim(),
    ...(input.definition.description?.trim()
      ? { description: input.definition.description.trim() }
      : {}),
    provides: normalizeStringList(input.definition.capabilities?.provides),
    permissions: normalizeHostMethodList(input.definition.permissions?.requires),
    requires_env: normalizeStringList(input.definition.requires_env),
    profiles: normalizeDescribeProfiles(input.definition.profiles),
    resources: {
      skills: normalizeDescribeResourcePaths(input.definition.resources?.skills),
      loops: normalizeDescribeResourcePaths(input.definition.resources?.loops),
      agents: normalizeDescribeResourcePaths(input.definition.resources?.agents),
      automation: normalizeDescribeResourcePaths(input.definition.resources?.automation),
      layouts: normalizeDescribeResourcePaths(input.definition.resources?.layouts),
      ...(input.definition.resources?.cmd_palette === undefined
        ? {}
        : { cmd_palette: structuredClone(input.definition.resources.cmd_palette) }),
    },
    subprocess: {
      command,
      args: [...(input.definition.subprocess?.args ?? [])],
      env: { ...input.definition.subprocess?.env },
    },
    ...(input.definition.network_participation === undefined
      ? {}
      : {
          network_participation: {
            required: input.definition.network_participation.required,
            mode: input.definition.network_participation.mode.trim().toLowerCase(),
            channel_scopes: normalizeStringList(
              input.definition.network_participation.channel_scopes
            ),
          },
        }),
    tools: [...input.tools].sort((left, right) => left.handler.localeCompare(right.handler)),
    command_groups: input.commandGroups.map(group => ({ ...group })),
    hook_events: normalizeDescribeHookEvents(input.definition.supported_hook_events),
    watch_source_kinds: input.watchSourceKinds,
    ...(viewIDs.length === 0 ? {} : { cmd_palette_views: viewIDs }),
    sdk: {
      name: SDK_NAME,
      version: input.sdkVersion,
      protocol_version: "1",
      min_compozy_version: SDK_MIN_COMPOZY_VERSION,
    },
  };
}

function normalizeDescribeResourcePaths(
  resources: DescribeResourcePath[] | undefined
): DescribeResourcePath[] {
  const unique = new Map<string, DescribeResourcePath>();
  for (const resource of resources ?? []) {
    const normalized = { path: resource.path.trim(), profile: resource.profile?.trim() };
    unique.set(`${normalized.path}\u0000${normalized.profile ?? ""}`, {
      path: normalized.path,
      ...(normalized.profile ? { profile: normalized.profile } : {}),
    });
  }
  return [...unique.values()].sort(
    (left, right) =>
      left.path.localeCompare(right.path) || (left.profile ?? "").localeCompare(right.profile ?? "")
  );
}

function normalizeDescribeHookEvents(events: DescribeHookEvent[] | undefined): DescribeHookEvent[] {
  const unique = new Map<string, DescribeHookEvent>();
  for (const item of events ?? []) {
    const event = item.event.trim() as DescribeHookEvent["event"];
    const name = normalizeDetectableDescribeOptional(item.name);
    const profile = item.profile?.trim();
    const mode = normalizeDetectableDescribeOptional(item.mode) as DescribeHookEvent["mode"];
    const matcher = item.matcher === undefined ? undefined : cloneDescribeHookMatcher(item.matcher);
    const normalized: DescribeHookEvent = {
      ...(name !== undefined ? { name } : {}),
      event,
      ...(profile ? { profile } : {}),
      ...(mode !== undefined ? { mode } : {}),
      ...(matcher !== undefined && Object.keys(matcher).length > 0 ? { matcher } : {}),
      ...(item.required ? { required: true } : {}),
    };
    const key = describeHookEventKey(normalized);
    if (!unique.has(key)) {
      unique.set(key, normalized);
    }
  }
  return [...unique.values()].sort(
    (left, right) =>
      compareUTF8Strings(left.profile ?? "", right.profile ?? "") ||
      compareUTF8Strings(left.event, right.event) ||
      compareUTF8Strings(left.name ?? "", right.name ?? "") ||
      compareUTF8Strings(describeHookEventKey(left), describeHookEventKey(right))
  );
}

function normalizeDetectableDescribeOptional(value: string | undefined): string | undefined {
  // Exact empty follows Go's value-typed absent form; non-empty whitespace must remain rejectable.
  if (value === undefined || value === "") {
    return undefined;
  }
  const normalized = value.trim();
  return normalized === "" ? value : normalized;
}

type StringKeyOf<T> = {
  [Key in keyof T]-?: Exclude<T[Key], undefined> extends string ? Key : never;
}[keyof T];

type HookMatcherStringKey = StringKeyOf<HookMatcher>;
type AutonomyMatcherStringKey = StringKeyOf<AutonomyMatcher>;

function defineExhaustiveStringKeys<AllKeys extends string>() {
  return <Keys extends readonly AllKeys[]>(
    keys: Keys &
      (Exclude<AllKeys, Keys[number]> extends never
        ? unknown
        : { missing: Exclude<AllKeys, Keys[number]> })
  ): Keys => keys;
}

const HOOK_MATCHER_STRING_KEYS_BEFORE_TOOL_READ_ONLY = [
  "agent_name",
  "agent_type",
  "workspace_id",
  "worktree_id",
  "workspace_root",
  "session_type",
  "sandbox_id",
  "sandbox_backend",
  "sandbox_profile",
  "sync_direction",
  "input_class",
  "acp_event_type",
  "turn_id",
  "tool_id",
  "tool_name",
] as const satisfies readonly HookMatcherStringKey[];

const HOOK_MATCHER_STRING_KEYS_AFTER_TOOL_READ_ONLY = [
  "decision_class",
  "message_role",
  "message_delta_type",
  "channel",
  "surface",
  "kind",
  "direction",
  "work_state",
  "participation_mode",
  "participation_source",
  "compaction_reason",
  "compaction_strategy",
] as const satisfies readonly HookMatcherStringKey[];

const HOOK_MATCHER_STRING_KEYS = defineExhaustiveStringKeys<HookMatcherStringKey>()([
  ...HOOK_MATCHER_STRING_KEYS_BEFORE_TOOL_READ_ONLY,
  ...HOOK_MATCHER_STRING_KEYS_AFTER_TOOL_READ_ONLY,
] as const);

const AUTONOMY_MATCHER_STRING_KEYS = defineExhaustiveStringKeys<AutonomyMatcherStringKey>()([
  "task_id",
  "run_id",
  "loop_run_id",
  "loop_name",
  "node_id",
  "workflow_id",
  "participation_channel",
  "coordinator_session_id",
  "parent_session_id",
  "root_session_id",
  "child_session_id",
  "spawn_role",
  "release_reason",
] as const);

function cloneDescribeHookMatcher(matcher: HookMatcher): HookMatcher {
  const cloned: HookMatcher = {};
  const target = cloned as Record<HookMatcherStringKey, string | undefined>;
  copyTrimmedMatcherStrings(target, matcher, HOOK_MATCHER_STRING_KEYS);
  if (matcher.tool_read_only !== undefined) {
    cloned.tool_read_only = matcher.tool_read_only;
  }
  if (matcher.autonomy !== undefined) {
    cloned.autonomy = cloneDescribeAutonomyMatcher(matcher.autonomy);
  }
  return cloned;
}

function copyTrimmedMatcherStrings(
  target: Record<HookMatcherStringKey, string | undefined>,
  matcher: HookMatcher,
  keys: readonly HookMatcherStringKey[]
): void {
  for (const key of keys) {
    const value = matcher[key]?.trim();
    if (value) {
      target[key] = value;
    }
  }
}

function cloneDescribeAutonomyMatcher(matcher: AutonomyMatcher): AutonomyMatcher {
  const cloned: AutonomyMatcher = {};
  const target = cloned as Record<AutonomyMatcherStringKey, string | undefined>;
  for (const key of AUTONOMY_MATCHER_STRING_KEYS) {
    const value = matcher[key]?.trim();
    if (value) {
      target[key] = value;
    }
  }
  return cloned;
}

function describeHookEventKey(event: DescribeHookEvent): string {
  const parts: string[] = [];
  appendDescribeKeyPart(parts, event.profile ?? "");
  appendDescribeKeyPart(parts, event.event);
  appendDescribeKeyPart(parts, event.name ?? "");
  appendDescribeKeyPart(parts, event.mode ?? "");
  appendDescribeHookMatcherKey(parts, event.matcher);
  parts.push(event.required ? "1" : "0");
  return parts.join("");
}

function appendDescribeHookMatcherKey(parts: string[], matcher: HookMatcher | undefined): void {
  for (const key of HOOK_MATCHER_STRING_KEYS_BEFORE_TOOL_READ_ONLY) {
    appendDescribeKeyPart(parts, matcher?.[key] ?? "");
  }
  parts.push(matcher?.tool_read_only === undefined ? "n" : matcher.tool_read_only ? "t" : "f");
  for (const key of HOOK_MATCHER_STRING_KEYS_AFTER_TOOL_READ_ONLY) {
    appendDescribeKeyPart(parts, matcher?.[key] ?? "");
  }
  if (matcher?.autonomy === undefined) {
    parts.push("n");
    return;
  }
  parts.push("v");
  for (const key of AUTONOMY_MATCHER_STRING_KEYS) {
    appendDescribeKeyPart(parts, matcher.autonomy[key] ?? "");
  }
}

function appendDescribeKeyPart(parts: string[], value: string): void {
  parts.push(`${UTF8_ENCODER.encode(value).length}:${value}`);
}

function normalizeDescribeProfiles(profiles: DescribeProfile[] | undefined): DescribeProfile[] {
  return [...(profiles ?? [])]
    .map(profile => {
      const defaults = profile.defaults ?? {};
      return {
        name: profile.name.trim(),
        ...(profile.color?.trim() ? { color: profile.color.trim() } : {}),
        ...(profile.icon?.trim() ? { icon: profile.icon.trim() } : {}),
        ...(profile.emoji?.trim() ? { emoji: profile.emoji.trim() } : {}),
        defaults: {
          ...(defaults.agent?.trim() ? { agent: defaults.agent.trim() } : {}),
          ...(defaults.provider?.trim() ? { provider: defaults.provider.trim() } : {}),
          ...(defaults.sandbox?.trim() ? { sandbox: defaults.sandbox.trim() } : {}),
        },
        credentials: [...(profile.credentials ?? [])]
          .map(credential => ({
            provider: credential.provider.trim(),
            slot: credential.slot.trim(),
          }))
          .sort(
            (left, right) =>
              left.provider.localeCompare(right.provider) || left.slot.localeCompare(right.slot)
          ),
      };
    })
    .sort((left, right) => left.name.localeCompare(right.name));
}
