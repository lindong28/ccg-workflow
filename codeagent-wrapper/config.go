package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds CLI configuration
type Config struct {
	Mode               string // "new" or "resume"
	Task               string
	SessionID          string
	WorkDir            string
	ExplicitStdin      bool
	Timeout            int
	Backend            string
	SkipPermissions    bool
	MaxParallelWorkers int
	GeminiModel        string // Gemini model name (empty = use default)
	CodexModel         string // Codex model slug (empty = inherit ~/.codex/config.toml)
	CodexEffort        string // Codex reasoning effort (empty = inherit ~/.codex/config.toml)
	Progress           bool   // Emit compact progress lines to stderr
}

// ParallelConfig defines the JSON schema for parallel execution
type ParallelConfig struct {
	Tasks         []TaskSpec `json:"tasks"`
	GlobalBackend string     `json:"backend,omitempty"`
}

// TaskSpec describes an individual task entry in the parallel config
type TaskSpec struct {
	ID              string          `json:"id"`
	Task            string          `json:"task"`
	WorkDir         string          `json:"workdir,omitempty"`
	Dependencies    []string        `json:"dependencies,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	Backend         string          `json:"backend,omitempty"`
	Progress        bool            `json:"-"`
	Mode            string          `json:"-"`
	UseStdin        bool            `json:"-"`
	SkipPermissions bool            `json:"-"`
	// Codex per-call overrides. TaskSpec — not Config — is what the execution path
	// actually carries: runCodexTaskWithContext rebuilds a fresh Config from this struct,
	// so a field that lives only on the Config built in main() reaches the printed
	// `Command:` line and nothing else. Tagged `json:"-"` because --parallel builds its
	// TaskSpecs from stdin and deliberately does not support these (main.go refuses the
	// env form there rather than honouring it silently).
	CodexModel  string          `json:"-"`
	CodexEffort string          `json:"-"`
	Context     context.Context `json:"-"`
}

// TaskResult captures the execution outcome of a task
type TaskResult struct {
	TaskID    string `json:"task_id"`
	ExitCode  int    `json:"exit_code"`
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
	Error     string `json:"error"`
	LogPath   string `json:"log_path"`
	// Structured report fields
	Coverage       string   `json:"coverage,omitempty"`        // extracted coverage percentage (e.g., "92%")
	CoverageNum    float64  `json:"coverage_num,omitempty"`    // numeric coverage for comparison
	CoverageTarget float64  `json:"coverage_target,omitempty"` // target coverage (default 90)
	FilesChanged   []string `json:"files_changed,omitempty"`   // list of changed files
	KeyOutput      string   `json:"key_output,omitempty"`      // brief summary of what was done
	TestsPassed    int      `json:"tests_passed,omitempty"`    // number of tests passed
	TestsFailed    int      `json:"tests_failed,omitempty"`    // number of tests failed
	sharedLog      bool
}

var backendRegistry = map[string]Backend{
	"codex":  CodexBackend{},
	"claude": ClaudeBackend{},
	"gemini": GeminiBackend{},
}

func selectBackend(name string) (Backend, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = defaultBackendName
	}
	if backend, ok := backendRegistry[key]; ok {
		return backend, nil
	}
	return nil, fmt.Errorf("unsupported backend %q", name)
}

func envFlagEnabled(key string) bool {
	val, ok := os.LookupEnv(key)
	if !ok {
		return false
	}
	val = strings.TrimSpace(strings.ToLower(val))
	switch val {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func parseBoolFlag(val string, defaultValue bool) bool {
	val = strings.TrimSpace(strings.ToLower(val))
	switch val {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func parseParallelConfig(data []byte) (*ParallelConfig, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("parallel config is empty")
	}

	tasks := strings.Split(string(trimmed), "---TASK---")
	var cfg ParallelConfig
	seen := make(map[string]struct{})

	taskIndex := 0
	for _, taskBlock := range tasks {
		taskBlock = strings.TrimSpace(taskBlock)
		if taskBlock == "" {
			continue
		}
		taskIndex++

		parts := strings.SplitN(taskBlock, "---CONTENT---", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("task block #%d missing ---CONTENT--- separator", taskIndex)
		}

		meta := strings.TrimSpace(parts[0])
		content := strings.TrimSpace(parts[1])

		task := TaskSpec{WorkDir: defaultWorkdir}
		for _, line := range strings.Split(meta, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			kv := strings.SplitN(line, ":", 2)
			if len(kv) != 2 {
				continue
			}
			key := strings.TrimSpace(kv[0])
			value := strings.TrimSpace(kv[1])

			switch key {
			// Refused, not ignored. Unknown meta keys are dropped silently here, which was
			// harmless while no one had a reason to write these two — the per-call model
			// flags are what make `codex_model:` a natural thing to try. Dropping it would
			// run the whole batch on the config default while the block says otherwise:
			// the same invisible-at-the-call-site failure the flag and env paths already
			// refuse. Named explicitly rather than by rejecting every unknown key, which
			// would break blocks carrying fields this parser never claimed to read.
			case "codex_model", "codex_effort":
				return nil, fmt.Errorf(
					"task block #%d (%q) sets %s, which --parallel does not support.\n"+
						"  Per-call model and effort are single-call only: run that task on its own with\n"+
						"  codeagent-wrapper --codex-model <slug> --codex-effort <level> - <workdir>\n"+
						"  Left in place it would be ignored and the batch would run on the model from\n"+
						"  ~/.codex/config.toml, with nothing in the output to show it.",
					taskIndex, task.ID, key)
			case "id":
				task.ID = value
			case "workdir":
				task.WorkDir = value
			case "session_id":
				task.SessionID = value
				task.Mode = "resume"
			case "backend":
				task.Backend = value
			case "dependencies":
				for _, dep := range strings.Split(value, ",") {
					dep = strings.TrimSpace(dep)
					if dep != "" {
						task.Dependencies = append(task.Dependencies, dep)
					}
				}
			}
		}

		if task.Mode == "" {
			task.Mode = "new"
		}

		if task.ID == "" {
			return nil, fmt.Errorf("task block #%d missing id field", taskIndex)
		}
		if content == "" {
			return nil, fmt.Errorf("task block #%d (%q) missing content", taskIndex, task.ID)
		}
		if task.Mode == "resume" && strings.TrimSpace(task.SessionID) == "" {
			return nil, fmt.Errorf("task block #%d (%q) has empty session_id", taskIndex, task.ID)
		}
		if _, exists := seen[task.ID]; exists {
			return nil, fmt.Errorf("task block #%d has duplicate id: %s", taskIndex, task.ID)
		}

		task.Task = content
		cfg.Tasks = append(cfg.Tasks, task)
		seen[task.ID] = struct{}{}
	}

	if len(cfg.Tasks) == 0 {
		return nil, fmt.Errorf("no tasks found")
	}

	return &cfg, nil
}

func parseArgs() (*Config, error) {
	args := os.Args[1:]
	if len(args) == 0 {
		return nil, fmt.Errorf("task required")
	}

	// Read environment variable (lowest precedence)
	geminiModel := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	codexModel := strings.TrimSpace(os.Getenv("CODEX_MODEL"))
	codexEffort := strings.TrimSpace(os.Getenv("CODEX_REASONING_EFFORT"))

	backendName := defaultBackendName
	skipPermissions := envFlagEnabled("CODEAGENT_SKIP_PERMISSIONS")
	progress := false

	// Two guards, both aimed at the same hazard: on the prompt-as-argument call form the
	// task text *is* argv, and an unquoted prompt gets word-split by the caller's shell —
	// so a prompt that merely discusses a flag arrives looking like one. Measured twice,
	// both recorded under HARNESS-822 in the ai-agent-config ledger: a prompt containing
	// `--once` overwrote the `-C` workdir, and a mistyped `--sandbox workspace-write` was
	// absorbed as a positional workdir with the flag itself silently dropped.
	//
	//   endOfFlags    `--` stops flag parsing for *every* flag, not just the new ones.
	//                 Protecting only --codex-* would be a half-measure: a prompt after
	//                 `--` containing `--backend` would still be eaten. This regresses
	//                 nobody — `--` is not recognised today, so any call passing it is
	//                 already broken (it lands as the task).
	//   sawPositional the two --codex-* flags are honoured only ahead of the first
	//                 positional. Past it they are a hard error rather than a silent
	//                 no-op: silently running on the default model while the caller
	//                 believes they selected one is the failure this whole feature would
	//                 otherwise introduce. Existing flags keep their scan-anywhere
	//                 behaviour — narrowing them would break live callers.
	endOfFlags := false
	sawPositional := false

	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if endOfFlags {
			filtered = append(filtered, arg)
			sawPositional = true
			continue
		}
		if arg == "--" {
			endOfFlags = true
			continue
		}
		switch {
		case arg == "--lite", arg == "-L":
			liteMode = true
			continue
		case arg == "--backend":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--backend flag requires a value")
			}
			backendName = args[i+1]
			i++
			continue
		case strings.HasPrefix(arg, "--backend="):
			value := strings.TrimPrefix(arg, "--backend=")
			if value == "" {
				return nil, fmt.Errorf("--backend flag requires a value")
			}
			backendName = value
			continue
		case arg == "--gemini-model":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--gemini-model flag requires a non-empty model name")
			}
			value := strings.TrimSpace(args[i+1])
			if value == "" {
				return nil, fmt.Errorf("--gemini-model flag requires a non-empty model name")
			}
			geminiModel = value
			i++
			continue
		case strings.HasPrefix(arg, "--gemini-model="):
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--gemini-model="))
			if value == "" {
				return nil, fmt.Errorf("--gemini-model flag requires a non-empty model name")
			}
			geminiModel = value
			continue
		case arg == "--skip-permissions", arg == "--dangerously-skip-permissions":
			skipPermissions = true
			continue
		case arg == "--progress":
			progress = true
			continue
		case strings.HasPrefix(arg, "--skip-permissions="):
			skipPermissions = parseBoolFlag(strings.TrimPrefix(arg, "--skip-permissions="), skipPermissions)
			continue
		case strings.HasPrefix(arg, "--dangerously-skip-permissions="):
			skipPermissions = parseBoolFlag(strings.TrimPrefix(arg, "--dangerously-skip-permissions="), skipPermissions)
			continue
		case arg == "--codex-model", strings.HasPrefix(arg, "--codex-model="),
			arg == "--codex-effort", strings.HasPrefix(arg, "--codex-effort="):
			name := "--codex-model"
			if strings.HasPrefix(arg, "--codex-effort") {
				name = "--codex-effort"
			}
			if sawPositional {
				// Short lines, action first. This message renders inside the deferred
				// "Recent Errors" block, i.e. already several lines down and after
				// unrelated startup warnings — a single long paragraph there is
				// effectively unreadable. Say what to type before explaining why.
				// "first positional", not "the task": `resume` is itself a positional, so
				// `resume <sid> --codex-model X <task>` lands here even though the flag IS
				// before the task. An earlier wording said "move it before the task" and
				// left that caller with no way to act on the message — it was already there.
				return nil, fmt.Errorf(
					"%[1]s was found after the first positional argument; it must come before all of them.\n"+
						"  New session:  codeagent-wrapper %[1]s <value> \"<task>\" [workdir]\n"+
						"  Resume:       codeagent-wrapper %[1]s <value> resume <session-id> \"<task>\" [workdir]\n"+
						"                (`resume` is itself a positional, so the flag goes before it too)\n"+
						"  Is this text part of your prompt? Pass the prompt on stdin with `-`.\n"+
						"  This is an error, not a warning: honouring it after the task would change which "+
						"token is the task, and ignoring it would run on the default model with no sign that it did.",
					name)
			}
			var value string
			if eq := strings.IndexByte(arg, '='); eq >= 0 {
				value = strings.TrimSpace(arg[eq+1:])
			} else {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("%s flag requires a non-empty value", name)
				}
				value = strings.TrimSpace(args[i+1])
				i++
			}
			if value == "" {
				return nil, fmt.Errorf("%s flag requires a non-empty value", name)
			}
			// Trimmed, then passed through unvalidated. No allowlist, because the two
			// obtainable lists of valid efforts do not contain each other: codex's own
			// rejection names none/minimal/low/medium/high/xhigh/max (no `ultra`), while
			// models_cache.json unions to low/medium/high/xhigh/max/ultra (no none or
			// minimal) and varies per model — luna has five, sol and terra six. Any static
			// table would refuse valid values AND advertise inapplicable ones, and would
			// rot with every model generation. codex rejects a bad value with a loud 400
			// and enumerates what it accepts, which is a better error than we could write.
			if name == "--codex-model" {
				codexModel = value
			} else {
				codexEffort = value
			}
			continue
		}
		filtered = append(filtered, arg)
		sawPositional = true
	}

	if len(filtered) == 0 {
		return nil, fmt.Errorf("task required")
	}
	args = filtered

	cfg := &Config{WorkDir: defaultWorkdir, Backend: backendName, SkipPermissions: skipPermissions, GeminiModel: geminiModel, CodexModel: codexModel, CodexEffort: codexEffort, Progress: progress}
	cfg.MaxParallelWorkers = resolveMaxParallelWorkers()

	if args[0] == "resume" {
		if len(args) < 3 {
			return nil, fmt.Errorf("resume mode requires: resume <session_id> <task>")
		}
		cfg.Mode = "resume"
		cfg.SessionID = strings.TrimSpace(args[1])
		if cfg.SessionID == "" {
			return nil, fmt.Errorf("resume mode requires non-empty session_id")
		}
		cfg.Task = args[2]
		cfg.ExplicitStdin = (args[2] == "-")
		if len(args) > 3 {
			cfg.WorkDir = args[3]
		}
	} else {
		cfg.Mode = "new"
		cfg.Task = args[0]
		cfg.ExplicitStdin = (args[0] == "-")
		if len(args) > 1 {
			cfg.WorkDir = args[1]
		}
	}

	return cfg, nil
}

const maxParallelWorkersLimit = 100

func resolveMaxParallelWorkers() int {
	raw := strings.TrimSpace(os.Getenv("CODEAGENT_MAX_PARALLEL_WORKERS"))
	if raw == "" {
		return 0
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		logWarn(fmt.Sprintf("Invalid CODEAGENT_MAX_PARALLEL_WORKERS=%q, falling back to unlimited", raw))
		return 0
	}

	if value > maxParallelWorkersLimit {
		logWarn(fmt.Sprintf("CODEAGENT_MAX_PARALLEL_WORKERS=%d exceeds limit, capping at %d", value, maxParallelWorkersLimit))
		return maxParallelWorkersLimit
	}

	return value
}
