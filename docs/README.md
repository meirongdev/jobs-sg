# jobs-sg 文档

新加坡 SWE 职位监控与周报系统：每日拉取 MyCareersFuture 公开 JSON API → SQLite → 本地 LLM 技术栈富化 → 每周一生成趋势周报（`jobs.meirong.dev` + Telegram）。

## 文档地图

| 文档 | 内容 | 生命周期 |
|---|---|---|
| [01-requirements.md](01-requirements.md) | 要解决什么：周报内容、指标要求、合规红线、非目标 | 稳定 |
| [02-design.md](02-design.md) | 怎么解决：架构、部署位置、技术栈、组件设计、调度资源、风险 | 稳定，实现期微调 |
| [03-data-model.md](03-data-model.md) | SQLite schema、SWE 口径、派生维度、指标计算口径 | **活文档**，Phase 0 起持续演进 |
| [04-operations.md](04-operations.md) | GitOps 落地、homelab 踩坑清单、可观测性告警、备份恢复、集群安全 | 部署期主参照 |
| [05-roadmap.md](05-roadmap.md) | Phase 0–3 任务与 DoD checklist | **活文档**，打勾用 |
| [06-multi-source.md](06-multi-source.md) | 多源化扩展设计（ATS / 跨源去重 / 选择策略） | 设计存档，Phase 3 前冻结 |
| [07-prd.md](07-prd.md) | 产品需求文档：问题/方案/User Stories/实现与测试决策 | 稳定，实现期微调 |
| [08-bdd.md](08-bdd.md) | BDD 行为规格：Gherkin 场景集（采集/对账/去重/分类/富化/周报/web/运维） | **活文档**，随实现补全场景 |
| [superpowers/](superpowers/) | 设计规格（`specs/`）与实现计划（`plans/`），按日期命名 | 逐次追加，完成后只读 |
| [archive/](archive/) | 已废弃方案（v1）与时点勘查证据（2026-08-02） | 只读 |

**阅读顺序**：首次 01 → 02 → 03 → 07；写代码时常驻 03 + 08；部署时 04；日常只看 05。

## 当前状态（2026-08-02）

- 设计完成（v2.1），**尚未开工**。下一步：Phase 0 采样脚本（先测量再定义口径）。
- 应用代码在本仓库；k8s manifests 落 `meirongdev/homelab` 仓库（原因见 [04-operations.md](04-operations.md) §1.1）。

## 变更记录

| 日期 | 版本 | 变更 |
|---|---|---|
| 2026-08-02 | v1 | 初版架构方案（爬虫 + PG + 调度器）。技术选型当日即被实测推翻 → [归档](archive/2026-08-02-v1-architecture.md) |
| 2026-08-02 | v2 | 实测驱动的 k3s 设计（API 直连 / CronJob / SQLite / Go）+ §15 多源化。原文见 git `ca356f6` |
| 2026-08-02 | v2.1 | **评审修正 + 文档重构**：①全量对账 closed_at 判定重写（success 门控 / 两周未见 / reopen）；②日增量归档改全类目；③跨源指纹降级为"动工前重审"；④首跑基线策略；⑤fixture 回放测试入 Phase 1 DoD。原两份文档拆分为 01–06 + archive |
