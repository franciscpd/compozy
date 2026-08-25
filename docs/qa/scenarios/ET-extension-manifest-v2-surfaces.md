---
id: ET-extension-manifest-v2-surfaces
area: ET
title: Generate and expose one valid manifest v2 contract
persona: Ada
journey: J-extension-policy-admin
expected: Code-first registrations generate a byte-stable manifest v2 whose closed `capabilities.provides` and `permissions.requires`, extension hook source and exact name/profile/event/mode/required/matcher/fixed subprocess policy, command groups, tool command metadata, trusted_workspace, and invocation_id survive every owning projection; unknown or lossy hook values and malformed command metadata fail build and load before mutation, and a matched required synchronous tool.pre_call failure is denied by RuntimeRegistry before the protected handler while a non-match succeeds.
entry_points: `compozy extension build`; `compozy extension validate`; generated `extension.toml` `capabilities.provides`/`permissions.requires`/`resources.hooks`/`resources.command_groups`/`resources.tools.command`; `compozy hooks list`; `compozy extension commands`; `GET /api/extensions/commands` (HTTP+UDS); `compozy__extensions_build|validate`; `tools.RuntimeRegistry`; Go `extensiontest`; TypeScript `@compozy/extension-sdk/testing`; https://compozy.com/docs/extensions/develop; https://compozy.com/docs/extensions/manifest; https://compozy.com/docs/extensions/permissions; https://compozy.com/docs/extensions/commands
qa_status: blocked-verify
bug_ids: BUG-20260729-public-extension-sdks-unpublished;BUG-20260807-extension-template-help
fix_status: partial
retest_status: blocked-verify
fix_commits: a88ebda02;819a6ac3e;719236762;70e2b0550;563722ed9;efb59afb6;6b2ee6095;5058a926b;fe7c13bc9
evidence: docs/qa/reports/2026-08-24-code-first-required-hooks.md; internal/extension/build_test.go; internal/extension/manifest_test.go; internal/daemon/hook_binding_resources_integration_test.go
last_report: docs/qa/reports/2026-08-24-code-first-required-hooks.md
overlaps: ET-extension-code-first-authoring; ET-discover-extension-command-tree; ET-044
---

QA impact 2026-07-29: derived from the ext-improvs hard-cut contract. This scenario owns the
cross-surface declaration invariant; behavior-specific execution remains in the dev, hook, and
command scenarios.

QA impact 2026-08-02: duplicate layout diagnostics now report both owning paths; re-walk build and
validation failure output across CLI and native tools.

QA impact 2026-08-06: the closed `capabilities.provides` set now includes the public
`connectivity.provider` surface. Flag only; Tasks 08–09 own the re-walk.

QA walk 2026-08-07: the public provider contract is discoverable and the templates scaffold, but
the external SDK cannot produce the new manifest surface yet. Existing non-provider manifest-v2
evidence remains valid; the connectivity-provider projection remains blocked.

QA impact 2026-08-24: reset the stale retest pass for scoped required hooks. Durable automated
coverage now builds and reloads the generated manifest, proves lossless hook policy plus fixed
executor state, publishes through the real binding pipeline, and exercises matched denial with zero
handler calls and non-matching success through `tools.RuntimeRegistry`. The scenario remains
`blocked-verify` because no isolated persona walk used a released external SDK and live provider to
repeat build, validate, hook-catalog inspection, matched failure, non-match success, and teardown
across the public CLI/native surfaces. Automated integration evidence is not relabeled as that live
provider proof.
