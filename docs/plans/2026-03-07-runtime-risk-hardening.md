# Runtime Risk Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix the five identified runtime correctness and guardrail risks without expanding the slimmed product surface.

**Architecture:** Implement the fixes in two batches. Phase 1 repairs correctness in provider fallback, resume selection, and session copying. Phase 2 upgrades runtime guardrails so tool restrictions are enforced by the executor and loop detection becomes conservative enough to avoid false positives.

**Tech Stack:** Go, existing `pkg/agent` / `pkg/tools` / `pkg/session` / `pkg/providers` packages, Go test.

---

### Task 1: Make cross-provider fallback switch provider instances

**Files:**
- Modify: `pkg/agent/loop.go`
- Modify: `pkg/agent/instance.go`
- Modify if needed: `pkg/providers/fallback.go`
- Test: `pkg/agent/loop_test.go`

**Step 1: Write the failing test**

Add a regression test that proves a fallback from one provider family to another uses the target provider instance, not just the target model name.

Suggested test shape:
- primary provider fails for its primary model
- fallback candidate points to another provider family
- assert the fallback call is executed by the fallback provider instance

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/agent -run 'TestAgentLoop_.*Fallback.*Provider' -count=1`

Expected: FAIL because the current implementation still reuses the original provider instance.

**Step 3: Write minimal implementation**

Introduce the smallest runtime selection layer needed so each fallback candidate can resolve to the correct provider instance before `Chat(...)` is called.

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/agent -run 'TestAgentLoop_.*Fallback.*Provider' -count=1`

Expected: PASS.

**Step 5: Commit**

Do not commit in this session unless explicitly requested.

### Task 2: Tighten resume_last_task semantics

**Files:**
- Modify: `pkg/agent/run_pipeline_impl.go`
- Modify if needed: `pkg/agent/instance.go`
- Test: `pkg/agent/run_pipeline_impl_test.go` or `pkg/agent/loop_test.go`

**Step 1: Write the failing tests**

Add tests for:
- `run.error` must not be selected as an unfinished run candidate
- unfinished runs in a non-default agent workspace can still be discovered

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/agent -run 'TestFindLastUnfinishedRun|TestResumeLastTask' -count=1`

Expected: FAIL because `run.error` is still resumable and/or only the default workspace is scanned.

**Step 3: Write minimal implementation**

Update the run scan logic so:
- `run.end` and `run.error` are both terminal
- candidate search covers known agent workspaces instead of only the default agent workspace

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/agent -run 'TestFindLastUnfinishedRun|TestResumeLastTask' -count=1`

Expected: PASS.

**Step 5: Commit**

Do not commit in this session unless explicitly requested.

### Task 3: Make session snapshots true deep copies

**Files:**
- Modify: `pkg/session/manager_mutations.go`
- Modify: `pkg/session/manager.go`
- Possibly modify: `pkg/providers/protocoltypes` related aliases if helper access is needed
- Test: `pkg/session/manager_test.go`

**Step 1: Write the failing tests**

Add tests that mutate nested fields on returned messages, such as:
- `Media`
- `ToolCalls`
- nested `Function` fields

and prove the stored session state does not change.

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/session -run 'Test.*DeepCopy' -count=1`

Expected: FAIL because current cloning only copies the top-level slice.

**Step 3: Write minimal implementation**

Add clone helpers for nested message structures and route all session snapshot APIs through them.

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/session -run 'Test.*DeepCopy' -count=1`

Expected: PASS.

**Step 5: Commit**

Do not commit in this session unless explicitly requested.

### Task 4: Enforce PlanMode, Estop, and ToolPolicy in the executor

**Files:**
- Modify: `pkg/tools/toolcall_executor.go`
- Modify if needed: `pkg/tools/registry.go`
- Test: `pkg/tools/toolcall_executor_test.go`

**Step 1: Write the failing tests**

Replace or update the slim-runtime tests so they assert denial instead of permissive execution when:
- `PlanMode` blocks a tool
- `Estop` blocks tool execution
- `ToolPolicy` denies a tool

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/tools -run 'TestExecuteToolCalls_(PlanMode|Estop|ToolPolicy)' -count=1`

Expected: FAIL because the executor currently still executes the tool.

**Step 3: Write minimal implementation**

Implement hard checks in `ExecuteToolCalls` before actual tool invocation, returning structured error results without executing the tool.

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/tools -run 'TestExecuteToolCalls_(PlanMode|Estop|ToolPolicy)' -count=1`

Expected: PASS.

**Step 5: Commit**

Do not commit in this session unless explicitly requested.

### Task 5: Make tool loop detection conservative

**Files:**
- Modify: `pkg/agent/loop.go`
- Test: `pkg/agent/loop_test.go`

**Step 1: Write the failing tests**

Add tests that show:
- repeated identical calls only trigger when they are recent and consecutive
- non-consecutive repetitions or alternating progress do not trigger

**Step 2: Run test to verify it fails**

Run: `go test ./pkg/agent -run 'TestDetectToolCallLoop' -count=1`

Expected: FAIL because current logic counts all prior matching calls globally.

**Step 3: Write minimal implementation**

Change loop detection to use a recent-window / consecutive-repeat strategy instead of total historical matches.

**Step 4: Run test to verify it passes**

Run: `go test ./pkg/agent -run 'TestDetectToolCallLoop' -count=1`

Expected: PASS.

**Step 5: Commit**

Do not commit in this session unless explicitly requested.

### Task 6: Run targeted regression verification

**Files:**
- Verify only: `pkg/agent`, `pkg/tools`, `pkg/session`

**Step 1: Run focused package tests**

Run:
- `go test ./pkg/agent -run 'TestAgentLoop_.*Fallback.*Provider|TestFindLastUnfinishedRun|TestResumeLastTask|TestDetectToolCallLoop' -count=1`
- `go test ./pkg/tools -run 'TestExecuteToolCalls_(PlanMode|Estop|ToolPolicy)' -count=1`
- `go test ./pkg/session -run 'Test.*DeepCopy' -count=1`

Expected: PASS.

**Step 2: Run broader validation**

Run:
- `go test ./pkg/agent ./pkg/tools ./pkg/session ./pkg/providers -count=1`

Expected: PASS, or report exact failing package if unrelated regressions appear.

**Step 3: Commit**

Do not commit in this session unless explicitly requested.
