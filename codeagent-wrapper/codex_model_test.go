package main

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestRunCodexTask_CarriesCodexOverridesToArgsBuilder guards the layer that actually
// broke, and that every buildCodexArgs test above structurally cannot see.
//
// main() builds a Config, prints its argv on the `Command:` line, and then throws that
// Config away: execution goes through runCodexTaskWithContext, which rebuilds a fresh
// Config from the TaskSpec. A field carried only on the first one reaches the banner and
// nowhere else. Measured, not hypothesised — an end-to-end run whose banner read
// `-m gpt-5.6-luna` produced a rollout whose turn_context read model=gpt-5.6-sol.
//
// So this asserts on the Config the args builder is *handed at run time*, not on one
// assembled by the test. Note the effort value here is deliberately not "high": high is
// the config.toml default, so it takes the same value whether or not the override
// survived, and would have passed against the broken code.
func TestRunCodexTask_CarriesCodexOverridesToArgsBuilder(t *testing.T) {
	defer resetTestHooks()

	var seen *Config
	buildCodexArgsFn = func(cfg *Config, targetArg string) []string {
		seen = cfg
		return []string{}
	}
	commandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "noop")
	}

	runCodexTask(TaskSpec{
		Task:        "noop",
		WorkDir:     "/tmp",
		CodexModel:  "gpt-5.6-luna",
		CodexEffort: "xhigh",
	}, true, 5)

	if seen == nil {
		t.Fatal("args builder was never called; the test cannot conclude anything")
	}
	if seen.CodexModel != "gpt-5.6-luna" {
		t.Fatalf("CodexModel lost on the execution path: got %q", seen.CodexModel)
	}
	if seen.CodexEffort != "xhigh" {
		t.Fatalf("CodexEffort lost on the execution path: got %q", seen.CodexEffort)
	}
}

// codexBase is the argv tail buildCodexArgs emits for a new-mode task once the
// sandbox/git-check prefix is settled. Kept as one value so the "nothing requested"
// case below can assert byte-identity rather than merely "contains no -m".
var codexBase = []string{"--skip-git-repo-check", "-C", "/tmp", "--json", "task"}

func withCleanCodexEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CODEX_SANDBOX", "")
	t.Setenv("CODEX_MODEL", "")
	t.Setenv("CODEX_REASONING_EFFORT", "")
}

