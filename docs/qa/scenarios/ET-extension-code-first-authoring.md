---
id: ET-extension-code-first-authoring
area: ET
title: Build a code-first extension from the embedded CLI templates
persona: Ada
journey: J-extension-kit-lifecycle
expected: Running `compozy extension init hello -t tool-provider-go`, then authoring, building, and validating the Go and TypeScript SDK definitions with declared resources produces immutable generations matching each definition; a scoped required sync tool.pre_call preserves name/profile/event/mode/required/matcher and the fixed subprocess command/args/env, legacy event-only declarations retain compatible defaults, invalid or lossy declarations fail before generation publication, and live RuntimeRegistry admission blocks a matched required failure without affecting a non-match.
entry_points: `compozy extension init`; `compozy extension build`; `compozy extension validate`; generated `extension.toml` `resources.hooks`; `compozy hooks list`; Go `github.com/compozy/compozy/sdk/go`; TypeScript `@compozy/extension-sdk`; `tools.RuntimeRegistry`
qa_status: blocked-verify
bug_ids: BUG-20260802-scaffold-sdk-version;BUG-20260802-manifest-mcp-tool-handler;BUG-20260729-public-extension-sdks-unpublished;BUG-20260807-extension-template-help
fix_status: partial
retest_status: blocked-verify
fix_commits: 7866661;881a254;a88ebda02;819a6ac3e;719236762;70e2b0550;563722ed9;efb59afb6;6b2ee6095;5058a926b;fe7c13bc9
evidence: docs/qa/reports/2026-08-24-code-first-required-hooks.md; sdk/go/runtime_test.go; sdk/typescript/src/__tests__/extension.test.ts; internal/extension/build_test.go; internal/daemon/hook_binding_resources_integration_test.go
last_report: docs/qa/reports/2026-08-24-code-first-required-hooks.md
overlaps: ET-compozy-extension-contract-identity
---

Added by ext-improvs Task 03. Repeat the first-success path for all seven embedded templates, confirm `build` never mutates an existing generation, and inspect structured output for the stamped SDK minimum version, positioned issues, and derived consent areas.

QA impact 2026-08-02: code-first `resources` now declares and copies agents, automation, and layouts
into the generated manifest. Reset to verify the complete kit rather than the earlier tool-only build.

QA impact 2026-08-06: Go and TypeScript connectivity-provider templates joined the embedded
scaffold catalog. Flag only; Tasks 08–09 own the re-walk.

QA walk 2026-08-07: both connectivity templates scaffolded and became discoverable in CLI help.
The clean Go build then proved the published SDK lacks their declared API; TypeScript dependency
resolution was blocked by the machine's minimum-release-age policy. External build remains blocked.

QA impact 2026-08-24: reset the stale retest pass. The executable verification must preserve the
event-only form in both SDKs, inspect the generated hook policy and fixed executor, exercise invalid
mode/required/matcher/identity cases, then invoke one matched failing and one non-matching successful
tool call through production admission. Repository SDK, builder, and daemon integration suites are
durable evidence for those contracts, but no released external SDK/live-provider persona walk was
performed in this environment. The scenario therefore remains `blocked-verify`; the exact remaining
walk and clean teardown requirements are recorded in the current report.
