package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// Build metadata (`+…`) rather than a bumped number: upstream's own releases already
	// occupy 5.10.0 and 5.11.0, so a plain bump would make `--version` unable to tell a
	// fork build from an upstream one — which is the single check the consuming repo
	// relies on to detect a missed vendor. Kept in lockstep with EXPECTED_BINARY_VERSION
	// in src/utils/installer.ts; version.test.ts asserts the two are equal.
	version                  = "5.9.2+codex-model.1"
	defaultWorkdir           = "."
	defaultTimeout           = 21600 // seconds (6 hours)
	defaultInactivityTimeout = 1800  // seconds (30 minutes)
	defaultCoverageTarget    = 90.0
	codexLogLineLimit        = 0 // 0 = unlimited (prevent truncation of long JSON events like agent_message)
	stdinSpecialChars        = "\n\\\"'`$"
	stderrCaptureLimit       = 4 * 1024
	defaultBackendName       = "codex"
	defaultCodexCommand      = "codex"

	// stdout close reasons
	stdoutCloseReasonWait  = "wait-done"
	stdoutCloseReasonDrain = "drain-timeout"
	stdoutCloseReasonCtx   = "context-cancel"
	stdoutDrainTimeout     = 100 * time.Millisecond
)

var useASCIIMode = os.Getenv("CODEAGENT_ASCII_MODE") == "true"

// Lite mode: disable WebServer, reduce logging, faster post-message delay
// Can be enabled via --lite flag or CODEAGENT_LITE_MODE=true environment variable
var liteMode = os.Getenv("CODEAGENT_LITE_MODE") == "true"

// Test hooks for dependency injection
var (
	stdinReader  io.Reader = os.Stdin
	isTerminalFn           = defaultIsTerminal
	codexCommand           = defaultCodexCommand
	cleanupHook  func()
	loggerPtr    atomic.Pointer[Logger]

	buildCodexArgsFn   = buildCodexArgs
	selectBackendFn    = selectBackend
	commandContext     = exec.CommandContext
	jsonMarshal        = json.Marshal
	cleanupLogsFn      = cleanupOldLogs
	signalNotifyFn     = signal.Notify
	signalStopFn       = signal.Stop
	terminateCommandFn = terminateCommand
	defaultBuildArgsFn = buildCodexArgs
	runTaskFn          = runCodexTask
	exitFn             = os.Exit
)

var forceKillDelay atomic.Int32

// globalWebServer is the SSE web server for live streaming output
var globalWebServer *WebServer

func init() {
	forceKillDelay.Store(5) // seconds - default value
}

// findParallelIndex locates `--parallel` in raw argv, stopping at a `--` terminator.
//
// This is a second parser: it runs before parseArgs and parseArgs never sees these
// args, so the terminator has to be honoured in both places or in neither. Implemented
// only in parseArgs, `codeagent-wrapper -- --parallel` would be task text to one parser
// and a mode switch to the other — exactly the ambiguity `--` exists to remove.
//
// Extracted so its test can drive this function rather than a copy of it: a test that
// re-implements the scan passes whenever the copy is self-consistent, including when
// the real one is wrong.
func findParallelIndex(args []string) int {
	for i, arg := range args {
		if arg == "--" {
			return -1
		}
		if arg == "--parallel" {
			return i
		}
	}
	return -1
}

// parallelFlags is what the parallel branch reads out of argv.
type parallelFlags struct {
	backendName     string
	fullOutput      bool
	geminiModelSeen bool
	extras          []string
}

