# 系统设计

> 需求与指标定义见 [01-requirements](01-requirements.md)；schema 与分类口径见 [03-data-model](03-data-model.md)；部署与运维见 [04-operations](04-operations.md)。
> 本文所有 **[已验证]** 事实的实测证据见 [archive/2026-08-02-site-survey](archive/2026-08-02-site-survey.md)。
> **v2.1 变更**：全量对账 closed_at 判定重写（§4.1：success 门控 + 两周未见 + reopen）、日增量归档改全类目（§4.1）、首跑基线策略（§4.1）。

---

## 0. TL;DR — 三个实测事实决定整个设计

1. **MyCareersFuture 有免鉴权公开 JSON API。[已验证]**
   `GET https://api.mycareersfuture.gov.sg/v2/jobs?limit=100&page=N&sortBy=new_posting_date`
   返回**完整职位对象**（含 description / skills / salary / 公司 UEN / SSOC 职业码 / 浏览量 / 投递数），列表项与详情项字段完全一致（无需二次抓取）。`robots.txt` 全站放行。
   → **Playwright / Scrapy / 住宅代理 / UA 轮换全部不需要。**

2. **官方数据已自带大部分"分类"字段。**
   `ssocCode`、`positionLevels`、`minimumYearsExperience`、`salary.{min,max,type}`、`postedCompany.uen`、`skills[]` 全部结构化。
   → 去重用 uuid/UEN **精确键**；角色/资历/薪资走官方字段；LLM 只负责一件事——从 JD 正文抽取**技术栈**。

3. **homelab 是内存受限节点。[已验证]**（实测 9095Mi/11918Mi = 78%）
   → 不引入 Airflow/Prefect/Dagster（常驻 1–2GB），用 k8s CronJob；不引入 PostgreSQL 服务端，用 SQLite。
   常驻占用目标 **≤64Mi**，批处理峰值 **≤512Mi**。

与 v1 方案的差异（v1 已[归档](archive/2026-08-02-v1-architecture.md)）：

| 维度 | v1 方案 | 本设计 | 原因 |
|---|---|---|---|
| 采集 | Playwright + Scrapy + 代理轮换 | Go `net/http` 直连官方 JSON API | API 公开可用 [已验证]；省掉 ~1GB 内存与全部反爬工程 |
| 调度 | Prefect / Dagster | k8s CronJob | 2 个作业/天，不值 1–2GB 常驻 |
| 存储 | PostgreSQL + pgvector + S3/MinIO | SQLite(回滚日志) + PVC 上的 gzip JSON 归档 | 年增 ~145k 行，SQLite 绰绰有余；常驻内存 0 |
| 去重 | job_id + MinHash + embedding 相似度 | `uuid` / `jobPostId` / `UEN` 精确键 | 官方主键存在，模糊匹配无必要 |
| 向量库 | pgvector / Chroma | **不引入** | Bifrost 两个 provider 均 `embedding: false` [已验证] |
| LLM 分类 | Claude / GPT-4o（付费 API） | Bifrost → DGX Spark 本地模型 | 已有网关 + 本地推理，成本 $0 |
| 报告 | Markdown → PDF → Slack/Email | 静态 HTML（`jobs.meirong.dev`）+ Telegram | 复用现有 Cilium Gateway 与 Telegram 通道 |

---

## 1. 部署位置

**结论：`k3s-homelab`，namespace `jobs-sg`。**

| 判据 | homelab | oracle-k3s | 权重 |
|---|---|---|---|
| Bifrost LLM 网关 | 同集群 Service 直连 | 需绕公网或 ClusterMesh | **高** |
| Prometheus / ServiceMonitor | 原生 | 需改 otel-collector 配置 | 中 |
| 出口 IP 性质 | 住宅 IP | 日本机房 IP | 中（API 场景已降权） |
| CPU 架构 | amd64 | arm64，需多架构镜像 | 中 |
| 内存余量 | **紧张（78%）** | 宽裕（38%） | **高（反向）** |
| PostgreSQL | 无 | CNPG 现成 | 低（本设计不用 PG） |

