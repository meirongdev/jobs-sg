# jobs-sg PRD — 新加坡 SWE 求职者市场情报站点

> 本文把 [01-requirements](01-requirements.md)（要什么）、[02-design](02-design.md)（怎么做）、[03-data-model](03-data-model.md)（口径与 schema）合成为一份可执行的 PRD，供实现与测试对齐。
> 已按 2026-08-07 求职者站点 spec（`docs/superpowers/specs/2026-08-07-jobseeker-facing-site-design.md`）同步：定位、路由、周报 7 节、`internal/metric` 口径、抑制一等状态。
> 领域词汇（MCF / SWE / ssoc_code / role_family / tech_taxonomy / is_candidate / is_swe / closed_at / weekly_metric 等）定义见 [03-data-model](03-data-model.md)。

---

## Problem Statement

新加坡 SWE 求职者（维护者是其中首要读者之一）需要持续跟踪本地 Software Engineer 招聘市场的**招聘趋势**与**技术趋势**，用于求职决策与市场判断。手翻招聘网站不可持续：MCF 每日新增数千条职位，逐条阅读费时费力且会漏；第三方市场报告延迟数月、面向全球或非新加坡。需要一条可重复、可回溯、跨周可比的数据管线：一个对外市场情报站点（`jobs.meirong.dev`）提供实时统计页（`/` `/tech` `/pay` `/companies`），并每周一产出一份可信周报归档，且**外部成本为 $0**（公开 API + 本地 LLM + 现有 homelab 平台）。

约束：

- 数字必须可信：口径可解释、可回溯、跨周可比；LLM 不得参与任何数字的产生（防幻觉污染指标）。对求职者读者而言，这条从自用要求**升级为对外承诺**（docs/01 §1）。
- 成本：外部支出 $0；常驻内存 ≤64Mi、批处理峰值 ≤512Mi（homelab 内存受限，实测 78% 已用）。
- 合规：只访问公开数据（MCF robots 全站放行，[已验证]）；不登录、不伪装浏览器；不采集候选人/个人数据；不转售、不分发原始数据集；每个对外页面标注数据延迟（最长 24h）。

## Solution

一条"采集 → 富化 → 指标 → 展示"的批处理流水线，全部跑在 `k3s-homelab`（ns `jobs-sg`）：

1. **ingest（每日 02:15 SGT）**：分页拉取 MCF 公开 JSON API → 全类目归档到 gzip JSONL → 候选职位宽口径入 SQLite；周日附加全量在架对账，识别下架（`closed_at`）。
2. **enrich（每日 03:10 SGT）**：对新增/描述变更职位做技术栈抽取——规则层（`tech_taxonomy` + 正则）永远先跑，LLM 层（tailnet 直连本地模型）补充且允许失败，输出归一后写 `job_tech`，无法归一的落 `unmapped_tech`。
3. **report（周一 09:00 SGT）**：物化周度聚合表 `weekly_metric` → 渲染自包含 HTML + Markdown 周报（7 节，数字来自 `internal/metric`）→ 推 Telegram 摘要 + 链接。
4. **web（常驻）**：只读打开 SQLite，现算求职者统计页（`/` `/tech` `/pay` `/companies`，60s 缓存）与运维视图（`/ops`、`/ops/{date}`），托管周报归档（`/reports`、`/w/{YYYY-Www}`），暴露 `/healthz`、`/robots.txt`；`/metrics` 仅在独立 9090 端口（集群内抓取）。`/daily` 与 `/daily/{date}` 301 → `/ops`。

技术栈：Go 1.26 单静态二进制、标准库 `net/http`、`modernc.org/sqlite`（纯 Go 免 CGO）、`scratch`/`distroless` 镜像、k8s CronJob + Deployment；通过 Cloudflare Tunnel 暴露于 `jobs.meirong.dev`。

## User Stories

### 采集（ingest）

