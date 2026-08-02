# 新加坡 SWE 职位监控系统 — k3s 部署系统设计

> 上游需求文档：[`Singapore_SWE_Job_Monitoring_Architecture.md`](./Singapore_SWE_Job_Monitoring_Architecture.md)
> 目标集群：`k3s-homelab`（`~/projects/homelab`，ArgoCD GitOps）
> 日期：2026-08-02 ｜ 状态：已评审，含 v2 扩展（§15 多源化/跨源去重/选择策略）；技术栈定为 **Go**（§3.2）
> 本文中标注 **[已验证]** 的事实均由当日实测得到，命令见 [附录 A](#附录-a验证证据)。
> **v2 增量**：多数据源（ATS 公开接口）、canonical 跨源/跨刷新去重、分析层选择策略、**Go 技术栈**。

---

## 0. TL;DR

三条结论决定了整个设计，都与上游架构方案有出入：

1. **MyCareersFuture 有可用的公开 JSON API，不需要爬虫。[已验证]**
   `GET https://api.mycareersfuture.gov.sg/v2/jobs?limit=100&page=N&sortBy=new_posting_date`
   免鉴权返回**完整职位对象**（含 description / skills / salary / 公司 UEN / SSOC 职业码 / 浏览量 / 投递数）。
   `robots.txt` 为 `Disallow:`（全站允许）+ 提供 sitemap。
   → **Playwright / Scrapy / 住宅代理 / UA 轮换 全部不需要**，Phase 1 直接删除。

2. **官方数据已自带大部分"分类"字段。**
   `ssocCode`（新加坡标准职业分类）、`positionLevels`、`minimumYearsExperience`、
   `salary.{min,max,type}`、`postedCompany.uen`（法人唯一码）、`skills[]` 全部结构化。
   → 上游方案里"LLM 做分类 + MinHash 去重"大幅收缩：**去重用 uuid/UEN 精确键**，
   **角色/资历/薪资走官方字段**，LLM 只负责一件事——从 JD 正文抽取**技术栈**。

3. **homelab 是内存受限节点，架构必须无常驻重组件。[已验证]**
   节点实测内存已用 **9095Mi / 11918Mi = 78%**，requests 52%，pod 47/110，磁盘余 42.7GB。
   → **不引入 Airflow/Prefect/Dagster（常驻 1–2GB）**，用 k8s CronJob；
   **不引入 PostgreSQL/pgvector 服务端**，用 SQLite（常驻内存 0）。
   常驻占用目标 **≤ 64Mi**，批处理峰值 **≤ 512Mi**。

与上游方案的差异汇总：

| 维度 | 上游架构方案 | 本设计 | 原因 |
|---|---|---|---|
| 采集 | Playwright + Scrapy + 代理轮换 | Go `net/http` 直连官方 JSON API | API 公开可用 [已验证]；省掉 ~1GB 内存与全部反爬工程 |
| 调度 | Prefect / Dagster | k8s CronJob | 2 个作业/天，不值 1–2GB 常驻 |
| 存储 | PostgreSQL + pgvector + S3/MinIO | SQLite(WAL) + PVC 上的 gzip JSON 归档 | 年增 ~145k 行 / ~1GB，SQLite 绰绰有余；常驻内存 0 |
| 去重 | job_id + MinHash + embedding 相似度 | `uuid` / `jobPostId` / `UEN` 精确键 | 官方主键存在，模糊匹配无必要 |
| 向量库 | pgvector / Chroma | **不引入** | Bifrost 两个 provider 均 `embedding: false` [已验证]，集群内无 embedding 端点 |
| LLM 分类 | Claude / GPT-4o（付费 API） | Bifrost → DGX Spark 本地模型 | 已有网关 + 本地推理，成本 $0 |
| 报告 | Markdown → PDF → Slack/Email | 静态 HTML（`jobs.meirong.dev`）+ Telegram | 复用现有 Cilium Gateway 与 Telegram 告警通道 |

---

## 1. 现场勘查结论（设计约束的事实基础）

### 1.1 数据源实测 [已验证]

| 探测项 | 结果 |
|---|---|
| `robots.txt` | `User-agent: *` / `Disallow:`（空 = 全站允许）；含 `sitemap-index.xml` |
| `POST /v2/jobs`（旧写法） | **401 Unauthorized** — 上游文档里流传的 POST search 已关闭 |
| `GET /v2/jobs?limit=100&page=0&sortBy=new_posting_date` | **200**，返回 `{results[], total, _links, countWithoutFilters}` |
| `GET /v2/jobs/{uuid}` | **200**，单条完整对象 |
| 列表项与详情项字段 | **完全一致**（列表已含 `description`）→ **无需二次详情抓取** |
| `limit` 上限 | **100**（`limit=200` → 400） |
| `categories=21` | **400** — 类目过滤参数格式不通，改为客户端过滤 |
| `search=software engineer` | 200，`total=902` — 全文检索可用 |
| 全站在架职位 `total` | **86,678**（2026-08-02） |
| 单条 JSON 体积 | **≈ 7.0 KB** |
| 日增量（按分页反查） | page0=08-02、page15=08-01、page30/60=07-31 → **工作日 ≥3,000/天** |
| IT 类目占比（page 0 抽样） | **13%** → 估算 IT 日增 ~400、在架 ~11,000 |
| sitemap | 6 个分片，`sitemap-1.xml` = 45,000 URL / 9.58MB，**不支持 gzip**，含 `<lastmod>` |
| sitemap URL 结构 | `/job/<category-slug>/<title-slug>-<uuid>` → 免解析即得类目与主键 |

**单条职位可用字段**（实测样本，非推测）：

```
uuid, metadata.jobPostId(MCF-2026-1162108), title, description(HTML)
ssocCode(5位), occupationId, ssocVersion          → 官方职业分类
positionLevels[].position, minimumYearsExperience → 资历
salary.{minimum,maximum,type}, metadata.isHideSalary → 薪资
employmentTypes[], categories[], schemes[], flexibleWorkArrangements[]
skills[].{skill,isKeySkill}                       → 官方技能标签
postedCompany.{uen,name,ssicCode,employeeCount}   → 公司唯一键 + 行业码
address.{postalCode,districts,lat,lng,isOverseas}
metadata.{newPostingDate,originalPostingDate,expiryDate,
          repostCount,totalNumberOfView,totalNumberJobApplication}
status.jobStatus, numberOfVacancies, screeningQuestions[]
```

> `totalNumberOfView` / `totalNumberJobApplication` 是**需求侧信号**——商业职位 API 通常不给。
> 值得作为本系统的差异化指标（见 §7.3）。

### 1.2 集群容量实测 [已验证]

| | `k3s-homelab` | `oracle-k3s` |
|---|---|---|
| 架构 | **amd64** | **arm64**（Ampere A1） |
| 位置 | 家宽住宅 IP（Proxmox 笔记本） | OCI **ap-osaka-1**（日本，机房 IP） |
| CPU | 8C（allocatable 7600m） | 4C |
| 内存 | 13.27GB（allocatable 11918Mi） | 24.55GB（allocatable 22453Mi） |
| 内存 requests / 实测 | 6118Mi (52%) / **9095Mi (78%)** | 4073Mi (18%) / 8417Mi (38%) |
| CPU requests / 实测 | 2722m (35%) / 1805m (23%) | 2005m (50%) / 302m (7%) |
| Pod | 47 / 110 | — |
| 磁盘 | 123.7GB，**余 42.7GB** | 203GB |
| StorageClass | 仅 `local-path` | 仅 `local-path` |
| PostgreSQL | 无 operator | **CNPG 就绪**（`zitadel-pg` 运行中） |
| Prometheus Operator | ✅ 原生（ServiceMonitor 可用） | ❌ 走 otel-collector |
| Vault / ESO | ✅ 本体在此 | ESO 有，`vault-backend` 跨 Tailscale 可用 |
| Bifrost LLM 网关 | ✅ 本体在此 | ❌ |

### 1.3 Bifrost LLM 网关现状 [已验证]

集群内 `bifrost.bifrost.svc.cluster.local:8080`，两个 **keyless** 自定义 OpenAI provider：

| provider | base_url | 说明 |
|---|---|---|
| `custom_dgx` | `http://100.97.87.120:8000` | DGX Spark，vLLM |
| `custom_m2` | `http://100.89.15.120:8000` | MacBook 本地推理 |

- 两者均 `chat_completion: true` / `chat_completion_stream: true`
- 两者均 **`embedding: false`** → **集群内当前没有 embedding 端点**，这是不引入向量库的直接依据
- 网关开了 `enforce_auth_on_inference` + governance routing rule，**集群内调用同样需要 virtual key**
  （governance PreHook 在 ingress 之前，不区分来源）

---

## 2. 部署位置决策

**结论：部署到 `k3s-homelab`，namespace `jobs-sg`。**

| 判据 | homelab | oracle-k3s | 权重 |
|---|---|---|---|
| Bifrost LLM 网关 | 同集群 Service 直连 | 需绕公网 `llm.meirong.dev` 或 ClusterMesh | **高** |
| Prometheus / ServiceMonitor | 原生 | 需改 otel-collector ConfigMap + 手动 rollout | 中 |
| 出口 IP 性质 | 住宅 IP | 日本机房 IP（更易被风控） | 中（API 场景下已降权） |
| CPU 架构 | amd64，镜像无风险 | arm64，需多架构构建 | 中 |
| 内存余量 | **紧张（78%）** | 宽裕（38%） | **高（反向）** |
| PostgreSQL | 无 | CNPG 现成 | 低（本设计不用 PG） |

内存是唯一指向 oracle 的强判据，而**本设计已把常驻内存压到 ~64Mi**（无 DB 服务端、无调度器、
无浏览器），使该判据失效。LLM 网关同集群 + Prometheus 原生 + amd64 三项合力指向 homelab。

**迁移出口（预先设计好，不要事后重构）**：所有出集群依赖只有两个——Bifrost 与 Telegram，
均通过环境变量注入 URL。若日后 homelab 内存告急，迁 oracle 只需：
① 镜像加 `linux/arm64`（GH Actions 里加一行 platforms）；
② `LLM_BASE_URL` 改为 `https://llm.meirong.dev`；
③ manifests 移到 `cloud/oracle/manifests/personal-services/`，HTTPRoute parentRef 端口 80→80、
Gateway 名改 `oracle-gateway`；④ metrics 改由 otel-collector 抓取。**无代码改动。**

---

## 3. 目标架构

```
                    ┌──────────────────────── k3s-homelab / ns: jobs-sg ────────────────────────┐
                    │                                                                            │
 api.mycareersfuture│  ┌────────────────┐   写      ┌──────────────────────────────┐            │
 .gov.sg  ──────────┼─▶│ CronJob ingest │──────────▶│ PVC jobs-sg-data (local-path)│            │
 (公开 JSON API)     │  │ 每日 02:15 SGT │           │  ├─ jobs.db      (SQLite/WAL)│            │
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
                    │  │  /            周报 HTML       │                                            │
                    │  │  /metrics     Prometheus      │◀── ServiceMonitor (release=kube-prom...)  │
                    │  └───────────┬──────────────────┘                                            │
                    └──────────────┼─────────────────────────────────────────────────────────────┘
                                   │ HTTPRoute (parentRef port 80)
                                   ▼
                    Cilium Gateway (kube-system/homelab-gateway)
                                   ▼
                    Cloudflare Tunnel → jobs.meirong.dev
```

### 3.1 组件职责

| 组件 | 形态 | 频率 | 职责 |
|---|---|---|---|
| `ingest` | CronJob | 每日 02:15 SGT | 分页拉取新增职位 → 落 SQLite + 原始 JSONL.gz 归档；周日额外做全量在架对账 |
| `enrich` | CronJob | 每日 03:10 SGT | 对新增/描述变更的职位调 LLM 抽技术栈；按描述哈希缓存 |
| `report` | CronJob | 周一 09:00 SGT | 物化周度聚合表 → 渲染 HTML + Markdown → 推 Telegram |
| `web` | Deployment×1 | 常驻 | 静态托管周报 + `/metrics` + `/healthz` |

**为什么 ingest 与 enrich 分开**：LLM 是唯一可能长时间失败的外部依赖（DGX 关机、
虚拟密钥过期）。分离后 enrich 失败不影响数据完整性——原始数据已落盘，
enrich 是幂等可重放的补算作业。这符合 homelab **fail-open** 硬约束。

### 3.2 技术栈（Go）

> 语言取舍结论：**Go**。实测对比（最小 HTTP 服务常驻 RSS）：Go `net/http` **10.7MB**
> / Java 25 默认 61.9MB / Java 25 激进瘦身 18.3MB。Go 无 VM、单一静态二进制，
> 直接命中 64Mi 常驻目标（余 ~6 倍），并保持多架交叉编译。

| 层 | 选型 | 说明 |
|---|---|---|
| 语言 | **Go 1.26**，单静态二进制 | 无 VM/无运行时；`web` 实测常驻 ~10.7MB RSS |
| HTTP 客户端 | 标准库 `net/http` | 替代 httpx，直连 MCF/ATS JSON API |
| Web 服务 | 标准库 `net/http` | 无需 web 框架；`/metrics` 用 `prometheus/client_golang` |
| SQLite | **`modernc.org/sqlite`**（纯 Go，免 CGO） | 保单二进制 + `GOOS/GOARCH` 多架交叉编译（§2 oracle 出口） |
| 镜像 | `scratch` / `distroless` | ~8–10MB，边缘/受限节点最友好 |
| 命令布局 | `cmd/ingest` `cmd/enrich` `cmd/report` `cmd/web` + `internal/` | 三作业一共享代码库，k8s CronJob 直接执行 |

---

## 4. 数据模型

SQLite，WAL 模式，位于 `/data/jobs.db`。全部时间戳统一存 UTC ISO8601。

```sql
PRAGMA journal_mode = WAL;      -- 单写多读
PRAGMA busy_timeout = 10000;    -- 与 web 只读连接并发时避免 SQLITE_BUSY
PRAGMA synchronous = NORMAL;    -- local-path 本地盘，NORMAL 足够

-- ── 公司维度：UEN 是新加坡法人唯一码，天然主键，无需模糊匹配 ──────────────
CREATE TABLE company (
  uen             TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  name_normalized TEXT NOT NULL,       -- 仅用于展示聚合，不用于去重
  ssic_code       TEXT,                -- 新加坡标准行业分类 → 公司类型
  employee_count  INTEGER,
  company_type    TEXT,                -- MNC/Local Tech/Bank&FinTech/Startup/Gov/Consulting，由 ssic + 规则表推导
  first_seen_at   TEXT NOT NULL,
  last_seen_at    TEXT NOT NULL
);

-- ── 职位主表：一行一个 MCF 职位，SCD-lite（用区间而非每日快照）─────────────
CREATE TABLE job (
  uuid                TEXT PRIMARY KEY,      -- MCF uuid
  job_post_id         TEXT UNIQUE NOT NULL,  -- MCF-2026-1162108
  title               TEXT NOT NULL,
  description_sha256  TEXT NOT NULL,         -- 富化缓存键；重贴/改版检测
  source              TEXT NOT NULL DEFAULT 'mcf',  -- 来源: mcf|greenhouse|lever|ashby|...
  canonical_fp        TEXT,                 -- 跨源逻辑指纹=sha256(norm(title)+uen/domain+norm(desc)); 同一职位跨源归并
  company_uen         TEXT REFERENCES company(uen),
  -- 官方分类字段（无需推断）
  ssoc_code           TEXT,                  -- 5 位；251x = 软件与应用开发（ISCO-08 对齐）
  occupation_id       TEXT,
  category            TEXT,                  -- e.g. Information Technology
  position_level      TEXT,                  -- e.g. Professional / Manager
  employment_type     TEXT,                  -- Full Time / Contract / ...
  min_years_exp       INTEGER,
  salary_min          INTEGER,
  salary_max          INTEGER,
  salary_type         TEXT,                  -- Monthly / Annual / ...
  salary_hidden       INTEGER NOT NULL DEFAULT 0,
  vacancies           INTEGER,
  -- 派生分类（本系统计算，见 §7.1）
  role_family         TEXT,                  -- Backend/Frontend/Fullstack/SRE/Data/AI-ML/Mobile/Security/Platform
  seniority           TEXT,                  -- Intern/Junior/Mid/Senior/Staff+/Lead/Manager
  work_mode           TEXT,                  -- Onsite/Hybrid/Remote（由 flexibleWorkArrangements 推导）
  is_swe              INTEGER NOT NULL DEFAULT 0,  -- 是否纳入 SWE 口径（见 §7.1）
  -- 时间与生命周期
  posting_date        TEXT NOT NULL,         -- metadata.newPostingDate
  original_posting_date TEXT,
  expiry_date         TEXT,
  repost_count        INTEGER DEFAULT 0,
  status              TEXT,                  -- status.jobStatus
  first_seen_at       TEXT NOT NULL,         -- 本系统首次见到
  last_seen_at        TEXT NOT NULL,         -- 最近一次在对账中见到 → 推导"是否仍在架"
  closed_at           TEXT,                  -- 全量对账中消失的时刻
  -- 需求侧信号（差异化指标）
  view_count          INTEGER,
  application_count   INTEGER,
  -- 地理
  district            TEXT, postal_code TEXT, lat REAL, lng REAL, is_overseas INTEGER DEFAULT 0,
  raw_path            TEXT NOT NULL          -- raw/YYYY-MM-DD/xxx.jsonl.gz#offset
);
CREATE INDEX idx_job_posting_date ON job(posting_date);
CREATE INDEX idx_job_active       ON job(last_seen_at, closed_at);
CREATE INDEX idx_job_swe          ON job(is_swe, posting_date);
CREATE INDEX idx_job_company      ON job(company_uen);
CREATE INDEX idx_job_fp           ON job(canonical_fp);

-- ── 官方技能标签（MCF 自带，偏业务向）──────────────────────────────────
CREATE TABLE job_skill (
  job_uuid     TEXT NOT NULL REFERENCES job(uuid),
  skill        TEXT NOT NULL,
  is_key_skill INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (job_uuid, skill)
);

-- ── 技术栈（本系统抽取：规则 + LLM）────────────────────────────────────
CREATE TABLE job_tech (
  job_uuid  TEXT NOT NULL REFERENCES job(uuid),
  tech_slug TEXT NOT NULL,       -- 规范化 slug: python / kubernetes / aws / pytorch
  tech_kind TEXT NOT NULL,       -- language|framework|cloud|database|tool|ai
  source    TEXT NOT NULL,       -- rule | llm
  PRIMARY KEY (job_uuid, tech_slug)
);
CREATE TABLE tech_taxonomy (     -- 别名归一：golang→go, k8s→kubernetes, gcp→google-cloud
  alias TEXT PRIMARY KEY, tech_slug TEXT NOT NULL, tech_kind TEXT NOT NULL
);

-- ── SSOC → role_family 映射（先测量再定义，§7.1）───────────────────────
CREATE TABLE ssoc_taxonomy (
  ssoc_code   TEXT PRIMARY KEY,   -- 5 位
  role_family TEXT NOT NULL,      -- Backend/Frontend/.../Other-IT
  note        TEXT                -- 人工核定备注
);

-- ── 跨源/跨刷新去重：canonical 身份的证据与归组 ───────────────────────
CREATE TABLE job_source_xref (    -- 同一 canonical 职位在各来源的 id
  source      TEXT NOT NULL,      -- mcf|greenhouse|lever|ashby|...
  source_id   TEXT NOT NULL,      -- 该源自己的 id（MCF 为 uuid，GH 为 id，Lever 为 uuid）
  canon_uuid  TEXT NOT NULL REFERENCES job(uuid),
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  PRIMARY KEY (source, source_id)
);
CREATE TABLE job_repost (         -- 复制成新 uuid 的重复 → 归并到 canonical
  repost_uuid TEXT PRIMARY KEY REFERENCES job(uuid),
  canon_uuid  TEXT NOT NULL REFERENCES job(uuid),
  linked_at   TEXT NOT NULL,
  basis       TEXT NOT NULL       -- fingerprint | human_review
);

-- ── LLM 富化无法归一的词（分类体系演进入口，§5.2）─────────────────────
CREATE TABLE unmapped_tech (
  raw_term      TEXT NOT NULL,
  first_seen_at TEXT NOT NULL,
  seen_count    INTEGER DEFAULT 1,
  reviewed      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (raw_term)
);

-- ── LLM 富化缓存：按描述哈希，重贴/改版不重复推理 ────────────────────────
CREATE TABLE enrich_cache (
  description_sha256 TEXT NOT NULL,
  model              TEXT NOT NULL,
  prompt_version     TEXT NOT NULL,
  result_json        TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  PRIMARY KEY (description_sha256, model, prompt_version)
);

-- ── 周度指标物化（长表：新增指标不改 schema）──────────────────────────
CREATE TABLE weekly_metric (
  week_start TEXT NOT NULL,      -- ISO 周一 (SGT)
  metric     TEXT NOT NULL,      -- new_jobs | active_jobs | tech_freq | salary_median | ...
  dim_key    TEXT NOT NULL DEFAULT '',   -- 维度值：python / Senior / Backend / <UEN>
  dim_type   TEXT NOT NULL DEFAULT '',   -- tech / seniority / role_family / company
  value      REAL NOT NULL,
  computed_at TEXT NOT NULL,
  PRIMARY KEY (week_start, metric, dim_type, dim_key)
);

-- ── 运行审计：可观测性与"数据是否新鲜"的唯一真相源 ──────────────────────
CREATE TABLE ingest_run (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  kind         TEXT NOT NULL,   -- incremental | full_reconcile | enrich | report
  started_at   TEXT NOT NULL,
  ended_at     TEXT,
  pages_fetched INTEGER DEFAULT 0,
  jobs_seen    INTEGER DEFAULT 0,
  jobs_new     INTEGER DEFAULT 0,
  jobs_updated INTEGER DEFAULT 0,
  jobs_closed  INTEGER DEFAULT 0,
  llm_calls    INTEGER DEFAULT 0,
  llm_cached   INTEGER DEFAULT 0,
  errors       INTEGER DEFAULT 0,
  watermark    TEXT,            -- 本次处理到的最早 posting_date
  status       TEXT NOT NULL    -- running | success | partial | failed
);
```

### 4.1 存储容量估算

| 项 | 估算 | 依据 |
|---|---|---|
| IT 职位年增 | ~145,000 行 | 日增 ~3,000 × 13% × 365 |
| 原始 JSONL.gz 归档 | ~1.0 GB/年 → gzip 后 **~200 MB/年** | 实测 7.0 KB/条，JSON 压缩比 ~5:1 |
| `jobs.db`（不含 description 正文） | ~150 MB/年 | 每行结构化字段 ~1KB |
| **PVC 规格** | **10Gi** | 覆盖 4–5 年；节点余 42.7GB，占比可接受 |

> `description` HTML 正文**只存进归档不进 DB**（DB 只留 `description_sha256`）。
> 需要正文时从归档按 `raw_path` 回读。这是把 DB 体积压到 1/7 的关键决定。

---

## 5. 组件设计

### 5.1 ingest（每日增量 + 每周全量对账）

**增量策略（每日）**

```
watermark = SELECT max(posting_date) FROM job          -- 上次处理到的日期
page = 0
while True:
    r = GET /v2/jobs?limit=100&page={page}&sortBy=new_posting_date
    for job in r.results:
        if job.metadata.newPostingDate < watermark - 2d:   # 2 天回溯窗，容忍乱序与补录
            stop
        if not is_candidate(job): continue                 # 客户端类目过滤，见 §7.1
        upsert(job)
    page += 1
    sleep(1.5)                                             # 固定限速
    if page > MAX_PAGES (=300): break                      # 熔断，防止 API 语义变更导致无限翻页
```

- **限速 1.5s/请求**，单线程。
  实测一个工作日的发布量横跨 page 16–60+（≥3,000 条），叠加 2 天回溯窗
  → **单次约 60–150 页，2–4 分钟**。熔断阈值取 300 页（≈3 个工作日的量）留足余量：
  设得太紧会在节假日后的补录高峰**静默丢数据**，比跑久一点危险得多。
  触发熔断时记 `status='partial'` 并计入 `jobs_sg_ingest_errors_total`。
- `User-Agent` 声明身份与联系方式（如 `jobs-sg-monitor/1.0 (+https://jobs.meirong.dev)`），
  不伪装浏览器——数据源是政府公开 API 且 robots 全放行，透明访问是正确做法。
- 重试：429/5xx 走指数退避（3 次，2/4/8s），最终失败记 `errors` 但**不中断整轮**。
- **写入即归档**：每条原始 JSON 追加到 `raw/YYYY-MM-DD/incremental.jsonl.gz`，
  再解析入库。归档先于解析，保证 schema 演进后可回放重建。

**全量对账（每周日随 ingest 一并执行）**

目的：识别**已下架**职位（`closed_at`），支撑"在架活跃量"指标。

```
遍历 /v2/jobs 全量（total≈86,678 → 867 页 @limit=100，1.5s 间隔 ≈ 22 分钟）
  → 收集全部在架 uuid 集合 S
  → UPDATE job SET last_seen_at=now WHERE uuid IN S
  → UPDATE job SET closed_at=now WHERE closed_at IS NULL AND last_seen_at < today AND is_candidate
```

> **备选方案（不采用）**：sitemap 差分。6 个分片共 ~57MB 且**不支持 gzip**[已验证]，
> 而全量分页只传 IT 相关字段之外的完整对象……实际两者流量相当，但分页方式复用同一套
> 解析代码、且能顺带刷新 `view_count`/`application_count`，故统一走分页。
> sitemap 保留为**校验手段**：若分页总数与 sitemap 数量偏离 >20%，告警。

### 5.2 enrich（LLM 技术栈抽取）

**为什么还需要 LLM**：MCF 的 `skills[]` 是业务技能（实测样本："Liaising with cross functional
teams"），**不是技术栈**。语言/框架/云/AI 工具只存在于 `description` 自由文本里。

**两级抽取**：

1. **规则层（先跑，永远跑）**：`tech_taxonomy` 别名表 + 词边界正则扫描描述。
   覆盖高频确定项（python/java/go/react/kubernetes/aws/…），零成本、零延迟、可复现。
2. **LLM 层（补充，允许失败）**：把 title + 去 HTML 的描述（截断 4000 字符）交给本地模型，
   要求返回**严格 JSON**：`{"languages":[],"frameworks":[],"cloud":[],"databases":[],"tools":[],"ai":[]}`。
   输出经 `tech_taxonomy` 归一后写 `job_tech(source='llm')`；无法归一的 slug 落
   `unmapped_tech` 表供人工每周审阅——**这是分类体系持续演进的入口**。

**调用方式**（Bifrost，OpenAI 兼容）：

```
POST http://bifrost.bifrost.svc.cluster.local:8080/v1/chat/completions
Header: x-bf-vk: <virtual key, 来自 Vault>
Body:   {"model": "custom_dgx/deepseek-v4-flash", "messages":[...], "temperature": 0}
```

- **并发 3**，超时 60s/条，失败重试 1 次。~400 条/天 → **约 5–10 分钟**。
- **缓存必做**：先查 `enrich_cache[description_sha256, model, prompt_version]`。
  MCF 的 `repostCount` 表明重贴很常见，缓存命中率预期高。
- **fail-open**：Bifrost 不可达 / 401 / DGX 关机 → 记录 `errors`，job 保留规则层结果，
  `status='partial'`，**作业退出码 0**（不触发 CronJob 失败告警风暴），
  由 `JobsSgEnrichBacklog` 告警（§8）反映积压。
- **模型降级链**：`custom_dgx` → `custom_m2` → 纯规则。用环境变量配置优先级列表。

### 5.3 report（周报）

周一 09:00 SGT 执行：

1. 物化 `weekly_metric`（覆盖上周一~周日，SGT）
2. 渲染 `report/YYYY-Www.html`（自包含单文件，内联 CSS + SVG 图表，无外部资源）
   与 `report/YYYY-Www.md`
3. Telegram 推送：摘要文本 + 周报链接
   （复用 Vault `secret/homelab/telegram` 的 bot token；**发到独立话题，不占用告警话题
   `messageThreadID: 2`**——运维告警与内容推送混流会稀释告警注意力）
4. 更新 `report/index.html` 与 `report/latest.html` 软链内容

**周报内容**（对齐上游 §4，用官方字段落实）：

| 章节 | 指标 | 数据来源 |
|---|---|---|
| Executive Snapshot | 本周新增 SWE 岗位数、环比、在架总量、最热角色 | `job.posting_date` / `closed_at` |
| Hiring Trends | 角色分布、资历分布、公司类型分布、Top 10 招聘公司 | `role_family` / `seniority` / `company.company_type` / `company_uen` |
| Tech Trends | 技术频次 Top 30、环比升降 Top 10 | `job_tech` × `weekly_metric` |
| Compensation | 按角色/资历的薪资中位数与四分位 | `salary_min/max`（排除 `salary_hidden=1`） |
| Demand Signals | 平均浏览量/投递数、投递竞争度（applications/vacancy） | `view_count` / `application_count` — **差异化指标** |
| Skills-first | 无学历门槛、`min_years_exp=0` 岗位占比 | `min_years_exp` / `ssecEqa` |
| Data Quality | 本周 ingest/enrich 成功率、LLM 缓存命中率、未映射技术词 | `ingest_run` / `unmapped_tech` |

> **数字与解读分离**：所有数字由 SQL 计算并直接渲染；LLM **只允许**生成
> "Insights" 段落的自然语言解读，且 prompt 里注入的是**已算好的数字**，
> 禁止模型自行计算。这样 LLM 幻觉不会污染指标。

### 5.4 web（常驻）

- 单进程 Go 二进制：标准库 `net/http` 即可（不引 web 框架）；只读打开 SQLite
  （`modernc.org/sqlite`，纯 Go 免 CGO，保持单二进制 + 多架交叉编译）
- `/metrics` 用 `prometheus/client_golang` 暴露（Prometheus 文本格式，见 §8）
- 只读打开 SQLite：`file:/data/jobs.db?mode=ro&immutable=0`
- 路由：`/`（最新周报）、`/w/{YYYY-Www}`、`/healthz`、`/metrics`
- **不做认证**——内容是公开就业市场统计，无个人数据。
  若日后需要，按 bifrost 模式加 oauth2-proxy。

**`/metrics` 暴露**（从 `ingest_run` 与 `job` 现算，见 §8）。

---

## 6. 调度、资源与并发

### 6.1 CronJob 时间表（节点为 UTC，SGT = UTC+8）

| 作业 | UTC cron | SGT | 避让说明 |
|---|---|---|---|
| `ingest` | `15 18 * * *` | 02:15 | 避开 restic 备份 03:00 UTC、calibre 04:00 UTC |
| `enrich` | `10 19 * * *` | 03:10 | ingest 之后 55 分钟 |
| `report` | `0 1 * * 1` | 周一 09:00 | 周日全量对账（随周日 ingest）已完成 |

### 6.2 资源预算

| 组件 | requests (cpu/mem) | limits (cpu/mem) | 形态 |
|---|---|---|---|
| `ingest` | 100m / 128Mi | 500m / 384Mi | 瞬时 |
| `enrich` | 100m / 192Mi | 500m / 512Mi | 瞬时 |
| `report` | 100m / 128Mi | 500m / 384Mi | 瞬时（每周） |
| `web` | 25m / 64Mi | 200m / 192Mi | **常驻** |

- **常驻新增：64Mi requests**（节点内存 requests 52% → 52.5%）
- **峰值新增：≤512Mi**（作业互不重叠；实测余量 ~2.8GB）
- **不设 cpu limit 的例外**：本项目设了 cpu limit（500m）。理由是这些是批作业，
  被节流只是慢一点，而节点是过热笔记本（idle ~74°C）——限制 CPU 尖峰比作业快 30 秒更重要。
  这与仓库"不设 cpu limit 避免节流"的通用建议是**有意偏离**，原因如上。

### 6.3 并发与一致性

- 三个 CronJob 全部 `concurrencyPolicy: Forbid` + `backoffLimit: 2` +
  `successfulJobsHistoryLimit: 3` / `failedJobsHistoryLimit: 1` +
  **`activeDeadlineSeconds`**（ingest 3600 / enrich 3600 / report 1800）——
  卡死的 Job 必须能自己死掉，这正是 92-pod 泄漏事故的根因
- `ingest`/`enrich`/`report` 之间用 SQLite 事务 + `busy_timeout=10000` 保证；
  时间表已错开，重叠只在异常长跑时发生，此时后者等待而非失败
- `web` 以只读连接打开，WAL 模式下不阻塞写入
- **RWO PVC 多 Pod 挂载**：单节点集群下 Kubernetes 允许同节点多 Pod 挂载同一 RWO PVC，
  `local-path` 是 hostPath 支撑，WAL 的 `-shm` 共享内存文件正常工作。
  **这条依赖单节点**——若集群将来加节点，必须给所有 jobs-sg 工作负载加
  `nodeSelector`/`nodeAffinity` 钉到同一节点，否则 Pod 会因 PVC 不可跨节点而 Pending。

---

## 7. 分类口径

### 7.1 SWE 口径判定（`is_swe`）

三层判定，**顺序优先、可解释、可回溯**：

1. **SSOC 主判**：`ssoc_code` 前 3 位 ∈ 白名单。
   SSOC 2020 与 ISCO-08 对齐，`251` = Software and Applications Developers and Analysts。
   实测样本：`25121`（Golang Developer）、`21222`（Data Engineer）。
   → **Phase 1 交付物之一是"SSOC → role_family 映射表"**：先按频次导出 IT 类目下
   全部 ssoc_code，人工核定一次（预计 30–50 个码），落 `ssoc_taxonomy` 表。
   **不要凭猜测硬编码**——先测量再定义。
2. **类目辅判**：`categories[].category == 'Information Technology'`。
3. **标题兜底**：正则匹配 engineer/developer/programmer/SRE/architect 等，
   用于捕捉挂在 Engineering/Professional Services 类目下的 SWE 岗。

三层结果写入 `job.is_swe` 并记录命中层级（便于事后调整口径时重算）。

### 7.2 派生维度

| 维度 | 推导规则 |
|---|---|
| `seniority` | `position_level` + `min_years_exp` + 标题关键词三者投票；冲突时以标题为准（实际招聘信号最强） |
| `role_family` | `ssoc_taxonomy` 主判 + 标题关键词修正（如 SSOC 相同但标题含 "Frontend"） |
| `work_mode` | `flexibleWorkArrangements[]` 有值 → Hybrid/Remote；空 → Onsite（**并标注为推断值**，MCF 该字段填写率需在 Phase 1 测量） |
| `company_type` | `ssic_code`（行业分类）+ `employee_count` + 人工规则表（如 UEN 前缀 T→政府相关） |

### 7.3 指标定义（口径一次定死，写进代码注释）

- **新增量**：统计周内出现的**去重 canonical 职位数**（同 `uuid` 只计一次；
  `repost_count>0` 的重贴与跨源重复按 `original_posting_date`/`canonical_fp` 归属，避免刷新与多源虚增；见 §15.3）
- **在架量**：`closed_at IS NULL AND (expiry_date IS NULL OR expiry_date >= 周末)`
- **技术频次**：出现该技术的**职位数**（不是词频），按 `job_tech` distinct
- **薪资中位数**：`(salary_min + salary_max) / 2` 的中位数，仅 `salary_hidden=0`
  且 `salary_type='Monthly'`；其他币种/周期单独统计不混算；
  跨源来源先按 `salaryRange.interval+currency` 归一成 `salary_type`（见 §15.5）
- **投递竞争度**：`application_count / max(vacancies,1)`

---

## 8. 可观测性与告警

### 8.1 指标（`web` Pod 的 `/metrics`，Prometheus 文本格式）

```
jobs_sg_last_success_timestamp_seconds{kind="incremental|full_reconcile|enrich|report"}
jobs_sg_run_duration_seconds{kind=...}
jobs_sg_jobs_total{state="active|closed"}
jobs_sg_jobs_new_total{week=...}
jobs_sg_enrich_backlog          # is_swe=1 且无 job_tech(source='llm') 的数量
jobs_sg_llm_calls_total / jobs_sg_llm_cache_hits_total / jobs_sg_llm_errors_total
jobs_sg_ingest_errors_total
jobs_sg_unmapped_tech_total
```

ServiceMonitor **必须带 `release: kube-prometheus-stack` 标签**，否则 operator 静默忽略。

### 8.2 告警规则（PrometheusRule，同样需 `release` 标签）

| 告警 | 条件 | severity | 说明 |
|---|---|---|---|
| `JobsSgIngestStale` | `time() - jobs_sg_last_success_timestamp_seconds{kind="incremental"} > 36h` | warning | **最重要的告警**——静默失效比崩溃更危险 |
| `JobsSgReconcileStale` | 同上，`full_reconcile > 10d` | warning | 在架量指标失真 |
| `JobsSgIngestErrors` | `increase(jobs_sg_ingest_errors_total[1d]) > 20` | warning | API 语义变更的早期信号 |
| `JobsSgEnrichBacklog` | `jobs_sg_enrich_backlog > 2000` | warning | LLM 长期不可用 |
| `JobsSgCronJobFailed` | `kube_job_status_failed{namespace="jobs-sg"} > 0` | warning | 兜底（kube-state-metrics 现成） |

均走现有 Alertmanager → Telegram（`severity=warning|critical` 路由已在线）。

### 8.3 日志与追踪

- 结构化 JSON 日志到 stdout → 现有 OTel Collector → Loki，无需额外配置
- **Phase 1 不接 Tempo**：批作业的分布式追踪收益低于配置成本。
  若日后 LLM 链路排障需要，按仓库约定注入
  `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.monitoring.svc:4317` 即可

### 8.4 Grafana 面板

`k8s/helm/manifests/jobs-sg-dashboard.yaml`，ConfigMap 带
`grafana_dashboard: "1"` label + `grafana_folder: Platform` annotation，
文件名加入 `argocd/applications/monitoring-dashboards.yaml` 的 `directory.include`。

---

## 9. 备份与恢复

`local-path` **无冗余、无快照**，备份是强制项。

现有 restic 夜备脚本（`backup/overlays/homelab/backup-script.yaml`）已经在扫
`*.db` / `*.db-wal` / `*.db-shm`，接入只需**一行改动**：

```sh
# 第 53 行
for pat in bifrost-data calibre-web-automated-config jobs-sg-data; do
```

- 覆盖：`jobs.db` + WAL/SHM（restore 时 SQLite 自恢复）
- **不覆盖**：`raw/*.jsonl.gz` 归档（可从 API 重新拉取的部分有限，但体积增长快）。
  → **决策：归档目录纳入 restic 的独立路径**（类似 calibre 书库的整目录做法），
  因为历史快照**不可重建**（API 只返回当前在架职位，下架的就永远拿不回来了）。
  这是本系统最不可替代的数据资产。归档年增 ~200MB，restic 去重后成本很低。
- PVC 加 `argocd.argoproj.io/sync-options: Prune=false`
- **恢复演练**：Phase 2 DoD 包含一次实际 restore + `PRAGMA integrity_check`

---

## 10. 安全与合规

### 10.1 数据合规

- 数据源为**新加坡政府公开平台**，`robots.txt` 全站放行 [已验证]，访问的是其前端同款公开 API
- **只取公开 listing**，不登录、不抓取候选人数据、不做个人画像
- `User-Agent` 透明声明身份与项目地址，不伪装浏览器
- 固定 1.5s 限速、单线程、全量对账每周仅一次——负载远低于普通用户浏览
- 数据仅用于个人趋势分析，**不转售、不再分发原始数据集**；周报只发布聚合统计
- `postedCompany` 是法人信息（非个人数据）；`createdBy`/`emailRecipient` 等
  **发布者 ID 字段一律不落库**

### 10.2 集群安全

| 项 | 做法 |
|---|---|
| Namespace PSA | `restricted`（而非其他 ns 的 `baseline`）——全新 Go 应用可轻松满足，白拿一档 |
| Pod 安全上下文 | `runAsNonRoot: true`、`runAsUser: 10001`、`allowPrivilegeEscalation: false`、`capabilities.drop: [ALL]`、`seccompProfile: RuntimeDefault`、`readOnlyRootFilesystem: true`（`/tmp` 用 emptyDir） |
| 镜像 | `ghcr.io/meirongdev/jobs-sg`，**按 digest 固定**（Kyverno `disallow-latest-tag` 已是 **Enforce** [已验证]） |
| 密钥 | Bifrost virtual key + Telegram bot token 走 Vault → ESO；**不进 git** |
| 网络 | 集群当前无 CiliumNetworkPolicy（有意延后），本项目不单独引入，保持一致 |

**Vault 路径**：`secret/homelab/jobs-sg`，键 `bifrost-vk`、`telegram-bot-token`、`telegram-chat-id`、`telegram-thread-id`。

---

## 11. GitOps 落地

### 11.1 仓库划分

| 内容 | 仓库 | 理由 |
|---|---|---|
| 应用代码 + Dockerfile + 测试 | `meirongdev/jobs-sg`（本仓库） | 独立演进，独立 CI |
| K8s manifests | **`meirongdev/homelab`** | ⚠️ `argocd/projects/homelab.yaml` 的 `sourceRepos` 是**白名单**，只允许 `github.com/meirongdev/homelab`。若把 manifests 放在本仓库，必须①往 `sourceRepos` 加一条，且②**手动 `kubectl apply` AppProject**——它不在 root App 的托管路径下，`git push` 不生效。为省掉这个长期维护陷阱，manifests 放 homelab 仓库。 |

### 11.2 文件清单（homelab 仓库）

```
k8s/helm/manifests/jobs-sg/          # 新建 kustomize 目录（仿 calibre-metadata 模式）
├── kustomization.yaml
├── namespace.yaml                   # ns + PSA restricted 标签
├── limits.yaml                      # LimitRange + ResourceQuota (count/jobs.batch)
├── pvc.yaml                         # 10Gi local-path, Prune=false
├── external-secret.yaml             # Vault → jobs-sg-secrets
├── cronjob-ingest.yaml
├── cronjob-enrich.yaml
├── cronjob-report.yaml
├── web.yaml                         # Deployment + Service + ReferenceGrant(v1beta1) + HTTPRoute
└── monitoring.yaml                  # ServiceMonitor + PrometheusRule

argocd/applications/jobs-sg.yaml     # 独立 Application（含 batch/Job ignoreDifferences）

k8s/helm/manifests/jobs-sg-dashboard.yaml         # Grafana 面板
argocd/applications/monitoring-dashboards.yaml    # ← 把上面文件名加进 include glob
backup/overlays/homelab/backup-script.yaml        # ← 加 jobs-sg-data 到扫描列表 + 归档目录
cloud/oracle/manifests/homepage/homepage.yaml     # ← homepage 条目（改后需 rollout restart）
cloud/oracle/manifests/uptime-kuma/provisioner.yaml # ← Uptime Kuma monitor
```

**用独立 Application 而不是并入 `personal-services`**，原因有二：
① `personal-services` 的 `ResourceQuota` 限死 `count/jobs.batch: "15"`，
本项目三个 CronJob 的历史 Job 会挤占该配额（那条配额是为 92-pod 泄漏事故加的护栏，不该稀释）；
② 需要 kustomize 目录 + `ignoreDifferences`，`directory.include` 模式不支持。

### 11.3 ArgoCD Application 骨架

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: jobs-sg
  namespace: argocd
  finalizers: [resources-finalizer.argocd.argoproj.io]
spec:
  project: homelab
  source:
    repoURL: https://github.com/meirongdev/homelab
    targetRevision: main
    path: k8s/helm/manifests/jobs-sg
  destination:
    server: https://kubernetes.default.svc
    namespace: jobs-sg
  # CronJob 产生的 Job 被 job-controller 注入 suspend/selector/template labels，
  # git 清单没有这些字段 → 永久 OutOfSync。同 calibre-metadata。
  ignoreDifferences:
    - group: batch
      kind: Job
      jqPathExpressions:
        - .spec.suspend
        - .spec.selector
        - .spec.template.metadata.labels
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [CreateNamespace=true, ServerSideApply=true]
```

### 11.4 镜像构建（本仓库 `.github/workflows/image.yml`）

仿 `homelab/.github/workflows/excalidraw-room-image.yml`：

- 推 `ghcr.io/meirongdev/jobs-sg`，tag `sha-<short>` + `<semver>`
- **`platforms: linux/amd64,linux/arm64`**——homelab 只需 amd64，但多构一个 arm64
  就把 §2 的 oracle 迁移出口留着了，成本是几分钟 CI 时间
- 首次构建后把 GHCR 包设为 **Public**（否则拉取需要 imagePullSecret）
- manifests 里**按 digest 固定**镜像（Kyverno Enforce）
- **不启用 ArgoCD Image Updater**：集群里当前 0 个 `ImageUpdater` CR，组件处于空闲状态；
  为一个应用引入它得先补 CR + git write-back 凭据，收益不抵复杂度。手动更新 digest 即可。

---

## 12. homelab 特有踩坑清单（实现前必读）

> 以下每条都是本次勘查中**实际确认**的，不是通用建议。

1. **⚠️ HTTPRoute 的 `parentRefs.port` 必须是 `80`，不是 8000。**
   `docs/CONVENTIONS.md` 与 `.claude/skills/add-service/SKILL.md` 都写着 homelab 用
   **端口 8000**，但实测 `homelab-gateway` 的 listener 只有 `{"name":"http","port":80}`，
   且线上 6 条 HTTPRoute（argocd/bifrost/grafana/calibre-web/open-notebook/vault）
   **全部用 port 80** [已验证]。照文档写 8000 会得到一条永远不挂载的路由。
   → 实现时用 80；**并顺手修掉这两处文档**。

2. **`ReferenceGrant` 必须是 `v1beta1`**。写成 `v1` 会让整个 Application 报 `ComparisonError`。

3. **Kyverno `disallow-latest-tag` 是 Enforce**，不是 Audit——本次勘查中一个用
   `:latest` 的临时 curl Pod 被准入 webhook **实际拒绝** [已验证]。镜像必须 digest 或明确 tag。

4. **CronJob 必须设 `concurrencyPolicy: Forbid` + `successfulJobsHistoryLimit` +
   `failedJobsHistoryLimit`**。仓库有先例：一个没设这些的 CronJob 泄漏了 92 个卡住的 Job Pod，
   把单节点推过 110-pod 上限，**阻塞了全集群调度**。

5. **`ServiceMonitor` / `PrometheusRule` 必须带 label `release: kube-prometheus-stack`**，
   否则 operator 的 selector 静默忽略，指标和告警都不会生效且**没有任何报错**。

6. **PVC 必须 `storageClassName: local-path`**。`nfs-client` provisioner 已于 2026-07-11 卸载，
   引用它的 PVC 会**永久 Pending**。

7. **Gateway 的 `Programmed` 条件为 `False`（`AddressNotAssigned`）是正常的** [已验证]——
   NodePort GatewayClass 挂在 Cloudflare Tunnel 后面没有 LB 地址。路由照常工作，不要去"修"它。

8. **DNS 不需要任何手工步骤**。写 HTTPRoute 就是建 DNS（external-dns + 通配隧道路由）。
   **不要**改 `cloudflare/terraform`。注意 `policy: upsert-only`——删 HTTPRoute 不会删 DNS 记录。

9. **Bifrost 集群内调用同样需要 virtual key**。governance PreHook 在入口之前生效，
   走 `bifrost.bifrost.svc` 不能绕过。密钥在 Bifrost UI 里创建（持久化在 PVC 的 SQLite，
   **不在 git 里**），再手工写进 Vault。

10. **homepage 的 ConfigMap 用 `subPath` 挂载，不热加载**。改完必须
    `kubectl --context oracle-k3s rollout restart deployment/homepage -n homepage`。

11. **Uptime Kuma monitor 是声明式且会 prune**——不在 `MONITORS` 列表里的会被删除。

12. **`AppProject` 不受 GitOps 管理**。本设计把 manifests 放 homelab 仓库正是为了绕开这点（§11.1）。

---

## 13. 实施路线图

### Phase 0 — 打地基（0.5 天）

- [ ] 用一次性脚本（本机跑，不进集群）抽样 **2,000 条 IT 职位**，导出：
      `ssoc_code` 频次分布、`positionLevels` 取值集合、`flexibleWorkArrangements` 填写率、
      `salary_hidden` 比例、`categories` 分布
- [ ] 据此**人工核定** `ssoc_taxonomy`（SSOC → role_family）与 `tech_taxonomy` 种子表
- **DoD**：两张映射表落库，SWE 口径可解释、可复现

> 这一步先于写任何生产代码。分类口径是整个系统的地基，
> 拍脑袋定下来后面每周的趋势数字都不可信。

### Phase 1 — MVP（1 周）

- [ ] `ingest` 增量 + 归档 + SQLite schema
- [ ] 规则层技术栈抽取（无 LLM）
- [ ] `report` 生成 HTML/Markdown（本地跑通）
- [ ] 容器化 + GH Actions → GHCR
- [ ] manifests 落 homelab 仓库，ArgoCD 同步，CronJob 跑通
- **DoD**：连续 3 天自动增量成功，`ingest_run` 表有 3 条 `success`，
  周报 HTML 可在 `jobs.meirong.dev` 打开

### Phase 2 — 生产化（1 周）

- [ ] 每周全量对账 + `closed_at` 生命周期
- [ ] `enrich` 接 Bifrost（含缓存、降级链、fail-open）
- [ ] `/metrics` + ServiceMonitor + 5 条 PrometheusRule
- [ ] Telegram 周报推送（独立话题）
- [ ] restic 备份接入 + **实际恢复演练**（`PRAGMA integrity_check` 通过）
- [ ] Grafana 面板、homepage 条目、Uptime Kuma monitor
- **DoD**：告警链路端到端验证（手动停一次 CronJob，确认 `JobsSgIngestStale` 到 Telegram）；
  restore 演练通过

### Phase 3 — 持续演进（按需）

- [ ] **多源采集第一期**：接入 Greenhouse / Lever / Ashby 三个 ATS 公开 JSON API 为第一批
      `Source` 策略（§15）——收益最高；schema 已在 Phase 1 留好 `source`/`canonical_fp`/链接表
- [ ] 环比/同比、上升下降最快技术榜
- [ ] 异常检测（某技术或某公司突然爆发）
- [ ] JobStreet / Indeed **仅做交叉验证/补漏**（不改核心口径；爬取前先查公开 API，合规优先）
- [ ] LinkedIn：**默认不做**（无干净公开 API + 反爬 + ToS 风险，见 §15.1）
- [ ] `unmapped_tech` 周审 → 分类体系自动演进
- [ ] 若数据量或查询复杂度超出 SQLite：迁 oracle-k3s 的 CNPG（schema 基本可平移）

---

## 14. 风险与取舍

| 风险 | 影响 | 缓解 |
|---|---|---|
| **MCF API 未公开承诺稳定**（无版本化契约，可能加鉴权或改 schema） | 采集中断 | ① `JobsSgIngestErrors` 早期告警；② **原始 JSON 先归档后解析**，schema 变更可回放重建；③ 解析层对未知字段宽容（只读需要的键）；④ sitemap 作为独立校验通道 |
| 单节点 + RWO PVC | 集群扩节点后 Pod Pending | 现在就在 manifests 里写好 `nodeAffinity` 注释；扩节点时启用 |
| SQLite 单写者 | 作业重叠时阻塞 | `Forbid` + 错开时间 + `busy_timeout` |
| DGX Spark 关机 | LLM 富化停摆 | fail-open + 降级到 `custom_m2` + 纯规则；`JobsSgEnrichBacklog` 反映积压 |
| homelab 内存进一步吃紧 | 作业被 OOM kill | 已设 limits；§2 迁移出口保持可用（多架构镜像 + 环境变量化依赖） |
| LLM 幻觉污染指标 | 趋势数字失真 | 数字全由 SQL 算；LLM 只写解读段落且喂给它的是算好的数 |
| 分类口径漂移 | 跨周不可比 | 口径写进代码注释 + `weekly_metric` 保留 `computed_at`；口径变更后**全量重算历史** |
| 归档数据丢失 | **不可重建**（API 不返回已下架职位） | 归档目录纳入 restic；这是最高优先级的备份对象 |
| 多源化新增风险（§15） | ATS 接口也可能改 schema/加鉴权；跨源归并误合并 | 每源独立 freshness/error 指标 + 独立告警；`job_repost` 留 `basis` 供审计；canonical 归并保守（宁可多留行，勿误并） |

---

## 15. 扩展设计：多源化、跨源身份与选择策略（v2 增量）

> 评审后新增：① 多数据源（公司官网走 ATS 公开接口）；② 跨源/跨刷新去重到 **canonical 身份**；
> ③ "≥10k 薪资" 作为分析层可插拔选择策略。配套改动：§4 schema、§7.3 口径、§13 路线图。

### 15.1 来源分级（实测判定，2026-08-02）

| 来源 | 拿法 | 鉴权/反爬 | 合规 | 薪资填充 | 优先级 |
|---|---|---|---|---|---|
| **MCF** | 公开 JSON API（已在用） | 无 / 无 | 干净 | 高（结构化） | ✅ MVP |
| **公司官网 → ATS**（Greenhouse/Lever/Ashby/Workday） | **公开 JSON API**（GH/Lever 已实测免鉴权） | 无 / 低 | 自家发布接口 | 中→低 | 🥇 多源第一期 |
| **Indeed** | 无干净公开 API；Publisher API 需合作 | 反爬重 | 中 | 低 | 末位/仅校验 |
| **LinkedIn** | 公开 API 需企业合作；刮取违反 ToS | Cloudflare+登录墙+住宅代理 | **高风险** | 中 | **跳过/最后** |

**实证**（详见附录 A 增补）：
- `GET https://boards-api.greenhouse.io/v1/boards/{company}/jobs` → 免鉴权
  `{jobs:[{id,title,location,first_published,updated_at,absolute_url,data_compliance}]}`
- `GET https://api.lever.co/v0/postings/{company}?mode=json` → 免鉴权数组
  `[{id,text,categories{department,location,team},workplaceType,descriptionPlain,hostedUrl,salaryRange?}]`

**判断**：多源化该**接 ATS 公开接口**，而非爬 LinkedIn/Indeed——后者成本高、收益低、
ToS/GDPR 风险大，其职位 MCF+ATS 基本都有。

### 15.2 Source 策略接口（策略模式落地）

```
Source 接口
  name():  str
  fetch(window) -> SourceJob[]        # 各源自己的抓取（日期窗/分页/增量）
  normalize(j) -> UnifiedJob          # 映射到统一 schema + canonical_fp
  classify(j) -> kept: bool           # 选择谓词（可选，缺省恒真）
注册表: { "mcf":..., "greenhouse":..., "lever":..., "ashby":... }
Driver: 遍历启用源 → fetch → normalize → ingest(upsert + 去重)
```

- 每源一个 `ingest_run.kind`（`ingest_mcf` / `ingest_greenhouse`…）与各自的 freshness/error 指标。
- **添加新源 = 加一个 `sources/<name>.go` 文件**（实现 `Source` 接口 + 注册），不动 ingest 主流程——这是策略模式的核心收益。

### 15.3 canonical 跨源身份与去重

`uuid` 仅 MCF 有，跨源无通用主键 → 用**逻辑指纹**做 canonical 身份：

```
canonical_fp = sha256(normalize(title) + "|" + uen_or_domain + "|" + normalize(description))
```

- 同一职位跨源命中同 fp → 归并到同一 canonical job（`job.canonical_fp`）；
  来源证据落 `job_source_xref(source, source_id, canon_uuid)`。
- 指标按 **canonical** 去重（不出现在 3 个源就翻 3 倍，见 §7.3）。
- 交叉验证信号：某职位仅出现在 ATS 而未进 MCF → 更快被移除/未注册，本身是需求侧信号。

**刷新去重（HR 只刷新不动内容）**：MCF 刷新通常在**同一 uuid** 上顶 `newPostingDate` +
`repostCount++` → 主键 upsert 已是 UPDATE 非新行；再叠加：
- 指标按 `original_posting_date` 归属（§7.3，防日增灌水）；
- `description_sha256`/`canonical_fp` 不变 → 跳过 LLM 富化（enrich_cache 命中）；
- 复制成**新 uuid** 且内容相同 → fp 命中在架/近期职位 → 写 `job_repost(new→canonical)`，不计新。

**刷新判定信号表**

| 现象 | 信号 | 判定 |
|---|---|---|
| 同 uuid 刷新 | `repostCount`↑/`updatedAt` 变，hash 不变 | 刷新，按原始日期归属，不计新 |
| 同 uuid 内容小改 | 同 uuid，hash 变 | 刷新+改版，`original_posting_date` 不变 → 仍不算新 |
| 复制成新 uuid | 新 uuid + fp 命中 | 重贴，走 `job_repost` 归组 |
| 真新职位 | 新 uuid + fp 未命中 | 新职位，计入 |

**边界**：改关键字的复制到新 uuid 抓不住（需模糊匹配，设计不引入向量库）；靠 `repost_count` +
"极端相似 JD 周报"暴露人工判断。

### 15.4 选择策略（≥10k 薪资等可插拔谓词）

- **不放在 ingest 硬过滤**：服务器端过滤不了薪资；抓取层过滤省不下流量（近窗整段都要拉），
  却带来不可逆 + 漏掉「隐藏高薪岗」（`isHideSalary=1` 常是高薪岗）。
- **放分析/指标层**：爬全部 SWE 候选落库（schema 已存 `salary_min/max/type`），
  阈值作为 `selection_strategy` 谓词在 `weekly_metric` 物化时按 SQL 应用：

```sql
WHERE (salary_type='Monthly' AND salary_max >= 10000)
   OR (salary_type='Annual'  AND salary_max >= 120000)
   OR salary_hidden=1                     -- 保留、单独计数、不直接排除
```

- 阈值可调、历史可重算；用**组合规则**而非单一薪资谓词，否则系统性漏掉隐藏高薪岗。

### 15.5 跨源薪资归一化

- 薪资口径只对**有结构化薪资的源**成立（主要是 MCF；ATS 部分 `salaryRange.interval/currency`）。
- `source ∈ {greenhouse, lever}` 且无 `salaryRange` → 标 `salary_hidden=1`（视为未知，
  不参与 10k 选择策略）。
- 跨源汇总前把 `salaryRange.interval`（monthly/annual）+ `currency` 归一成 MCF 一致的
  `salary_type`；非 SGD 单独统计不混算。

### 15.6 落地顺序

- **Phase 1（当前）**：仅 MCF，但 schema **现在就放好** `source` 列 + `canonical_fp` +
  三张链接/映射表（成本低、先留位，避免后补重灌）。
- **Phase 3 提前**：加 Greenhouse/Lever/Ashby 三个 ATS 源为第一批 `Source` 策略。
- **最后才评估** Indeed/LinkedIn，且只做交叉验证/补漏，不依赖做核心口径。

## 附录 A：验证证据

本文 **[已验证]** 标记均来自 2026-08-02 的以下实测：

```bash
# ── 数据源 ────────────────────────────────────────────────────────────
curl -s https://www.mycareersfuture.gov.sg/robots.txt
#   → User-agent: * / Disallow:  (全站允许) + sitemap-index.xml

curl -s -X POST 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=2&page=0' \
     -H 'Content-Type: application/json' -d '{"search":"software engineer"}'
#   → 401 Unauthorized

curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=100&page=0&sortBy=new_posting_date'
#   → 200, {"results":[...100 条完整对象...],"total":86678,...}
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=200&page=0'      # → 400 (limit 上限 100)
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=3&categories=21' # → 400 (类目参数不通)
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=3&search=software%20engineer'  # → 200, total=902
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs/0002d6e300a67475b8b4be8b94db6189'    # → 200 单条

# 日增量反查：page 0 → 2026-08-02 ×100；page 15 → 08-01；page 30/60 → 07-31
# IT 类目占比：page 0 的 100 条中 13 条 categories[0].category == 'Information Technology'
# 单条 JSON ≈ 7,024 bytes

curl -s https://www.mycareersfuture.gov.sg/sitemap-1.xml    # 9,579,317 bytes, 45,000 <loc>, 3,185 IT
#   Accept-Encoding: gzip 无效（返回 application/octet-stream 全量）

# ── ATS 公开接口（§15.1，多源化实证）──────────────────────────────────
curl -s https://boards-api.greenhouse.io/v1/boards/stripe/jobs   # 免鉴权 {"jobs":[...]}
#   → 字段: id, title, location.name, first_published, updated_at, absolute_url,
#           data_compliance[].type, company_name, internal_job_id
curl -s https://api.lever.co/v0/postings/leverdemo?mode=json     # 免鉴权 [...]
#   → 字段: id, text, categories{department,location,team}, workplaceType,
#           descriptionPlain, hostedUrl, 部分含 salaryRange{min,max,currency,interval}

# ── 集群 ──────────────────────────────────────────────────────────────
kubectl --context k3s-homelab describe node k8s-node   # 8C/13.27GB; req 2722m/6118Mi; 47 pods
kubectl --context k3s-homelab top node                 # 1805m (23%) / 9095Mi (78%)
kubectl --context k3s-homelab get sc                   # local-path (default) 唯一
kubectl --context k3s-homelab get --raw \
  "/api/v1/nodes/k8s-node/proxy/stats/summary"         # 123.7GB 容量 / 42.7GB 可用

kubectl --context oracle-k3s describe node oracle-k3s  # 4C/24.55GB; req 2005m/4073Mi
kubectl --context oracle-k3s get cluster.postgresql.cnpg.io -A   # zitadel-pg healthy

# ── Gateway 端口（文档与实际不一致的证据）──────────────────────────────
kubectl --context k3s-homelab get gateway homelab-gateway -n kube-system -o jsonpath='{.spec.listeners}'
#   → [{"name":"http","port":80,"protocol":"HTTP",...}]     ← 只有 80
kubectl --context k3s-homelab get httproute -A \
  -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,PORT:.spec.parentRefs[*].port'
#   → 6 条路由全部 PORT=80
kubectl --context k3s-homelab get gateway homelab-gateway -n kube-system -o jsonpath='{.status.conditions}'
#   → Programmed=False / AddressNotAssigned（NodePort + Tunnel 下属正常）

# ── Kyverno Enforce（实测拒绝）─────────────────────────────────────────
kubectl --context k3s-homelab run curltest --image=curlimages/curl:latest ...
#   → admission webhook "validate.kyverno.svc-ignore" denied the request:
#     disallow-latest-tag: 禁止使用 :latest tag

# ── Bifrost ───────────────────────────────────────────────────────────
curl -s http://bifrost.bifrost.svc.cluster.local:8080/api/providers   # 集群内
#   → custom_dgx (http://100.97.87.120:8000), custom_m2 (http://100.89.15.120:8000)
#     两者 chat_completion=true, embedding=FALSE
```

---

*文档版本：2026-08-02（v2，含 §15 扩展）｜ 上游需求见 `Singapore_SWE_Job_Monitoring_Architecture.md`*
