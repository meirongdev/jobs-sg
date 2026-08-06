# 面向求职者的站点重构设计

> 本设计由 brainstorming skill 产出，2026-08-07。实现计划另见 `docs/superpowers/plans/`。
> 上游文档：[docs/01-requirements.md](../../01-requirements.md)（需求与口径）、[docs/03-data-model.md](../../03-data-model.md)（字段级口径）、[docs/02-design.md](../../02-design.md)（架构）。

**Goal:** 把 jobs-sg 的对外输出从「运维/自用视角的采集与周报系统」重构为「面向新加坡 SWE 求职者的市场情报站点」——按求职者真正会问的问题重排信息架构，并补上现在缺失或口径有偏的高价值统计。

**Scope:** Phase A 交付 4 个统计页（`/`、`/tech`、`/pay`、`/companies`）+ 周报重排 + `/daily` 降级为 `/ops`。Phase B（可浏览职位列表 `/jobs`）**缓做**，前置条件见 §6。

**非目标:** 不做候选人匹配/简历分析；不做数据集导出或对外 API；不扩数据源（MCF 仍是唯一源）；不改采集与富化管线（所有新指标用现有表算得出来）。

---

## 1. 定位变更（先改文档，再写代码）

`docs/01-requirements.md` 现在写的是「用户就是维护者本人」「周报只发布聚合统计」。用户群改为外部求职者后，这两条必须重新定调，否则代码会违反本仓库自己的红线。

### 1.1 Phase A 需要的改动

| 位置 | 改动 |
|---|---|
| §1 目标 | 用户从「维护者本人」改为「新加坡 SWE 求职者（维护者是首要读者之一）」。保留「数字必须可信：口径可解释、可回溯、跨周可比」——它从自用要求升级为对外承诺。 |
| §2 周报章节定义 | 改写为「页面定义」，对齐 §2 的路由表；周报退化为其中一个不可变归档视图。 |
| §5 合规红线 | 新增一条：每个对外页面必须标注数据延迟（最长 24h），不得让读者误读为实时职位源。其余红线（只访问公开数据、UA 透明、不落个人字段、不转售不再分发）**保持不变**。 |

Phase A 全部页面仍然只输出聚合量，因此 §5「只发布聚合统计」这条**在 Phase A 内无需放宽**。唯一逐条列职位的页面是 `/ops/{date}`，它已从主导航移除，属运维排障视图。

### 1.2 Phase B 才需要的改动（本次不做，先记账）

`/jobs` 上线时 §5 需要改写为受约束的逐条展示，条款如下（实现前再定稿）：

1. 只展示公开法人/职位字段（现有 `mcf.Job` 已不建模 `createdBy`/`emailRecipient`，继续保持）；
2. 每条职位必须回链 MCF 原页——本站是索引与分析视图，不是数据集副本；
3. 不提供批量导出、数据集 API 或打包下载；
4. `robots.txt`：`/jobs` 放行索引，`/ops/` 继续 `Disallow`。

---

## 2. 页面信息架构

### 2.1 路由

| 路由 | 回答的问题 | 渲染 | 阶段 |
|---|---|---|---|
| `GET /` | 现在热不热 | 现算，60s 缓存 | A |
| `GET /tech` | 学什么最划算 | 现算 | A |
| `GET /pay` | 我值多少 | 现算 | A |
| `GET /companies` | 投谁 | 现算 | A |
| `GET /w/{week}` | 历史周报（不可变归档） | 静态，`report` CronJob 产出 | 现有，重排 |
| `GET /ops`、`GET /ops/{date}` | 采集状态 | 现算 | 现有，改名 |
| `GET /daily`、`GET /daily/{date}` | — | 301 → `/ops`、`/ops/{date}` | A |
| `GET /jobs` | 现在投什么 | 现算，分页 | **B（缓做）** |
| `/metrics`、`/healthz`、`/robots.txt` | — | 不变 | 现有 |

主导航只含 `/`、`/tech`、`/pay`、`/companies` 四项 + 历史周报入口。`/ops` 不进主导航，只在页脚以「数据新鲜度」链接出现。

### 2.2 为什么新页现算、只有周报静态

- 镜头 × 维度是组合爆炸，预渲染不了。
- 「在架岗位数」对求职者必须是当前值。现在 `/` 直接 serve `latest.html`，最长陈旧 7 天，对求职者是硬伤。
- 周报保持静态：它是已发布的不可变归档，也是 Telegram 的链接目标；合规上「定期发布的聚合报告」这个定位最干净。
- 沿用 `internal/web/cache.go` 现有的 `pageCache`（60s TTL、LRU），与 `/ops` 同一机制。