内存是唯一指向 oracle 的强判据，而本设计已把常驻内存压到 ~64Mi（无 DB 服务端、无调度器、无浏览器），使该判据失效。LLM 网关同集群 + Prometheus 原生 + amd64 三项合力指向 homelab。

**迁移出口（预先设计，不事后重构）**：所有出集群依赖只有 Bifrost 与 Telegram，均通过环境变量注入。若日后 homelab 内存告急，迁 oracle 只需：
① 镜像已多架构（GH Actions `platforms` 含 arm64）；② `LLM_BASE_URL` 改 `https://llm.meirong.dev`；③ manifests 移到 `cloud/oracle/manifests/`，Gateway 名改 `oracle-gateway`；④ metrics 改 otel-collector 抓取。**无代码改动。**

---

## 2. 目标架构

```
                    ┌──────────────────────── k3s-homelab / ns: jobs-sg ────────────────────────┐
                    │                                                                            │
 api.mycareersfuture│  ┌────────────────┐   写      ┌──────────────────────────────┐            │
 .gov.sg  ──────────┼─▶│ CronJob ingest │──────────▶│ PVC jobs-sg-data (local-path)│            │
 (公开 JSON API)     │  │ 每日 02:15 SGT │           │  ├─ jobs.db      (SQLite)│            │
                    │  └────────────────┘           │  └─ raw/YYYY-MM-DD/*.jsonl.gz│            │
                    │                                └──────────────┬───────────────┘            │
                    │  ┌────────────────┐   读写                     │  只读                      │
 bifrost.bifrost.svc│◀─┤ CronJob enrich │◀──────────────────────────┤                            │
 :8080 (virtual key)│  │ 每日 03:10 SGT │                            │                            │
   ↓ tailnet        │  └────────────────┘                            │                            │
 DGX Spark / M2     │                                                │                            │
 (本地模型, $0)      │  ┌────────────────┐   读写                     │                            │
                    │  │ CronJob report │◀───────────────────────────┤                            │
                    │  │ 周一 09:00 SGT │───┐                        │                            │
                    │  └────────────────┘   │ 推送                    │                            │
                    │                       ▼                        │                            │
                    │                  Telegram Bot                  │                            │
                    │                                                │                            │
                    │  ┌──────────────────────────────┐  只读(mode=ro)│                            │
                    │  │ Deployment jobs-sg-web (1)   │◀──────────────┘                            │
                    │  │  /      现算统计页+周报归档     │                                            │
                    │  │  /metrics     Prometheus      │◀── ServiceMonitor (release=kube-prom...)  │
                    │  └───────────┬──────────────────┘                                            │
                    └──────────────┼─────────────────────────────────────────────────────────────┘
                                   │ HTTPRoute (parentRef port 80)
                                   ▼
                    Cilium Gateway (kube-system/homelab-gateway)
                                   ▼
                    Cloudflare Tunnel → jobs.meirong.dev
```

### 2.1 组件职责

| 组件 | 形态 | 频率 | 职责 |
|---|---|---|---|
| `ingest` | CronJob | 每日 02:15 SGT | 分页拉取新增职位 → 全类目归档 + 候选入 SQLite；周日额外做全量在架对账 |
| `enrich` | CronJob | 每日 03:10 SGT | 对新增/描述变更的职位调 LLM 抽技术栈；按描述哈希缓存 |
| `report` | CronJob | 周一 09:00 SGT | 物化周度聚合表 → 渲染 HTML + Markdown → 推 Telegram |
| `web` | Deployment×1 | 常驻 | 现算求职者统计页（`/` `/tech` `/pay` `/companies`，60s 缓存）+ `/ops` 运维视图 + 周报归档（`/reports` `/w/{week}`）+ `/metrics`（独立 9090）+ `/healthz` |