// TestBuildCodexArgs_ModelAndEffort pins the emitted argv. The first subtest is the
// one that matters most: with neither value requested the command must be byte-for-byte
// what it was before this feature existed, because every existing caller is on that path.
func TestBuildCodexArgs_ModelAndEffort(t *testing.T) {
	t.Run("neither requested leaves argv untouched", func(t *testing.T) {
		withCleanCodexEnv(t)
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		want := append([]string{"e", "--dangerously-bypass-approvals-and-sandbox"}, codexBase...)
		if !slices.Equal(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})

	t.Run("model only", func(t *testing.T) {
		withCleanCodexEnv(t)
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp", CodexModel: "gpt-5.6-luna"}, "task")
		if i := slices.Index(got, "-m"); i < 0 || got[i+1] != "gpt-5.6-luna" {
			t.Fatalf("expected -m gpt-5.6-luna in %v", got)
		}
		// Effort must stay inherited from config.toml, not reset to some built-in default.
		for _, a := range got {
			if strings.HasPrefix(a, "model_reasoning_effort=") {
				t.Fatalf("model-only must not pin effort, got %v", got)
			}
		}
	})

	t.Run("effort only", func(t *testing.T) {
		withCleanCodexEnv(t)
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp", CodexEffort: "high"}, "task")
		if !slices.Contains(got, `model_reasoning_effort="high"`) {
			t.Fatalf("expected effort override in %v", got)
		}
		if slices.Contains(got, "-m") {
			t.Fatalf("effort-only must not pin a model, got %v", got)
		}
	})

	// Resume is asserted separately because the resume branch returns early; an
	// implementation that appended after that return would pass every case above and
	// silently drop both values on exactly the calls that reuse an existing session.
	t.Run("resume carries both", func(t *testing.T) {
		withCleanCodexEnv(t)
		got := buildCodexArgs(&Config{Mode: "resume", SessionID: "sid", WorkDir: "/tmp", CodexModel: "gpt-5.6-sol", CodexEffort: "xhigh"}, "task")
		if i := slices.Index(got, "-m"); i < 0 || got[i+1] != "gpt-5.6-sol" {
			t.Fatalf("expected -m on resume, got %v", got)
		}
		if !slices.Contains(got, `model_reasoning_effort="xhigh"`) {
			t.Fatalf("expected effort on resume, got %v", got)
		}
		if !slices.Contains(got, "resume") || !slices.Contains(got, "sid") {
			t.Fatalf("resume argv malformed: %v", got)
		}
	})

	// Values reach codex verbatim: it rejects a bad model or effort with a loud 400, and
	// its own error enumerates more valid efforts than models_cache.json exposes, so a
	// local allowlist would refuse valid input.
	t.Run("values pass through verbatim", func(t *testing.T) {
		withCleanCodexEnv(t)
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp", CodexModel: "some-future-model", CodexEffort: "minimal"}, "task")
		if i := slices.Index(got, "-m"); i < 0 || got[i+1] != "some-future-model" {
			t.Fatalf("model must not be validated locally: %v", got)
		}
		if !slices.Contains(got, `model_reasoning_effort="minimal"`) {
			t.Fatalf("effort must not be validated locally: %v", got)
		}
	})
}

// TestParseParallelConfig_RefusesCodexOverrides covers a failure surface this feature
// created rather than inherited: before the per-call flags existed, nobody had a reason
// to write `codex_model:` in a task block, and parseParallelConfig drops unknown meta
// keys silently. Making the flags credible makes the stdin spelling a natural thing to
// try — and silently dropping it runs the whole batch on the config default while the
// block says otherwise. Refusal has to be loud on all three paths (flag, env, stdin);
// this is the third.
func TestParseParallelConfig_RefusesCodexOverrides(t *testing.T) {
	for _, key := range []string{"codex_model", "codex_effort"} {
		block := "id: t1\nbackend: codex\n" + key + ": gpt-5.6-luna\n---CONTENT---\nnoop\n"
		_, err := parseParallelConfig([]byte(block))
		if err == nil {
			t.Fatalf("%s in a parallel block must be refused, not ignored", key)
		}
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("error must name the offending key, got: %v", err)
		}
	}

	// The refusal must be specific to these two keys: rejecting every unknown meta key
	// would break blocks that carry fields this parser never claimed to read.
	block := "id: t1\nbackend: codex\nsome_future_key: whatever\n---CONTENT---\nnoop\n"
	cfg, err := parseParallelConfig([]byte(block))
	if err != nil {
		t.Fatalf("unrelated unknown keys must still be tolerated, got: %v", err)
	}
	if len(cfg.Tasks) != 1 || cfg.Tasks[0].ID != "t1" {
		t.Fatalf("parsed wrong: %+v", cfg.Tasks)
	}
}

func TestParseArgs_CodexModelFlags(t *testing.T) {
	t.Run("space and equals forms", func(t *testing.T) {
		withCleanCodexEnv(t)
		for _, args := range [][]string{
			{"codeagent-wrapper", "--codex-model", "gpt-5.6-luna", "--codex-effort", "high", "task"},
			{"codeagent-wrapper", "--codex-model=gpt-5.6-luna", "--codex-effort=high", "task"},
		} {
			os.Args = args
			cfg, err := parseArgs()
			if err != nil {
				t.Fatalf("%v: unexpected error %v", args, err)
			}
			if cfg.CodexModel != "gpt-5.6-luna" || cfg.CodexEffort != "high" {
				t.Fatalf("%v: got model=%q effort=%q", args, cfg.CodexModel, cfg.CodexEffort)
			}
			if cfg.Task != "task" {
				t.Fatalf("%v: task was consumed, got %q", args, cfg.Task)
			}
		}
	})

	t.Run("env supplies default and flag wins", func(t *testing.T) {
		withCleanCodexEnv(t)
		t.Setenv("CODEX_MODEL", "gpt-5.6-sol")
		t.Setenv("CODEX_REASONING_EFFORT", "low")

		os.Args = []string{"codeagent-wrapper", "task"}
		cfg, err := parseArgs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.CodexModel != "gpt-5.6-sol" || cfg.CodexEffort != "low" {
			t.Fatalf("env not honoured: model=%q effort=%q", cfg.CodexModel, cfg.CodexEffort)
		}

		os.Args = []string{"codeagent-wrapper", "--codex-model", "gpt-5.6-luna", "task"}
		cfg, err = parseArgs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.CodexModel != "gpt-5.6-luna" {
			t.Fatalf("flag must beat env, got %q", cfg.CodexModel)
		}
		if cfg.CodexEffort != "low" {
			t.Fatalf("unset flag must leave env value intact, got %q", cfg.CodexEffort)
		}
	})

	t.Run("empty value is refused", func(t *testing.T) {
		withCleanCodexEnv(t)
		for _, args := range [][]string{
			{"codeagent-wrapper", "--codex-model="},
			{"codeagent-wrapper", "--codex-effort", "   ", "task"},
			{"codeagent-wrapper", "task", "--codex-model"},
		} {
			os.Args = args
			if _, err := parseArgs(); err == nil {
				t.Fatalf("%v: expected an error", args)
			}
		}
	})
}