### 2.3 镜头（全站通用筛选）

`?exp=` 与 `?role=` 贯穿全部统计页，进 URL、可分享。所有页面的数字按镜头**重算**，不是前端过滤。

```
exp=0-2      → min_years_exp IS NOT NULL AND min_years_exp <= 2
exp=3-5      → min_years_exp BETWEEN 3 AND 5
exp=6+       → min_years_exp >= 6
exp=unstated → min_years_exp IS NULL
role=<Backend|Frontend|Fullstack|Mobile|Platform|SRE|Data|AI-ML|Security|Other-IT>
```

- 人群不作为页面切分维度：应届生与跳槽者问的是同一批问题，只是经验档不同。按人群分页会让薪资/竞争度/技术排名在多页重复，后续改口径要改多处。
- 值走**白名单校验**，非法值返回 400 而非静默忽略：既是正确性，也防缓存键被灌爆（白名单下每页缓存键上限 = 5 exp 值 × 11 role 值 = 55，含空值组合）。
- 缓存键 = 页面名 + 镜头规范化串。

---

## 3. 指标口径

本节是设计的核心。比「多加图表」更重要的是 §3.7 列出的 6 处口径修正——它们决定数字是否**可信**，而不只是**更多**。

所有窗口一律 SGT 日历期，与现有 `WeekBounds`/`DayBounds` 约定一致。滚动窗统一 **90 天**（不为不同指标各设窗长，避免无谓的不一致），锚点为请求时刻所属 SGT 日的日末，即 `[今日日末 − 90 天, 今日日末)`。

### 3.1 技术动量（`/tech` 主图）

```
share_W(t) = 本周提到 t 的新增 SWE 岗位数 / 本周已富化的新增 SWE 岗位数
momentum(t) = share_W(t) − mean(share_{W-1..W-4}(t))     单位 pp
```

- **分母必须是「已富化」岗位**：`EXISTS(job_tech) OR EXISTS(enrich_done)`（与 `store.EnrichBacklogCount` 的 backlog 定义互补）。用全部岗位作分母时，enrich backlog 会系统性压低所有技术的占比。
- **W = 最后一个已完成 ISO 周**，不含进行中的当周——当周永远是部分数据，会让每个技术都显示暴跌。「已完成」= 该周周日 24:00 SGT 已过；页面显式标注所报周次。
- **用 pp（百分点）而非相对百分比**：相对值会让 1→3 条的技术显示 +200% 排到榜首。榜单按 pp 排序，同时并列展示 share 与绝对计数，让读者自己判断样本厚度。
- 抑制门槛：`本周计数 < 10` 或 `已完成周数 < 5` → 不参与升降榜（见 §5）。
- 展示：升温 Top 10 / 降温 Top 10 + 需求排名 Top 30。
- 数据来源为现算（5 次按周的索引聚合，60s 缓存）。同时在 `weekly_metric` 新增 `tech_share` 与 `swe_enriched` 两个 metric 行，作为历史审计与「口径变更须全量重算」（docs/01 §4）的载体；**展示只走现算这一条路径**，物化仅供审计与周报。

### 3.2 薪资溢价（`/tech`）

```
premium(t) = median(提到 t 的岗位薪资) / median(全体岗位薪资) − 1
```

- 窗口：滚动 90 天。样本限定 `salary_hidden=0 AND salary_type='Monthly' AND salary_min IS NOT NULL AND salary_max IS NOT NULL`，取 `(salary_min+salary_max)/2`。
- 抑制门槛：该技术公开薪资样本 `n < 20`。
- **必标偏差——资历混杂**：Senior 岗更常提 Kubernetes，溢价里混着资历。因此溢价**跟随镜头重算**：`?exp=3-5` 下看到的是同经验档内的溢价，这才可行动。无镜头时展示全体值并在图旁标注混杂来源。

### 3.3 薪资分位数与经验阶梯（`/pay`）

