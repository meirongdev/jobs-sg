# 路线图与 DoD

> 活文档：完成打勾。**范围纪律：Phase 1 DoD 变绿之前，Phase 3 与 [06-multi-source](06-multi-source.md) 一律不动工**（schema 预留除外）——个人项目最大的风险不是技术失败，是停在半路。

---

## Phase 0 — 打地基（0.5 天）：先测量再定义

- [x] 抽样与分布导出工具：`go run ./scripts/taxonomyaudit --data-dir ./data`
      输出 `ssoc_code` 覆盖率（**含未映射码按影响量排序**，以及它们当前被兜底成什么）、
      `position_level`/`category`/`work_mode`/`employment_type`/`seniority` 分布、
      `salary_hidden` 与 `min_years_exp` 填写率、未复核的 `unmapped_tech` 词表
- [ ] 据此**人工核定** `ssoc_taxonomy`（SSOC → role_family，预计 30–50 个码）与 `tech_taxonomy` 种子表

**DoD**：两张映射表落库；SWE 口径可解释、可复现。

> 原计划先于任何生产代码，用一次性抓取脚本取样。实际顺序反了过来——先写了代码。
> 但这让剩下的那半变**更容易**而非更冒险：首次 ingest 之后，"2,000 条 IT 职位样本"就是
> `jobs.db` 里现成的东西，`scripts/taxonomyaudit` 直接读库，不必再抓一次网。
> 分类口径仍是整个系统的地基——口径拍脑袋，后面每周的趋势数字都不可信，
> 所以**首个完整周报对外发布之前必须完成人工核定那一步**。

## Phase 1 — MVP（1 周）

> **勾选约定（2026-08-07 起）**：`[x]` = 代码已完成并有测试；`[ ]` = 未做或**只差部署**。
> 代码与部署是两条独立进度，混在一个勾里会让人读不出「能不能上线」。逐项部署状态见
> [09-deploy-runbook](09-deploy-runbook.md)。

- [x] `ingest`：增量 + **全类目归档** + SQLite schema + 首跑基线（[02](02-design.md) §4.1）
- [x] 规则层技术栈抽取（无 LLM）
- [x] `report` 生成 HTML/Markdown（本地跑通）
- [x] fixture（360 条，跨 6 个完整 ISO 周）+ 归一化/口径回放测试（**仍是 `genfixture` 生成的样例，待实录 API 后替换为真实捕获**；已下架岗位由 `closeFixturePostings` 在建库时注入）
- [x] 容器化 + GH Actions → GHCR（amd64 + arm64）
- [ ] **部署**：GHCR 包设 Public → 取 digest → manifests 同步 homelab → Vault 密钥 → ArgoCD sync（步骤见 [09](09-deploy-runbook.md)）
- [ ] 顺手修 homelab 文档两处 port 8000 → 80（`docs/CONVENTIONS.md`、`.claude/skills/add-service/SKILL.md`，见 [04](04-operations.md) §2 第 1 条）

**DoD**：连续 3 天自动增量成功（`ingest_run` 有 3 条 `success`）；周报 HTML 可在 `jobs.meirong.dev` 打开。

## Phase 2 — 生产化（1 周）

> 本阶段**代码基本已完成**，卡在部署与真实环境验证上。

- [x] 每周全量对账 + `closed_at` 生命周期（success 门控 + `miss_count` 两周判定 + reopen，[02](02-design.md) §4.1）
- [x] `enrich` 接本地 LLM（缓存、降级链、fail-open、思考模式开关；模型相关参数全部环境变量化，见 [09](09-deploy-runbook.md) §3.2）
- [x] `/metrics`（独立 9090 端口）+ ServiceMonitor + 5 条 PrometheusRule
- [x] Telegram 周报推送（独立话题，不占告警话题；错误不打印 bot token）
- [ ] restic 备份接入（`jobs.db` + **归档目录**）+ **实际恢复演练**（`PRAGMA integrity_check` 通过）
- [ ] Grafana 面板、homepage 条目、Uptime Kuma monitor
- [ ] **告警链路端到端验证**（真实集群里才做得了）