**为什么 ingest 与 enrich 分开**：LLM 是唯一可能长时间失败的外部依赖（DGX 关机、虚拟密钥过期）。分离后 enrich 失败不影响数据完整性——原始数据已落盘，enrich 是幂等可重放的补算作业。符合 homelab **fail-open** 硬约束。

---

## 3. 技术栈（Go）

> 语言取舍：实测最小 HTTP 服务常驻 RSS —— Go `net/http` **10.7MB** / Java 25 默认 61.9MB / Java 25 激进瘦身 18.3MB。Go 无 VM、单一静态二进制，直接命中 64Mi 常驻目标（余 ~6 倍），且保持多架构交叉编译。

| 层 | 选型 | 说明 |
|---|---|---|
| 语言 | **Go 1.26**，单静态二进制 | 无 VM / 无运行时 |
| HTTP 客户端 | 标准库 `net/http` | 直连 MCF / ATS JSON API |
| Web 服务 | 标准库 `net/http` | 无需框架；`/metrics` 用 `prometheus/client_golang` |
| SQLite | **`modernc.org/sqlite`**（纯 Go，免 CGO） | 保单二进制 + `GOOS/GOARCH` 交叉编译（§1 oracle 迁移出口） |
| 镜像 | `scratch` / `distroless` | ~8–10MB |
| 命令布局 | `cmd/ingest` `cmd/enrich` `cmd/report` `cmd/web` + `internal/` | 四命令一共享代码库 |

---

## 4. 组件设计

### 4.1 ingest（每日增量 + 每周全量对账）

**增量策略（每日）**

```
watermark = SELECT max(posting_date) FROM job    -- 首跑为 NULL → 走首跑基线（见下）
page = 0
while true:
    r = GET /v2/jobs?limit=100&page={page}&sortBy=new_posting_date
    archive(r.results)                           -- ① 全类目原样归档，先于任何过滤/解析
    for j in r.results:
        if j.metadata.newPostingDate < watermark - 2d:  stop   -- 2 天回溯窗，容忍乱序与补录
        if is_candidate(j): upsert(j)            -- ② DB 只收候选（宽口径，见 03 §4）
    page += 1
    sleep(1.5)                                   -- 固定限速，单线程
    if page > MAX_PAGES(=300): 熔断 → status='partial'
```

- **限速 1.5s/请求**。实测一个工作日发布量横跨 page 16–60+（≥3,000 条），叠加 2 天回溯窗 → 单次约 60–150 页，**2–4 分钟**。熔断阈值 300 页（≈3 个工作日的量）：设太紧会在节假日补录高峰**静默丢数据**，比多跑几分钟危险得多。触发熔断记 `status='partial'` 并计入 `jobs_sg_ingest_errors_total`。
- `User-Agent` 声明身份与联系方式（`jobs-sg-monitor/1.0 (+https://jobs.meirong.dev)`），不伪装浏览器——政府公开 API 且 robots 全放行，透明访问是正确做法。
- 重试：429/5xx 指数退避（3 次，2/4/8s）。最终失败会**中止本轮翻页**（`FetchPage` 返回 error → `EachPage` 中止），但**不使作业失败**：已抓数据全部保留，退出码 0。
- **状态口径（2026-08-07 收敛）**：一轮只要没能完整记录它抓到的东西，就记 `partial`，不记 `success`。触发条件包括页请求失败、熔断、对账覆盖率 <80%、**归档写入失败、upsert 失败、归档 Close（gzip flush）失败**。
  从前只有扫描中止会降级，归档/upsert 失败仅 `errors++` 而整轮仍报 success——那是最坏的组合：该职位既不在归档也不在 DB，watermark 靠邻居照常前进、增量再也不会回头取它，而 `jobs_sg_last_success_timestamp_seconds` 继续跳动，`JobsSgIngestStale` 一声不响。归档是唯一不可重建资产（§4.1），从它这里静默丢数据是最该吵的一种。
  单次瞬时错误的代价只是一晚：下一轮成功即自愈。持续失败（磁盘满、挂载坏）会让每轮都是 partial，直到告警响——这正是意图。附带收益：对账的关闭判定本就以 `status='success'` 为门控，归档失败因此自动不再触发关闭。
  > **覆盖率不足仍记 partial，但（2026-08-10 起）不再 `errors++`**：它是「本轮扫描不足以支撑*按缺席*关闭」的判断，不是故障。计进 `errors` 会流进 `jobs_sg_ingest_errors_total`，而那条告警的语义是「MCF 字段形状变了」—— 等于让一个谨慎但健康的夜晚和一个真坏了的夜晚长得一模一样。partial 扣住的只是 `full_reconcile` 的成功戳（`JobsSgReconcileStale` 照常盯得到）；`incremental` 的新鲜度戳照常前移，因为数据本身刚被全量刷新过。