// TestParseArgs_CodexFlagsAfterTaskAreLoud is the reason the positional guard exists.
// Honouring the flag there is impossible without changing which token is the task, and
// ignoring it would run on the default model while the caller believes otherwise —
// indistinguishable from success at the call site.
func TestParseArgs_CodexFlagsAfterTaskAreLoud(t *testing.T) {
	withCleanCodexEnv(t)
	os.Args = []string{"codeagent-wrapper", "task", "/tmp", "--codex-model", "gpt-5.6-luna"}
	cfg, err := parseArgs()
	if err == nil {
		t.Fatalf("expected a hard error, got cfg with model=%q", cfg.CodexModel)
	}
	if !strings.Contains(err.Error(), "--codex-model") {
		t.Fatalf("error must name the offending flag, got %v", err)
	}

	// `resume` is itself a positional, so this ordering — which reads as "the flag is
	// before the task" and is what a user would naturally type — also lands in the
	// error. The message therefore has to talk about positionals, not about "the task":
	// an earlier wording told this caller to move the flag before the task, where it
	// already was, leaving them nothing to act on.
	os.Args = []string{"codeagent-wrapper", "resume", "sid-123", "--codex-model", "gpt-5.6-luna", "task"}
	_, err = parseArgs()
	if err == nil {
		t.Fatal("flag after `resume` must be refused: resume is a positional")
	}
	if !strings.Contains(err.Error(), "resume") {
		t.Fatalf("error must show the correct resume ordering, got: %v", err)
	}

	// And the ordering the message points at must actually work.
	os.Args = []string{"codeagent-wrapper", "--codex-model", "gpt-5.6-luna", "resume", "sid-123", "task"}
	cfg, err = parseArgs()
	if err != nil {
		t.Fatalf("the ordering the error message recommends must parse, got %v", err)
	}
	if cfg.CodexModel != "gpt-5.6-luna" || cfg.Mode != "resume" || cfg.SessionID != "sid-123" {
		t.Fatalf("recommended ordering parsed wrong: model=%q mode=%q sid=%q", cfg.CodexModel, cfg.Mode, cfg.SessionID)
	}
}

// TestParallelPreScan_RespectsDoubleDash guards the second parser. `--parallel` is
// recognised by a raw argv scan in main() that runs before parseArgs and never reaches
// it, so a terminator implemented only in parseArgs would be honoured by one parser and
// ignored by the other — `codeagent-wrapper -- --parallel` would switch modes instead of
// treating the token as text. Found by adversarial review, not by the author.
func TestParallelPreScan_RespectsDoubleDash(t *testing.T) {
	scan := findParallelIndex // the real one, not a copy of it
	if got := scan([]string{"--", "--parallel"}); got != -1 {
		t.Fatalf("`--parallel` after `--` must not select parallel mode, got index %d", got)
	}
	if got := scan([]string{"--parallel"}); got != 0 {
		t.Fatalf("`--parallel` before any terminator must still select parallel mode, got %d", got)
	}
	if got := scan([]string{"--backend", "codex", "--parallel"}); got != 2 {
		t.Fatalf("`--parallel` after other flags must still be found, got %d", got)
	}
}

