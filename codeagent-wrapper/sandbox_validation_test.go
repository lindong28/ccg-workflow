package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSandboxValidationEntrypoints(t *testing.T) {
	for _, approval := range []string{"", "true"} {
		for _, value := range []string{"read-onyl", "invalid\nvalue"} {
			t.Run(approval+value, func(t *testing.T) {
				isolateE02(t)
				t.Setenv("CODEX_SANDBOX", value)
				t.Setenv("CODEX_REQUIRE_APPROVAL", approval)
				calls := 0
				runTaskFn = func(TaskSpec, bool, int) TaskResult { calls++; return TaskResult{} }
				os.Args = []string{"fixture-wrapper", "--backend", "codex", "task", t.TempDir()}
				if code := run(); code != 1 || calls != 0 {
					t.Errorf("main: exit=%d dispatch=%d", code, calls)
				}
				// A rejecting entry must return before the fixture's forbidden process runner.
				for _, custom := range []bool{false, true} {
					result := runCodexTaskWithContext(context.Background(), TaskSpec{Task: "task", WorkDir: t.TempDir()}, CodexBackend{}, []string{"e", "task"}, custom, true, 1)
					if result.ExitCode != 1 || !strings.Contains(result.Error, "invalid CODEX_SANDBOX") {
						t.Errorf("executor custom=%v: %+v", custom, result)
					}
				}
			})
		}
	}
}

func TestSandboxValidMainDispatch(t *testing.T) {
	for _, value := range []string{"", "read-only", "workspace-write", "  Read-Only  ", "  Workspace-Write  "} {
		for _, approval := range []string{"", "true"} {
			t.Run(value+approval, func(t *testing.T) {
				isolateE02(t)
				t.Setenv("CODEX_SANDBOX", value)
				t.Setenv("CODEX_REQUIRE_APPROVAL", approval)
				calls := 0
				runTaskFn = func(TaskSpec, bool, int) TaskResult { calls++; return TaskResult{} }
				os.Args = []string{"fixture-wrapper", "--backend", "codex", "task", t.TempDir()}
				if code := run(); code != 0 || calls != 1 {
					t.Fatalf("exit=%d dispatch=%d", code, calls)
				}
			})
		}
	}
}

func TestSandboxValidExecutorDispatch(t *testing.T) {
	for _, value := range []string{"", "  Read-Only  ", "  Workspace-Write  "} {
		t.Run(value, func(t *testing.T) {
			isolateE02(t)
			t.Setenv("CODEX_SANDBOX", value)
			calls := 0
			newCommandRunner = func(context.Context, string, ...string) commandRunner {
				calls++
				return &execFakeRunner{stdout: newReasonReadCloser(`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`), process: &execFakeProcess{pid: 4321}}
			}
			result := runCodexTaskWithContext(context.Background(), TaskSpec{Task: "task", WorkDir: t.TempDir()}, CodexBackend{}, nil, false, true, 1)
			if result.ExitCode != 0 || calls != 1 {
				t.Fatalf("exit=%d dispatch=%d error=%s", result.ExitCode, calls, result.Error)
			}
		})
	}
}