1. As 维护者, I want 系统每天自动拉取 MCF 新增职位, so that 数据保持新鲜、日批即可（无需实时监控）。
2. As 维护者, I want 拉取时按抓到什么归档什么（**全类目**，先于任何过滤/解析）, so that 下架后 API 不再返回的原始数据成为唯一可重建资产。
3. As 维护者, I want 归档先于入库, so that API schema 变更时可以从 raw 回放重建 DB，解析逻辑随便改。
4. As 维护者, I want 入库只收宽口径候选职位（`is_candidate`）, so that DB 体积可控且口径可解释。
5. As 维护者, I want 每天限速 1.5s/请求、单线程分页, so that 负载远低于普通用户浏览、对公开服务友好。
6. As 维护者, I want 429/5xx 指数退避重试（3 次 2/4/8s）, so that 瞬时故障不中断整轮采集。
7. As 维护者, I want 超过 300 页（约 3 个工作日量）时熔断并标记 `status='partial'`, so that 节假日补录高峰不会静默丢数据，也不会无限跑下去。
8. As 维护者, I want `User-Agent` 透明声明身份与项目地址, so that 访问合规、不伪装浏览器。
9. As 维护者, I want 首跑（watermark 为空）自动执行一次全量基线扫描 + 全类目归档在架快照, so that 有历史可比起点、周报从第一个完整 ISO 周起算。
10. As 维护者, I want 每轮运行记录 `ingest_run`（页数/新/更新/关闭/错误/watermark/status）, so that 数据新鲜度与错误可观测、可回溯。

### 去重与生命周期（dedup & lifecycle）

11. As 维护者, I want 同一 MCF `uuid` 的刷新只做 UPDATE 不新增行, so that 同职位反复出现不虚增新增量。
12. As 维护者, I want 刷新/重贴/改版按 `original_posting_date` 归属统计周, so that 日增量灌水不污染"新增"指标。
13. As 维护者, I want 复制成新 uuid 且指纹命中时归组到 canonical（`job_repost`）, so that 重贴不算新职位。
14. As 维护者, I want reopen（`closed_at` 清 NULL）不产生新增, so that 复活/重贴职位不计入新增量。
15. As 维护者, I want 每周全量对账识别已下架职位并写 `closed_at`, so that "在架活跃量"指标可信。
16. As 维护者, I want 关闭判定**仅在本轮扫描 status='success' 时执行**, so that partial 扫描不会批量误关职位。
17. As 维护者, I want 未到期但连续两周未见（`miss_count >= 2`）才关闭, so that 22 分钟扫描窗内的下架竞态不会造成系统性误标。
18. As 维护者, I want 对账页数与 API total 偏差 ≥2% 或熔断时标记 partial, so that 对账质量可审计。
19. As 维护者, I want 对账顺带刷新 `view_count` / `application_count` / `last_seen_at`, so that 需求侧信号保持最新。

### 分类口径（classification）

20. As 维护者, I want 角色分类（`role_family`）以官方 `ssoc_code` 映射表为主判 + 标题关键词修正, so that 分类可解释、可回溯。
21. As 维护者, I want `is_candidate`（宽）与 `is_swe`（严）两级谓词分离, so that 口径收紧只需重算 `is_swe`，不需要重灌数据。
22. As 维护者, I want 三层判定（SSOC 主判 / 类目辅判 / 标题兜底）顺序优先并记录命中层级, so that 口径调整时可定位到具体规则。
23. As 维护者, I want `ssoc_taxonomy` 由 Phase 0 实测采样人工核定（先测量再定义）, so that 分类口径不靠猜测硬编码。
24. As 维护者, I want 资历（`seniority`）由 `position_level` + `min_years_exp` + 标题三者投票、冲突时以标题为准, so that 贴近实际招聘信号。
25. As 维护者, I want `work_mode` 由 `flexibleWorkArrangements` 推导、空值标注为推断 Onsite, so that Remote/Hybrid/Onsite 占比可统计且诚实。
26. As 维护者, I want `company_type` 由 `ssic_code` + `employee_count` + 规则表推导, so that 公司类型分布（MNC/Startup/Bank 等）可统计。

### 技术栈富化（enrich）

