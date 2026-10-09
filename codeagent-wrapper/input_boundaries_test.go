package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type forbiddenInput struct{ reads int }

func (r *forbiddenInput) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("fixture: unexpected stdin read")
}

func isolateE02(t *testing.T) {
	t.Helper()
	oldArgs, oldLite := os.Args, liteMode
	oldCleanup, oldCommand, oldRunner := cleanupLogsFn, commandContext, newCommandRunner
	oldTask, oldBuilder, oldBackend := runTaskFn, buildCodexArgsFn, selectBackendFn
	oldCodex, oldTerminal, oldReader := codexCommand, isTerminalFn, stdinReader
	t.Cleanup(func() {
		os.Args, liteMode = oldArgs, oldLite
		cleanupLogsFn, commandContext, newCommandRunner = oldCleanup, oldCommand, oldRunner
		runTaskFn, buildCodexArgsFn, selectBackendFn = oldTask, oldBuilder, oldBackend
		codexCommand, isTerminalFn, stdinReader = oldCodex, oldTerminal, oldReader
	})
	for _, k := range []string{"GEMINI_MODEL", "CODEX_MODEL", "CODEX_REASONING_EFFORT", "CODEX_SANDBOX", "CODEX_REQUIRE_APPROVAL"} {
		t.Setenv(k, "")
	}
	t.Setenv("CODEAGENT_STATE_DIR", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	liteMode = true
	cleanupLogsFn = func() (CleanupStats, error) { return CleanupStats{}, nil }
	commandContext = func(context.Context, string, ...string) *exec.Cmd { panic("fixture forbids commandContext") }
	newCommandRunner = func(context.Context, string, ...string) commandRunner { panic("fixture forbids any process runner") }
	runTaskFn = func(TaskSpec, bool, int) TaskResult { panic("fixture requires explicit task stub") }
	buildCodexArgsFn = defaultBuildArgsFn
	selectBackendFn = selectBackend
	codexCommand = defaultCodexCommand
	isTerminalFn = func() bool { return false }
	stdinReader = strings.NewReader("")
	t.Cleanup(func() { _ = closeLogger(); setLogger(nil) })
}

func TestE02UnknownFlags(t *testing.T) {
	isolateE02(t)
	for _, a := range [][]string{{"--sandbox", "read-only", "-C", ".", "-"}, {"resume", "sid", "task", ".", "--once"}, {"-C", ".", "task"}} {
		os.Args = append([]string{"fixture-wrapper"}, a...)
		if _, err := parseArgs(); err == nil {
			t.Errorf("unknown flag accepted: %q", a)
		}
	}
	for _, a := range [][]string{{"--", "--sandbox", "."}, {"resume", "sid", "--", "--once", "."}, {"task contains --once", "."}} {
		os.Args = append([]string{"fixture-wrapper"}, a...)
		if _, err := parseArgs(); err != nil {
			t.Errorf("literal task rejected: %q: %v", a, err)
		}
	}
}

func TestE02ArgTaskDoesNotReadStdin(t *testing.T) {
	isolateE02(t)
	for _, mode := range []string{"new", "resume"} {
		r := &forbiddenInput{}
		stdinReader = r
		os.Args = []string{"fixture-wrapper", "--lite", "literal task", t.TempDir()}
		if mode == "resume" {
			os.Args = []string{"fixture-wrapper", "--lite", "resume", "sid", "literal task", t.TempDir()}
		}
		calls := 0
		runTaskFn = func(ts TaskSpec, _ bool, _ int) TaskResult {
			calls++
			if ts.Task != "literal task" {
				t.Errorf("wrong task")
			}
			return TaskResult{Message: "fixture complete"}
		}
		if code := run(); code != 0 || r.reads != 0 || calls != 1 {
			t.Errorf("mode=%s exit=%d reads=%d calls=%d", mode, code, r.reads, calls)
		}
	}
	stdinReader = strings.NewReader("explicit task")
	os.Args = []string{"fixture-wrapper", "--lite", "-", t.TempDir()}
	runTaskFn = func(ts TaskSpec, _ bool, _ int) TaskResult {
		if ts.Task != "explicit task" {
			t.Errorf("stdin task lost")
		}
		return TaskResult{Message: "fixture complete"}
	}
	if run() != 0 {
		t.Error("explicit stdin failed")
	}
}

func TestE02WorkdirBeforeDispatch(t *testing.T) {
	isolateE02(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"codex", "claude", "gemini"} {
		for _, wd := range []string{filepath.Join(dir, "missing"), file, dir, "."} {
			calls := 0
			runTaskFn = func(TaskSpec, bool, int) TaskResult { calls++; return TaskResult{Message: "fixture complete"} }
			os.Args = []string{"fixture-wrapper", "--lite", "--backend", backend, "task", wd}
			stdinReader = strings.NewReader("")
			code := run()
			valid := wd == dir || wd == "."
			if valid && (code != 0 || calls != 1) {
				t.Errorf("valid cwd rejected %s", backend)
			}
			if !valid && (code == 0 || calls != 0) {
				t.Errorf("invalid cwd dispatched %s %q exit=%d calls=%d", backend, wd, code, calls)
			}
		}
	}
}

func TestE02GeminiExecutionArgs(t *testing.T) {
	isolateE02(t)
	for _, mode := range []string{"new", "resume"} {
		var ts TaskSpec
		os.Args = []string{"fixture-wrapper", "--lite", "--backend", "gemini", "--gemini-model", "fixture-model", "task", t.TempDir()}
		if mode == "resume" {
			os.Args = append(os.Args[:6], append([]string{"resume", "fixture-sid"}, os.Args[6:]...)...)
		}
		runTaskFn = func(task TaskSpec, _ bool, _ int) TaskResult {
			ts = task
			return TaskResult{Message: "fixture complete"}
		}
		if run() != 0 {
			t.Fatal("main dispatch failed")
		}
		var actual []string
		buildCodexArgsFn = func(cfg *Config, target string) []string {
			actual = buildGeminiArgs(cfg, target)
			panic("captured-argv")
		}
		func() {
			defer func() {
				if r := recover(); r != "captured-argv" {
					t.Errorf("unexpected execution path %v", r)
				}
			}()
			runCodexTaskWithContext(context.Background(), ts, nil, nil, false, false, 1)
		}()
		if len(actual) < 2 || !reflect.DeepEqual(actual[:2], []string{"-m", "fixture-model"}) {
			t.Errorf("mode=%s executed model absent: %v", mode, actual)
		}
	}
}

func TestE02WarningsNotErrors(t *testing.T) {
	isolateE02(t)
	l, err := NewLoggerWithSuffix("e02")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Warn("startup cleanup warning")
	l.Error("primary failure")
	l.Flush()
	got := l.ExtractRecentErrors(10)
	if !reflect.DeepEqual(got, []string{"primary failure"}) {
		t.Errorf("error view includes warnings: %v", got)
	}
	data, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "startup cleanup warning") {
		t.Error("warning lost from diagnostic log")
	}
}

func TestE02SymlinkTempContainment(t *testing.T) {
	isolateE02(t)
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	link := filepath.Join(root, "alias")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(realDir, "fixture.log")
	os.WriteFile(inside, []byte("fixture"), 0600)
	outside := filepath.Join(root, "outside.log")
	os.WriteFile(outside, []byte("fixture"), 0600)
	leaf := filepath.Join(realDir, "leaf.log")
	os.Symlink(outside, leaf)
	for _, c := range []struct {
		path, dir string
		unsafe    bool
	}{{inside, realDir, false}, {filepath.Join(link, "fixture.log"), link, false}, {outside, link, true}, {leaf, link, true}} {
		got, why := isUnsafeFile(c.path, c.dir)
		if got != c.unsafe {
			t.Errorf("path=%q dir=%q unsafe=%v reason=%s", c.path, c.dir, got, why)
		}
	}
}

var _ io.Reader = (*forbiddenInput)(nil)