// TestParallelBranch_ConsumesDoubleDash covers the third argv parser: the loop inside
// the parallel branch. findParallelIndex and parseArgs both stop at `--`; if this one
// did not, the terminator would itself be counted as a stray argument and the run would
// die on a message naming the wrong token, while help promises `--` ends flag parsing
// everywhere. Tokens after it stay extras on purpose — parallel takes its tasks from
// stdin and accepts no positionals — so the extras error is the right outcome; what must
// not happen is the terminator becoming one of them.
func TestParallelBranch_ConsumesDoubleDash(t *testing.T) {
	collect := func(args []string) []string {
		f, err := parseParallelFlags(args) // the real one, not a copy of it
		if err != nil {
			t.Fatalf("unexpected error for %v: %v", args, err)
		}
		return f.extras
	}

	got := collect([]string{"--parallel", "--", "--backend", "codex"})
	want := []string{"--backend", "codex"}
	if !slices.Equal(got, want) {
		t.Fatalf("`--` must be consumed, not reported as the offending argument: got %v want %v", got, want)
	}
	if slices.Contains(got, "--") {
		t.Fatal("the terminator itself must never appear among the extras")
	}
	if got := collect([]string{"--parallel", "--full-output"}); len(got) != 0 {
		t.Fatalf("recognised flags before any terminator must not become extras, got %v", got)
	}
}

// TestParseArgs_DoubleDashEndsFlagParsing covers the call form that motivated all of
// this: the prompt travels in argv and an unquoted one is word-split by the caller's
// shell, so text that merely mentions a flag arrives looking like one.
func TestParseArgs_DoubleDashEndsFlagParsing(t *testing.T) {
	t.Run("new flags after -- are task text, not flags", func(t *testing.T) {
		withCleanCodexEnv(t)
		os.Args = []string{"codeagent-wrapper", "--", "--codex-model", "gpt-5.6-luna"}
		cfg, err := parseArgs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.CodexModel != "" {
			t.Fatalf("token after -- must not be parsed as a flag, got %q", cfg.CodexModel)
		}
		if cfg.Task != "--codex-model" {
			t.Fatalf("first token after -- should be the task, got %q", cfg.Task)
		}
	})

	// -- covers every flag, not just the new pair. Protecting only --codex-* would leave
	// a prompt containing --backend just as exposed, which is the same defect one name over.
	t.Run("-- also shields pre-existing flags", func(t *testing.T) {
		withCleanCodexEnv(t)
		os.Args = []string{"codeagent-wrapper", "--", "--backend", "gemini"}
		cfg, err := parseArgs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Backend != defaultBackendName {
			t.Fatalf("--backend after -- must not select a backend, got %q", cfg.Backend)
		}
		if cfg.Task != "--backend" {
			t.Fatalf("expected task %q, got %q", "--backend", cfg.Task)
		}
	})

	// What `--` does NOT do. It protects a task that is already one argv element; it
	// cannot reassemble a prompt the shell split into several words — the second word
	// still lands on workdir. Asserting only "the first token became the task" would
	// pass against that broken behaviour, which is what the earlier version of this
	// test did. Pinned so the help text's stated limit stays true.
	t.Run("-- cannot rescue a word-split prompt", func(t *testing.T) {
		withCleanCodexEnv(t)
		os.Args = []string{"codeagent-wrapper", "--", "explain", "--codex-model", "gpt-5.6-luna", "/tmp"}
		cfg, err := parseArgs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Task != "explain" {
			t.Fatalf("task: got %q", cfg.Task)
		}
		if cfg.WorkDir != "--codex-model" {
			t.Fatalf("documented limit changed: workdir is now %q; update --help and the ADR together with this test", cfg.WorkDir)
		}
		if cfg.CodexModel != "" {
			t.Fatalf("token after -- must never become a flag value, got %q", cfg.CodexModel)
		}
	})

	t.Run("flags before -- still apply", func(t *testing.T) {
		withCleanCodexEnv(t)
		os.Args = []string{"codeagent-wrapper", "--codex-model", "gpt-5.6-luna", "--", "discuss --codex-model here"}
		cfg, err := parseArgs()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.CodexModel != "gpt-5.6-luna" {
			t.Fatalf("flag before -- must apply, got %q", cfg.CodexModel)
		}
		if cfg.Task != "discuss --codex-model here" {
			t.Fatalf("task mangled: %q", cfg.Task)
		}
	})
}
