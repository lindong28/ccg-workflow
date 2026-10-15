# 配置说明

## 装完之后文件在哪

```
~/.claude/
├── commands/ccg/       # 29 个核心命令模板
├── agents/ccg/         # 7 个子智能体
├── skills/ccg/         # 质量检查 + 多 Agent 协同
├── bin/codeagent-wrapper
└── .ccg/
    ├── config.toml     # CCG 配置文件
    └── prompts/
        ├── codex/      # 6 个 Codex 角色提示词
        └── gemini/     # 7 个 Gemini 角色提示词
```

## 环境变量

在 `~/.claude/settings.json` 的 `"env"` 里配：

| 变量 | 干什么 | 默认值 | 什么时候改 |
|------|--------|--------|-----------|
| `CODEAGENT_POST_MESSAGE_DELAY` | Codex 跑完后等几秒 | `5` | 进程卡住不退出就改成 `1` |
| `CODEX_TIMEOUT` | wrapper 总超时 | `21600000` 毫秒（6 小时） | 特别大的任务改大点 |
| `BASH_DEFAULT_TIMEOUT_MS` | Bash 命令超时 | `120000` | 命令跑超时就改大 |
| `BASH_MAX_TIMEOUT_MS` | Bash 最大超时 | `600000` | 构建特别慢就改大 |

::: details 完整 settings.json 示例

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

## 模型路由与命令选择

初始化 Step 2/4 与菜单第 6 项可分别配置前端、后端使用 Gemini 或 Codex，默认仍是前端 Gemini、后端 Codex；选用 Gemini 时还可设置具体模型名。菜单修改后重新安装模板使路由生效。协作模式仍为 `smart`。

`src/utils/installer-data.ts` 的核心注册表含 29 个命令；Skill Registry 还能生成命令，Impeccable 命令在初始化中可选、默认不安装，因此最终数量随所选技能而变。

当前 wrapper 总超时默认为 6 小时。`CODEX_TIMEOUT` 大于 `10000` 时按毫秒解析并除以 1000；不大于 `10000` 的正值兼容按秒解析。上面的 `7200` 示例表示两小时。此 fork 尚未发布的 wrapper 改动及外部管理安装入口边界见仓库 README。

## 实用工具

```bash
npx ccg-workflow menu  # 选「实用工具」
```

- **ccusage** — 看看你的 Claude Code 花了多少钱
- **CCometixLine** — 状态栏上显示 Git 信息 + 用量

## 常见问题

**Codex 跑完了但进程不退出**

`CODEAGENT_POST_MESSAGE_DELAY` 设成 `1`。这是 Codex CLI 0.80.0 在 `--json` 模式下的已知问题。

**Node 18 报 SyntaxError**

升级到 Node 20+。`ora@9.x` 用了 Node 20 的语法。

**MCP 工具没反应**

跑一下 `npx ccg-workflow diagnose-mcp`。

**Agent Teams 命令找不到**

在 settings.json 里加 `"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1"`。这还是实验特性。
