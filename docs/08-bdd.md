# jobs-sg BDD — 行为规格（Gherkin 场景集）

> 与 [07-prd](07-prd.md) 配套：每条 PRD User Story 在这里落成可执行场景。领域词汇与口径定义见 [03-data-model](03-data-model.md)，实现细节见 [02-design](02-design.md)。
> 用法：按 Feature 逐条实现；每条 Given/When/Then 即一个可测断言。**数字口径场景必须以真实 API fixture 回放验证**（[05-roadmap](05-roadmap.md) Phase 1 DoD）。
> 约定：时间均为 SGT；`status` 取值 `running|success|partial|failed`；`closed_at` 判定仅在 status='success' 的对账轮执行。

---

## Feature: 每日增量采集（incremental ingest）

每日 02:15 SGT 拉取 MCF 公开 JSON API 的新增职位，全类目归档，候选职位入库。

### Scenario: 首次运行执行基线扫描
- Given 系统从未运行过，`job` 表为空
- And API 返回当前在架职位列表（total≈86k）
- When 执行 ingest
- Then 执行一次全量基线扫描（不设 2 天回溯窗）
- And 所有在架职位全类目归档到 `raw/<date>/*.jsonl.gz`
- And 候选职位按官方 `posting_date` 入库
- And `ingest_run` 记录 kind='incremental' 且 status='success'

### Scenario: 增量拉取只处理近窗新增
- Given 上次成功运行的 watermark = 2026-08-01T00:00:00Z
- When 执行 ingest
- Then 对 `newPostingDate` 早于 watermark-2d 的职位停止翻页
- And 该页之前的所有职位先归档、后按 `is_candidate` 决定是否入库
- And 请求间隔 ≥1.5s、单线程顺序翻页

### Scenario: 429 限流触发退避重试
- Given API 返回 HTTP 429
- When 请求该页
- Then 指数退避重试（2s、4s、8s）
- And 三次后仍失败则记录 `errors` 但不中断整轮
- And `ingest_run.errors` 递增、status 仍为 success（单页失败不降级整轮）

### Scenario: 超过页数熔断阈值标记 partial
- Given 已连续翻页超过 300 页
- And 尚未到达 watermark 回溯窗
- When 继续请求下一页
- Then 停止本轮并记录 status='partial'
- And `jobs_sg_ingest_errors_total` 递增
- And 已归档数据不回滚（partial 不丢数据）

### Scenario: 归档先于解析（schema 变更可回放）
- Given API 返回包含未知新字段的职位对象
- When 解析该页
- Then 原始 JSON 已先写入 gzip 归档
- And 解析器对未知字段宽容（跳过而非报错）
- And 可从归档回放重建 DB

### Scenario: 归档为全类目（不过滤）
- Given API 页中包含 87 条非 IT 类目职位与 13 条 IT 类目职位
- When 执行 ingest
- Then 100 条全部写入归档
- And 仅 `is_candidate` 为真的职位写入 `job` 表

### Scenario: User-Agent 透明声明
- When ingest 发起请求
- Then 请求头 User-Agent 包含 `jobs-sg-monitor/1.0 (+https://jobs.meirong.dev)`
- And 不伪装浏览器 UA

### Scenario: 每轮记录运行审计
- When ingest 完成
- Then `ingest_run` 落一条记录含 pages_fetched/jobs_seen/jobs_new/jobs_updated/errors/watermark/status

---

## Feature: 职位生命周期与全量对账（full reconcile + closed_at）

每周日随 ingest 执行全量在架对账，识别已下架职位。

### Scenario: 仍在架的职位保持 active
- Given 上轮对账后职位 A 在架（closed_at IS NULL）
- When 本周全量对账扫描再次见到 A
- Then 更新 `last_seen_at`、`miss_count=0`、`closed_at=NULL`
- And 刷新 `view_count` / `application_count`

### Scenario: 到期职位直接关闭
- Given 职位 A `expiry_date < today` 且 `closed_at IS NULL`
- When 本轮对账 status='success'
- Then A 的 `closed_at` 置为当前时间、`miss_count=0`

