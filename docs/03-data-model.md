# 数据模型与分类口径

> 实现期主参照。上游设计见 [02-design](02-design.md)；指标业务含义见 [01-requirements](01-requirements.md) §2（页面定义）与 §2.1（周报章节）。
> 演进原则：**归档先于解析**——DB 永远可从 `raw/*.jsonl.gz` 回放重建，schema 改动不焦虑。
> **v2.1 变更**：`job.miss_count` 新增（对账防误关）；容量估算按全类目归档修订；明确 `is_candidate`（宽）/`is_swe`（严）两级谓词；§8 可选快照表。

---

## 1. SQLite 配置

SQLite，回滚日志（DELETE）模式，位于 `/data/jobs.db`。全部时间戳统一存 UTC ISO8601。

```sql
PRAGMA journal_mode = DELETE;   -- 单写多读；web 只读挂载下 WAL 无法建 -shm
PRAGMA busy_timeout = 10000;    -- 异常长跑时后写者等待而非 SQLITE_BUSY
PRAGMA synchronous = NORMAL;    -- local-path 本地盘，NORMAL 足够
```

> **为什么是回滚日志而非 WAL**：web 将 `/data` 只读挂载，而 WAL 打开时需在数据目录
> 创建/附加 `-shm` 文件——只读文件系统上做不到，`mode=ro` 打开会报 SQLITE_CANTOPEN。
> 本系统写入由 cron 时间表严格串行（ingest/enrich/report 互不重叠），且 web 只读挂载
> 本就读已提交快照（看不到 live WAL），WAL 在此无实际收益。回滚日志即可被只读 web
> 直接打开。

## 2. Schema

