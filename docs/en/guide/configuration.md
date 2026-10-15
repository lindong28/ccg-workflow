# Configuration

## Where things live after install

```
~/.claude/
├── commands/ccg/       # 29 core command templates
├── agents/ccg/         # 7 sub-agents
├── skills/ccg/         # Quality checks + multi-agent orchestration
├── bin/codeagent-wrapper
└── .ccg/
    ├── config.toml     # CCG config
    └── prompts/
        ├── codex/      # 6 Codex role prompts
        └── gemini/     # 7 Gemini role prompts
```

## Environment variables

Set these in `~/.claude/settings.json` under `"env"`:

| Variable | What it does | Default | When to change |
|----------|-------------|---------|----------------|
| `CODEAGENT_POST_MESSAGE_DELAY` | Seconds to wait after Codex finishes | `5` | Process hangs? Set to `1` |
| `CODEX_TIMEOUT` | Total wrapper timeout | `21600000` ms (6 hours) | Very large tasks |
| `BASH_DEFAULT_TIMEOUT_MS` | Bash command timeout (ms) | `120000` | Commands timing out |
| `BASH_MAX_TIMEOUT_MS` | Bash max timeout (ms) | `600000` | Slow builds |

::: details Full settings.json example

```json
{
  "env": {
    "CODEAGENT_POST_MESSAGE_DELAY": "1",
    "CODEX_TIMEOUT": "7200",
    "BASH_DEFAULT_TIMEOUT_MS": "600000",
    "BASH_MAX_TIMEOUT_MS": "3600000"
  }
}
```
:::

## Model routing and command selection

Initialization Step 2/4 and menu option 6 configure the frontend and backend independently, choosing Gemini or Codex. Defaults are Gemini for frontend and Codex for backend; selecting Gemini also exposes its model-name setting. Menu changes reinstall templates to apply the routing. Collaboration mode remains `smart`.

The core registry in `src/utils/installer-data.ts` contains 29 commands. Skill Registry can add commands; Impeccable commands are optional and disabled by default during initialization. The installed total therefore depends on the selected skills.

The current wrapper defaults to a six-hour total timeout. `CODEX_TIMEOUT` values above `10000` are interpreted as milliseconds and divided by 1000; positive values up to `10000` are accepted as seconds for compatibility. The example `7200` means two hours. See the repository README for the fork's unpublished wrapper changes and managed-installation boundaries.

## Utilities

```bash
npx ccg-workflow menu  # Select "Tools"
```

- **ccusage** — See how much your Claude Code sessions cost
- **CCometixLine** — Git info + usage tracking in your status bar

## FAQ

**Codex finishes but the process won't exit**

Set `CODEAGENT_POST_MESSAGE_DELAY` to `1`. Known issue with Codex CLI 0.80.0 in `--json` mode.

**Node 18 throws SyntaxError**

Upgrade to Node 20+. `ora@9.x` uses Node 20 syntax.

**MCP tools not responding**

Run `npx ccg-workflow diagnose-mcp`.

**Can't find Agent Teams commands**

Add `"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1"` to your settings.json env. It's still experimental.