> **归档策略（v2.1 修订）**：日增量**按抓到什么归档什么（全类目）**，`is_candidate` 只过滤入库，不过滤归档。
> 理由：①下架职位 API 不再返回，归档是**唯一不可重建资产**；②口径将来扩展（如纳入 Data Science）可从归档回放重建，无需重灌；③翻页本来就要过全量数据，带宽已付。
> 成本：~3,000 条/天 × 7.0KB → gzip 后 **~1.5GB/年**（容量核算见 [03](03-data-model.md) §3）。
> 反之，**周对账不归档完整对象**（86k 条 ≈ 600MB/周 → gz ~120MB，年增 ~6.3GB，会让 10Gi PVC 在约 15 个月写满而非 5 年），只刷新计数与 `last_seen_at`。
> **例外：对账遇到从未入库的职位仍要归档那一条**——归档是唯一不可重建资产，且 `enrich` 靠 `raw_path` 回读描述正文，对账首次发现的职位没有自己的归档副本就永远读不出正文、永久卡在 backlog。实现上对账开跑前载入一次已知 uuid 集合（`store.KnownUUIDs`），只对不在集合里的写归档；重新见到的职位传空 `raw_path`，`UpsertJob` 保留原值而非清空。

> **本条曾与实现不符（2026-08-07 修复）**：`ingest` 的归档 pass 对所有轮次无条件执行，对账因此每周重复归档整块在架数据。容量核算（[03](03-data-model.md) §3）与 restic 预算（[04](04-operations.md) §4）都建立在本条上，是部署前必须闭合的偏差。

> **首跑基线（v2.1 新增）**：`watermark` 为 NULL 时执行一次与周对账相同的全量扫描（≈867 页 / 22 分钟）：候选职位全部入库（`posting_date` 用官方值，保留历史信息），并全类目归档一份在架快照（一次性 ~120MB gz）。周报从上线后第一个完整 ISO 周起算，在架量图表注明基线日期。

**全量对账（每周日随 ingest 一并执行）**

目的：识别**已下架**职位（`closed_at`），支撑"在架活跃量"指标。