- **p25/p50/p75 用最近秩取值**：升序取 `vals[floor(q*n)]`，与现有上中位数（`vals[len(vals)/2]`，即 `q=0.5`）同一约定——每个报出的数字都是真实登过的薪资，不是插值算出来、市场上没人开过的数。该式为「上侧」最近秩：小样本下 p75 可能取到最大值（如 `n=4` 时 `floor(3)=3` 即最大值），这是有意为之的一致性选择，而非边界 bug；`n < MinSalarySamplesPerCell` 的格子本就被抑制。
- **网格**：资历（Intern/Junior/Mid/Senior/Staff+/Lead/Manager）× 方向（role_family）。单元格 `n < 5` 渲染 `—(n=3)`，不出数字。既避免伪精度，也避免变相暴露单个雇主的挂牌薪资。
- **经验阶梯**：`min_years_exp` 分档 `0 / 1-2 / 3-5 / 6+ / 未标注`，各档 p25/p50/p75。回答「再熬 2 年能涨多少」。
- **薪资透明率**：`salary_hidden=0 AND salary_min IS NOT NULL` 的占比，整体 + 按 company_type 拆分。

### 3.4 入门门槛（`/tech`、`/pay`、`/companies` 的入门镜头 + `/` 卡片）

入门岗定义（单一显式谓词）：

```sql
(min_years_exp IS NOT NULL AND min_years_exp <= 2)
OR (min_years_exp IS NULL AND seniority IN ('Intern','Junior'))
```

- 给**绝对数**而非只给占比：「本周 37 个 0-2 年岗位」比「18.4%」可行动得多。按方向拆分。
- 入门岗最常要求的技术 Top 15——与全体排名不同，这才是给应届生的清单。
- 每个技术的**入门友好度** = 提到该技术的岗位中入门岗占比（`/tech` 表格列）。统计窗与薪资溢价同为滚动 90 天——同一张表里两列各用一套窗口，数字会悄悄失去可比性。
- 在架入门岗数：`closed_at IS NULL AND (expiry_date IS NULL OR expiry_date >= now)`。

### 3.5 岗位寿命（`/companies`）

```sql
days = julianday(date(closed_at)) - julianday(date(posting_date))
```

- 中位数 + 分档占比（`<7 / 7-14 / 15-30 / 30-60 / 60+` 天）。
- **必标偏差——右删失**：只统计**已下架**岗位，仍在架的不计入，结果系统性偏短。页面标题即写「已下架岗位的挂牌天数」，不写「岗位平均存活时间」。
- 精度下限 1 天（日批采集），标注之。
- 幽灵岗信号：在架且挂牌 > 60 天的占比、`repost_count > 0` 的占比。

### 3.6 竞争度分层（`/`、`/companies`）

```sql
apps_per_day = application_count / max(1, julianday(date(last_seen_at)) - julianday(date(posting_date)) + 1)
```

- 按 方向 × 资历 给 `apps_per_day` 的**中位数**（非均值）。
- 目的：找出「需求高但投的人少」的窗口——周新增量高而竞争度低的方向。
- 同法处理 `view_count` → `views_per_day`，并给 投递/浏览 转化率。

### 3.7 口径修正清单（相对现状的实质改动）

| # | 现状 | 问题 | 改法 |
|---|---|---|---|
| 1 | `no_exp_ratio` 用 `min_years_exp IS NULL OR = 0` | 把「明确不要求经验」和「没写」算成一类。对求职者这是「我能投」与「不知道」的区别 | 拆成 `0` 与 `未标注` 两档（§3.3、§3.4） |
| 2 | `avg_views`/`avg_apps` 直接对累计值取均值 | `view_count`/`application_count` 是采集时刻累计值：第 1 天抓到的天然低、第 30 天抓到的天然高。现指标实际在量「岗位有多老」 | 归一化为日均（§3.6） |
| 3 | 技术频次分母为全部岗位 | enrich backlog 系统性压低所有技术占比 | 分母改为已富化岗位（§3.1） |
| 4 | 单一 `salary_median`，无样本披露 | 中位数实为有偏子集（fixture 中 20% 岗位隐藏薪资）的中位数，读者看不出来 | p25/p50/p75 + 每处薪资数字旁固定挂「基于 N 条公开薪资（占 X%）」（§3.3） |
| 5 | 无寿命指标 | — | 新增，且明确标注右删失（§3.5） |
| 6 | 无动量 | 只有绝对排名，看不出升降温 | 新增，用 pp + 最小计数门槛（§3.1） |

---

## 4. 架构与模块边界

### 4.1 现状问题

`internal/report` 同时承担 SQL 计算（`metrics.go`、`daily.go`）与 HTML 渲染（`render.go`、`daily_render.go`），`baseCSS` 与 SVG 组件也长在 `render.go` 里。新指标要被**静态周报和现算页面共同消费**，必须下沉到两者之下。

### 4.2 目标结构