### Scenario: 未到期消失需连续两周未见才关闭
- Given 职位 B 未到期、closed_at IS NULL
- And 第一轮对账未见到 B（miss_count=1）
- When 第二轮对账仍未见到 B（miss_count=2）
- Then B 的 `closed_at` 置为当前时间
- And 仅见一次时（miss_count=1）不关闭

### Scenario: partial 扫描不执行关闭判定
- Given 本轮对账因熔断/页数偏差被标记 status='partial'
- When 扫描结束
- Then 不批量写入任何 `closed_at`
- And 仅更新已见职位的 last_seen_at

### Scenario: reopen 不清除新增归属
- Given 职位 C 曾关闭（closed_at 有值）
- When 本周对账重新见到 C
- Then C 的 `closed_at` 清为 NULL（reopen）
- And C 不产生"新增"（新增量仍按 original_posting_date 归属）

### Scenario: 对账完整性偏差告警
- Given 抓取条数与 API total 偏差 ≥2%
- When 对账结束
- Then 标记 status='partial'
- And 记入 `jobs_sg_ingest_errors_total`

---

## Feature: 去重与重贴（dedup & repost）

### Scenario: 同 uuid 刷新不新增
- Given 职位 D 已存在（uuid 相同、description_sha256 不变、repostCount 从 0 变 1）
- When ingest 处理该职位
- Then UPDATE 现有行而非 INSERT
- And 统计归属 `original_posting_date`，不计入新增

### Scenario: 同 uuid 内容小改不算新职位
- Given 职位 E 同 uuid 但 description_sha256 变化（改版）
- When ingest 处理该职位
- Then UPDATE 现有行
- And `original_posting_date` 不变 → 仍不计新增
- And 标记描述已变更（供 enrich 重跑）

### Scenario: 复制成新 uuid 且指纹命中归组
- Given 职位 F 以新 uuid 出现，title+公司+描述指纹与既有职位命中
- When ingest 处理该职位
- Then 记入 `job_repost(repost_uuid, canon_uuid, basis='fingerprint')`
- And 指标层不计其为新职位

### Scenario: 真新职位计入新增
- Given 职位 G 为新 uuid 且指纹未命中任何既有职位
- When ingest 处理该职位
- Then 正常 INSERT
- And 计入所在 ISO 周的新增 canonical 职位数

---

## Feature: SWE 口径分类（is_candidate / is_swe）

### Scenario: SSOC 主判命中
- Given 职位 ssoc_code='25121'（Golang Developer，属于 251 软件与应用开发）
- When 运行分类
- Then is_candidate=1、role_family='Backend'（按 ssoc_taxonomy）
- And 记录命中层级='ssoc'

### Scenario: 类目辅判命中
- Given 职位 ssoc_code 不在白名单但 categories[].category='Information Technology'
- When 运行分类
- Then is_candidate=1、命中层级='category'

### Scenario: 标题兜底命中
- Given 职位挂在 'Engineering' 类目且标题含 'engineer'、ssoc 不在白名单
- When 运行分类
- Then is_candidate=1、命中层级='title'

### Scenario: 三级全部未命中不入候选
- Given 职位 ssoc 不在白名单、类目非 IT、标题无 SWE 关键词
- When 运行分类
- Then is_candidate=0
- And 该职位不写 `job` 表（但仍在归档）

### Scenario: is_swe 严口径独立于 is_candidate
- Given 职位 is_candidate=1 且 Phase 0 核定未将其纳入 is_swe
- When 运行统计
- Then 该职位不计入周报指标（is_swe=0）
- And 收紧口径只需重算 is_swe，无需重灌数据

### Scenario: 资历冲突时以标题为准
- Given 职位 position_level='Professional'、min_years_exp=6、标题含 'Staff Engineer'
- When 推导 seniority
- Then seniority='Staff+'
- And 记录投票依据（标题优先）

### Scenario: work_mode 推导与标注
- Given 职位 flexibleWorkArrangements=['remote']
- When 推导 work_mode
- Then work_mode='Remote'
- And 空值时推导 'Onsite' 并标注为推断值

---

## Feature: 技术栈富化（enrich）