// parseParallelFlags is the third argv parser in this file, after findParallelIndex and
// parseArgs. `--` has to mean the same thing in all three or it means nothing: without it
// here, `codeagent-wrapper --parallel -- --backend codex` counts the terminator itself as
// a stray argument and dies naming the wrong token, while help promises `--` ends flag
// parsing everywhere.
//
// Tokens after `--` deliberately stay extras: parallel takes its tasks from stdin and
// accepts no positionals, so the extras error is the right outcome — what must not happen
// is the terminator becoming one of them.
//
// Extracted for the same reason as findParallelIndex: a test that re-implements this loop
// passes whenever its copy is self-consistent, including when the real one is wrong.
func parseParallelFlags(args []string) (parallelFlags, error) {
	out := parallelFlags{backendName: defaultBackendName}
	endOfFlags := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !endOfFlags && arg == "--" {
			endOfFlags = true
			continue
		}
		if endOfFlags {
			out.extras = append(out.extras, arg)
			continue
		}
		switch {
		case arg == "--parallel":
			continue
		case arg == "--full-output":
			out.fullOutput = true
		case arg == "--backend":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--backend flag requires a value")
			}
			out.backendName = args[i+1]
			i++
		case strings.HasPrefix(arg, "--backend="):
			value := strings.TrimPrefix(arg, "--backend=")
			if value == "" {
				return out, fmt.Errorf("--backend flag requires a value")
			}
			out.backendName = value
		case arg == "--gemini-model" || strings.HasPrefix(arg, "--gemini-model="):
			out.geminiModelSeen = true
			continue
		default:
			out.extras = append(out.extras, arg)
		}
	}
	return out, nil
}

func isWindows() bool {
	return os.Getenv("OS") == "Windows_NT" || len(os.Getenv("WINDIR")) > 0
}

func runStartupCleanup() {
	if cleanupLogsFn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logWarn(fmt.Sprintf("cleanupOldLogs panic: %v", r))
		}
	}()
	if _, err := cleanupLogsFn(); err != nil {
		logWarn(fmt.Sprintf("cleanupOldLogs error: %v", err))
	}
	// Prune old durable terminal records so failure records don't accumulate.
	cleanupOldResults()
}

func runCleanupMode() int {
	if cleanupLogsFn == nil {
		fmt.Fprintln(os.Stderr, "Cleanup failed: log cleanup function not configured")
		return 1
	}

	stats, err := cleanupLogsFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
		return 1
	}

	fmt.Println("Cleanup completed")
	fmt.Printf("Files scanned: %d\n", stats.Scanned)
	fmt.Printf("Files deleted: %d\n", stats.Deleted)
	if len(stats.DeletedFiles) > 0 {
		for _, f := range stats.DeletedFiles {
			fmt.Printf("  - %s\n", f)
		}
	}
	fmt.Printf("Files kept: %d\n", stats.Kept)
	if len(stats.KeptFiles) > 0 {
		for _, f := range stats.KeptFiles {
			fmt.Printf("  - %s\n", f)
		}
	}
	if stats.Errors > 0 {
		fmt.Printf("Deletion errors: %d\n", stats.Errors)
	}
	return 0
}

func main() {
	exitCode := run()
	exitFn(exitCode)
}