```sql
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
  canonical_fp        TEXT,                 -- 跨源逻辑指纹；多源动工前需重审（见 06 §3）
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
  -- 派生分类（本系统计算，见 §4–§5）
  role_family         TEXT,                  -- Backend/Frontend/Fullstack/SRE/Data/AI-ML/Mobile/Security/Platform
  seniority           TEXT,                  -- Intern/Junior/Mid/Senior/Staff+/Lead/Manager
  work_mode           TEXT,                  -- Onsite/Hybrid/Remote（由 flexibleWorkArrangements 推导）
  is_swe              INTEGER NOT NULL DEFAULT 0,  -- 是否纳入 SWE 统计口径（见 §4）
  -- 时间与生命周期
  posting_date        TEXT NOT NULL,         -- metadata.newPostingDate；纯日期 "2026-08-03"（API 实际格式，非 RFC3339——解析统一走 mcf.ParsePostingDate）
  original_posting_date TEXT,
  expiry_date         TEXT,
  repost_count        INTEGER DEFAULT 0,
  status              TEXT,                  -- status.jobStatus
  first_seen_at       TEXT NOT NULL,         -- 本系统首次见到
  last_seen_at        TEXT NOT NULL,         -- 最近一次对账中见到 → 推导"是否仍在架"
  miss_count          INTEGER NOT NULL DEFAULT 0,  -- v2.1: 对账连续未见次数；≥2 才关闭（防分页竞态误关，02 §4.1）
  closed_at           TEXT,                  -- 判定下架的时刻；重新见到时清 NULL（reopen）
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
  source    TEXT NOT NULL,       -- rule | llm（可同 job+slug 共存）
  PRIMARY KEY (job_uuid, tech_slug, source)
); -- 实现注：PK 含 source，使 LLM 层可补充规则层未覆盖词，且 llm 积压口径可清零
CREATE TABLE tech_taxonomy (     -- 别名归一：golang→go, k8s→kubernetes, gcp→google-cloud
  alias TEXT PRIMARY KEY, tech_slug TEXT NOT NULL, tech_kind TEXT NOT NULL
);

-- ── 富化完成标记：抽取结果为空也算"已处理" ─────────────────────────────
-- job_tech 表达不了"处理过但零命中"：LLM 结果全部落 unmapped_tech 的职位
-- 不产生任何 job_tech 行，曾导致 ~1.4k 个职位永远留在积压里、每晚被
-- enrich_cache 重放一遍（llm_cached ≈ backlog）。积压口径 = 既无 job_tech
-- 行也无本标记，因此上线时存量已富化职位无需回填。
CREATE TABLE enrich_done (
  job_uuid TEXT NOT NULL REFERENCES job(uuid),
  source   TEXT NOT NULL,          -- rule | llm
  done_at  TEXT NOT NULL,
  PRIMARY KEY (job_uuid, source)
);

-- ── SSOC → role_family 映射（先测量再定义，Phase 0 交付物）───────────────
CREATE TABLE ssoc_taxonomy (
  ssoc_code   TEXT PRIMARY KEY,   -- 5 位
  role_family TEXT NOT NULL,      -- Backend/Frontend/.../Other-IT
  note        TEXT                -- 人工核定备注
);

-- ── 跨源/跨刷新去重：canonical 身份的证据与归组（Phase 1 仅建表预留）────
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

-- ── LLM 富化无法归一的词（分类体系演进入口，§7）────────────────────────
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

## 3. 归档与容量

- `description` HTML 正文**只存归档不进 DB**（DB 只留 `description_sha256`），需要正文时按 `raw_path` 回读。这是把 DB 体积压到 1/7 的关键决定。
- 日增量归档为**全类目**（v2.1，理由见 [02](02-design.md) §4.1）；周对账不归档完整对象，**只归档它从未入库过的那几条**（[02](02-design.md) §4.1 例外条款）。下表的 PVC 核算依赖这一条：对账若重复归档整块在架数据，年增变成 ~7.8GB，10Gi 约 15 个月写满。

| 项 | 估算 | 依据 |
|---|---|---|
| 候选职位年增（入库） | ~145,000 行 | 日增 ~3,000 × IT 占比 13% × 365 |
| 日增量归档（**全类目**） | raw ~7.7GB/年 → gzip **~1.5GB/年** | 3,000 条/天 × 7.0KB [已验证]，压缩比 ~5:1 |
| 首跑基线快照（一次性） | ~120MB gz | 86,678 条 × 7.0KB |
| `jobs.db` | ~150MB/年 | 每行结构化字段 ~1KB，正文不入库 |
| **PVC 规格** | **10Gi** | 5 年 ≈ 7.5 + 0.75 + 0.12 ≈ **8.4GB**；节点余 42.7GB |

## 4. SWE 口径（`is_candidate` / `is_swe`）

两级谓词，**宽进严出**：

- **`is_candidate`（入库谓词）**：三层判定的**并集**（宽口径）。决定哪些职位解析进 DB。
- **`is_swe`（统计谓词）**：Phase 0 人工核定后的严口径。决定哪些职位进周报指标。
- 口径收紧只需重算 `is_swe` 标志；口径扩展超出 `is_candidate` 时从归档回放。

三层判定，顺序优先、可解释、可回溯，**命中层级随结果一并记录**（便于事后调整口径时重算）：

1. **SSOC 主判**：`ssoc_code` 前 3 位 ∈ 白名单。SSOC 2020 与 ISCO-08 对齐，`251` = Software and Applications Developers and Analysts。实测样本：`25121`（Golang Developer）、`21222`（Data Engineer）。
   → **Phase 0 交付物：SSOC → role_family 映射表**。先按频次导出 IT 类目下全部 ssoc_code，人工核定一次（预计 30–50 个码），落 `ssoc_taxonomy`。**不要凭猜测硬编码——先测量再定义。**
2. **类目辅判**：`categories[].category == 'Information Technology'`。
3. **标题兜底**：正则匹配 engineer/developer/programmer/SRE/architect 等，捕捉挂在 Engineering/Professional Services 类目下的 SWE 岗。

## 5. 派生维度

| 维度 | 推导规则 |
|---|---|
| `seniority` | `position_level` + `min_years_exp` + 标题关键词三者投票；冲突时以标题为准（实际招聘信号最强） |
| `role_family` | `ssoc_taxonomy` 主判 + 标题关键词修正（如 SSOC 相同但标题含 "Frontend"） |
| `work_mode` | `flexibleWorkArrangements[]` 有值 → Hybrid/Remote；空 → Onsite（**标注为推断值**，该字段填写率 Phase 0 测量） |
| `company_type` | `ssic_code` + `employee_count` + 人工规则表（如 UEN 前缀 T→政府相关） |

## 6. 指标计算口径（一次定死，写进代码注释）

- **新增量**：统计周内出现的**去重 canonical 职位数**。同 `uuid` 只计一次；刷新/重贴按 `original_posting_date` 归属（防日增灌水）；**reopen（closed_at 清 NULL）不产生新增**；跨源重复按 `canonical_fp` 归并（多源启用后）。
- **在架量**：`closed_at IS NULL AND (expiry_date IS NULL OR expiry_date >= 周末)`。
- **技术频次**：出现该技术的**职位数**（非词频），按 `job_tech` distinct。
- **薪资中位数**：`(salary_min + salary_max) / 2` 的中位数，仅 `salary_hidden=0` 且 `salary_type='Monthly'`；其他币种/周期单独统计不混算；跨源先归一 `salary_type`（见 [06](06-multi-source.md) §5）。
  - **偶数样本取上中位数**（`vals[n/2]`）而非上下两值取平均。理由：口径要跨周稳定且可复现，上中位数总是一个真实出现过的薪资数字，不会造出市场上不存在的值；样本量小时也不会被两侧极值拉动。变更此口径必须全量重算历史（见本节末"口径漂移"）。
- **投递竞争度**：`application_count / max(vacancies, 1)`。

刷新/重贴判定信号表（与 [02](02-design.md) §4.1 upsert 逻辑对应）：

| 现象 | 信号 | 判定 |
|---|---|---|
| 同 uuid 刷新 | `repostCount`↑，hash 不变 | 刷新，按原始日期归属，不计新 |
| 同 uuid 内容小改 | 同 uuid，hash 变 | 改版，`original_posting_date` 不变 → 仍不计新 |
| 复制成新 uuid | 新 uuid + fp 命中 | 重贴，走 `job_repost` 归组 |
| 真新职位 | 新 uuid + fp 未命中 | 新职位，计入 |

## 7. 分类体系演进闭环

```
LLM 输出 → tech_taxonomy 归一失败 → unmapped_tech（计数累积）
    → 每周人工审阅（周报 Data Quality 章节列出 Top 未映射词）
    → 增补 tech_taxonomy 别名 → 下轮 enrich 自动生效
    → 需要回填历史时：按 enrich_cache 重放归一（不重调 LLM）
```

`ssoc_taxonomy` 同理：新出现的 ssoc_code 在周报中列出，人工核定后入表。

## 8. 可选扩展表（默认不建，Phase 2+ 按需）

`view_count` / `application_count` 是原地覆盖的累计值，生命周期轨迹会丢失。若要做"需求侧信号"的时间序列分析（某职位一周内浏览量增速等），周对账时快照：

```sql
CREATE TABLE job_stats_snapshot (
  job_uuid    TEXT NOT NULL REFERENCES job(uuid),
  snapped_at  TEXT NOT NULL,      -- 对账日期
  view_count  INTEGER,
  application_count INTEGER,
  PRIMARY KEY (job_uuid, snapped_at)
);
```

成本：候选 ~11k 行/周 ≈ 570k 行/年，SQLite 无压力。当前周报只用最新值，**先不建**。
