# 项目文档索引

> 在 `docs/` 下工作时遵循 `~/.claude/skills/documentation-workflows/references/docs-organization-protocol.md`。根目录 `README.md`、`README.zh-CN.md` 与 `CHANGELOG.md` 也属于该协议的用户文档范围。

## 用户文档

| 文档 | 何时读取 | 何时更新 |
| --- | --- | --- |
| [`../README.md`](../README.md) / [`../README.zh-CN.md`](../README.zh-CN.md) | 了解项目定位、安装、使用与配置 | 用户可见功能、安装、使用或配置方式变化时 |
| [`../CHANGELOG.md`](../CHANGELOG.md) | 了解近期用户可见变更 | 用户可见行为变化时在 `Unreleased` 下按逻辑变更新增条目 |
| [`index.md`](./index.md) / [`en/index.md`](./en/index.md) | 浏览中文或英文文档站入口 | 项目定位、主导航或首页展示内容变化时 |
| [`guide/getting-started.md`](./guide/getting-started.md) / [`en/guide/getting-started.md`](./en/guide/getting-started.md) | 获取、安装并首次运行 CCG | 安装前提、安装或首次运行流程变化时同步更新两种语言 |
| [`guide/commands.md`](./guide/commands.md) / [`en/guide/commands.md`](./en/guide/commands.md) | 查阅命令能力与用法 | 命令入口、能力或用法变化时同步更新两种语言 |
| [`guide/configuration.md`](./guide/configuration.md) / [`en/guide/configuration.md`](./en/guide/configuration.md) | 查阅安装布局、环境变量与常见问题 | 配置、安装布局或排障入口变化时同步更新两种语言 |
| [`guide/mcp.md`](./guide/mcp.md) / [`en/guide/mcp.md`](./en/guide/mcp.md) | 配置或排查 MCP | MCP 选择、同步或授权方式变化时同步更新两种语言 |
| [`guide/workflows.md`](./guide/workflows.md) / [`en/guide/workflows.md`](./en/guide/workflows.md) | 选择并执行协作工作流 | 工作流选择、阶段或验收语义变化时同步更新两种语言 |

## 开发者文档

| 文档 | 何时读取 | 何时更新 |
| --- | --- | --- |
| [`AGENTS.md`](./AGENTS.md) | 开始写、改或审 `docs/` 下的内容前 | `docs/` 文档集合或读取/更新触发条件变化时 |
| [codeagent-wrapper Ubuntu 源码构建与跨平台交付计划](./plans/20260729-codeagent-wrapper-ubuntu/plan.md) | 追溯 Ubuntu 支持的目标、边界、验收与设计意图 | 归档后保持不变；后续变化由新的 plan 或当前态用户文档承载 |
| [`.vitepress/config.mts`](./.vitepress/config.mts) | 修改文档站导航、语言或主题配置前 | 文档站结构或展示配置变化时 |
| [`public/logo.svg`](./public/logo.svg) | 修改文档站标识资源前 | 文档站引用的标识资源变化时 |