**DoD**：告警链路端到端验证（手动停一次 CronJob，确认 `JobsSgIngestStale` 到 Telegram）；restore 演练通过。

## Backlog — 首个部署版本刻意推迟的（2026-08-07 定）

> 为了尽快让管线在真实数据上跑起来，下列各项全部推迟。排序即建议的处理顺序。
> 判据：**不上线就验证不了的排前面；上线后反而变容易的，不该挡在上线之前。**

1. **Phase 0 人工核定**（工具已就绪，只差判断）。跑 `go run ./scripts/taxonomyaudit --data-dir ./data`，它会按影响量排序列出未映射的 SSOC 码及其当前兜底归类；照着从大到小往 `ssoc_taxonomy` 里加，前几条就能收掉大部分。同一份输出还给出 `unmapped_tech` 词表供 `tech_taxonomy` 扩充。
   **为什么这步机器代替不了**：SSOC 码到 role_family 是语义判断（"25199 其他软件开发员"该算 Backend 还是 Other-IT），错了整季度的趋势都跟着错。
   **风险与兜底**：核定之前，周报与 `/tech` 的角色分布可能偏斜。可接受，因为 archive-before-parse 使口径可全量重算（docs/01 §4 本就要求口径变更重算历史）。**首个完整周报发布前应先做完这项**。
2. ~~**fixture 掺入已下架岗位**~~ —— **已完成**（2026-08-07）。`buildFixtureDB` 里 `closeFixturePostings` 按确定性规则关掉 1/3 的岗位，寿命覆盖 `<7 / 7-14 / 15-30 / 30-60 / 60+` 各档含边界值；其余 2/3 保持在架，右删失因此可见。
3. ~~**A-2b**~~ —— **已完成**（2026-08-07）。`/` 现算快报页（在架量、周新增、WoW、12 周趋势、方向/资历/工作模式分布、入门岗绝对数）与 `/companies`（持续招聘者按 UEN 归并、类型分布、各家竞争度与透明率、岗位寿命含右删失标注、幽灵岗信号、按方向的竞争度分层），以及 `/tech` `/pay` 统计页与 `/daily` → `/ops` 改名（spec §2.1）。导航为 Market / Tech / Pay / Employers / Weekly report，周报移到 `/reports`。
4. ~~**A-2c**~~ —— **已完成**（2026-08-07）。周报按 spec §4.5 重排为 7 节、Data Quality 收为页脚一行；各节数字改由 `internal/metric` 计算（周报与实时页共用一份口径），顺带落地 §3.7 的第 ①②④ 三处口径修正；Telegram 改求职者口播（升温 Top 3、入门岗绝对数、各经验档薪资带、数据新鲜度、周报链接）并从 `cmd/report` 下沉到 `internal/report` 以便测试；`weekly_metric` 新增 `tech_share`/`swe_enriched` 审计行；`pct`/`money`/`topn` 收敛到 `internal/view`。
5. **Phase B `/jobs`**：前置为验证 MCF 回链格式（spec §6）；另注意描述正文不落库，全文搜索需读归档或加列。
6. ~~零散项~~ —— 除两条外**均已完成**（2026-08-07）：`closed_at` 逐周前移已修（原来是 `CloseExpired` 漏了候选集门控，不是口径问题）· 五份 SGT 常量已收敛到 `internal/sgt` · 两个 gauge 已去掉 `_total` 后缀 · `lensNav` 已改 copy-and-override。
   ~~`employment_type` 与整张 `job_skill` 表零消费~~ —— **已完成**（2026-08-08）：`/` 新增按 Full Time/Contract/Part Time/Internship 的分布；`/tech` 新增「What else they ask for」表，读 `job_skill` 的 MCF 原生技能标签（业务能力，非技术栈——两者刻意分开渲染，不与 `job_tech` 排名混排），含 must-have 占比。fixture 原来 360 条全部挂同样两个技能标签，无法验证任何区分度，已扩为按模板变化的 22 种标签 + 4 种雇佣类型。
   **仍开着**：日增量归档失败已降级 partial，但单条失败仍不重试。

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