```
internal/metric/            纯聚合层，零 HTML；每个函数返回 plain struct
  window.go                 Window：SGT 日/周/滚动 90 天边界（复用现有 WeekBounds/DayBounds 约定）
  lens.go                   Lens：白名单解析 + SQL 谓词构建
  coverage.go               Coverage + 抑制门槛常量（§5）
  market.go                 /  ：在架量、周新增、WoW、12 周趋势、方向/资历/工作模式分布
  tech.go                   /tech：需求排名、动量、溢价、入门友好度
  pay.go                    /pay ：分位数网格、经验阶梯、透明率
  company.go                /companies：持续招聘者、类型分布、各家竞争度与透明率
  lifecycle.go              寿命、幽灵岗信号、竞争度分层
internal/view/              共享视觉层：baseCSS、barSVG、columnSVG、抑制值渲染
internal/web/               路由、镜头解析、缓存、handler
internal/report/            weekly_metric 物化 + Telegram + 周报静态 HTML（消费 metric + view）
```

`internal/report/render.go` 的 `baseCSS`、`barSVG`、`columnSVG`、`chartScale` 迁入 `internal/view`，两处调用点（周报、`/ops`）改为引用——避免站点视觉在两套模板里分叉。

### 4.3 新增索引

`job_tech` 主键是 `(job_uuid, tech_slug, source)`，按 `tech_slug` 查是全表扫，而 `/tech` 会逐技术查询——第一条是必须的。

```sql
CREATE INDEX IF NOT EXISTS idx_job_tech_slug   ON job_tech(tech_slug, job_uuid);
CREATE INDEX IF NOT EXISTS idx_job_active_list ON job(is_swe, closed_at, posting_date);
CREATE INDEX IF NOT EXISTS idx_job_salary      ON job(is_swe, salary_type, salary_hidden, posting_date);
CREATE INDEX IF NOT EXISTS idx_job_exp         ON job(is_swe, min_years_exp);
```

schema 变更按仓库既有规则安全（archive-before-parse：DB 可从 `raw/*.jsonl.gz` 重建，见 `internal/store/schema.go` 头注释）。

### 4.4 资源预算

常驻内存目标 ≤64Mi 不变（docs/02）。

- 分位数需要每格取值列表：90 天窗内 `is_swe=1` 且公开月薪的岗位约数千行，一次查询后在 Go 内分组取秩，可接受。
- `pageCache` 保持 64 条上限。现实现满容时**整体清空**而非 LRU（`internal/web/cache.go`：条目是纯派生数据、重建毫秒级，这个规模下 LRU 簿记是负收益）——保持现状。镜头白名单把每个统计页的键空间限制在 ≤55。单条 HTML 大小上限推迟到 Phase B 随 `/jobs` 一起做：大页面才是它的动机。
- Phase B 的 `/jobs` 必须分页（50/页）并对第 2 页起绕过缓存——不在本次范围。

### 4.5 周报与 Telegram 重排

周报章节顺序改为：**快报 → 技术（含动量）→ 薪资分位 → 入门门槛 → 竞争与寿命 → 雇主 → 数据说明**。现有第 8 章 Data Quality 压缩为页脚一行状态 + 链到 `/ops`。Telegram 周推同步改口播：升温技术 Top 3、入门岗绝对数、各经验档薪资带、一行数据新鲜度、周报链接。

---

## 5. 「数据不足」是一等状态

每个 metric 模型带 `Coverage`：

```go
type Coverage struct {
    WeeksAvailable int    // 已完成且有数据的 ISO 周数
    WeeksRequired  int
    Samples        int
    Suppressed     bool
    Reason         string // "history" | "sample" | ""
}
```

抑制门槛集中为常量，单点可改：

| 常量 | 值 | 用途 |
|---|---|---|
| `MinWeeksForMomentum` | 5 | 动量需 1 当期 + 4 基线周 |
| `MinTechCountForMomentum` | 10 | 单周计数下限 |
| `MinSalarySamplesPerTech` | 20 | 溢价 |
| `MinSalarySamplesPerCell` | 5 | 分位数网格单元格 |
| `MinPostingsPerCompanyStat` | 5 | 各家竞争度/透明率 |

渲染规则：被抑制的数字输出 `—(n=3)` 或「需 5 周历史，当前 2 周」，**永不渲染成 0**。

这条不是锦上添花。本仓库尚未部署（docs/05 Phase 1 DoD 未达成），上线后**前 5 周动量榜必然为空**，寿命指标要等第一批岗位下架才有值。不把这个状态设计进去，站点上线第一天看起来就是坏的。

---

## 6. 阶段划分与 Phase B 前置条件

### Phase A（本次实现）