27. As 维护者, I want 规则层永远先跑（`tech_taxonomy` 别名表 + 词边界正则）, so that 高频确定项零成本、可复现。
28. As 维护者, I want LLM 层只对 title + 去 HTML 描述（截断 4000 字符）抽取技术栈并返回严格 JSON, so that 输出结构化、可归一。
29. As 维护者, I want LLM 输出经 `tech_taxonomy` 归一后写 `job_tech`、无法归一的落 `unmapped_tech`, so that 分类体系有持续演进入口。
30. As 维护者, I want enrich 按 `description_sha256 + model + prompt_version` 缓存, so that 重贴/未变职位不重复推理、省钱省时。
31. As 维护者, I want enrich fail-open：LLM 端点不可达/401/DGX 关机时保留规则层结果、标 partial、退出码 0, so that 不触发告警风暴、不阻塞数据完整性。
32. As 维护者, I want 模型降级链按 `LLM_MODELS` 顺序依次尝试、全失败退回纯规则, so that 单个模型故障不影响整体可用性。
33. As 维护者, I want 每轮记录 `llm_calls` / `llm_cached` / `errors` 与 backlog, so that LLM 长期不可用可被 `JobsSgEnrichBacklog` 告警发现。

### 指标与周报（report）

34. As 求职者, I want 周一生成含 7 个章节的周报（Snapshot / Technology / Pay / Getting in / Competition and listing length / Employers / About these numbers）, so that 一份报告覆盖热度、谁在招、技术趋势、薪资、门槛、竞争度与口径说明（spec §4.5 重排；原 Data Quality 收为页脚一行）。
35. As 维护者, I want 所有数字由 SQL 计算并直接渲染，LLM 不参与任何数字产生, so that 幻觉不污染指标。
36. As 维护者, I want 周为最小统计粒度（ISO 周、SGT）且历史快照保留, so that 跨周可比、支持时间序列。
37. As 维护者, I want 新增量按去重 canonical 职位数统计（刷新/重贴/reopen/跨源不虚增）, so that 趋势数字真实。
38. As 维护者, I want 薪资中位数只统计 `salary_hidden=0` 且 `salary_type='Monthly'` 的职位（其他币种/周期单独统计不混算）, so that 薪资统计可信。
39. As 求职者, I want 投递竞争度按日均归一化 `application_count / 在架天数` 计算（同时给 `views/day` 与投递/浏览转化率）, so that "竞争多激烈"不被岗位年龄干扰（spec §3.6；累计值快照、非趋势，页面如实标注）。
40. As 求职者, I want 周报页脚一行列出数据质量（ingest/enrich 状态、LLM 缓存命中率、未映射技术词数）并链到 `/ops`, so that 读者能判断这期数据可信度而不打断正文。
41. As 维护者, I want 口径变更时全量重算历史并在周报中标注变更点, so that 跨周可比性不因口径漂移被破坏。
42. As 维护者, I want 周报为自包含 HTML（内联 CSS + SVG，无外部资源）并同步 Markdown, so that 任意环境可打开、可归档。
43. As 维护者, I want 周一自动推 Telegram 摘要 + 链接（独立话题，不占用告警话题）, so that 运维告警与内容推送不混流。

### 展示（web 与求职者站点）

44. As 维护者, I want web 常驻、只读打开 SQLite（回滚日志 mode=ro）, so that 读写互不阻塞、重启不丢状态。
45. As 求职者, I want `/` `/tech` `/pay` `/companies` 现算统计（60s 缓存）、`/reports` 与 `/w/{YYYY-Www}` 显示最新/历史周报归档、`/healthz` 健康检查、`/metrics` Prometheus 指标（独立 9090 端口，集群外 404）, so that 可浏览、可监控。
45b. As 运维者, I want `/ops` 显示每日采集明细统计（按 SGT 日聚合的运行计数与入库量）、`/ops/{YYYY-MM-DD}` 下钻单日，`/daily` 与 `/daily/{date}` 301 到对应 `/ops` 路径, so that 不必翻 kubectl logs 或 Grafana 就能确认「昨天到底爬到了什么、有没有漏跑」；页面请求时现算，ingest 一结束即最新。
46. As 运维者, I want web 不做认证（内容为公开就业市场统计、无个人数据）, so that 部署简单、可直接公开访问。
46b. As 求职者, I want `/` 回答「现在热不热」：在架量、周新增、WoW、12 周趋势、方向/资历/工作模式/雇佣类型分布、入门岗绝对数（现算，60s 缓存）, so that 一眼判断市场热度。
46c. As 求职者, I want `/tech` 回答「学什么最划算」：技术需求排名、动量（pp）、薪资溢价、入门友好度、MCF 原生技能标签, so that 选技术栈有依据。
46d. As 求职者, I want `/pay` 回答「我值多少」：分位数网格（资历×方向）、经验阶梯、薪资透明率，每处标注样本量, so that 报价谈判有数据。
46e. As 求职者, I want `/companies` 回答「投谁」：持续招聘者、公司类型分布、各家竞争度与透明率、岗位寿命（右删失标注）、幽灵岗信号, so that 投递目标有取舍。
46f. As 求职者, I want `?exp=` 与 `?role=` 镜头贯穿全部统计页、进 URL 可分享，数字按镜头重算，非法值返回 400, so that 可按经验档/方向自取所需。
46g. As 求职者, I want 数据不足是一等状态：样本不足渲染 `—(n=…)` 或「需 N 周历史」而非 0, so that 上线头几周页面不被误读为坏。
46h. As 求职者, I want 每个对外页面标注数据延迟（最长 24h）、周报注明所覆盖周次, so that 不被误读为实时职位源（合规红线，docs/01 §5）。
46i. As 运维者, I want 周报与统计页共用 `internal/metric` 一份口径, so that 周报与实时页不可能对「这周发生了什么」给出不同答案。