// run is the main logic, returns exit code for testability
func run() (exitCode int) {
	name := currentWrapperName()
	// Handle --version and --help first (no logger needed)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Printf("%s version %s\n", name, version)
			return 0
		case "--help", "-h":
			printHelp()
			return 0
		case "--cleanup":
			return runCleanupMode()
		}
	}

	// Initialize logger for all other commands
	logger, err := NewLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to initialize logger: %v\n", err)
		return 1
	}
	setLogger(logger)

	defer func() {
		logger := activeLogger()
		if logger != nil {
			logger.Flush()
		}
		// Shutdown WebServer if it was started
		if globalWebServer != nil {
			_ = globalWebServer.Stop()
			globalWebServer = nil
		}
		if err := closeLogger(); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to close logger: %v\n", err)
		}
		// On failure, extract and display recent errors before removing log
		if logger != nil {
			if exitCode != 0 {
				if errors := logger.ExtractRecentErrors(10); len(errors) > 0 {
					fmt.Fprintln(os.Stderr, "\n=== Recent Errors ===")
					for _, entry := range errors {
						fmt.Fprintln(os.Stderr, entry)
					}
					fmt.Fprintf(os.Stderr, "Log file: %s (retained)\n", logger.Path())
				}
			}
			// Keep the log on failure/kill so an externally-terminated run
			// (SIGTERM → exit 130) stays auditable and resumable; only remove it
			// on clean success. Orphaned logs are reclaimed by cleanupOldLogs.
			if exitCode == 0 {
				if err := logger.RemoveLogFile(); err != nil && !os.IsNotExist(err) {
					// Silently ignore removal errors
				}
			}
		}
	}()
	defer runCleanupHook()

	// Clean up stale logs from previous runs.
	runStartupCleanup()

	// Handle remaining commands
	if len(os.Args) > 1 {
		args := os.Args[1:]
		parallelIndex := findParallelIndex(args)

		if parallelIndex != -1 {
			flags, err := parseParallelFlags(args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				return 1
			}
			backendName := flags.backendName
			fullOutput := flags.fullOutput
			extras := flags.extras
			geminiModelInParallel := flags.geminiModelSeen

			// Warn about unsupported parameter
			if geminiModelInParallel {
				logWarn("--gemini-model parameter is not supported in parallel mode")
			}

			if len(extras) > 0 {
				fmt.Fprintln(os.Stderr, "ERROR: --parallel reads its task configuration from stdin; only --backend and --full-output are allowed.")
				fmt.Fprintln(os.Stderr, "Usage examples:")
				fmt.Fprintf(os.Stderr, "  %s --parallel < tasks.txt\n", name)
				fmt.Fprintf(os.Stderr, "  echo '...' | %s --parallel\n", name)
				fmt.Fprintf(os.Stderr, "  %s --parallel <<'EOF'\n", name)
				fmt.Fprintf(os.Stderr, "  %s --parallel --full-output <<'EOF'  # include full task output\n", name)
				return 1
			}

			backend, err := selectBackendFn(backendName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				return 1
			}
			backendName = backend.Name()

			data, err := io.ReadAll(stdinReader)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to read stdin: %v\n", err)
				return 1
			}

			cfg, err := parseParallelConfig(data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				return 1
			}

			cfg.GlobalBackend = backendName
			// Parallel mode is driven entirely from stdin + a small flag set; the
			// skip-permissions toggle is sourced from the env (CLI extras are rejected
			// above). Thread it onto every task so the backend arg builder emits
			// --dangerously-skip-permissions, matching single-task behavior.
			skipPermissions := envFlagEnabled("CODEAGENT_SKIP_PERMISSIONS")
			for i := range cfg.Tasks {
				if strings.TrimSpace(cfg.Tasks[i].Backend) == "" {
					cfg.Tasks[i].Backend = backendName
				}
				cfg.Tasks[i].SkipPermissions = skipPermissions
				// Inject ROLE_FILE content if present
				injectedTask, err := injectRoleFile(cfg.Tasks[i].Task)
				if err != nil {
					logWarn(fmt.Sprintf("Failed to inject ROLE_FILE for task %s: %v", cfg.Tasks[i].ID, err))
				} else {
					cfg.Tasks[i].Task = injectedTask
				}
			}

			// CODEX_MODEL / CODEX_REASONING_EFFORT are single-call only: parallel builds
			// its tasks from stdin and never constructs the Config that carries them, so
			// leaving them unguarded would run the whole batch on the default model while
			// the caller believes otherwise — invisible at the call site, which is the one
			// outcome this feature must not produce.
			//
			// Refuse only when codex is actually in the batch. An unconditional check
			// would fail a pure gemini/claude run over variables that could not have
			// affected it, contradicting this feature's scope. Backends are resolved
			// above, so by here every task's backend is its final value.
			if envModel, envEffort := strings.TrimSpace(os.Getenv("CODEX_MODEL")), strings.TrimSpace(os.Getenv("CODEX_REASONING_EFFORT")); envModel != "" || envEffort != "" {
				codexInvolved := false
				for i := range cfg.Tasks {
					if strings.EqualFold(strings.TrimSpace(cfg.Tasks[i].Backend), "codex") {
						codexInvolved = true
						break
					}
				}
				if codexInvolved {
					fmt.Fprintln(os.Stderr, "ERROR: CODEX_MODEL / CODEX_REASONING_EFFORT are not honoured in --parallel mode, and at least one task in this batch uses the codex backend.")
					fmt.Fprintln(os.Stderr, "The batch would silently run on the model from ~/.codex/config.toml instead. Unset them, or run the codex tasks separately.")
					return 1
				}
			}

			timeoutSec := resolveTimeout()
			layers, err := topologicalSort(cfg.Tasks)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				return 1
			}

			results := executeConcurrent(layers, timeoutSec)

			// Extract structured report fields from each result
			for i := range results {
				results[i].CoverageTarget = defaultCoverageTarget
				if results[i].Message == "" {
					continue
				}

				lines := strings.Split(results[i].Message, "\n")

				// Coverage extraction
				results[i].Coverage = extractCoverageFromLines(lines)
				results[i].CoverageNum = extractCoverageNum(results[i].Coverage)

				// Files changed
				results[i].FilesChanged = extractFilesChangedFromLines(lines)

				// Test results
				results[i].TestsPassed, results[i].TestsFailed = extractTestResultsFromLines(lines)

				// Key output summary
				results[i].KeyOutput = extractKeyOutputFromLines(lines, 150)
			}

			// Default: summary mode (context-efficient)
			// --full-output: legacy full output mode
			fmt.Println(generateFinalOutputWithMode(results, !fullOutput))

			exitCode = 0
			for _, res := range results {
				if res.ExitCode != 0 {
					exitCode = res.ExitCode
				}
			}

			return exitCode
		}
	}

	logInfo("Script started")

	cfg, err := parseArgs()
	if err != nil {
		logError(err.Error())
		return 1
	}
	logInfo(fmt.Sprintf("Parsed args: mode=%s, task_len=%d, backend=%s", cfg.Mode, len(cfg.Task), cfg.Backend))

	// Log environment variable usage
	if cfg.GeminiModel != "" {
		envModel := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
		if envModel != "" && envModel == cfg.GeminiModel {
			logInfo(fmt.Sprintf("Gemini model from env: %s", cfg.GeminiModel))
		}
	}

	backend, err := selectBackendFn(cfg.Backend)
	if err != nil {
		logError(err.Error())
		return 1
	}
	cfg.Backend = backend.Name()

	cmdInjected := codexCommand != defaultCodexCommand
	argsInjected := buildCodexArgsFn != nil && reflect.ValueOf(buildCodexArgsFn).Pointer() != reflect.ValueOf(defaultBuildArgsFn).Pointer()

	// Wire selected backend into runtime hooks for the rest of the execution,
	// but preserve any injected test hooks for the default backend.
	if backend.Name() != defaultBackendName || !cmdInjected {
		codexCommand = backend.Command()
	}
	if backend.Name() != defaultBackendName || !argsInjected {
		buildCodexArgsFn = backend.BuildArgs
	}
	logInfo(fmt.Sprintf("Selected backend: %s", backend.Name()))

	// Log model parameter usage
	if cfg.GeminiModel != "" && cfg.Backend == "gemini" {
		logInfo(fmt.Sprintf("Using Gemini model: %s", cfg.GeminiModel))
	}

	// Warn if model parameter used with non-gemini backend
	if cfg.GeminiModel != "" && cfg.Backend != "gemini" {
		logWarn("--gemini-model parameter is only effective with --backend gemini")
	}

	if cfg.Backend == "codex" {
		if cfg.CodexModel != "" {
			logInfo(fmt.Sprintf("Using Codex model: %s", cfg.CodexModel))
		}
		if cfg.CodexEffort != "" {
			logInfo(fmt.Sprintf("Using Codex reasoning effort: %s", cfg.CodexEffort))
		}
	} else if cfg.CodexModel != "" || cfg.CodexEffort != "" {
		// A warning, not an error: unlike the parallel case these can also arrive from
		// the environment, where they are set once and inherited by every later call —
		// so refusing here would break unrelated gemini/claude runs in a shell that has
		// them exported. The run itself is unaffected; only the request is inert.
		logWarn(fmt.Sprintf("--codex-model / --codex-effort are only effective with --backend codex (this run uses %s); ignoring them", cfg.Backend))
	}

	timeoutSec := resolveTimeout()
	logInfo(fmt.Sprintf("Timeout: %ds", timeoutSec))
	cfg.Timeout = timeoutSec

	var taskText string
	var piped bool

	if cfg.ExplicitStdin {
		logInfo("Explicit stdin mode: reading task from stdin")
		data, err := io.ReadAll(stdinReader)
		if err != nil {
			logError("Failed to read stdin: " + err.Error())
			return 1
		}
		taskText = string(data)
		if taskText == "" {
			logError("Explicit stdin mode requires task input from stdin")
			return 1
		}
		// Inject ROLE_FILE content if present
		taskText, err = injectRoleFile(taskText)
		if err != nil {
			logWarn(fmt.Sprintf("Failed to inject ROLE_FILE: %v", err))
		}
		piped = !isTerminal()
	} else {
		pipedTask, err := readPipedTask()
		if err != nil {
			logError("Failed to read piped stdin: " + err.Error())
			return 1
		}
		piped = pipedTask != ""
		if piped {
			// Inject ROLE_FILE content if present
			taskText, err = injectRoleFile(pipedTask)
			if err != nil {
				logWarn(fmt.Sprintf("Failed to inject ROLE_FILE: %v", err))
			}
		} else {
			taskText = cfg.Task
		}
	}

	useStdin := cfg.ExplicitStdin || shouldUseStdin(taskText, piped)

	targetArg := taskText
	// Gemini CLI does not support "-" as stdin marker for -p flag.
	// Match the geminiDirect logic in executor.go so the display is accurate.
	geminiDirect := useStdin && cfg.Backend == "gemini"
	if useStdin && !geminiDirect {
		targetArg = "-"
	}
	codexArgs := buildCodexArgsFn(cfg, targetArg)

	// Print startup information to stderr
	fmt.Fprintf(os.Stderr, "[%s]\n", name)
	fmt.Fprintf(os.Stderr, "  Backend: %s\n", cfg.Backend)
	fmt.Fprintf(os.Stderr, "  Command: %s %s\n", codexCommand, strings.Join(codexArgs, " "))
	fmt.Fprintf(os.Stderr, "  PID: %d\n", os.Getpid())
	fmt.Fprintf(os.Stderr, "  Log: %s\n", logger.Path())

	if useStdin {
		var reasons []string
		if piped {
			reasons = append(reasons, "piped input")
		}
		if cfg.ExplicitStdin {
			reasons = append(reasons, "explicit \"-\"")
		}
		if strings.Contains(taskText, "\n") {
			reasons = append(reasons, "newline")
		}
		if strings.Contains(taskText, "\\") {
			reasons = append(reasons, "backslash")
		}
		if strings.Contains(taskText, "\"") {
			reasons = append(reasons, "double-quote")
		}
		if strings.Contains(taskText, "'") {
			reasons = append(reasons, "single-quote")
		}
		if strings.Contains(taskText, "`") {
			reasons = append(reasons, "backtick")
		}
		if strings.Contains(taskText, "$") {
			reasons = append(reasons, "dollar")
		}
		if len(taskText) > 800 {
			reasons = append(reasons, "length>800")
		}
		if len(reasons) > 0 {
			logWarn(fmt.Sprintf("Using stdin mode for task due to: %s", strings.Join(reasons, ", ")))
		}
	}

	logInfo(fmt.Sprintf("%s running...", cfg.Backend))

	taskSpec := TaskSpec{
		Task:            taskText,
		WorkDir:         cfg.WorkDir,
		Mode:            cfg.Mode,
		SessionID:       cfg.SessionID,
		UseStdin:        useStdin,
		Progress:        cfg.Progress,
		SkipPermissions: cfg.SkipPermissions,
		CodexModel:      cfg.CodexModel,
		CodexEffort:     cfg.CodexEffort,
	}

	result := runTaskFn(taskSpec, false, cfg.Timeout)

	// On a non-zero exit (esp. an external SIGTERM → 130), persist a durable
	// terminal record so the run leaves an auditable outcome and a resume handle
	// instead of vanishing with its temp log. Successful runs already print
	// SESSION_ID to stdout and delete their log, so they need no record. See H8.
	// (Single-task path only; --parallel resume handles are out of scope here.)
	if result.ExitCode != 0 {
		if recPath, err := writeTerminalRecord(cfg, result); err != nil {
			logWarn(fmt.Sprintf("failed to write terminal record: %v", err))
		} else if recPath != "" {
			fmt.Fprintf(os.Stderr, "  Result: %s\n", recPath)
		}
	}

	if result.ExitCode != 0 {
		return result.ExitCode
	}

	fmt.Println(result.Message)
	if result.SessionID != "" {
		fmt.Printf("\n---\nSESSION_ID: %s\n", result.SessionID)
	}

	// CRITICAL: Windows-specific fix for Git Bash background process output capture
	// Git Bash may buffer stdout when running in background mode, causing incomplete output
	if isWindows() {
		_ = os.Stdout.Sync()
	}

	return 0
}