```
遍历 /v2/jobs 全量（total≈86,678 → ~867 页 @limit=100，1.5s 间隔 ≈ 22 分钟）
  → 收集全部在架 uuid 集合 S；覆盖率 scanned/max(total) < 80% 或触发熔断 → status='partial'
     -- 闸门读**覆盖率**（本轮走过的条数 / 本轮见过的最大 total），下限刻意放宽到 80%
     --（2026-08-10 二修）。API 的 total 是噪声源：实测一次完整扫描里它在
     -- 75255↔83003（约 9.3%）之间摆，而真截断从不含蓄——随机停在某页会丢掉
     -- 一大块板子。80% = 最差噪声的两倍余量，仍远高于任何值得暂停生命周期的
     -- 截断；放过去的零头（翻页漂移漏看、API 藏起的小块）恰好落进 miss_count
     -- 两周判定 + reopen 自愈的守备范围。
     -- 「与 total 的偏差」降级为纯遥测（对最后一页读数计算，进 /metrics）：
     -- 拿它当闸门先后翻过两次车——对 max 比（结构性偏高，2026-08-09 读出
     -- 假 4.5%），对最后一页比（凹陷正好落在末页就反向误跳）。
  → 重新见到：UPDATE job SET last_seen_at=now, miss_count=0, closed_at=NULL WHERE uuid IN S
       -- closed_at=NULL 即 reopen：重贴/复活不算新增（归属 original_posting_date，见 03 §6）
  → 关闭判定（v2.1：仅当本轮扫描干净才执行，partial 扫描严禁批量关闭）：
     -- 2026-08-10 起两个分支分开门控。扫描本身出错（页失败/熔断/归档/upsert）
     -- 仍然整块跳过；但**只是覆盖率不足**时，仅停掉「按缺席关闭」那一支：
     -- 缺席只有在扫全了的前提下才算「它没了」的证据，而 expiry_date 是 MCF
     -- 自己公布的下架日期，根本不是从缺席推出来的，照关。
     -- 停的是 miss_count++ 而不只是那句 close：否则连续两轮不可信的扫描
     -- 会累加成一次关闭。误关也能自愈 —— 再见到就 closed_at=NULL。
     -- 代价有多实在：2026-08-09 那一轮把整个关闭流程一起停了，于是 11580 条
     -- 在架职位一条没关，其中 2770 条早就过了 expiry_date。
     -- 覆盖率闸门跳闸的轮次（close_skipped）**数据本身是新鲜的**（对账 ⊇ 增量），
     -- 所以 /metrics 的 incremental last_success 照常前移（store.LastSuccess
     -- 对 errors=0 的对账行放行），JobsSgIngestStale 不再陪跑；但 full_reconcile
     -- 的成功戳仍扣住 —— 生命周期没对账就是没对账，JobsSgReconcileStale 照盯。
       -- 实现为单次 store.MissAndClose(seen, today)：两个分支共用同一个候选集，
       -- 且以精确的 seen 集合为键而非 last_seen_at 时间戳（NowUTC 只到秒，
       -- 同一秒内的两轮分辨不出来）。
       候选 = closed_at IS NULL AND last_seen_at < 本轮 started_at
       ├─ expiry_date < today  →  closed_at=now, miss_count=0     -- 到期下架，直接关
       └─ 未到期消失            →  miss_count += 1
            miss_count ≥ 2      →  closed_at=now                  -- 连续两周未见才关
```

> **为什么要"两周未见"**：22 分钟扫描窗内的下架会使未读页条目前移，产生系统性漏看（按 ~3k/天流动率估算每轮几十条）。一次未见就关会持续误标 `closed_at`；两周判定 + reopen 自愈把误差压到可忽略。未到期消失且急于出报告的周，可选对该小集合做单条 `GET /v2/jobs/{uuid}` 复核加速判定。

> **备选方案（不采用）**：sitemap 差分。6 个分片共 ~57MB 且不支持 gzip [已验证]，流量与全量分页相当，但分页复用同一套解析代码、且顺带刷新 `view_count`/`application_count`。sitemap 保留为**校验手段**：分页总数与 sitemap 数量偏离 >20% 时告警。

### 4.2 enrich（LLM 技术栈抽取）

**为什么还需要 LLM**：MCF 的 `skills[]` 是业务技能（实测样本："Liaising with cross functional teams"），**不是技术栈**。语言/框架/云/AI 工具只存在于 `description` 自由文本里。

**两级抽取**：

1. **规则层（先跑，永远跑）**：`tech_taxonomy` 别名表 + 词边界正则扫描。覆盖高频确定项（python/java/go/react/kubernetes/aws/…），零成本、可复现。
2. **LLM 层（补充，允许失败）**：title + 去 HTML 描述（截断 4000 字符）交给本地模型，要求返回严格 JSON：`{"languages":[],"frameworks":[],"cloud":[],"databases":[],"tools":[],"ai":[]}`。输出经 `tech_taxonomy` 归一后写 `job_tech(source='llm')`；无法归一的词落 `unmapped_tech` 供每周人工审阅——**这是分类体系持续演进的入口**（见 03 §7）。

