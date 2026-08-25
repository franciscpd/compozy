# Code-first required hook QA

- **Date:** 2026-08-24
- **Scenarios:** `ET-extension-manifest-v2-surfaces`, `ET-extension-code-first-authoring`
- **Status:** `blocked-verify`
- **Durable automated evidence:** SDK normalization, immutable manifest generation/reload, hook
  publication/reconciliation, and production `tools.RuntimeRegistry` admission all pass.

## Evidence demonstrated

Repository SDK verification passed with `TMPDIR` and `GOTMPDIR` unset for Go:

- Go SDK nested module: `CGO_ENABLED=1 go test -race ./... -count=1` — PASS.
- TypeScript SDK: `bun test --cwd sdk/typescript` — PASS, 70 tests.
- TypeScript SDK: `bun run --cwd sdk/typescript typecheck` — PASS.

The SDK suites prove exact legacy event-only serialization, deterministic complete-declaration
normalization and deduplication, initialize event-name deduplication, all current matcher fields,
pointer-valued `tool_read_only: false`, and caller-data independence in Go and TypeScript.

Builder and runtime verification also passed:

- `CGO_ENABLED=1 go test -race ./internal/extension -run 'Build|Manifest|Hook|Encode' -count=1`
  — PASS.
- `CGO_ENABLED=1 go test -race ./internal/hooks -count=1` — PASS.
- `CGO_ENABLED=1 go test -race -tags=integration ./internal/daemon -run
  '^(TestGeneratedRequiredExtensionHookContainsMatchedToolCall|TestRuntimeRegistryRequiredToolMatchersUseTrustedContext|TestHookBindingResourceReconcile.*)$'
  -count=1` — PASS.
- `CGO_ENABLED=1 go test -race ./internal/daemon -run '^TestDaemonBootToolRegistry$' -count=1`
  — PASS.

This automated path builds a public Go SDK fixture, reloads its immutable generated TOML, verifies
the exact hook name/event/mode/required/agent matcher and fixed subprocess command/args/env, starts
the installed manager, publishes through `extensionDeclarationProvider` and
`hookBindingSourceSyncer.Sync`, reconciles into the live catalog, and invokes a counting protected
tool through production `tools.RuntimeRegistry`. The matched `batuta-publisher` call receives the
structured required-hook denial with zero handler calls; the otherwise identical
`batuta-reviewer` call succeeds and produces exactly one handler call. The fixture fails
deliberately only after decoding a typed raw hook payload and exits with its dedicated status 23.

`make codegen` and `make codegen-check` passed without changing generated SDK contracts. Windows
cross-builds passed for `internal/extension/...`, `internal/hooks/...`, and the separate `sdk/go`
module.

## Why the scenarios remain blocked-verify

The repository integration is durable automated evidence, not a released external-provider persona
walk. This environment did not prove all of the following through public surfaces using published
Go and TypeScript SDK packages:

1. Scaffold or install a clean external fixture from each released SDK.
2. Build and validate it through `compozy extension build|validate` and the native equivalents.
3. Inspect the generated manifest and `compozy hooks list` projection.
4. Invoke one matched failing and one non-matching successful tool call against a live provider.
5. Confirm instance/process cleanup through public status and logs.

No live provider proof is inferred from the integration harness, and no scenario is marked pass.

## Remaining live walk

Use an isolated `COMPOZY_HOME`, workspace, ports, socket, and provider credentials. Author both exact
required synchronous `tool.pre_call` declarations plus an event-only compatibility declaration.
Inspect the generated TOML for lossless policy and fixed executor state; run the invalid
mode/required/matcher/identity matrix; enable the instance; inspect its hook catalog; invoke matched
and non-matching calls; then stop the daemon/provider and record a survivor-free teardown artifact.

## Isolation and teardown

No QA lab, live daemon, browser, or external provider was started for this documentation task. The
daemon integration owns temporary stores, stops the extension manager, checks that no process remains
running or interrupting, and runs `goleak.VerifyNone`. The one cross-build scratch directory created
for this run, `/home/francisross/tmp-builds/compozy-hooks.JruhnR`, was path-validated and removed; a
final existence check passed.

## Final branch-owned review fixes

- The extension provider harness now configures the authoritative trusted-workspace-root resolver,
  verifies the canonical `workspace-identity` lookup, and proves that caller decoy root
  `/caller/spoof` cannot replace `/trusted/workspace`. The focused case passes normally and under the
  race detector; the full `internal/extension` suite now reaches only the pre-existing lifecycle-token
  failure below.
- The branch-owned formatting drift in
  `packages/site/content/docs/configuration/config-toml.mdx` was corrected with the repository
  formatter without changing the runtime-rule wording. The aggregate site Turbo lint, typecheck, and
  test run completed all 8 tasks successfully with 322 tests passing; direct content generation also
  passed.

## Unrelated repository baselines

- Full `go test ./internal/extension -count=1` still fails the pre-existing lifecycle-token case
  `TestRegistryConditionalDisableFencesOlderLifecycle`.
- `make gate` ran once and stopped at the known source-size baseline:
  `web/src/systems/marketplace/routes/marketplace-detail.stories.tsx` (508 lines) and
  `web/src/systems/session/index.ts` (504 lines).

`make gate-full` was intentionally not run; the final whole-branch review owns it after both
prerequisite plans settle.
