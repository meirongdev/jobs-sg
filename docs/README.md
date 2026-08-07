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
| [09-deploy-runbook.md](09-deploy-runbook.md) | 首次上线操作清单：镜像/digest、homelab 同步、Vault、验证、头两天什么算正常、回退 | **上线期主参照** |
| [superpowers/](superpowers/) | 设计规格（`specs/`）与实现计划（`plans/`），按日期命名 | 逐次追加，完成后只读 |
| [archive/](archive/) | 已废弃方案（v1）与时点勘查证据（2026-08-02） | 只读 |

**阅读顺序**：首次 01 → 02 → 03 → 07；写代码时常驻 03 + 08；**部署时 09（原理查 04）**；日常只看 05。

## 当前状态（2026-08-07）

- **代码侧 Phase 1 + Phase 2 基本完成**：ingest（增量/首跑基线/周对账 + closed_at 生命周期）、enrich（规则 + LLM 降级链、fail-open）、report（周报 HTML/MD + Telegram）、web（`/`、`/tech`、`/pay`、`/ops`、`/healthz` + 独立端口 `/metrics`）。
- **尚未部署**——这是当前唯一的关键路径。照 [09-deploy-runbook.md](09-deploy-runbook.md) 走。
- **Phase 0 分类口径核定仍未做**，已刻意推迟到首次 ingest 之后（届时 `jobs.db` 里就有现成样本），理由与风险见 [05-roadmap.md](05-roadmap.md) Backlog 第 1 条。**发布首个完整周报前应先做完。**
- 求职者站点 A-2b（`/` 快报、`/companies`）与 A-2c（周报重排）在 backlog。

## 变更记录

| 日期 | 版本 | 变更 |
|---|---|---|
| 2026-08-02 | v1 | 初版架构方案（爬虫 + PG + 调度器）。技术选型当日即被实测推翻 → [归档](archive/2026-08-02-v1-architecture.md) |
| 2026-08-02 | v2 | 实测驱动的 k3s 设计（API 直连 / CronJob / SQLite / Go）+ §15 多源化。原文见 git `ca356f6` |
| 2026-08-07 | — | **上线准备**：一轮设计审查修完 P0/P1/P2（reopen 缺失、对账重复归档、告警每周误报、bot token 进日志、首页裸 404、`/metrics` 公网可达、指标类型与标签基数、缓存惊群、SGT 日历两处）；新增 09 部署 runbook；roadmap 拆分「代码完成」与「部署完成」 |
| 2026-08-02 | v2.1 | **评审修正 + 文档重构**：①全量对账 closed_at 判定重写（success 门控 / 两周未见 / reopen）；②日增量归档改全类目；③跨源指纹降级为"动工前重审"；④首跑基线策略；⑤fixture 回放测试入 Phase 1 DoD。原两份文档拆分为 01–06 + archive |