**调用方式**（Bifrost，OpenAI 兼容）：

```
POST http://bifrost.bifrost.svc.cluster.local:8080/v1/chat/completions
Header: x-bf-vk: <virtual key, 来自 Vault>
Body:   {"model": "custom_dgx/deepseek-v4-flash", "messages":[...], "temperature": 0}
```

- **并发 3**，超时 60s/条，失败重试 1 次。~400 条/天 → 约 5–10 分钟。
- **缓存必做**：先查 `enrich_cache[description_sha256, model, prompt_version]`。`repostCount` 表明重贴常见，命中率预期高。
- **fail-open**：Bifrost 不可达 / 401 / DGX 关机 → 记 `errors`，保留规则层结果，`status='partial'`，**作业退出码 0**（不触发 CronJob 失败告警风暴），由 `JobsSgEnrichBacklog` 告警反映积压。
- **模型降级链**：`custom_dgx` → `custom_m2` → 纯规则。环境变量配置优先级列表。

### 4.3 report（周报）

周一 09:00 SGT 执行：

1. 物化 `weekly_metric`（覆盖上周一~周日，SGT）
2. 渲染 `report/YYYY-Www.html`（自包含单文件，内联 CSS + SVG 图表，无外部资源）与 `.md`
3. Telegram 推送摘要 + 链接（复用 Vault `secret/homelab/telegram` 的 bot token；**发独立话题，不占用告警话题 `messageThreadID: 2`**——运维告警与内容推送混流会稀释告警注意力）
4. 更新 `report/index.html` 与 `report/latest.html`

周报章节 → 数据来源映射（章节定义见 [01](01-requirements.md) §2.1）：

| 章节 | 数据来源（`internal/metric`，与实时页同一份口径） |
|---|---|
| 1. Snapshot | `metric.MarketReport`：新增量、环比、在架量、最忙方向 + 三个分布 |
| 2. Technology | `metric.TechReport`：需求排名 + 动量（pp，历史不足则说明） |
| 3. Pay | `metric.PayReport`：经验阶梯 p25/p50/p75 + 透明率；样本不足即抑制 |
| 4. Getting in | 入门岗**绝对数**（取代原 `no_exp_ratio`） |
| 5. Competition and listing length | `metric.CompanyReport`：日均投递/浏览 + 已下架岗位挂牌天数（标右删失） |
| 6. Employers | Top 雇主 + 类型分布 |
| 7. About these numbers | 一段方法说明（原 Data Quality 收为页脚一行 + 链 `/ops`） |

> **数字与解读分离**：所有数字由 SQL 计算（`internal/metric` 纯聚合层）并直接渲染；report **全程不调用 LLM**——Telegram 摘要也是从已算好的数字拼出的求职者口播（升温 Top 3 / 入门岗绝对数 / 各经验档薪资带 / 数据新鲜度）。LLM 幻觉不会污染指标。

### 4.4 web（常驻）

- 单进程 Go 二进制，标准库 `net/http`；只读打开 SQLite：`file:/data/jobs.db?mode=ro`
- 路由：

