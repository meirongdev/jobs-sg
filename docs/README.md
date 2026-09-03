# jobs-sg 文档

新加坡 SWE 求职者市场情报站点：每日拉取 MyCareersFuture 公开 JSON API → SQLite → 本地 LLM 技术栈富化 → 求职者统计页（`/` `/tech` `/pay` `/companies`）+ 每周一生成趋势周报归档（`jobs.meirong.dev` + Telegram）。

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

## 当前状态（2026-08-09）

- **代码侧 Phase 1 + Phase 2 基本完成**：ingest（增量/首跑基线/周对账 + closed_at 生命周期）、enrich（规则 + LLM 降级链、fail-open）、report（7 节周报 HTML/MD + Telegram 求职者口播）、web（求职者统计页 `/` `/tech` `/pay` `/companies` + `/ops` 运维视图 + 周报归档 `/reports` `/w/{week}` + `/healthz` + 独立端口 `/metrics`；`/daily` 301 → `/ops`）。
- **求职者站点 Phase A 已完成**（A-2b + A-2c，2026-08-07/08）：`internal/metric` 纯聚合层 + `internal/view` 共享视觉，周报与实时页共用一份口径；`?exp=`/`?role=` 镜头、抑制一等状态均已落地。
- **已部署在 k3s-homelab**（ns `jobs-sg`，web + 三个 CronJob）。上线步骤与验证判据仍见 [09-deploy-runbook.md](09-deploy-runbook.md)。
- **Phase 0 分类口径核定仍未做**，已刻意推迟到首次 ingest 之后（届时 `jobs.db` 里就有现成样本），理由与风险见 [05-roadmap.md](05-roadmap.md) Backlog 第 1 条。**发布首个完整周报前应先做完。**

## 变更记录

| 日期 | 版本 | 变更 |
|---|---|---|
| 2026-08-02 | v1 | 初版架构方案（爬虫 + PG + 调度器）。技术选型当日即被实测推翻 → [归档](archive/2026-08-02-v1-architecture.md) |
| 2026-08-02 | v2 | 实测驱动的 k3s 设计（API 直连 / CronJob / SQLite / Go）+ §15 多源化。原文见 git `ca356f6` |
| 2026-08-07 | — | **上线准备**：一轮设计审查修完 P0/P1/P2（reopen 缺失、对账重复归档、告警每周误报、bot token 进日志、首页裸 404、`/metrics` 公网可达、指标类型与标签基数、缓存惊群、SGT 日历两处）；新增 09 部署 runbook；roadmap 拆分「代码完成」与「部署完成」 |
| 2026-08-09 | — | **文档同步**：07-prd / 02-design / 03-data-model / 08-bdd / README 对齐 2026-08-07 求职者站点 spec（定位、路由、周报 7 节、`internal/metric` 口径、抑制一等状态、新索引）；`/daily` 改名 `/ops` 落文 |
| 2026-09-03 | — | **LLM 配置化**：模型相关参数全部下沉到 `LLM_*` 环境变量（端点/模型链/超时/并发/重试/思考开关及其键名/提示词及版本/输出上限/描述截断/鉴权头/请求体兜底），换模型不再改代码；修复换模型后 `chat_template_kwargs` 键名静默失效（只有 `enable_thinking` 生效），并加了 `reasoning_tokens` 守卫；耗时数字按 `qwen38-flash-next` 回校；清理 Bifrost 网关退役残留（改为直连 DGX vLLM）。见 [09](09-deploy-runbook.md) §3.2 |
| 2026-08-02 | v2.1 | **评审修正 + 文档重构**：①全量对账 closed_at 判定重写（success 门控 / 两周未见 / reopen）；②日增量归档改全类目；③跨源指纹降级为"动工前重审"；④首跑基线策略；⑤fixture 回放测试入 Phase 1 DoD。原两份文档拆分为 01–06 + archive |