### 可观测性与运维（ops）

47. As 维护者, I want 5 条关键告警（IngestStale / ReconcileStale / IngestErrors / EnrichBacklog / CronJobFailed）接入现有 Alertmanager→Telegram, so that 静默失效（比崩溃更危险）能被及时感知。
48. As 维护者, I want 结构化 JSON 日志到 stdout（经现有 OTel→Loki）, so that 排查零额外配置。
49. As 维护者, I want jobs.db + 归档目录 `raw/` 纳入 restic 备份并做恢复演练, so that 不可重建的归档数据不丢。
50. As 维护者, I want 三个 CronJob 全部 `concurrencyPolicy: Forbid` + `activeDeadlineSeconds` + 历史 Job 上限, so that 卡死 Job 能自死、不重蹈 92-pod 泄漏事故。
51. As 维护者, I want 镜像多架构（amd64+arm64）并按 digest 固定、PSA restricted、非 root、只读根文件系统, so that 安全基线达标且保留 oracle 迁移出口。
52. As 维护者, I want 部署位置与迁移路径预先设计（homelab 默认、oracle 备选）, so that 将来内存告急可零代码改动迁移。

### 合规（compliance）

53. As 维护者, I want 只访问公开数据、不登录、不伪装浏览器, so that 访问合规（robots 全站放行 [已验证]）。
54. As 维护者, I want 不采集候选人/个人数据、不落库发布者个人字段, so that 隐私红线不越界。
55. As 维护者, I want 数据仅用于个人趋势分析、不转售、不分发原始数据集、周报只发聚合统计, so that 使用边界清晰。

### 多源化（Phase 3，本期不做）

56. As 维护者, I want Phase 3 接入 Greenhouse / Lever / Ashby 公开 JSON API 作为第二期来源, so that 覆盖率提升且成本可控。
57. As 维护者, I want 跨源归并保守（宁可漏并、勿误并）, so that 误并不会污染两个源的生命周期。
58. As 维护者, I want 跨源薪资归一（interval + currency）后才汇总, so that 薪资口径不混算。
59. As 维护者, I want LinkedIn/Indeed 默认不做、只做交叉验证/补漏, so that ToS/反爬风险不进入核心口径。

## Implementation Decisions

### 模块划分（计划构建）

| 模块 | 形态 | 关键接口/行为 | 可独立测试性 |
|---|---|---|---|
| `ingest`（cmd/ingest） | CronJob，每日 02:15 SGT | 分页拉取 → 归档 → upsert；周日附加全量对账 + closed_at 生命周期 | 高：fetch 层与 reconcile 层解耦，输入 fixture 可回放 |
| `enrich`（cmd/enrich） | CronJob，每日 03:10 SGT | 规则层 + LLM 层技术栈抽取，缓存、降级、fail-open | 高：LLM client 为接口，可注入 stub |
| `report`（cmd/report） | CronJob，周一 09:00 SGT | 物化 weekly_metric → 渲染 7 节 HTML/MD → 推 Telegram | 高：数字来自 internal/metric，渲染纯函数化 |
| `web`（cmd/web） | Deployment×1 常驻 | 现算求职者统计页（/ /tech /pay /companies，60s 缓存）+ `/ops` 运维视图 + 周报归档（/reports /w/{week}）+ `/metrics`（独立 9090）+ `/healthz` | 高：只读 DB + 无状态 HTTP |
| `internal/metric`（纯聚合层） | 包 | 站点与周报共用指标：market/tech/pay/company/lifecycle/coverage/lens/window | 高：表驱动 SQL 测试，fixture 回放 |
| `internal/view`（共享视觉层） | 包 | baseCSS/SVG 图表/抑制值渲染（`—(n=…)`）、镜头导航 | 高：渲染纯函数化 |
| `internal/`（共享库） | 包 | schema/db 访问、分类（is_candidate/is_swe/role_family/seniority/work_mode/company_type）、tech_taxonomy 归一、去重、指标 SQL | 中：核心谓词可单测 |