| 路由 | 内容 | 产出方式 |
|---|---|---|
| `/` | **求职者快报**：在架量、周新增、WoW、12 周趋势、方向/资历/工作模式/雇佣类型分布、入门岗绝对数 | 请求时从 DB 现算渲染（60s 缓存） |
| `/tech` | **技术趋势**：需求排名、动量（pp）、薪资溢价、入门友好度、MCF 原生技能标签 | 同上 |
| `/pay` | **薪资**：分位数网格、经验阶梯、透明率（每处标注样本量） | 同上 |
| `/companies` | **雇主**：持续招聘者、类型分布、各家竞争度与透明率、岗位寿命（右删失）、幽灵岗信号 | 同上 |
| `/reports` | 最新周报入口（尚无周报时渲染说明页） | `report` CronJob 预生成的静态 HTML |
| `/w/{YYYY-Www}` | 历史周报（不可变归档） | 同上 |
| `/ops` | **采集状态**：按 SGT 日聚合的运行明细表（kind/status/耗时/pages/归档/新增/SWE/更新/下架/errors/LLM）、日新增 SWE 柱状图、近 7 天技术栈 Top 15 | 请求时从 DB 现算渲染 |
| `/ops/{YYYY-MM-DD}` | 单日下钻：逐条 run 记录、当日 role_family/seniority 分布、当日技术栈、当日首见职位列表（上限 200 条） | 同上 |
| `/daily`、`/daily/{date}` | 301 → `/ops`、`/ops/{date}`（历史路径） | 301 重定向 |
| `/healthz` | 健康检查 | DB ping |
| `/robots.txt` | 放行索引、禁抓 `/ops/` | 静态 |
| `/metrics` | Prometheus 指标（**独立监听端口 9090，不在公网路由上**） | 从 `ingest_run` 与 `job` 表现算 |

- **两种产出方式、三层消费**：统计页与 `/ops` 是「请求一发生就要最新」的现算视图（60s 缓存，ingest 一跑完即可见，不依赖任何 CronJob 产物）；周报是「每周一次、需要推 Telegram、要可回溯归档」的产物，落静态文件只读托管。全站统计数字由 `internal/metric` 纯聚合层计算（spec §4.2），视觉由 `internal/view` 共享（baseCSS/SVG/抑制值渲染）——周报与实时页共用一份口径、一套样式，不会就「这周发生了什么」给出不同答案。
- **镜头**：`?exp=`/`?role=` 贯穿全部统计页，白名单校验（非法值 400），缓存键含镜头。**数据不足是一等状态**：样本不足渲染 `—(n=…)` 或「需 N 周历史」而非 0（spec §5）。
- **SGT 日历日分桶**：时间戳按 UTC 存储，而 02:15 SGT 的 ingest 落在前一天 18:15 UTC——按 UTC 日分组会把每次采集记到前一天。SQL 侧用 `date(col,'+8 hours')`，Go 侧用 `.In(sgt)`。
- 日页面窗口默认 30 天（`?days=` 可调，上限 90），并裁掉管线首次运行之前的空白日；活跃日之间的空缺**保留**，那是漏跑信号。
- `/metrics` 从 `ingest_run` 与 `job` 表现算（状态在 DB 不在进程内，重启无损）
- **不做认证**——内容是公开就业市场统计，无个人数据。若日后需要，按 bifrost 模式加 oauth2-proxy。
- **但 `/metrics` 不属于「内容」**：它暴露 enrich 积压深度、各作业耗时、累计错误数，是运维姿态而非就业数据。HTTPRoute 是 `PathPrefix: /`，因此挂在公共 mux 上的一切都等于挂在 `jobs.meirong.dev` 上。故 `/metrics` 绑到**独立监听端口 9090**（`--metrics-addr`），Service 开第二个 `metrics` 端口供 ServiceMonitor 集群内抓取，HTTPRoute 不指向它。
  选择拆监听端口而非在 Gateway 上加过滤：这样该性质由进程本身保证，日后改路由也重新暴露不了从未绑到公共监听器上的东西。两个监听器任一启动失败即整进程退出——只服务页面却静默丢掉 `/metrics` 的 Pod 看起来是健康的，而 [04](04-operations.md) §3.2 的每一条告警都会瞎掉。

---

## 5. 调度、资源与并发

### 5.1 CronJob 时间表（节点为 UTC，SGT = UTC+8）

| 作业 | UTC cron | SGT | 避让说明 |
|---|---|---|---|
| `ingest` | `15 18 * * *` | 02:15 | 避开 restic 备份 03:00 UTC、calibre 04:00 UTC |
| `enrich` | `10 19 * * *` | 03:10 | ingest 之后 55 分钟 |
| `report` | `0 1 * * 1` | 周一 09:00 | 周日全量对账（随周日 ingest）已完成 |

