package main

import (
	"slices"
	"testing"
)

// TestBuildCodexArgs_ReadOnlySandbox verifies the opt-in least-privilege tier:
// CODEX_SANDBOX=read-only swaps the full-capability bypass for a read-only
// sandbox, while the default path stays byte-identical (non-breaking).
func TestBuildCodexArgs_ReadOnlySandbox(t *testing.T) {
	base := []string{"--skip-git-repo-check", "-C", "/tmp", "--json", "task"}

	t.Run("default keeps bypass (unchanged)", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		want := append([]string{"e", "--dangerously-bypass-approvals-and-sandbox"}, base...)
		if !slices.Equal(got, want) {
			t.Fatalf("default: got %v want %v", got, want)
		}
	})

	t.Run("read-only opt-in replaces bypass with read-only sandbox", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "read-only")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		want := append([]string{"e", "--sandbox", "read-only"}, base...)
		if !slices.Equal(got, want) {
			t.Fatalf("read-only: got %v want %v", got, want)
		}
		if slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("read-only must not carry the bypass flag: %v", got)
		}
	})

	t.Run("read-only is trimmed and case-insensitive", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "  Read-Only  ")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		if !slices.Contains(got, "--sandbox") || slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("case-insensitive read-only failed: %v", got)
		}
	})
}

// TestBuildCodexArgs_WorkspaceWriteSandbox verifies the middle tier: writes confined
// to the workspace, network pinned on. Network is asserted explicitly because it is
// the whole reason the tier exists — a delegate doing remote diagnosis needs to reach
// the host, and read-only cannot (its denial lands in connect(), pre-authentication).
// Pinning it here rather than inheriting from config.toml keeps the tier's behaviour
// the same on every machine; inherited, a config gap would present as a sandbox denial.
func TestBuildCodexArgs_WorkspaceWriteSandbox(t *testing.T) {
	base := []string{"--skip-git-repo-check", "-C", "/tmp", "--json", "task"}

	t.Run("workspace-write replaces bypass and pins the whole write surface", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "workspace-write")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		want := append([]string{
			"e", "--sandbox", "workspace-write",
			"--ignore-rules",
			"-c", "approvals_reviewer=user",
			"-c", "sandbox_workspace_write.network_access=true",
			"-c", "sandbox_workspace_write.writable_roots=[]",
			"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
			"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		}, base...)
		if !slices.Equal(got, want) {
			t.Fatalf("workspace-write: got %v want %v", got, want)
		}
		if slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("workspace-write must not carry the bypass flag: %v", got)
		}
	})

	// Each exclusion is asserted by name, not just by the equality above, because the
	// equality check would keep passing if a future edit dropped one and updated `want`
	// to match. These three are the difference between "writes are confined" and a
	// measured hole: /tmp and $TMPDIR are writable roots by default, and an operator's
	// writable_roots survives a leaf-only network_access override.
	t.Run("workspace-write pins every exclusion that was measured open", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "workspace-write")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		for _, required := range []string{
			"--ignore-rules",
			"approvals_reviewer=user",
			"sandbox_workspace_write.writable_roots=[]",
			"sandbox_workspace_write.exclude_slash_tmp=true",
			"sandbox_workspace_write.exclude_tmpdir_env_var=true",
		} {
			if !slices.Contains(got, required) {
				t.Fatalf("missing %q — the tier is weaker than it claims: %v", required, got)
			}
		}
	})

	// read-only must not pick up any of the workspace-write hardening. They are aimed
	// at a write surface it does not have, and --ignore-rules / approvals_reviewer
	// would silently change what an existing reviewer delegate is allowed to do.
	t.Run("read-only gains none of the workspace-write flags", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "read-only")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		for _, forbidden := range []string{
			"--ignore-rules",
			"approvals_reviewer=user",
			"sandbox_workspace_write.network_access=true",
			"sandbox_workspace_write.writable_roots=[]",
		} {
			if slices.Contains(got, forbidden) {
				t.Fatalf("read-only must not carry %q: %v", forbidden, got)
			}
		}
	})

	t.Run("workspace-write is trimmed and case-insensitive", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "  Workspace-Write  ")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		if !slices.Contains(got, "workspace-write") {
			t.Fatalf("case-insensitive workspace-write failed: %v", got)
		}
		if slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("case-insensitive workspace-write must not bypass: %v", got)
		}
	})

	t.Run("read-only stays read-only and gains no network override", func(t *testing.T) {
		t.Setenv("CODEX_SANDBOX", "read-only")
		got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")
		if slices.Contains(got, "sandbox_workspace_write.network_access=true") {
			t.Fatalf("read-only must not acquire the network override: %v", got)
		}
	})
}

// TestBuildCodexArgs_SandboxSelectionIsFourWay pins what `--help` now promises
// about CODEX_SANDBOX. The switch has three cases, so an unrecognised sandbox value
// does not simply "fall back to the bypass" — with CODEX_REQUIRE_APPROVAL=true
// none of them fires and codex is launched with no sandbox flag at all, deferring
// to its own configuration. Documenting only the recognised outcomes would tell a
// caller they are bypassed when they are not, or sandboxed when they are not.
//
// The unrecognised-value sentinel is deliberately a string that will never become a
// tier. It used to be "workspace-write", which stopped exercising the unrecognised
// path the moment that value was promoted to a real tier — the case kept passing
// under its old name while testing something else.
func TestBuildCodexArgs_SandboxSelectionIsFourWay(t *testing.T) {
	const bypass = "--dangerously-bypass-approvals-and-sandbox"
	const unrecognised = "not-a-sandbox-tier"

	cases := []struct {
		name           string
		sandbox        string
		requireApprove string
		wantSandbox    bool
		wantBypass     bool
	}{
		{"read-only wins over the bypass", "read-only", "", true, false},
		{"read-only wins even when approval is required", "read-only", "true", true, false},
		{"workspace-write wins over the bypass", "workspace-write", "", true, false},
		{"workspace-write wins even when approval is required", "workspace-write", "true", true, false},
		{"unset falls through to the bypass", "", "", false, true},
		{"unrecognised value falls through to the bypass", unrecognised, "", false, true},
		{"unrecognised value plus required approval yields neither flag", unrecognised, "true", false, false},
		{"unset plus required approval yields neither flag", "", "true", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEX_SANDBOX", tc.sandbox)
			t.Setenv("CODEX_REQUIRE_APPROVAL", tc.requireApprove)
			got := buildCodexArgs(&Config{Mode: "new", WorkDir: "/tmp"}, "task")

			if slices.Contains(got, "--sandbox") != tc.wantSandbox {
				t.Fatalf("--sandbox presence = %v, want %v: %v", !tc.wantSandbox, tc.wantSandbox, got)
			}
			if slices.Contains(got, bypass) != tc.wantBypass {
				t.Fatalf("bypass presence = %v, want %v: %v", !tc.wantBypass, tc.wantBypass, got)
			}
		})
	}
}