### 关键决策

- **采集**：Go 标准库 `net/http` 直连 MCF 公开 JSON API，免代理/免浏览器。限速 1.5s/请求、单线程；`MAX_PAGES=300` 熔断 → `status='partial'`；429/5xx 指数退避 3 次。
- **归档优先**：`raw/YYYY-MM-DD/*.jsonl.gz` 全类目归档**先于**任何过滤/解析；DB 只存 `description_sha256`，正文按 `raw_path` 回读（DB 压到 1/7）。
- **去重/生命周期**：`uuid` 精确主键；reopen 清 `closed_at=NULL`；`miss_count>=2` 且 success 门控才关闭；对账页数偏差 ≥2% → partial。
- **分类**：`is_candidate`（宽）/ `is_swe`（严）两级；三层判定（SSOC → 类目 → 标题）记录命中层级；`ssoc_taxonomy` 为 Phase 0 人工核定交付物。
- **富化**：规则层永远先跑；LLM 只输出严格 JSON（`languages/frameworks/cloud/databases/tools/ai`）；`enrich_cache[sha256, model, prompt_version]`；并发 3、超时 60s、重试 1 次；fail-open + 降级链。
- **指标**：所有数字 SQL 计算；`weekly_metric` 长表（新增指标不改 schema）；口径变更全量重算历史并标注；统计页与周报共用 `internal/metric` 一份口径；数据不足是一等状态（抑制门槛集中常量，spec §5）。
- **展示**：`/` `/tech` `/pay` `/companies` 现算 + 60s 缓存，镜头 `?exp=`/`?role=` 白名单校验（非法值 400）、缓存键含镜头；`/reports` 与 `/w/{week}` 静态周报归档；`/ops` 运维视图（`/daily` 301）；`/metrics` 独立 9090 端口；web 只读、不做认证。
- **口径修正（spec §3.7，已落地）**：经验 `0`/未标注分档；竞争度改日均归一化；技术频次分母为**已富化**岗位；薪资改分位阶梯 + 样本披露；岗位寿命新增（右删失标注）；动量新增（pp + 最小计数门槛）。
- **存储**：SQLite 回滚日志 + `busy_timeout=10000` + `synchronous=NORMAL`；PVC local-path 10Gi；web 只读（`mode=ro`）。
- **调度**：`concurrencyPolicy: Forbid`、`backoffLimit: 2`、`activeDeadlineSeconds`（ingest/enrich 3600 / report 1800）、历史 Job 上限 3/1。
- **部署**：`k3s-homelab` ns `jobs-sg`；manifests 落 `meirongdev/homelab` 仓库（绕开 sourceRepos 白名单 + AppProject 非 GitOps 管理）；`ReferenceGrant` 用 `v1beta1`；HTTPRoute `parentRefs.port: 80`；ServiceMonitor/PrometheusRule 带 `release: kube-prometheus-stack` 标签；镜像按 digest 固定。
- **可观测**：指标从 `ingest_run` / `job` 表现算（状态在 DB 不在进程内）；5 条告警规则；日志走 stdout → OTel → Loki（Phase 1 不接 Tempo）。
- **备份**：jobs.db + `raw/` 纳入 restic；PVC `Prune=false`；恢复演练（`PRAGMA integrity_check`）是 Phase 2 DoD。
- **安全**：PSA restricted；非 root（uid 10001）、drop ALL caps、seccomp RuntimeDefault、只读根文件系统；密钥走 Vault → ESO，不进 git。
- **合规**：UA 透明；不落库个人字段；不转售；web 不做认证。