4 个统计页 + 镜头 + `internal/metric`/`internal/view` 抽取 + 新索引 + 周报重排 + Telegram 改口播 + `/daily` → `/ops` + docs/01 §1.1 改动 + fixture 扩展（§7.1）。

### Phase B（缓做）：`/jobs` 可浏览职位列表

**前置验证——MCF 回链格式未知。** `internal/mcf/types.go` 的 `Job` 没有任何 URL/slug 字段（`_links` 只建模在 `Page` 层，且无 Job 级链接），`uuid` 是唯一句柄。回链是 §1.2 条款 2 的前提，也决定 `/jobs` 到底有多可用——**没有可用链接的职位列表价值大打折扣**。

开工前先做一次实证：验证 `https://www.mycareersfuture.gov.sg/job/{uuid}` 是否 302 到正确岗位；若否，退路是链到带 `jobPostId` 的 MCF 搜索页。结论落进 `docs/06-multi-source.md` 或新的勘查记录。此项未结论前 `/jobs` 不开工。

---

## 7. 测试策略

### 7.1 fixture 扩展（前置）

现有 `testdata/fixture/jobs.jsonl` 100 条只覆盖 **2026-07-28…08-03 共 7 个日历日**，且横跨两个**不完整** ISO 周（W31 缺周一、W32 仅周一），`status` 全为 `Active`、无 `closed_at`。动量（需 5 个已完成周）与寿命（需 `closed_at`）都无法测。

扩展 `scripts/genfixture`（确定性、可复现，现有约束不变）：

- 跨 **6 个完整 ISO 周**分布 `metadata.newPostingDate`（动量需 1 当期 + 4 基线周，另留 1 周余量以测「当周被排除」）；
- 掺入一批已下架岗位（覆盖 `<7 / 7-14 / 15-30 / 30-60 / 60+` 各寿命档）；
- 保留现有 20% `isHideSalary` 比例（正好用于验证透明率与抑制逻辑）；
- 覆盖 `minimumYearsExperience` 为 `null` 的样本（现有 fixture 无 null，而 §3.7-1 的修正正需要它）；
- 令部分 (资历 × 方向) 单元格样本数落在 4 与 5 两侧，用于验证抑制边界。

### 7.2 测试清单

`internal/metric` 表驱动 SQL 测试，跑在从 fixture 灌入的临时 SQLite 上（沿用仓库现有 fixture 回放模式）：

- 最近秩分位数取值：报出的每个值都存在于输入样本中；
- 抑制边界：`n=4` 抑制、`n=5` 出数；
- `min_years_exp` NULL 与 0 不合并；
- 日均投递归一化：同 `application_count` 不同岗位年龄给出不同结果；
- 动量：pp 计算正确、当周被排除、历史不足时降级为 `Reason="history"`；
- 溢价：跟随镜头重算、样本不足时抑制；
- 寿命：只计入 `closed_at IS NOT NULL`；
- 富化分母：backlog 岗位不进分母；
- 滚动 90 天窗边界（对齐现有 `TestWeekWindowDateOnlyBoundaries` 的 date-only 比较陷阱：`posting_date` 在线上是 date-only，边界须为 SGT 午夜）。

`internal/web` 路由测：

- `/daily` → 301 `/ops`、`/daily/{date}` → 301 `/ops/{date}`；
- 镜头白名单：合法值 200、非法值 400；
- 缓存键含镜头（不同镜头不串页）。

`internal/view`：抑制值渲染为 `—(n=…)` 而非 `0`。

现有 `TestWeekWindowDateOnlyBoundaries` 等日期边界测试保持不动。

---

## 8. 验收标准

1. `/`、`/tech`、`/pay`、`/companies` 四页可用，镜头在四页间保持并正确重算数字。
2. §3.7 六处口径修正全部落地，每处有对应测试。
3. 所有抑制门槛有边界测试（4/5 两侧）。
4. 每处薪资数字旁有样本量与透明率标注；寿命图有右删失标注；溢价图有资历混杂标注。
5. 历史不足 5 周时动量区块渲染为说明文案，不是空图或 0。
6. `/daily` 与 `/daily/{date}` 301 到 `/ops` 对应路径；`/ops` 不在主导航。
7. 周报按新顺序渲染，Data Quality 收为页脚一行；Telegram 周推为求职者口播。
8. `docs/01-requirements.md` §1/§2/§5 按 §1.1 更新。
9. `make test`、`make vet` 全绿；`internal/view` 抽取后周报与 `/ops` 视觉无回归。
10. Phase B 的 `/jobs` 与 §1.2 合规条款均未进本次实现。