### 5.2 资源预算

| 组件 | requests (cpu/mem) | limits (cpu/mem) | 形态 |
|---|---|---|---|
| `ingest` | 100m / 128Mi | 500m / 384Mi | 瞬时 |
| `enrich` | 100m / 192Mi | 500m / 512Mi | 瞬时 |
| `report` | 100m / 128Mi | 500m / 384Mi | 瞬时（每周） |
| `web` | 25m / 64Mi | 200m / 192Mi | **常驻** |

- 常驻新增 64Mi requests（节点 requests 52% → 52.5%）；峰值 ≤512Mi（作业互不重叠，实测余量 ~2.8GB）。
- **有意偏离仓库"不设 cpu limit"惯例**：这些是批作业，被节流只是慢一点，而节点是过热笔记本（idle ~74°C）——限制 CPU 尖峰比作业快 30 秒更重要。

### 5.3 并发与一致性

- 三个 CronJob 全部 `concurrencyPolicy: Forbid` + `backoffLimit: 2` + `successfulJobsHistoryLimit: 3` / `failedJobsHistoryLimit: 1` + **`activeDeadlineSeconds`**（ingest 3600 / enrich 3600 / report 1800）——卡死的 Job 必须能自己死掉（92-pod 泄漏事故的根因，见 [04](04-operations.md) §2）。
- 作业间用 SQLite 事务 + `busy_timeout=10000`；时间表已错开，异常长跑时后者等待而非失败。
- `web` 只读连接回滚日志库，直接打开已提交数据；写入按时间表串行，无并发写者（见 [03](03-data-model.md) §1）。
- **RWO PVC 多 Pod 挂载依赖单节点**：单节点下同节点多 Pod 挂载同一 RWO PVC 可行（local-path 是 hostPath）。若集群加节点，必须给所有 jobs-sg 工作负载加 `nodeSelector`/`nodeAffinity` 钉到同一节点——现在就把注释写进 manifests。

---

## 6. 风险与取舍

| 风险 | 影响 | 缓解 |
|---|---|---|
| **MCF API 未公开承诺稳定**（可能加鉴权/改 schema） | 采集中断 | ① `JobsSgIngestErrors` 早期告警；② 原始 JSON 先归档后解析，schema 变更可回放重建；③ 解析层对未知字段宽容；④ sitemap 独立校验通道 |
| 全量对账分页竞态误关职位（v2.1 识别） | 在架量失真 | success 门控 + `miss_count` 两周判定 + reopen 自愈（§4.1） |
| 单节点 + RWO PVC | 扩节点后 Pod Pending | manifests 预写 `nodeAffinity` 注释，扩节点时启用 |
| SQLite 单写者 | 作业重叠时阻塞 | `Forbid` + 错开时间 + `busy_timeout` |
| DGX Spark 关机 | LLM 富化停摆 | fail-open + 降级 `custom_m2` + 纯规则；`JobsSgEnrichBacklog` 反映积压 |
| homelab 内存进一步吃紧 | 作业被 OOM kill | 已设 limits；§1 迁移出口保持可用（多架构镜像 + 环境变量化依赖） |
| LLM 幻觉污染指标 | 趋势数字失真 | 数字全由 SQL 算；LLM 只写解读段落且只喂算好的数 |
| 分类口径漂移 | 跨周不可比 | 口径写进代码注释 + `weekly_metric` 保留 `computed_at`；口径变更后全量重算历史 |
| 归档数据丢失 | **不可重建**（API 不返回已下架职位） | 归档目录纳入 restic，最高优先级备份对象（[04](04-operations.md) §4） |
| 多源化新增风险 | ATS 改 schema/加鉴权；跨源误合并 | 每源独立指标与告警；归并保守（宁可多留行勿误并）；跨源指纹动工前重审（[06](06-multi-source.md) §3） |