## Testing Decisions

**好的测试定义**：只测外部可观察行为（给定输入 → 期望输出/副作用），不测实现细节。对批作业尤其如此——它们是幂等、可回放的计算，fixture 驱动是天然匹配。

### 将测试的模块与手段

| 模块 | 测试内容 | 手段 |
|---|---|---|
| 归一化/口径 | `is_candidate` / `is_swe` / `role_family` / `seniority` / `work_mode` / `company_type` / tech 归一 | fixture 回放测试（360 条跨 6 个完整 ISO 周，含已下架与 `min_years_exp` NULL）——口径可信的前提 |
| 去重与生命周期 | 同 uuid 刷新 / 内容小改 / 复制重贴（fingerprint）/ reopen / `miss_count` 两周关闭 / success 门控 | 构造 fixture 序列回放，断言 `ingest_run` 计数与 `job` 状态 |
| ingest | 分页、限速、退避重试、熔断 partial、归档先于解析、首跑基线 | fetch 层 mock + 归档目录断言 |
| enrich | 规则层命中、LLM JSON 归一、无法归一落 `unmapped_tech`、缓存命中/未命中、fail-open 退出码 0、降级链 | LLM client 接口 stub |
| report | 7 节周报渲染、Telegram 推送（求职者口播）、`weekly_metric` 物化（new_jobs/active_jobs/tech_freq/salary_median/tech_share/swe_enriched） | 固定 SQLite fixture → 断言 weekly_metric 与 HTML/MD 输出 |
| 求职者统计指标 | 动量 pp/当周排除/历史不足、溢价样本抑制、分位最近秩、透明率双谓词、寿命右删失、竞争度日均、已富化分母、抑制边界 n=4/n=5 | `internal/metric` 表驱动 SQL 测试 + fixture 回放 |
| web | 统计页路由与镜头白名单（合法 200 / 非法 400 / 缓存键含镜头）、`/daily` 301 `/ops`、`/w/{week}` 404、只读、`/metrics` 仅 9090 | httptest + 只读 DB |

### 既有先例

- 代码已实现（ingest/enrich/report/web + `internal/metric` + `internal/view`），fixture 360 条跨 6 个完整 ISO 周（含已下架与 `min_years_exp` NULL 样本），归一化/口径/指标抑制均有回放测试。Phase 1 DoD 的"真实 API 实录替换"**未完成**（当前仍是 `genfixture` 生成样例，见 [05-roadmap](05-roadmap.md)）。
- 归档回放（raw → DB 重建）本身就是一种端到端测试通道。

## Out of Scope

- 实时监控（日批足够）；候选人匹配、简历分析、人岗推荐。
- SG 以外地区。
- 全源覆盖率（MCF 主源口径一致性优先于跨源大而全）。
- LinkedIn / Indeed 抓取（默认不做；仅未来做交叉验证/补漏）。
- 向量库 / pgvector / embedding（Bifrost 两个 provider 均 `embedding: false`）。
- PostgreSQL / 分布式事务 / Saga（SQLite 单写者 + 时间表错开已足够）。
- web 认证（公开统计无个人数据；未来可按需加 oauth2-proxy）。
- `/jobs` 可浏览职位列表（Phase B 缓做，前置：MCF 回链格式验证，见 spec §6）。
- `job_stats_snapshot` 需求侧轨迹（Phase 2+ 按需，默认不建）。
- Tempo 分布式追踪（Phase 1 不接）。

## Further Notes

- **成本**：外部支出 $0（公开 API + 本地 LLM + 现有平台）。
- **资源**：常驻 ≤64Mi（web requests 64Mi）、批处理峰值 ≤512Mi。
- **数据规模**：年增 ~145k 候选行入库、~1.5GB gzip 归档、DB ~150MB/年；PVC 10Gi 够 5 年。
- **风险核心**：MCF API 未承诺稳定 → 归档优先 + sitemap 校验 + 告警；LLM 故障 → fail-open + 降级；全量对账竞态 → success 门控 + 两周判定 + reopen。
- **交付节奏**：Phase 0（测量+映射表）→ Phase 1（MVP+fixture 测试）→ Phase 2（生产化）→ Phase 3（多源等），见 [05-roadmap](05-roadmap.md)。