### Scenario: 规则层永远先跑
- Given 职位描述含 'Go'、'kubernetes'、'AWS'
- When 运行 enrich
- Then 规则层写入 job_tech（source='rule'）：go、kubernetes、aws
- And LLM 层只处理规则层未能覆盖的职位（或作为补充）

### Scenario: 规则层别名归一
- Given 描述含 'golang'、'k8s'、'gcp'
- When 规则层扫描
- Then 写入 go、kubernetes、google-cloud（按 tech_taxonomy 别名表）
- And 未命中别名的词不写 job_tech

### Scenario: LLM 输出结构化 JSON 并归一
- Given LLM 返回 `{"languages":["Python"],"frameworks":["Django"],"cloud":["GCP"],"databases":["PostgreSQL"],"tools":["Docker"],"ai":["PyTorch"]}`
- When enrich 处理
- Then 全部归一后写入 job_tech（source='llm'）
- And 无法归一的词落 `unmapped_tech`（seen_count 递增）

### Scenario: 缓存命中跳过 LLM
- Given 职位描述 sha256 已存在于 enrich_cache（同 model + prompt_version）
- When enrich 处理该职位
- Then 不调用 LLM、直接复用缓存结果
- And `llm_cached` 递增、`llm_calls` 不递增

### Scenario: 缓存未命中才调用 LLM
- Given 职位描述 sha256 不在 enrich_cache
- When enrich 处理该职位
- Then 调用 LLM 一次（并发 ≤3、超时 60s、失败重试 1 次）
- And 结果写入 enrich_cache

### Scenario: fail-open 保留规则层结果
- Given Bifrost 不可达（或 401、DGX 关机）
- When enrich 执行
- Then 作业退出码 0
- And 保留规则层结果、标记 status='partial'
- And `jobs_sg_enrich_backlog` 反映未富化职位积压
- And 不触发 CronJob 失败告警风暴

### Scenario: 模型降级链
- Given 主模型 custom_dgx 调用失败
- When enrich 重试
- Then 依次尝试 custom_m2 → 纯规则
- And 全部失败时保留规则层结果、status='partial'

---

## Feature: 周报指标计算（weekly_metric）

### Scenario: 新增量为去重 canonical 数
- Given 统计周内有 100 个不同 uuid、其中 10 个为刷新/重贴/reopen、5 个跨源重复
- When 计算新增量
- Then 新增 canonical 职位数 = 85
- And 刷新/重贴/reopen/跨源重复均不计入

### Scenario: 在架量口径
- Given 职位 closed_at IS NULL 且 expiry_date 为 NULL 或 ≥周末
- When 计算在架量
- Then 计入 active_jobs
- And closed_at 非空或已过期不计入

### Scenario: 技术频次按职位数去重
- Given 3 个职位各出现 'kubernetes' 一次、1 个职位出现两次
- When 计算技术频次
- Then kubernetes 频次 = 4（出现该技术的职位数，非词频）

### Scenario: 薪资中位数排除规则
- Given 6 个薪资为 Monthly 且 salary_hidden=0，2 个 Annual、1 个 salary_hidden=1
- When 计算薪资中位数
- Then 只用 6 个 Monthly 样本
- And Annual 与 hidden 单独统计不混算

### Scenario: 投递竞争度
- Given 职位 view_count=500、application_count=30、vacancies=3
- When 计算 Demand Signals
- Then 竞争度 = 30 / max(3,1) = 10
- And vacancies 为 0 或 NULL 时除以 1

### Scenario: 周粒度与口径重算
- Given 口径在 W34 变更
- When 运行周报
- Then 全量重算历史 weekly_metric
- And 周报 Data Quality 标注口径变更点

### Scenario: 数字不由 LLM 产生
- When 渲染周报数字章节
- Then 所有数字来自 SQL 计算（weekly_metric 或直接查询）
- And LLM 仅生成 Insights 段落的自然语言解读，且 prompt 只含已算好的数字

---

## Feature: 周报渲染与推送（report）

### Scenario: 生成自包含 HTML 与 Markdown
- Given weekly_metric 已物化
- When report 执行
- Then 生成 `report/YYYY-Www.html`（内联 CSS + SVG，无外部资源）
- And 同步生成同名 `.md`
- And 更新 `report/index.html` 与 `report/latest.html`

