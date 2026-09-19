package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the actual child environment after inherited, command, and settings
// values have all merged. Dummy parent markers deliberately exist at each layer.
func TestWorkerChildEnvironment(t *testing.T) {
	for _, backend := range []Backend{ClaudeBackend{}, CodexBackend{}, GeminiBackend{}} {
		for _, mode := range []string{"new", "resume"} {
			t.Run(backend.Name()+"/"+mode, func(t *testing.T) {
				homeDir := t.TempDir()
				t.Setenv("HOME", homeDir)
				for _, key := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "CODEAGENT_PURPOSE"} {
					t.Setenv(key, "parent")
				}
				t.Setenv("CODEAGENT_CALLER_HARNESS", "claude")
				t.Setenv("CODEAGENT_ROOT_SESSION_ID", "root-session")
				t.Setenv("CODEAGENT_WORKER", "1")
				if err := os.Mkdir(filepath.Join(homeDir, ".claude"), 0700); err != nil {
					t.Fatal(err)
				}
				settings := `{"env":{"CLAUDECODE":"settings","CLAUDE_CODE_SESSION_ID":"settings","CLAUDE_SESSION_ID":"settings","CODEAGENT_PURPOSE":"imagegen","WORKER_ENV_TEST":"settings"}}`
				if err := os.WriteFile(filepath.Join(homeDir, ".claude", "settings.json"), []byte(settings), 0600); err != nil {
					t.Fatal(err)
				}
				script := `
test "${CODEAGENT_PURPOSE+x}" != x || exit 21
test "$CODEAGENT_CALLER_HARNESS" = claude || exit 22
test "$CODEAGENT_ROOT_SESSION_ID" = root-session || exit 23
test "$WORKER_ENV_TEST" = settings || exit 24
test "$CODEAGENT_WORKER" = 1 || exit 28
`
				if backend.Name() == "claude" {
					script += `
test "${CLAUDECODE+x}" != x || exit 25
test "${CLAUDE_CODE_SESSION_ID+x}" != x || exit 26
test "${CLAUDE_SESSION_ID+x}" != x || exit 27
test . -ef "$HOME" || exit 29
`
				} else {
					script += `
test "$CLAUDECODE" = settings || exit 25
test "$CLAUDE_CODE_SESSION_ID" = settings || exit 26
test "$CLAUDE_SESSION_ID" = settings || exit 27
`
				}
				script += `printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"environment-ok"}}'`
				previousRunner := newCommandRunner
				t.Cleanup(func() { newCommandRunner = previousRunner })
				newCommandRunner = func(ctx context.Context, name string, args ...string) commandRunner {
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
					cmd.Env = []string{"CLAUDECODE=command", "CLAUDE_CODE_SESSION_ID=command", "CLAUDE_SESSION_ID=command", "CODEAGENT_PURPOSE=imagegen", "WORKER_ENV_TEST=command"}
					return &realCmd{cmd: cmd}
				}
				result := runCodexTaskWithContext(context.Background(), TaskSpec{
					ID: "env-test", Mode: mode, SessionID: "worker-session", Task: "payload", WorkDir: homeDir,
				}, backend, nil, false, true, 5)
				if result.ExitCode != 0 {
					t.Fatalf("child rejected environment: exit=%d error=%s", result.ExitCode, result.Error)
				}
			})
		}
	}
}
