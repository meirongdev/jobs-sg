# 路线图与 DoD

> 活文档：完成打勾。**范围纪律：Phase 1 DoD 变绿之前，Phase 3 与 [06-multi-source](06-multi-source.md) 一律不动工**（schema 预留除外）——个人项目最大的风险不是技术失败，是停在半路。

---

## Phase 0 — 打地基（0.5 天）：先测量再定义

- [ ] 一次性脚本（本机跑，不进集群）抽样 **2,000 条 IT 职位**，导出：
      `ssoc_code` 频次分布、`positionLevels` 取值集合、`flexibleWorkArrangements` 填写率、
      `salary_hidden` 比例、`categories` 分布
- [ ] 据此**人工核定** `ssoc_taxonomy`（SSOC → role_family，预计 30–50 个码）与 `tech_taxonomy` 种子表

**DoD**：两张映射表落库；SWE 口径可解释、可复现。

> 先于任何生产代码。分类口径是整个系统的地基——口径拍脑袋，后面每周的趋势数字都不可信。

## Phase 1 — MVP（1 周）

- [ ] `ingest`：增量 + **全类目归档** + SQLite schema + 首跑基线（[02](02-design.md) §4.1）
- [ ] 规则层技术栈抽取（无 LLM）
- [ ] `report` 生成 HTML/Markdown（本地跑通）
- [ ] **真实 API fixture（~100 条实录响应）+ 归一化/口径回放测试**（v2.1 新增——口径可信的前提）
- [ ] 容器化 + GH Actions → GHCR（amd64 + arm64，digest 固定）
- [ ] manifests 落 homelab 仓库，ArgoCD 同步，CronJob 跑通
- [ ] 顺手修 homelab 文档两处 port 8000 → 80（`docs/CONVENTIONS.md`、`.claude/skills/add-service/SKILL.md`，见 [04](04-operations.md) §2 第 1 条）

**DoD**：连续 3 天自动增量成功（`ingest_run` 有 3 条 `success`）；周报 HTML 可在 `jobs.meirong.dev` 打开。

## Phase 2 — 生产化（1 周）

- [ ] 每周全量对账 + `closed_at` 生命周期（success 门控 + `miss_count` 两周判定 + reopen，[02](02-design.md) §4.1）
- [ ] `enrich` 接 Bifrost（缓存、降级链、fail-open）
- [ ] `/metrics` + ServiceMonitor + 5 条 PrometheusRule
- [ ] Telegram 周报推送（独立话题，不占告警话题）
- [ ] restic 备份接入（`jobs.db` + **归档目录**）+ **实际恢复演练**（`PRAGMA integrity_check` 通过）
- [ ] Grafana 面板、homepage 条目、Uptime Kuma monitor

**DoD**：告警链路端到端验证（手动停一次 CronJob，确认 `JobsSgIngestStale` 到 Telegram）；restore 演练通过。

## Phase 3 — 持续演进（按需）

- [ ] **多源采集第一期**：Greenhouse / Lever / Ashby 三个 ATS 公开 JSON API（[06](06-multi-source.md)）。
      ⚠️ 动工前先重审跨源指纹设计（06 §3 警告）
- [ ] 环比/同比、上升下降最快技术榜
- [ ] 异常检测（某技术或某公司突然爆发）
- [ ] `unmapped_tech` 周审 → 分类体系自动演进
- [ ] JobStreet / Indeed **仅做交叉验证/补漏**（不改核心口径；爬取前先查公开 API）
- [ ] LinkedIn：**默认不做**（无干净公开 API + 反爬 + ToS 风险，[06](06-multi-source.md) §1）
- [ ] 可选：`job_stats_snapshot` 需求侧轨迹（[03](03-data-model.md) §8）
- [ ] 若数据量或查询复杂度超出 SQLite：迁 oracle-k3s 的 CNPG（schema 基本可平移）