### Scenario: 周一自动推送 Telegram
- Given 周报渲染完成
- When report 执行（周一 09:00 SGT）
- Then 推送摘要 + 链接到 Telegram 内容话题
- And 不使用告警话题（messageThreadID != 2）

### Scenario: 章节完整
- When report 生成
- Then HTML 含 8 个章节：Executive Snapshot / Hiring Trends / Tech Trends / Compensation / Demand Signals / Skills-first / Insights / Data Quality

---

## Feature: Web 展示与可观测（web）

### Scenario: 路由与只读
- Given SQLite 以 mode=ro 打开
- When 访问 `/`、`/w/2026-W32`、`/healthz`、`/metrics`
- Then 分别返回最新周报、历史周报、200 健康检查、Prometheus 指标
- And 任何请求不产生写入

### Scenario: 指标从 DB 现算
- Given ingest_run 表中最近一次 incremental 为 2026-08-01T02:15:00Z
- When 抓取 `/metrics`
- Then 暴露 `jobs_sg_last_success_timestamp_seconds{kind="incremental"}` 等指标
- And web 重启后指标仍在（状态在 DB）

### Scenario: 缺失周报返回 404
- Given 请求 `/w/1999-W01`
- When 访问该路由
- Then 返回 404

---

## Feature: 调度与运维约束（ops）

### Scenario: CronJob 互斥与自死
- Given ingest 仍在运行
- When 到下一轮触发时间
- Then 新 Job 不启动（concurrencyPolicy: Forbid）
- And 运行超 activeDeadlineSeconds 的 Job 被终止

### Scenario: 告警链路
- Given 最近成功 incremental 超过 36h
- When 评估 PrometheusRule
- Then 触发 JobsSgIngestStale（warning）
- And 经 Alertmanager 推送 Telegram

### Scenario: 备份覆盖不可重建资产
- When restic 夜备执行
- Then 备份包含 jobs.db + WAL/SHM + 归档目录 `raw/`
- And 恢复后 `PRAGMA integrity_check` 通过（Phase 2 DoD 演练）

### Scenario: 安全基线
- Given 部署到 ns jobs-sg（PSA restricted）
- When 创建 Pod
- Then 以非 root（uid 10001）运行、drop ALL caps、只读根文件系统、seccomp RuntimeDefault
- And 镜像按 digest 固定（非 :latest）

---

## Feature: 多源扩展（Phase 3，本期仅 schema 预留）

### Scenario: 新源接入不改主流程
- Given 已实现 `Source` 接口与注册表
- When 添加 greenhouse 源
- Then 新增 `sources/greenhouse.go` 实现 fetch/normalize/classify
- And ingest 主流程无需改动

### Scenario: 跨源归并保守
- Given 同职位在 MCF 与 Greenhouse 各有记录、指纹未确认命中
- When 归并
- Then 不强行合并（宁可漏并）
- And 来源证据落 `job_source_xref`
- And 人工确认的归并 basis='human_review'

### Scenario: 跨源薪资归一
- Given ATS 返回 salaryRange interval='monthly'、currency='SGD'
- When 汇总薪资
- Then 归一为 MCF 一致的 salary_type='Monthly'
- And 非 SGD 单独统计不混算

---

## 覆盖映射（PRD ↔ BDD）

| PRD 章节 | BDD Feature |
|---|---|
| 采集 User Stories 1–10 | 每日增量采集 |
| 去重与生命周期 11–19 | 职位生命周期与全量对账 / 去重与重贴 |
| 分类口径 20–26 | SWE 口径分类 |
| 技术栈富化 27–33 | 技术栈富化 |
| 指标与周报 34–43 | 周报指标计算 / 周报渲染与推送 |
| 展示 44–46 | Web 展示与可观测 |
| 可观测性与运维 47–52 | 调度与运维约束 |
| 合规 53–55 | （非功能约束，见 03 §5 合规红线；由 fixture 与代码评审保障） |
| 多源化 56–59 | 多源扩展 |