func setLogger(l *Logger) {
	loggerPtr.Store(l)
}

func closeLogger() error {
	logger := loggerPtr.Swap(nil)
	if logger == nil {
		return nil
	}
	return logger.Close()
}

func activeLogger() *Logger {
	return loggerPtr.Load()
}

func logInfo(msg string) {
	if logger := activeLogger(); logger != nil {
		logger.Info(msg)
	}
}

func logWarn(msg string) {
	if logger := activeLogger(); logger != nil {
		logger.Warn(msg)
	}
}

func logError(msg string) {
	if logger := activeLogger(); logger != nil {
		logger.Error(msg)
	}
}

func runCleanupHook() {
	if logger := activeLogger(); logger != nil {
		logger.Flush()
	}
	if cleanupHook != nil {
		cleanupHook()
	}
}

func printHelp() {
	name := currentWrapperName()
	help := fmt.Sprintf(`%[1]s - Go wrapper for AI CLI backends

Usage:
    %[1]s "task" [workdir]
    %[1]s --backend claude "task" [workdir]
    %[1]s --lite "task" [workdir]     Lite mode (faster, no Web UI)
    %[1]s - [workdir]              Read task from stdin
    %[1]s resume <session_id> "task" [workdir]
    %[1]s resume <session_id> - [workdir]
    %[1]s --parallel               Run tasks in parallel (config from stdin)
    %[1]s --parallel --full-output Run tasks in parallel with full output (legacy)
    %[1]s --version
    %[1]s --help

Parallel mode examples:
    %[1]s --parallel < tasks.txt
    echo '...' | %[1]s --parallel
    %[1]s --parallel --full-output < tasks.txt
    %[1]s --parallel <<'EOF'

Options:
    --lite, -L            Lite mode: disable Web UI, faster response
    --backend <name>      Select backend (codex, gemini, claude)
    --gemini-model <name> Specify Gemini model (gemini backend only)
                          Can also be set via GEMINI_MODEL environment variable
                          CLI parameter takes precedence over environment variable
                          Examples: gemini-2.5-flash, gemini-1.5-pro
    --codex-model <slug>  Specify Codex model (codex backend only)
                          MUST come before every positional argument, 'resume' included
                          Can also be set via CODEX_MODEL environment variable
                          CLI parameter takes precedence over environment variable
                          Omitted, the model from ~/.codex/config.toml is used
                          Examples: gpt-5.6-sol, gpt-5.6-luna, gpt-5.6-terra
    --codex-effort <level>
                          Specify Codex reasoning effort (codex backend only)
                          MUST come before every positional argument, 'resume' included
                          Can also be set via CODEX_REASONING_EFFORT
                          CLI parameter takes precedence over environment variable
                          Omitted, the effort from ~/.codex/config.toml is used
                          Levels vary per model and are not validated here; codex
                          rejects a bad one and names the ones it accepts
    --progress            Emit compact progress lines to stderr during execution
    --                    End of flags: every later argument is positional.
                          It protects a task that is ONE quoted argument. It cannot
                          reassemble a prompt the shell already split into several
                          words — those extra words still land on workdir. For any
                          multi-word prompt, pass it on stdin with '-' instead

Environment Variables:
    CODEX_TIMEOUT              Timeout in milliseconds (default: 21600000)
    CODEX_INACTIVITY_TIMEOUT   Stdout inactivity timeout in milliseconds (default: 1800000, 0 disables)
    CODEX_REQUIRE_APPROVAL     Require manual approval for file operations (default: false)
    CODEX_SANDBOX              Two recognised values (case-insensitive, surrounding
                               whitespace ignored), both taking precedence over the
                               approval bypass below:
                                 read-only       writes and kills denied; suits
                                                 reviewers and other delegates that
                                                 never need FS-write. Also denies
                                                 network: ssh fails in connect(),
                                                 before authentication.
                                 workspace-write for delegates that must reach the
                                                 network, such as remote diagnosis
                                                 over ssh. Writes are confined to the
                                                 workspace; network is on. The whole
                                                 policy is pinned by flag, not read
                                                 from config, so the tier means the
                                                 same thing on every machine:
                                                 network_access on, writable_roots
                                                 cleared, /tmp and $TMPDIR excluded
                                                 (both are writable roots by default
                                                 otherwise), the user/project rules
                                                 file ignored (an "allow" rule would
                                                 let a matching command leave the
                                                 sandbox entirely; protective
                                                 "forbidden" and "prompt" entries are
                                                 dropped with it, which is accepted
                                                 because their loss stays boxed inside
                                                 the workspace — admin/managed
                                                 requirements still apply), and
                                                 approvals kept away from an auto-
                                                 reviewer. Non-interactive exec keeps
                                                 approval_policy=never, so in practice
                                                 an action needing escalation is
                                                 denied, not prompted.
                                                 Known limits: reads outside the
                                                 workspace stay allowed (this bounds
                                                 damage, not exposure); nothing is
                                                 enforced on hosts reached over the
                                                 network; and a NESTED codeagent-
                                                 wrapper cannot start inside this tier
                                                 — it creates its log under
                                                 os.TempDir() before parsing, which
                                                 the temp exclusions deny. That
                                                 failure is loud, at startup.
                               UNSET IS NOT A SANDBOX, and neither is any other
                               value — those are ignored, and what runs instead is
                               decided by CODEX_REQUIRE_APPROVAL: left false (the
                               default) codex is launched with approvals and
                               sandboxing bypassed; set true, no sandbox flag is
                               passed at all and codex falls back to its own
                               configuration. Sandboxed work must opt in explicitly.
    CLAUDE_REQUIRE_APPROVAL    Require approval for Claude backend (default: false)
    CODEX_DISABLE_SKIP_GIT_CHECK  Disable skip-git-repo-check flag (default: false)
    CODEAGENT_ASCII_MODE       Use ASCII symbols instead of Unicode (PASS/WARN/FAIL)
    CODEAGENT_LITE_MODE        Enable lite mode (true/false)
    CODEAGENT_OPEN_BROWSER     Auto-open Web UI tab in browser (default: true)

Exit Codes:
    0    Success
    1    General error (missing args, no output)
    124  Timeout
    127  backend command not found
    130  Interrupted (Ctrl+C)
    *    Passthrough from backend process`, name)
	fmt.Println(help)
}
