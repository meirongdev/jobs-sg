# 求职者站点 Phase A-1：基座 + `/tech` 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立 `internal/metric`（纯聚合层）+ `internal/view`（共享视觉层）两个新包，并用它们交付第一个求职者页面 `/tech`（需求排名 + 四周动量 + 薪资溢价 + 入门友好度），同时把 `/daily` 降级为 `/ops`。

**Architecture:** 新增 `internal/metric` 承载所有 SQL 聚合（零 HTML），`internal/view` 承载模板与 SVG 组件（从 `internal/report/render.go` 抽出），`internal/web` 只做路由/镜头解析/缓存。`/tech` 按请求现算并复用现有 `pageCache`。`report` 包保留周报物化与 Telegram，改为消费 `metric` + `view`。

**Tech Stack:** Go 1.26、标准库 `html/template` + `net/http`、modernc.org/sqlite、现有 `store.DB`。

**上游规格:** [docs/superpowers/specs/2026-08-07-jobseeker-facing-site-design.md](../specs/2026-08-07-jobseeker-facing-site-design.md)。本计划覆盖该 spec 的 §2.3 镜头、§3.1 动量、§3.2 溢价、§3.4 入门友好度、§4.2 包结构、§4.3 索引、§5 抑制状态、§7.1 fixture 扩展（日期部分）。

**本计划不做（留 Phase A-2）:** `/`、`/pay`、`/companies` 三页；§3.3 分位数网格与经验阶梯；§3.5 寿命；§3.6 竞争度分层；周报重排与 Telegram 改口播；`docs/01` 文档改动。`/jobs`（Phase B）完全不在范围内。

**分支:** 本仓库约定不直接提交 `main`。执行前先 `git switch -c feat/jobseeker-tech-page`。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `scripts/genfixture/main.go` | **改**：postingDate 铺满 6 个完整 ISO 周；新增 `minimumYearsExperience: null` 模板行 |
| `testdata/fixture/jobs.jsonl` | **重新生成**：360 行（6 周 × 60） |
| `internal/classify/fixture_test.go` | **改**：期望行数 100 → 360 |
| `internal/store/schema.go` | **改**：新增 4 个索引 |
| `internal/metric/kv.go` | **新**：`KV` 规范类型（原 `report.KV`） |
| `internal/metric/window.go` | **新**：`Window`、SGT 日/ISO 周/滚动窗 |
| `internal/metric/lens.go` | **新**：`Lens` 白名单解析 + SQL 谓词 |
| `internal/metric/coverage.go` | **新**：`Coverage` + 抑制门槛常量 + 入门岗谓词 |
| `internal/metric/percentile.go` | **新**：最近秩分位数 |
| `internal/metric/tech.go` | **新**：`/tech` 的全部聚合 |
| `internal/view/css.go` | **新**：`BaseCSS`（从 `report/render.go` 迁入） |
| `internal/view/chart.go` | **新**：`Bar`、`Column`、`chartScale`（从 `report` 迁入） |
| `internal/view/value.go` | **新**：`Suppressed` 抑制值渲染 |
| `internal/view/tech.go` | **新**：`/tech` 模板与 `TechPage` |
| `internal/report/render.go` | **改**：删除已迁出的 CSS/SVG，改用 `view`；`KV` 改为别名 |
| `internal/report/daily_render.go` | **改**：FuncMap 改指向 `view`；导航 `/daily` → `/ops` |
| `internal/web/server.go` | **改**：注册 `/tech`、`/ops`、`/ops/{date}` 与 `/daily` 301 |
| `internal/web/tech.go` | **新**：`/tech` handler + 镜头解析 |
| `internal/web/daily.go` | **改**：robots 改 `Disallow: /ops/`；handler 复用 |
| `internal/web/web_test.go` | **改**：`/daily` 断言改 `/ops` + 新增 301 测试 |

---

## Task 1: fixture 铺满 6 个完整 ISO 周

现有 fixture 的 100 行只落在 2026-07-28…08-03（7 个日历日、两个不完整 ISO 周），动量需要 5 个**已完成**周才算得出来。新 fixture 覆盖 2026-W27…W32（周一分别是 06-29 … 08-03），每周 60 行，共 360 行。测试用固定时钟 `2026-08-10`（W33 周一），此时 `LastCompletedWeek` = W32，基线为 W31…W28，五个窗口全部有数据。

同时补 `minimumYearsExperience: null` 的模板行——现有 22 个模板全部有明确年限，而 spec §3.7-1 的口径修正（NULL 与 0 不合并）正需要 null 样本。

**Files:**
- Modify: `scripts/genfixture/main.go`
- Modify: `internal/classify/fixture_test.go:66-68`
- Regenerate: `testdata/fixture/jobs.jsonl`

- [ ] **Step 1: 让 `row` 支持 null 年限，并加两个 null 模板行**

`row.minYr` 是 `int`，无法表达 null。改为 `*int`：

在 `scripts/genfixture/main.go` 中把 `type row struct` 的 `minYr int` 改为 `minYr *int`，并在 `rows` 切片里把每个字面量的年限包一层 `intP(...)`。例如第一行：

```go
{"Backend Engineer (Go)", "25121", "Information Technology", "Professional", intP(3), []string{"hybrid"}, "Building APIs with Go, Kubernetes and PostgreSQL.", "ShopBack", "201601111G", "62011", 400},
```

在 `rows` 末尾（`Customer Service Officer` 之后）追加两行 null 年限样本：

```go
	// 年限未标注：spec §3.7-1 要求 NULL 与 0 分开统计，现有模板全部有明确年限
	{"Software Engineer", "25121", "Information Technology", "Professional", nil, []string{"hybrid"}, "Java, Spring Boot, AWS, Kubernetes.", "Zuellig Pharma", "201801111Y", "62011", 2000},
	{"Junior Frontend Engineer", "25131", "Information Technology", "Fresh/entry level", nil, []string{"onsite"}, "React, TypeScript, HTML, CSS.", "Ryde", "201501111M", "62011", 120},
```

`MinimumYearsExperience` 赋值处从 `intP(r.minYr)` 改为直接 `r.minYr`：

```go
				MinimumYearsExperience:   r.minYr,
```

- [ ] **Step 2: 把 postingDate 铺到 6 个完整 ISO 周**

替换 `main()` 的循环头部。原来 `now.Add(-(i%7) * 24h)` 只铺 7 天；改为按 60 行一周、周内按 `i%7` 铺满周一到周日：

```go
func main() {
	rng := rand.New(rand.NewSource(20260803))
	// 2026-06-29 是 ISO 2026-W27 的周一；6 周 × 60 行铺到 W32（周一 2026-08-03）。
	// 测试用固定时钟 2026-08-10（W33 周一）：LastCompletedWeek=W32，基线 W31..W28，
	// 五个窗口全部有数据。日期是 date-only，与线上 API 一致（testdata/live）。
	firstMonday := time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC)
	const weeks, perWeek = 6, 60
	var jobs []mcf.Job
	for i := 0; i < weeks*perWeek; i++ {
		r := rows[i%len(rows)]
		week, dayInWeek := i/perWeek, i%7
		posting := firstMonday.AddDate(0, 0, week*7+dayInWeek)
		uuid := fmt.Sprintf("%032x", rng.Uint64()+rng.Uint64()*1<<32)
```

同一循环体内，`Metadata` 的两个日期字段改为基于 `posting`：

```go
				NewPostingDate:            posting.Format("2006-01-02"),
				ExpiryDate:                posting.AddDate(0, 0, 30).Format("2006-01-02"),
```

删除 `main()` 顶部原有的 `now := time.Date(2026, 8, 3, ...)` 一行（已被 `firstMonday` 取代）。

- [ ] **Step 3: 重新生成 fixture 并核对分布**

Run:
```bash
go run ./scripts/genfixture && python3 -c "
import json,collections,datetime
w=collections.Counter(); nullexp=0
for line in open('testdata/fixture/jobs.jsonl'):
    j=json.loads(line)
    d=datetime.date.fromisoformat(j['metadata']['newPostingDate'])
    w[d.isocalendar()[:2]]+=1
    if j['minimumYearsExperience'] is None: nullexp+=1
print(dict(sorted(w.items()))); print('null minYr:', nullexp)
"
```
Expected: 六个键 `(2026,27)`…`(2026,32)` 各 60；`null minYr:` 为正数（模板轮转下约 32）。

- [ ] **Step 4: 更新 fixture 行数断言**

`internal/classify/fixture_test.go` 中：

```go
	// 360 = 6 个完整 ISO 周 × 60 行（scripts/genfixture）。动量指标需要 5 个已完成
	// 周的历史，7 天的旧 fixture 撑不起来。
	if count != 360 {
		t.Fatalf("fixture count = %d, want 360", count)
	}
```

- [ ] **Step 5: 跑全量测试**

Run: `go test ./... && go vet ./...`
Expected: PASS（`TestFixtureReplay` 记录 360 jobs）

- [ ] **Step 6: Commit**

```bash
git add scripts/genfixture/main.go testdata/fixture/jobs.jsonl internal/classify/fixture_test.go
git commit -m "test(fixture): spread postings over 6 ISO weeks, add null-experience rows"
```

> 执行记录 2026-08-07：已执行（`46963d4`）。质量 review 两条 Important 以补充提交跟进：①`count != 360` 是弱断言——4 周 × 90 或平移过的 Monday 也能凑出 360——回放测试补 ISO 周形状断言（恰为 2026-W27…W32 各 60 行）；②60 % 24 ≠ 0，每个模板每周出现 2 或 3 次、逐周交替，仅在 6 周全窗平衡到每模板 15 次——生成器循环旁注明，动量类测试不得把该机械振荡读成趋势。同批修掉两处过期行数注释（main.go 头部 "~100"、fixture_test "100-row mix"）。

---

## Task 2: 新增四个索引

`job_tech` 主键是 `(job_uuid, tech_slug, source)`，按 `tech_slug` 过滤是全表扫，而 `/tech` 会逐技术查询。其余三个服务后续页面的活跃/薪资/年限过滤。

**Files:**
- Modify: `internal/store/schema.go`
- Test: `internal/store/db_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `internal/store/db_test.go`：

```go
// TestJobTechSlugIndexExists pins the reverse index on job_tech. The table's
// primary key is (job_uuid, tech_slug, source), so every per-technology query
// on /tech is a full scan without it.
func TestJobTechSlugIndexExists(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"idx_job_tech_slug", "idx_job_active_list", "idx_job_salary", "idx_job_exp",
	} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, want).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("index %s missing", want)
		}
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/store/ -run TestJobTechSlugIndexExists -v`
Expected: FAIL，四个索引都报 missing

- [ ] **Step 3: 加索引**

`internal/store/schema.go` 的 schema 是**单次多语句 Exec**，按文件顺序执行——索引不能前向引用还没 CREATE 的表。四条分两处放。

在 `CREATE INDEX IF NOT EXISTS idx_job_closed ...` 之后插入三条只引用 `job` 的：

```sql
-- Job-seeker pages filter active SWE postings by posting window.
CREATE INDEX IF NOT EXISTS idx_job_active_list ON job(is_swe, closed_at, posting_date);
-- Salary percentiles / premium scan only disclosed monthly salaries.
CREATE INDEX IF NOT EXISTS idx_job_salary      ON job(is_swe, salary_type, salary_hidden, posting_date);
-- Experience-band lens and the entry-level dashboard.
CREATE INDEX IF NOT EXISTS idx_job_exp         ON job(is_swe, min_years_exp);
```

在 `job_tech` 表定义的 `);` 之后插入（与本文件"表的索引紧跟表"的既有惯例一致）：

```sql
-- Per-technology queries (/tech) filter by tech_slug, but the table's primary
-- key is (job_uuid, tech_slug, source) — a slug lookup scans without this.
CREATE INDEX IF NOT EXISTS idx_job_tech_slug   ON job_tech(tech_slug, job_uuid);
```

> 执行记录 2026-08-07：初版计划让四条全放 `idx_job_closed` 之后，`idx_job_tech_slug` 前向引用 `job_tech`，`Migrate` 报 "no such table: main.job_tech"，包内全部测试红。实现按上文修正（仅挪动位置，索引名/列序/注释未动），本节已回写为正确指令。
>
> 执行记录（补）：质量 review 指出本节 Step 1 的名字存在性测试弱于本文件既有惯例（`queryPlan` + EXPLAIN QUERY PLAN 断言，见 `TestCrawlTimeQueriesUseIndexes`）——同名换列照样通过。已以 `TestJobSeekerQueriesUseIndexes` 补齐（`c7d2d9e`）：每个索引一条代表查询，断言用到该索引且无 `SCAN job`/`SCAN job_tech` 兜底；`idx_job_tech_slug` 的代表查询必须是 per-slug 等值形状（group-by-all-slugs 的 join 会从 `job` 侧驱动、走 PK，不碰该索引）。两条 Minor 记录在案不处理：部分索引（`WHERE is_swe=1`）收益无法静态量化，留待有真实数据后重访；schema 注释的现在时态与 spec §4.3 一致，不改。
>
> 复核通过（实证：内存库中逐个改坏索引再跑 EQP）。**已知残留，接受不修**：`idx_job_exp` 缩列成 `(is_swe)` 时测试仍绿——没有竞争索引抢走规划器，SQLite 顶着原名继续用残废索引；失败模式是变宽扫描而非全表扫，性能问题会先从 /metrics 暴露。若日后要闭合：对四个索引加 `PRAGMA index_info` 列数断言（比 EQP 更直接且零规划器依赖）。

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/schema.go internal/store/db_test.go
git commit -m "perf(store): index job_tech by slug and job by active/salary/experience"
```

---

## Task 3: `internal/metric` 的 `Window`

所有窗口都是 SGT 日历期，但列里存的是 RFC3339 UTC；`posting_date` 在线上还是 date-only。现有 `report.WeekBounds` 的注释解释了为什么边界必须是 SGT 午夜（= 周日 16:00Z）——新的 `Window` 沿用同一约定，并补上 ISO 周与滚动窗。

**Files:**
- Create: `internal/metric/kv.go`
- Create: `internal/metric/window.go`
- Test: `internal/metric/window_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/metric/window_test.go`:

```go
package metric

import (
	"testing"
	"time"
)

func TestDayBoundsAreSGTMidnights(t *testing.T) {
	// 2026-08-03 09:00 SGT -> [2026-08-02T16:00Z, 2026-08-03T16:00Z)
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, SGT)
	w := Day(now)
	if !w.Start.Equal(time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 2026-08-02T16:00:00Z", w.Start)
	}
	if !w.End.Equal(time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2026-08-03T16:00:00Z", w.End)
	}
}

func TestISOWeekOfSnapsToMonday(t *testing.T) {
	// Thursday 2026-08-06 SGT belongs to the week starting Monday 2026-08-03
	w := ISOWeekOf(time.Date(2026, 8, 6, 23, 0, 0, 0, SGT))
	if got := w.WeekLabel(); got != "2026-W32" {
		t.Errorf("label = %s, want 2026-W32", got)
	}
	if !w.Start.Equal(time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want Monday 2026-08-03 00:00 SGT", w.Start)
	}
}

func TestLastCompletedWeekExcludesTheCurrentWeek(t *testing.T) {
	// Monday 2026-08-10 is in W33; the last completed week is W32.
	w := LastCompletedWeek(time.Date(2026, 8, 10, 0, 0, 0, 0, SGT))
	if got := w.WeekLabel(); got != "2026-W32" {
		t.Errorf("label = %s, want 2026-W32", got)
	}
	// Sunday 2026-08-09 23:59 SGT is still inside W32, so W32 is NOT complete.
	w = LastCompletedWeek(time.Date(2026, 8, 9, 23, 59, 0, 0, SGT))
	if got := w.WeekLabel(); got != "2026-W31" {
		t.Errorf("label = %s, want 2026-W31", got)
	}
}

func TestPrevWeeksReturnsOldestFirst(t *testing.T) {
	w := LastCompletedWeek(time.Date(2026, 8, 10, 0, 0, 0, 0, SGT)) // W32
	prev := PrevWeeks(w, 4)
	want := []string{"2026-W28", "2026-W29", "2026-W30", "2026-W31"}
	if len(prev) != len(want) {
		t.Fatalf("got %d weeks, want %d", len(prev), len(want))
	}
	for i, lbl := range want {
		if got := prev[i].WeekLabel(); got != lbl {
			t.Errorf("prev[%d] = %s, want %s", i, got, lbl)
		}
	}
}

func TestRollingEndsAtTodaysSGTDayEnd(t *testing.T) {
	now := time.Date(2026, 8, 3, 9, 0, 0, 0, SGT)
	w := Rolling(now, 90)
	if !w.End.Equal(Day(now).End) {
		t.Errorf("end = %v, want today's SGT day end %v", w.End, Day(now).End)
	}
	if got := w.End.Sub(w.Start).Hours(); got != 90*24 {
		t.Errorf("span = %vh, want %vh", got, 90*24)
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/metric/ -v`
Expected: FAIL — 包不存在（`no Go files` 或未定义符号）

- [ ] **Step 3: 实现 `KV` 与 `Window`**

Create `internal/metric/kv.go`:

```go
// Package metric computes every job-seeker statistic with SQL only — the LLM
// never produces a number (docs/01 §4). It renders nothing: HTML lives in
// internal/view, so the static weekly report and the live pages can share one
// aggregate layer.
package metric

// KV is a labeled value. It is the canonical type for chart input; report.KV
// is an alias of it so the weekly report keeps compiling unchanged.
type KV struct {
	Key   string
	Value float64
}
```

Create `internal/metric/window.go`:

```go
package metric

import (
	"fmt"
	"time"
)

// SGT is the site timezone: every bucket is an SGT calendar period while
// timestamps are stored as UTC (docs/03 §2).
var SGT = time.FixedZone("SGT", 8*3600)

// RollingDays is the standard trailing window for salary and company stats.
// One window length for every rolling metric, on purpose — per-metric windows
// would make two numbers on the same page silently incomparable.
const RollingDays = 90

// Window is a half-open UTC interval [Start, End) derived from an SGT period.
//
// Bounds are always SGT midnights, which render as 16:00Z the previous day.
// That is load-bearing: posting_date is date-only on the live API
// ("2026-08-03"), and comparing a date-only string against these bounds is
// correct ONLY because the bound's UTC calendar date is never an in-window SGT
// date. Do NOT "simplify" any bound to UTC midnight — it shifts the window by
// a day. Pinned by report.TestWeekWindowDateOnlyBoundaries.
type Window struct {
	Start time.Time
	End   time.Time
}

// Args renders the window as SQL bind arguments, in [start, end) order.
func (w Window) Args() []any {
	return []any{w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339)}
}

// WeekLabel is the YYYY-Www ISO label of the window's first SGT day.
func (w Window) WeekLabel() string {
	y, wk := w.Start.In(SGT).ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, wk)
}

// Day returns the SGT calendar day containing t.
func Day(t time.Time) Window {
	d := t.In(SGT)
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, SGT)
	return Window{Start: start.UTC(), End: start.AddDate(0, 0, 1).UTC()}
}

// ISOWeekOf returns the SGT ISO week (Monday-based) containing t.
func ISOWeekOf(t time.Time) Window {
	d := t.In(SGT)
	// time.Weekday counts Sunday as 0; ISO weeks start on Monday.
	back := (int(d.Weekday()) + 6) % 7
	monday := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, SGT).AddDate(0, 0, -back)
	return Window{Start: monday.UTC(), End: monday.AddDate(0, 0, 7).UTC()}
}

// LastCompletedWeek returns the most recent ISO week whose Sunday 24:00 SGT has
// passed. The in-progress week is always partial data: including it would show
// every technology crashing (spec §3.1).
func LastCompletedWeek(now time.Time) Window {
	return weekBefore(ISOWeekOf(now), 1)
}

// PrevWeeks returns the n ISO weeks immediately before w, oldest first.
func PrevWeeks(w Window, n int) []Window {
	out := make([]Window, 0, n)
	for i := n; i >= 1; i-- {
		out = append(out, weekBefore(w, i))
	}
	return out
}

func weekBefore(w Window, n int) Window {
	monday := w.Start.In(SGT).AddDate(0, 0, -7*n)
	return Window{Start: monday.UTC(), End: monday.AddDate(0, 0, 7).UTC()}
}

// Rolling returns the trailing days-long window ending at the end of the SGT
// day containing now.
func Rolling(now time.Time, days int) Window {
	end := Day(now).End
	return Window{Start: end.In(SGT).AddDate(0, 0, -days).UTC(), End: end}
}
```

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/metric/ -v`
Expected: PASS（5 个测试）

- [ ] **Step 5: Commit**

```bash
git add internal/metric/kv.go internal/metric/window.go internal/metric/window_test.go
git commit -m "feat(metric): add SGT window helpers for day, ISO week and rolling ranges"
```

> 执行记录 2026-08-07：已执行（`d18ea7c`，与本节代码逐字节一致）。质量 review 两条 Important 以 `22866bf` 跟进：①`Window` 注释宣称的 date-only 安全性引用的是 `report.TestWeekWindowDateOnlyBoundaries`——那钉的是平行的 `report.WeekBounds`，`Args()` 本身零覆盖——已补 `TestArgsRenderDateOnlySafeBounds` 并改注释双引用；②report/metric 两份窗口实现已有行为差异且无收敛记录——已记入 Phase A-2 待办第 7 项。Minor 记录在案不处理：`Rolling(now, days)` 不锁死 `RollingDays`（常量注释就在旁边，改签名会波及 Task 9 已写好的调用）；`WeekLabel()` 对滚动窗可调但无意义（单一 `Window` 结构服务多种区间形状的已知取舍）。

---

## Task 4: 镜头 `Lens`

镜头是全站筛选器（spec §2.3）：经验档 + 方向，进 URL、可分享，非法值 400 而不是静默忽略。谓词片段假定查询把 `job` 别名为 `j`——所有用镜头的 SQL 必须遵守。

**Files:**
- Create: `internal/metric/lens.go`
- Test: `internal/metric/lens_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/metric/lens_test.go`:

```go
package metric

import (
	"strings"
	"testing"
)

func TestParseLensAcceptsAllowlistedValues(t *testing.T) {
	for _, tc := range []struct{ exp, role string }{
		{"", ""},
		{"0-2", ""},
		{"3-5", "Backend"},
		{"6+", "Data"},
		{"unstated", "AI-ML"},
	} {
		if _, err := ParseLens(tc.exp, tc.role); err != nil {
			t.Errorf("ParseLens(%q,%q) = %v, want nil", tc.exp, tc.role, err)
		}
	}
}

func TestParseLensRejectsAnythingElse(t *testing.T) {
	// Free-text values would let a crafted URL mint unbounded cache keys, and a
	// silently ignored filter shows numbers that do not match the URL.
	for _, tc := range []struct{ exp, role string }{
		{"0-3", ""},
		{"junior", ""},
		{"0-2'; DROP TABLE job--", ""},
		{"", "backend"}, // case matters: role_family values are capitalised
		{"", "Nonexistent"},
	} {
		if _, err := ParseLens(tc.exp, tc.role); err == nil {
			t.Errorf("ParseLens(%q,%q) = nil error, want rejection", tc.exp, tc.role)
		}
	}
}

func TestLensWhereBuildsQualifiedPredicates(t *testing.T) {
	l, err := ParseLens("3-5", "Backend")
	if err != nil {
		t.Fatal(err)
	}
	where := l.Where()
	for _, want := range []string{"j.min_years_exp BETWEEN 3 AND 5", "j.role_family = 'Backend'"} {
		if !strings.Contains(where, want) {
			t.Errorf("Where() = %q, missing %q", where, want)
		}
	}
	if !strings.HasPrefix(where, " AND ") {
		t.Errorf("Where() must be appendable to a WHERE clause, got %q", where)
	}
}

func TestEmptyLensWhereIsEmpty(t *testing.T) {
	var l Lens
	if got := l.Where(); got != "" {
		t.Errorf("empty lens Where() = %q, want \"\"", got)
	}
}

func TestUnstatedExperienceIsItsOwnBand(t *testing.T) {
	// spec §3.7-1: "no requirement" (0) and "did not say" (NULL) must never be
	// merged — for a job seeker that is "I can apply" vs "unknown".
	zero, _ := ParseLens("0-2", "")
	unstated, _ := ParseLens("unstated", "")
	if strings.Contains(zero.Where(), "IS NULL") {
		t.Errorf("0-2 band must exclude NULL, got %q", zero.Where())
	}
	if !strings.Contains(unstated.Where(), "j.min_years_exp IS NULL") {
		t.Errorf("unstated band must select NULL, got %q", unstated.Where())
	}
}

func TestLensKeyIsStableAndDistinct(t *testing.T) {
	a, _ := ParseLens("3-5", "Backend")
	b, _ := ParseLens("3-5", "Frontend")
	if a.Key() == b.Key() {
		t.Errorf("different lenses share cache key %q", a.Key())
	}
	c, _ := ParseLens("3-5", "Backend")
	if a.Key() != c.Key() {
		t.Errorf("same lens gave different keys %q vs %q", a.Key(), c.Key())
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/metric/ -run Lens -v`
Expected: FAIL — `undefined: ParseLens`

- [ ] **Step 3: 实现 `Lens`**

Create `internal/metric/lens.go`:

```go
package metric

import (
	"fmt"
	"sort"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/classify"
)

// Lens narrows every statistic on a page to one experience band and/or role
// family (spec §2.3). Personas are lenses, not pages: a fresh graduate and a
// switcher ask the same questions from different experience bands, so splitting
// by persona would duplicate the same metrics across pages.
type Lens struct {
	Exp  string // "" | "0-2" | "3-5" | "6+" | "unstated"
	Role string // "" | a classify role family
}

// expBands maps an allowlisted band to its SQL predicate. Note that "0-2"
// excludes NULL: an unstated requirement is its own band, never folded into
// "no experience required" (spec §3.7-1).
var expBands = map[string]string{
	"0-2":      "j.min_years_exp IS NOT NULL AND j.min_years_exp <= 2",
	"3-5":      "j.min_years_exp BETWEEN 3 AND 5",
	"6+":       "j.min_years_exp >= 6",
	"unstated": "j.min_years_exp IS NULL",
}

var roleFamilies = map[string]bool{
	classify.FamilyBackend: true, classify.FamilyFrontend: true,
	classify.FamilyFullstack: true, classify.FamilyMobile: true,
	classify.FamilyPlatform: true, classify.FamilySRE: true,
	classify.FamilyData: true, classify.FamilyAIML: true,
	classify.FamilySecurity: true, classify.FamilyOther: true,
}

// ExpBands lists the allowlisted experience bands in display order, for
// building the lens picker.
func ExpBands() []string { return []string{"0-2", "3-5", "6+", "unstated"} }

// RoleFamilies lists the allowlisted role families in display order.
func RoleFamilies() []string {
	out := make([]string, 0, len(roleFamilies))
	for f := range roleFamilies {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// ParseLens validates raw query values against the allowlists. Unknown values
// are an error, not a silent no-op: silently ignoring a filter renders numbers
// that contradict the URL, and free-text values would let a crafted URL mint
// unbounded cache keys.
func ParseLens(exp, role string) (Lens, error) {
	if exp != "" {
		if _, ok := expBands[exp]; !ok {
			return Lens{}, fmt.Errorf("unknown exp band %q", exp)
		}
	}
	if role != "" && !roleFamilies[role] {
		return Lens{}, fmt.Errorf("unknown role family %q", role)
	}
	return Lens{Exp: exp, Role: role}, nil
}

// Where returns a fragment appendable to a WHERE clause, or "" for the empty
// lens. Every query using it MUST alias the job table as `j`.
//
// The role value is interpolated rather than bound because these fragments are
// concatenated into queries whose bind arguments are positional; interpolation
// is safe only because the value came through the allowlist above.
func (l Lens) Where() string {
	var b strings.Builder
	if p, ok := expBands[l.Exp]; ok {
		b.WriteString(" AND " + p)
	}
	if l.Role != "" {
		b.WriteString(" AND j.role_family = '" + l.Role + "'")
	}
	return b.String()
}

// Key is the canonical cache-key fragment for this lens.
func (l Lens) Key() string { return "exp=" + l.Exp + ";role=" + l.Role }

// Label describes the active lens for page headers, or "" when unfiltered.
func (l Lens) Label() string {
	var parts []string
	if l.Exp != "" {
		parts = append(parts, l.Exp+" yrs")
	}
	if l.Role != "" {
		parts = append(parts, l.Role)
	}
	return strings.Join(parts, " · ")
}
```

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/metric/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/metric/lens.go internal/metric/lens_test.go
git commit -m "feat(metric): add allowlisted exp/role lens with SQL predicates"
```

> 执行记录 2026-08-07：已执行（`71f99f8`，逐字节一致）。质量 review 两条 Important 以 `62b8366` 跟进：①`Where()` 对 Role 的插值只靠"必须经 ParseLens"的注释约定——直接构造 `Lens{}` 可绕过白名单注入。改为 `rolePredicates`（包初始化时从 classify 常量生成谓词，请求期只做 map 查找，未验证值贡献空串），与 `expBands` 同构；②`Label()` 对 unstated 档渲染 "unstated yrs" 且零测试——改 `expLabels` 映射（"experience unstated"）+ 四个新测试（含白名单值 SQL 字面量/Key 分隔符安全守卫、混合合法性拒绝、绕过构造零贡献）。Minor 记录在案：`Label()` 留在 metric 不迁 view（/tech 模板以 `.Lens.Label` 方法调用，迁移需 FuncMap 迂回，不值）。

---

## Task 5: `Coverage` 与最近秩分位数

抑制是站点可信度的核心（spec §5）：样本或历史不足时输出 `—(n=3)`，**永不输出 0**。分位数用最近秩取值，保证报出的每个数字都是真实登过的薪资。

**Files:**
- Create: `internal/metric/coverage.go`
- Create: `internal/metric/percentile.go`
- Test: `internal/metric/coverage_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/metric/coverage_test.go`:

```go
package metric

import "testing"

func TestSampleCoverageSuppressesBelowThreshold(t *testing.T) {
	if c := SampleCoverage(4, MinSalarySamplesPerCell); !c.Suppressed || c.Reason != ReasonSample {
		t.Errorf("n=4 -> %+v, want suppressed by sample", c)
	}
	if c := SampleCoverage(5, MinSalarySamplesPerCell); c.Suppressed {
		t.Errorf("n=5 -> %+v, want not suppressed", c)
	}
}

func TestHistoryCoverageSuppressesShortHistory(t *testing.T) {
	if c := HistoryCoverage(4, MinWeeksForMomentum); !c.Suppressed || c.Reason != ReasonHistory {
		t.Errorf("4 of 5 weeks -> %+v, want suppressed by history", c)
	}
	if c := HistoryCoverage(5, MinWeeksForMomentum); c.Suppressed {
		t.Errorf("5 of 5 weeks -> %+v, want not suppressed", c)
	}
}

func TestPercentileReturnsValuesThatActuallyAppeared(t *testing.T) {
	vals := []float64{5000, 6000, 7000, 8000, 9000}
	for _, q := range []float64{0.25, 0.5, 0.75} {
		got := Percentile(vals, q)
		found := false
		for _, v := range vals {
			if v == got {
				found = true
			}
		}
		if !found {
			t.Errorf("Percentile(q=%v) = %v, which is not in the sample", q, got)
		}
	}
}

func TestPercentileMatchesTheExistingUpperMedian(t *testing.T) {
	// The weekly report used vals[len(vals)/2]; Percentile(.,0.5) must agree so
	// the two never disagree on the same data.
	for _, vals := range [][]float64{
		{1, 2, 3},
		{1, 2, 3, 4},
		{1, 2, 3, 4, 5},
	} {
		if got, want := Percentile(vals, 0.5), vals[len(vals)/2]; got != want {
			t.Errorf("Percentile(%v, 0.5) = %v, want %v", vals, got, want)
		}
	}
}

func TestPercentileEmptyAndBounds(t *testing.T) {
	if got := Percentile(nil, 0.5); got != 0 {
		t.Errorf("empty sample = %v, want 0", got)
	}
	// q=0.75 over 4 values lands on the maximum — intentional (spec §3.3), and
	// such small cells are suppressed anyway.
	if got := Percentile([]float64{1, 2, 3, 4}, 0.75); got != 4 {
		t.Errorf("p75 of 4 values = %v, want 4", got)
	}
	if got := Percentile([]float64{1, 2, 3, 4}, 1.0); got != 4 {
		t.Errorf("q=1.0 must clamp to the max, got %v", got)
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/metric/ -run 'Coverage|Percentile' -v`
Expected: FAIL — `undefined: SampleCoverage`

- [ ] **Step 3: 实现**

Create `internal/metric/coverage.go`:

```go
package metric

// Suppression thresholds. Every number the site refuses to show is refused
// here, so the bar can be raised or lowered in one place (spec §5).
const (
	// MinWeeksForMomentum is 1 reported week + 4 baseline weeks.
	MinWeeksForMomentum = 5
	// MinTechCountForMomentum keeps a 1 -> 3 posting swing off the rising board.
	MinTechCountForMomentum = 10
	// MinSalarySamplesPerTech gates the salary premium per technology.
	MinSalarySamplesPerTech = 20
	// MinSalarySamplesPerCell gates one cell of the seniority x role grid. It
	// also keeps a cell from effectively exposing a single employer's posting.
	MinSalarySamplesPerCell = 5
	// MinPostingsPerCompanyStat gates per-company competition and transparency.
	MinPostingsPerCompanyStat = 5
)

// Suppression reasons.
const (
	ReasonSample  = "sample"
	ReasonHistory = "history"
)

// EntryPredicate is the single definition of an entry-level posting (spec
// §3.4). Queries using it MUST alias the job table as `j`.
const EntryPredicate = `((j.min_years_exp IS NOT NULL AND j.min_years_exp <= 2)
	OR (j.min_years_exp IS NULL AND j.seniority IN ('Intern','Junior')))`

// Coverage says whether a number is trustworthy enough to show, and why not
// when it is not. A suppressed value renders as "—(n=3)" or an explanation,
// never as 0 — a fabricated zero is worse than an admitted gap.
type Coverage struct {
	Samples        int
	WeeksAvailable int
	WeeksRequired  int
	Suppressed     bool
	Reason         string
}

// SampleCoverage suppresses a value computed from fewer than min observations.
func SampleCoverage(n, min int) Coverage {
	c := Coverage{Samples: n}
	if n < min {
		c.Suppressed, c.Reason = true, ReasonSample
	}
	return c
}

// HistoryCoverage suppresses a trend that does not have enough weeks behind it.
func HistoryCoverage(available, required int) Coverage {
	c := Coverage{WeeksAvailable: available, WeeksRequired: required}
	if available < required {
		c.Suppressed, c.Reason = true, ReasonHistory
	}
	return c
}
```

Create `internal/metric/percentile.go`:

```go
package metric

import "math"

// Percentile returns the nearest-rank value at q (0..1) over a slice sorted
// ascending.
//
// Nearest-rank, not interpolation: every number the site reports is a salary
// that actually appeared in a posting, never an averaged figure nobody
// advertised (docs/03 §6). q=0.5 reproduces the weekly report's existing upper
// median, vals[len(vals)/2], so the two can never disagree.
func Percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Floor(q * float64(len(sorted))))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	if i < 0 {
		i = 0
	}
	return sorted[i]
}
```

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/metric/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/metric/coverage.go internal/metric/percentile.go internal/metric/coverage_test.go
git commit -m "feat(metric): add suppression coverage and nearest-rank percentile"
```

> 执行记录 2026-08-07：已执行（`9390a1f`，逐字节一致）。质量 review 两条 Important 以 `6cea52e` 跟进：①`Percentile` 的"已排序"前置条件零防御——违反不崩溃而是给出貌似合理的错数，正是本包要杜绝的失败模式。加 `sort.Float64sAreSorted` 守卫 + panic（调用者 bug 应响亮死在测试期）+ 乱序 panic 测试；②既有测试只验 `Suppressed`/`Reason` 决策、不验渲染载荷（`Samples`/`WeeksAvailable`/`WeeksRequired`）——把 `Samples` 存成阈值全套照绿但页面渲染 `—(n=5)` 而非 `—(n=4)`。补字段断言。同批四条 Minor：负 q 钳位测试、`min` 参数改名 `threshold`（遮蔽内建）、`j.` 别名约定收进包文档、`Coverage` 注明只经构造函数创建。

---

## Task 6: 抽出 `internal/view` 共享视觉层

`baseCSS`、`barSVG`、`columnSVG`、`chartScale` 现在长在 `internal/report/render.go` 里，新页面需要同一套组件。抽到 `internal/view` 后周报与 `/ops` 都引用它，站点视觉不会在两套模板里分叉。

**Files:**
- Create: `internal/view/css.go`
- Create: `internal/view/chart.go`
- Create: `internal/view/value.go`
- Test: `internal/view/view_test.go`
- Modify: `internal/report/render.go`
- Modify: `internal/report/daily_render.go`
- Modify: `internal/report/metrics.go`（KV 别名）
- Modify: `internal/report/daily_test.go`（**仅**删除 `TestChartScaleIgnoresBaselineOutlier`——它直接调用被迁走的未导出符号，其覆盖以 Step 4 第 8 点迁入 view）

- [ ] **Step 1: 写失败测试**

Create `internal/view/view_test.go`:

```go
package view

import (
	"strings"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

func TestBarHeightFollowsBarCount(t *testing.T) {
	// A fixed viewBox once clipped everything past the 11th bar while callers
	// asked for 15.
	kvs := make([]metric.KV, 15)
	for i := range kvs {
		kvs[i] = metric.KV{Key: "t", Value: float64(i + 1)}
	}
	svg := string(Bar(kvs, 15))
	if !strings.Contains(svg, "viewBox=\"0 0 520 430\"") {
		t.Errorf("15 bars must size the viewBox to 10+15*28=430, got:\n%s", svg)
	}
}

func TestColumnIgnoresALoneOutlierWhenScaling(t *testing.T) {
	// The first-run baseline scan stores the whole live market on one day; with
	// a true-max axis every ordinary day renders as a 1px stub.
	kvs := []metric.KV{{Key: "01", Value: 86000}, {Key: "02", Value: 100}, {Key: "03", Value: 90}}
	svg := string(Column(kvs, "new postings"))
	if !strings.Contains(svg, ">86000<") {
		t.Errorf("the clipped outlier must still print its real value:\n%s", svg)
	}
}

func TestSuppressedNeverRendersZero(t *testing.T) {
	sample := Suppressed(metric.SampleCoverage(3, metric.MinSalarySamplesPerCell))
	if got := string(sample); !strings.Contains(got, "n=3") || strings.Contains(got, "0") {
		t.Errorf("sample suppression = %q, want an n=3 marker and no zero", got)
	}
	hist := Suppressed(metric.HistoryCoverage(2, metric.MinWeeksForMomentum))
	for _, want := range []string{"2", "5"} {
		if !strings.Contains(string(hist), want) {
			t.Errorf("history suppression %q must state available/required weeks", hist)
		}
	}
}

func TestSuppressedIsEmptyWhenNotSuppressed(t *testing.T) {
	if got := Suppressed(metric.SampleCoverage(50, 5)); got != "" {
		t.Errorf("unsuppressed coverage = %q, want empty", got)
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/view/ -v`
Expected: FAIL — 包不存在

- [ ] **Step 3: 建 `internal/view`（迁移 + 新增）**

Create `internal/view/css.go` — 把 `internal/report/render.go` 的 `baseCSS` 常量整段搬过来并导出：

```go
// Package view holds every HTML fragment the site renders: shared CSS, SVG
// chart components and page templates. It depends on internal/metric for data
// types and on nothing else, so the static weekly report and the live pages
// share one visual system instead of drifting apart in two template sets.
package view

// BaseCSS is shared by every page: the weekly report written to disk by
// cmd/report and the pages rendered live by internal/web.
const BaseCSS = `
:root{--bg:#0f172a;--card:#1e293b;--fg:#e2e8f0;--mut:#94a3b8;--acc:#2563eb}
*{box-sizing:border-box}body{margin:0;font:15px/1.55 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:var(--bg);color:var(--fg);padding:24px}
.wrap{max-width:900px;margin:0 auto}h1{font-size:26px;margin:0 0 4px}h2{font-size:19px;border-bottom:1px solid #334155;padding-bottom:6px;margin-top:32px}
.sub{color:var(--mut)}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin:18px 0}
.card{background:var(--card);border-radius:10px;padding:14px}.card .n{font-size:26px;font-weight:700;color:#60a5fa}
.card .k{color:var(--mut);font-size:13px}table{width:100%;border-collapse:collapse;margin-top:10px}
td,th{padding:6px 8px;text-align:left;border-bottom:1px solid #334155}th{color:var(--mut);font-weight:500}
svg text{font-size:12px;fill:var(--fg)}svg .lab{fill:var(--mut)}.foot{margin-top:36px;color:var(--mut);font-size:12px}
svg.chart{width:100%;height:auto;max-width:100%}
.nav{margin:14px 0 4px;font-size:14px}.nav a{color:#60a5fa;text-decoration:none;margin-right:16px}
.nav a:hover{text-decoration:underline}.nav a.on{color:var(--fg);font-weight:600}
`

// SuppressedCSS styles the "we will not show you this number" states.
const SuppressedCSS = `
.mut{color:var(--mut)}.sup{color:var(--mut);font-variant-numeric:tabular-nums}
.up{color:#6ee7b7}.down{color:#fca5a5}
.lens{margin:10px 0 0;font-size:13px}.lens a{color:#60a5fa;text-decoration:none;margin-right:10px}
.lens a.on{color:var(--fg);font-weight:600}
.note{color:var(--mut);font-size:13px;margin:6px 0 0}
`
```

Create `internal/view/chart.go` — 把 `barSVG`、`columnSVG`、`chartScale` 从 `internal/report/render.go` 与 `daily_render.go` 整段搬过来，签名改为导出、`[]report.KV` 改 `[]metric.KV`、`topn` 内联：

```go
package view

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// TopN returns at most n leading entries.
func TopN(kvs []metric.KV, n int) []metric.KV {
	if len(kvs) > n {
		return kvs[:n]
	}
	return kvs
}

// Bar draws a horizontal bar chart of up to maxBars entries.
func Bar(kvs []metric.KV, maxBars int) template.HTML {
	if len(kvs) == 0 {
		return template.HTML("")
	}
	kvs = TopN(kvs, maxBars)
	max := 0.0
	for _, kv := range kvs {
		if kv.Value > max {
			max = kv.Value
		}
	}
	if max == 0 {
		max = 1
	}
	const barH = 22
	const gap = 6
	// Height follows the bar count — a fixed viewBox clipped everything past
	// the 11th bar while callers ask for 15. max-width pins the chart to its
	// viewBox width; stretched to the full column it scales the 12px type up.
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<svg viewBox="0 0 520 %d" style="max-width:520px" xmlns="http://www.w3.org/2000/svg" class="chart" role="img" aria-label="bar chart">`,
		10+len(kvs)*(barH+gap)))
	y := 10
	for _, kv := range kvs {
		// 340 not 400: the value label sits after the bar and rendered past the
		// right edge of the viewBox on the longest bar.
		w := 4 + int(340*(kv.Value/max))
		b.WriteString(fmt.Sprintf(`<text x="2" y="%d" class="lab">%s</text>`, y+barH-6, template.HTMLEscapeString(kv.Key)))
		b.WriteString(fmt.Sprintf(`<rect x="120" y="%d" width="%d" height="%d" rx="2" fill="#2563eb"/>`, y, w, barH))
		b.WriteString(fmt.Sprintf(`<text x="%d" y="%d" class="val">%d</text>`, 126+w, y+barH-6, int(kv.Value)))
		y += barH + gap
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// chartScale picks the y-axis maximum, ignoring a lone outlier.
//
// The first-run baseline scan stores the entire live market (~86k postings) on
// a single day, so scaling to the true maximum renders every ordinary day as a
// 1px stub for the next 30 days. When the top value dwarfs the runner-up, the
// axis follows the runner-up and the outlier column is drawn clipped.
func chartScale(kvs []metric.KV) float64 {
	top, second := 0.0, 0.0
	for _, kv := range kvs {
		switch {
		case kv.Value > top:
			top, second = kv.Value, top
		case kv.Value > second:
			second = kv.Value
		}
	}
	if second > 0 && top > 3*second {
		return second
	}
	if top == 0 {
		return 1
	}
	return top
}

// Column draws a time-series column chart (dates left to right). Bar is
// horizontal and caps at ~11 rows, which cannot show a 30-day trend.
func Column(kvs []metric.KV, unit string) template.HTML {
	if len(kvs) == 0 {
		return template.HTML(`<p class="mut">No data yet.</p>`)
	}
	const (
		plotH   = 120
		baseY   = 140
		leftPad = 34
	)
	// Widen the columns when there are few days so a week of history is not
	// drawn as a 160px sliver, and keep them legible for a 90-day window.
	step := 700 / len(kvs)
	step = min(max(step, 17), 48)
	width := leftPad + len(kvs)*step + 10
	scale := chartScale(kvs)
	labelEvery := max((len(kvs)+7)/8, 1)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d 170" style="max-width:%dpx" xmlns="http://www.w3.org/2000/svg" class="chart" role="img" aria-label="%s per period">`,
		width, width, template.HTMLEscapeString(unit))
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#334155"/>`, leftPad-4, baseY, width-6, baseY)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#334155" stroke-dasharray="2 3"/>`,
		leftPad-4, baseY-plotH, width-6, baseY-plotH)
	fmt.Fprintf(&b, `<text x="0" y="%d" class="lab">%d</text>`, baseY-plotH+4, int(scale))
	fmt.Fprintf(&b, `<text x="0" y="%d" class="lab">0</text>`, baseY+4)

	for i, kv := range kvs {
		x := leftPad + i*step
		h := int(float64(plotH) * (kv.Value / scale))
		if h < 1 && kv.Value > 0 {
			h = 1
		}
		// A column past the scale is drawn clipped, in a lighter fill, with its
		// real value written above it.
		fill, clipped := "#2563eb", kv.Value > scale
		if clipped {
			h, fill = plotH, "#7c3aed"
		}
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" rx="1" fill="%s"><title>%s: %d</title></rect>`,
			x, baseY-h, step-5, h, fill, template.HTMLEscapeString(kv.Key), int(kv.Value))
		if clipped {
			fmt.Fprintf(&b, `<text x="%d" y="%d" class="lab" text-anchor="middle" font-size="10">%d</text>`,
				x+(step-5)/2, baseY-plotH-4, int(kv.Value))
		}
		if (i%labelEvery == 0 && len(kvs)-1-i >= labelEvery/2) || i == len(kvs)-1 {
			fmt.Fprintf(&b, `<text x="%d" y="%d" class="lab" text-anchor="middle" font-size="10">%s</text>`,
				x+(step-5)/2, baseY+16, template.HTMLEscapeString(kv.Key))
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
```

Create `internal/view/value.go`:

```go
package view

import (
	"fmt"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// Suppressed renders why a number is being withheld, and returns "" when the
// number is fine to show. A suppressed value is never rendered as 0: a
// fabricated zero reads as a real measurement, an admitted gap does not.
func Suppressed(c metric.Coverage) template.HTML {
	if !c.Suppressed {
		return ""
	}
	switch c.Reason {
	case metric.ReasonHistory:
		return template.HTML(fmt.Sprintf(
			`<span class="sup">needs %d weeks of history · have %d</span>`,
			c.WeeksRequired, c.WeeksAvailable))
	default:
		return template.HTML(fmt.Sprintf(`<span class="sup">—(n=%d)</span>`, c.Samples))
	}
}

// Pct formats a share as a percentage.
func Pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

// PP formats a percentage-point delta with an explicit sign.
func PP(f float64) string { return fmt.Sprintf("%+.1fpp", f*100) }

// Money formats a monthly salary, or "n/a" when absent.
func Money(f float64) string {
	if f == 0 {
		return "n/a"
	}
	return fmt.Sprintf("S$%.0f", f)
}
```

- [ ] **Step 4: 让 `report` 改用 `view`**

在 `internal/report/render.go` 中：

1. 删除 `baseCSS` 常量、`barSVG`、`chartScale` 函数（`chartScale` 在 `daily_render.go`）。
2. 删除 `columnSVG`（在 `daily_render.go`）。
3. `KV` 改为别名，`report.Report` 等既有 API 保持不变：

```go
// KV is a labeled value for report sections. It aliases metric.KV so the
// aggregate layer and the renderer cannot drift apart.
type KV = metric.KV
```

（把这段放到 `internal/report/metrics.go` 原 `type KV struct` 的位置，并删除原结构体定义。）

4. 两处 FuncMap 里的 `"bar"` / `"col"` 改指向 `view`：

`internal/report/render.go` 的 `RenderHTML`：
```go
	tmpl := template.Must(template.New("report").Funcs(template.FuncMap{
		"bar":    view.Bar,
		"pct":    pct,
		"money":  money,
		"topn":   topn,
		"fmtKV":  fmtKV,
		"mulPct": mulPct,
	}).Parse(htmlTmpl))
```

`internal/report/daily_render.go` 的 `newDailyTemplate`：
```go
	return template.New(name).Funcs(template.FuncMap{
		"bar":    view.Bar,
		"col":    view.Column,
		"fmtKV":  fmtKV,
		"pill":   statusPill,
		"kinds":  kindBadges,
		"dur":    humanDuration,
		"runAgo": runAgo,
	})
```

5. 两个模板里的 `<style>` 拼接改用 `view.BaseCSS`：

`render.go`：`<style>` + view.BaseCSS + `</style>`
`daily_render.go`：`<style>` + view.BaseCSS + dailyCSS + `</style>`

6. `daily_render.go` 的 `dailyCSS` 里删掉与 `view.SuppressedCSS` 重复的 `.mut` 和 `.note` 两条规则，并在拼接处加上 `view.SuppressedCSS`：

```go
<style>` + view.BaseCSS + view.SuppressedCSS + dailyCSS + `</style>
```

7. import 调整：`render.go`、`daily_render.go`、`metrics.go` 三个文件顶部加 `"github.com/meirongdev/jobs-sg/internal/view"`（`metrics.go` 加的是 `internal/metric`，供 `KV` 别名使用）。**不需要删任何 import**：`render.go` 移除 `barSVG` 后 `fmt`/`strings`/`math`/`html/template` 仍被 `pct`/`money`/`fmtKV`/`mulPct`/`RenderHTML` 用到；`daily_render.go` 移除 `columnSVG`/`chartScale` 后 `fmt`/`strings`/`slices`/`time`/`html/template` 仍被 `statusPill`/`kindBadges`/`humanDuration`/`runAgo` 用到。

8. **测试随代码迁移**：`internal/report/daily_test.go` 的 `TestChartScaleIgnoresBaselineOutlier`（含其上方两行注释）直接调用 `chartScale`/`columnSVG`，删除符号后它必然编译失败——把该函数整体删除，并将其覆盖（5 例 chartScale 表、`#7c3aed` 削顶填充、`height="0"` 塌陷守护）以下述形式追加到 `internal/view/view_test.go`（包名/类型/函数名按 view 适配，断言逐条保留）：

```go
// Migrated from internal/report/daily_test.go when chartScale moved here: the
// first-run baseline stores the whole live market in one day; without outlier
// handling every later day renders as a 1px stub.
func TestChartScaleIgnoresBaselineOutlier(t *testing.T) {
	cases := []struct {
		name string
		vals []float64
		want float64
	}{
		{"baseline day dwarfs the rest", []float64{6666, 40, 38, 45, 41}, 45},
		{"ordinary spread keeps true max", []float64{40, 38, 45, 41}, 45},
		{"2x is not an outlier", []float64{80, 40, 38}, 80},
		{"all zero", []float64{0, 0}, 1},
		{"single point", []float64{12}, 12},
	}
	for _, c := range cases {
		kvs := make([]metric.KV, len(c.vals))
		for i, v := range c.vals {
			kvs[i] = metric.KV{Key: "d", Value: v}
		}
		if got := chartScale(kvs); got != c.want {
			t.Errorf("%s: chartScale = %v, want %v", c.name, got, c.want)
		}
	}

	// the clipped column still reports its real value on the page
	svg := string(Column([]metric.KV{{Key: "08-01", Value: 6666}, {Key: "08-02", Value: 40}}, "new SWE"))
	if !strings.Contains(svg, ">6666<") {
		t.Errorf("clipped column must print its value:\n%s", svg)
	}
	if !strings.Contains(svg, `fill="#7c3aed"`) {
		t.Errorf("clipped column must be visually distinct:\n%s", svg)
	}
	if strings.Contains(svg, `height="0"`) {
		t.Errorf("ordinary column collapsed to zero height:\n%s", svg)
	}
}
```

除这一处删除外，`internal/report` 的任何测试文件不得再动——其余既有测试原样通过仍是无回归的证据。

- [ ] **Step 5: 确认全绿**

Run: `go test ./... -count=1 && go vet ./...`
Expected: PASS。`internal/report` 的既有测试（`TestComputeMetricsAndRender` 等，除按 Step 4 第 8 点迁移的那一个）必须原样通过——这是抽取无回归的证据。另跑 `grep -rn "baseCSS\|barSVG\|columnSVG\|chartScale" internal/report/`，必须零命中。

- [ ] **Step 6: Commit**

```bash
git add internal/view internal/report/render.go internal/report/daily_render.go internal/report/metrics.go internal/report/daily_test.go
git commit -m "refactor(view): extract shared CSS and SVG charts out of report" -- internal/view internal/report/render.go internal/report/daily_render.go internal/report/metrics.go internal/report/daily_test.go
```

> 执行记录 2026-08-07：首次执行按初版指令（"零测试文件改动"）走到 Step 5 卡死：`daily_test.go` 的 `TestChartScaleIgnoresBaselineOutlier` 直接调用包内未导出的 `chartScale`/`columnSVG`，与"删除符号 + 全绿 + 不动测试"三条互斥——plan 作者写 Step 4 时只查了模板调用点，漏了包内测试对未导出符号的直接引用。实现者实证确认后回滚手术、保留成品于 scratchpad 并报 BLOCKED。处置：测试随代码迁移（上文第 8 点），`daily_test.go` 仅删该函数。

---

## Task 7: `/tech` 聚合之一——需求排名与富化分母

技术占比的分母必须是**已富化**的岗位。用全部岗位作分母时，enrich backlog 会系统性压低所有技术的占比（spec §3.7-3）。另有一个容易漏的正确性点：`job_tech` 的主键含 `source`，同一岗位的同一技术可能同时有 `rule` 和 `llm` 两行，任何按技术聚合都必须按 `job_uuid` 去重。

**Files:**
- Create: `internal/metric/tech.go`
- Create: `internal/metric/fixture_test.go`
- Test: `internal/metric/tech_test.go`

- [ ] **Step 1: 写 fixture 载入辅助**

Create `internal/metric/fixture_test.go`:

```go
package metric

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

// fixtureNow is the clock every fixture-backed test uses: Monday of 2026-W33.
// LastCompletedWeek(fixtureNow) is W32 and the four baseline weeks W28..W31 are
// all populated, because scripts/genfixture spreads postings over W27..W32.
var fixtureNow = time.Date(2026, 8, 10, 9, 0, 0, 0, SGT)

// seedFixture loads testdata/fixture/jobs.jsonl into a temp DB the way
// cmd/ingest + cmd/enrich would: classify every posting, then run the rule
// layer over its description.
func seedFixture(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	ssoc, err := db.LoadSSOCMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cl := classify.New(ssoc)
	taxRows, err := db.LoadTechTaxonomy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tax := tech.LoadTaxonomy(taxRows)

	f, err := os.Open("../../testdata/fixture/jobs.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var j mcf.Job
		if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
			t.Fatal(err)
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/fixture.jsonl.gz#0"); err != nil {
			t.Fatal(err)
		}
		hits := tax.Extract(j.Title + " " + j.Description)
		rows := make([]store.TechRow, len(hits))
		for i, h := range hits {
			rows[i] = store.TechRow{Slug: h.Slug, Kind: h.Kind}
		}
		if err := db.WriteRuleTech(ctx, j.UUID, rows); err != nil {
			t.Fatal(err)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return db
}
```

- [ ] **Step 2: 写失败测试**

Create `internal/metric/tech_test.go`:

```go
package metric

import (
	"context"
	"testing"
)

func TestTechDemandRanksTheReportedWeek(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Week != "2026-W32" {
		t.Errorf("week = %s, want 2026-W32 (the last completed week)", r.Week)
	}
	if r.Denom == 0 {
		t.Fatal("enriched denominator is 0; the fixture should have enriched SWE postings")
	}
	if len(r.Ranked) == 0 {
		t.Fatal("no ranked technologies")
	}
	for i := 1; i < len(r.Ranked); i++ {
		if r.Ranked[i-1].Count < r.Ranked[i].Count {
			t.Fatalf("ranking not descending at %d: %+v", i, r.Ranked[i-1:i+1])
		}
	}
	for _, s := range r.Ranked {
		if s.Count > r.Denom {
			t.Errorf("%s count %d exceeds denominator %d", s.Slug, s.Count, r.Denom)
		}
		if s.Share < 0 || s.Share > 1 {
			t.Errorf("%s share = %v, want 0..1", s.Slug, s.Share)
		}
	}
}

func TestTechCountsDedupeRuleAndLLMRows(t *testing.T) {
	// job_tech's primary key is (job_uuid, tech_slug, source): the same posting
	// can carry the same technology from both layers. Counting rows instead of
	// distinct postings would double it.
	ctx := context.Background()
	db := seedFixture(t)
	var uuid string
	if err := db.QueryRowContext(ctx, `
		SELECT job_uuid FROM job_tech WHERE tech_slug='kubernetes' LIMIT 1`).Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	before, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO job_tech(job_uuid, tech_slug, tech_kind, source) VALUES(?,?,?,'llm')`,
		uuid, "kubernetes", "tool"); err != nil {
		t.Fatal(err)
	}
	after, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if countOf(before.Ranked, "kubernetes") != countOf(after.Ranked, "kubernetes") {
		t.Errorf("duplicate source row changed the count: %d -> %d",
			countOf(before.Ranked, "kubernetes"), countOf(after.Ranked, "kubernetes"))
	}
}

func TestEnrichedDenominatorExcludesBacklog(t *testing.T) {
	// A posting with neither job_tech rows nor an enrich_done marker is still in
	// the backlog; counting it would depress every technology's share.
	ctx := context.Background()
	db := seedFixture(t)
	before, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var uuid string
	if err := db.QueryRowContext(ctx, `
		SELECT uuid FROM job WHERE is_swe=1 LIMIT 1`).Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	// Un-enriching means removing BOTH traces: writeTech marks enrich_done even
	// for zero-match jobs (internal/store/enrich.go), so deleting job_tech
	// alone leaves the posting "processed" and the denominator unchanged.
	if _, err := db.ExecContext(ctx, `DELETE FROM job_tech WHERE job_uuid=?`, uuid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM enrich_done WHERE job_uuid=?`, uuid); err != nil {
		t.Fatal(err)
	}
	after, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Denom >= before.Denom {
		t.Errorf("denominator %d did not shrink after un-enriching a posting (was %d)",
			after.Denom, before.Denom)
	}
}

func TestTechLensNarrowsTheDenominator(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	all, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	lens, err := ParseLens("0-2", "")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := TechReportFor(ctx, db, fixtureNow, lens)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Denom == 0 {
		t.Fatal("0-2 band has no enriched postings in the fixture")
	}
	if entry.Denom >= all.Denom {
		t.Errorf("lensed denominator %d must be smaller than %d", entry.Denom, all.Denom)
	}
}

func countOf(stats []TechStat, slug string) int {
	for _, s := range stats {
		if s.Slug == slug {
			return s.Count
		}
	}
	return -1
}
```

- [ ] **Step 3: 确认失败**

Run: `go test ./internal/metric/ -run Tech -v`
Expected: FAIL — `undefined: TechReportFor`

- [ ] **Step 4: 实现排名与分母**

Create `internal/metric/tech.go`:

```go
package metric

import (
	"context"
	"sort"
	"time"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// RankedTechLimit is how many technologies the demand table shows.
const RankedTechLimit = 30

// TechStat is one technology's row on /tech.
type TechStat struct {
	Slug  string
	Kind  string
	Count int     // postings in the reported week mentioning it
	Share float64 // Count / TechReport.Denom

	MomentumPP float64  // Share − mean(previous 4 weeks' share), as a fraction
	Momentum   Coverage // suppressed when the week is thin or history is short

	PremiumPct float64  // median(with it) / median(all) − 1
	Premium    Coverage // suppressed below MinSalarySamplesPerTech

	EntryFriendly float64 // share of postings mentioning it that are entry-level
}

// TechReport is the /tech page model.
type TechReport struct {
	Week        string     // reported ISO week, e.g. "2026-W32"
	Denom       int        // enriched SWE postings in that week (the share denominator)
	Ranked      []TechStat // by Count desc, capped at RankedTechLimit
	Rising      []TechStat // by MomentumPP desc, unsuppressed only
	Falling     []TechStat // by MomentumPP asc, unsuppressed only
	MedianAll   float64    // rolling-90d median monthly salary, the premium baseline
	SalaryN     int        // disclosed monthly salaries behind MedianAll
	SalaryTotal int        // every SWE posting in the same window — the transparency denominator
	History     Coverage   // how many of the 5 momentum windows had data
	Lens        Lens
}

// swePosted is the shared predicate: SWE postings whose posting_date falls in
// the window. Callers append Lens.Where(), which qualifies columns with `j.`.
const swePosted = `FROM job j WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`

// enrichedPredicate marks a posting the enrichment pipeline has finished with.
// enrich_done matters on its own: a posting whose extraction found nothing has
// no job_tech rows but is not backlog (see internal/store/schema.go).
const enrichedPredicate = `AND (EXISTS(SELECT 1 FROM job_tech t WHERE t.job_uuid=j.uuid)
	 OR EXISTS(SELECT 1 FROM enrich_done e WHERE e.job_uuid=j.uuid))`

// TechReportFor builds the /tech model for the last completed ISO week.
func TechReportFor(ctx context.Context, db *store.DB, now time.Time, lens Lens) (*TechReport, error) {
	week := LastCompletedWeek(now)
	r := &TechReport{Week: week.WeekLabel(), Lens: lens}

	var err error
	if r.Denom, err = enrichedCount(ctx, db, week, lens); err != nil {
		return nil, err
	}
	counts, kinds, err := techCounts(ctx, db, week, lens)
	if err != nil {
		return nil, err
	}
	for slug, n := range counts {
		s := TechStat{Slug: slug, Kind: kinds[slug], Count: n}
		if r.Denom > 0 {
			s.Share = float64(n) / float64(r.Denom)
		}
		r.Ranked = append(r.Ranked, s)
	}
	sort.SliceStable(r.Ranked, func(i, j int) bool {
		if r.Ranked[i].Count != r.Ranked[j].Count {
			return r.Ranked[i].Count > r.Ranked[j].Count
		}
		return r.Ranked[i].Slug < r.Ranked[j].Slug
	})
	if len(r.Ranked) > RankedTechLimit {
		r.Ranked = r.Ranked[:RankedTechLimit]
	}
	return r, nil
}

// enrichedCount is the share denominator: SWE postings in the window that the
// enrichment pipeline has already processed. Using all postings instead would
// let the enrich backlog systematically depress every technology's share
// (spec §3.7-3).
func enrichedCount(ctx context.Context, db *store.DB, w Window, lens Lens) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) `+swePosted+lens.Where()+` `+enrichedPredicate, w.Args()...).Scan(&n)
	return n, err
}

// techCounts returns slug -> distinct postings, and slug -> kind.
//
// count(DISTINCT j.uuid), not count(*): job_tech's primary key includes
// `source`, so a posting carrying the same technology from both the rule and
// LLM layers has two rows and would otherwise be counted twice.
func techCounts(ctx context.Context, db *store.DB, w Window, lens Lens) (map[string]int, map[string]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.tech_slug, min(t.tech_kind), count(DISTINCT j.uuid)
		FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`+lens.Where()+`
		GROUP BY t.tech_slug`, w.Args()...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	counts, kinds := map[string]int{}, map[string]string{}
	for rows.Next() {
		var slug, kind string
		var n int
		if err := rows.Scan(&slug, &kind, &n); err != nil {
			return nil, nil, err
		}
		counts[slug], kinds[slug] = n, kind
	}
	return counts, kinds, rows.Err()
}
```

- [ ] **Step 5: 确认通过**

Run: `go test ./internal/metric/ -v`
Expected: PASS（4 个 Tech 测试 + 前面的）

- [ ] **Step 6: Commit**

```bash
git add internal/metric/tech.go internal/metric/tech_test.go internal/metric/fixture_test.go
git commit -m "feat(metric): rank weekly tech demand against an enriched denominator"
```

---

## Task 8: `/tech` 聚合之二——四周动量

动量用**百分点**而不是相对百分比：相对值会让 1→3 条的技术显示 +200% 排到榜首（spec §3.1）。当周被排除，基线是前 4 个已完成周，任一窗口无数据就按 history 抑制。

**Files:**
- Modify: `internal/metric/tech.go`
- Test: `internal/metric/tech_momentum_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/metric/tech_momentum_test.go`:

```go
package metric

import (
	"context"
	"testing"
	"time"
)

func TestMomentumIsPercentagePointsNotRelativeChange(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.History.Suppressed {
		t.Fatalf("fixture covers W27..W32, momentum history should be complete: %+v", r.History)
	}
	// The fixture repeats the same template rows every week, so shares are
	// near-flat: a pp delta stays tiny while a relative delta would too, but a
	// pp value can never exceed 1.0 in magnitude by construction.
	for _, s := range r.Ranked {
		if s.Momentum.Suppressed {
			continue
		}
		if s.MomentumPP < -1 || s.MomentumPP > 1 {
			t.Errorf("%s momentum = %v, outside the -1..1 range a share delta must live in",
				s.Slug, s.MomentumPP)
		}
	}
}

func TestMomentumSuppressedForThinTechnologies(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var thin, thick int
	for _, s := range r.Ranked {
		if s.Count < MinTechCountForMomentum {
			thin++
			if !s.Momentum.Suppressed || s.Momentum.Reason != ReasonSample {
				t.Errorf("%s has %d postings but momentum is not sample-suppressed: %+v",
					s.Slug, s.Count, s.Momentum)
			}
		} else {
			thick++
			if s.Momentum.Suppressed {
				t.Errorf("%s has %d postings, momentum should be shown: %+v",
					s.Slug, s.Count, s.Momentum)
			}
		}
	}
	if thin == 0 || thick == 0 {
		t.Fatalf("fixture must exercise both sides of the threshold (thin=%d thick=%d)", thin, thick)
	}
}

func TestMomentumSuppressedWhenHistoryIsShort(t *testing.T) {
	// A clock early in the fixture leaves fewer than 4 baseline weeks behind the
	// reported week, which must degrade to a history suppression, not a 0.
	db := seedFixture(t)
	early := time.Date(2026, 7, 13, 9, 0, 0, 0, SGT) // W29 Monday -> reports W28
	r, err := TechReportFor(context.Background(), db, early, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.History.Suppressed || r.History.Reason != ReasonHistory {
		t.Errorf("history = %+v, want history suppression", r.History)
	}
	if r.History.WeeksRequired != MinWeeksForMomentum {
		t.Errorf("WeeksRequired = %d, want %d", r.History.WeeksRequired, MinWeeksForMomentum)
	}
	for _, s := range r.Ranked {
		if !s.Momentum.Suppressed {
			t.Errorf("%s momentum shown despite short history", s.Slug)
		}
		if s.MomentumPP != 0 {
			t.Errorf("%s carries a momentum value while suppressed: %v", s.Slug, s.MomentumPP)
		}
	}
	if len(r.Rising) != 0 || len(r.Falling) != 0 {
		t.Errorf("rising/falling boards must be empty with short history")
	}
}

func TestRisingAndFallingExcludeSuppressedRows(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(append([]TechStat{}, r.Rising...), r.Falling...) {
		if s.Momentum.Suppressed {
			t.Errorf("%s is suppressed but appears on a momentum board", s.Slug)
		}
	}
	for i := 1; i < len(r.Rising); i++ {
		if r.Rising[i-1].MomentumPP < r.Rising[i].MomentumPP {
			t.Errorf("rising board not descending at %d", i)
		}
	}
	for i := 1; i < len(r.Falling); i++ {
		if r.Falling[i-1].MomentumPP > r.Falling[i].MomentumPP {
			t.Errorf("falling board not ascending at %d", i)
		}
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/metric/ -run Momentum -v`
Expected: FAIL — `r.History` 为零值、`Rising` 为空、thin/thick 断言失败

- [ ] **Step 3: 实现动量**

在 `internal/metric/tech.go` 的 `TechReportFor` 里，紧跟 `counts, kinds, err := techCounts(...)` 的错误检查之后、`for slug, n := range counts` 循环**之前**插入基线计算（`r.History` 必须在循环里被读到）：

```go
	// momentum: baseline is the 4 completed weeks before the reported one. The
	// in-progress week is never included — it is always partial data and would
	// show every technology crashing (spec §3.1).
	baseline := PrevWeeks(week, MinWeeksForMomentum-1)
	shares := make([]map[string]float64, 0, len(baseline))
	available := 0
	if r.Denom > 0 {
		available++
	}
	for _, bw := range baseline {
		denom, err := enrichedCount(ctx, db, bw, lens)
		if err != nil {
			return nil, err
		}
		if denom == 0 {
			continue
		}
		available++
		counts, _, err := techCounts(ctx, db, bw, lens)
		if err != nil {
			return nil, err
		}
		m := make(map[string]float64, len(counts))
		for slug, n := range counts {
			m[slug] = float64(n) / float64(denom)
		}
		shares = append(shares, m)
	}
	r.History = HistoryCoverage(available, MinWeeksForMomentum)
```

在 `for slug, n := range counts` 循环体内，`r.Ranked = append(...)` **之前**加上：

```go
		s.Momentum = momentumCoverage(n, r.History)
		if !s.Momentum.Suppressed {
			var sum float64
			for _, m := range shares {
				sum += m[slug]
			}
			s.MomentumPP = s.Share - sum/float64(len(shares))
		}
```

在函数末尾 `return r, nil` 之前加榜单构建：

```go
	r.Rising, r.Falling = momentumBoards(r.Ranked)
	return r, nil
```

在文件末尾追加两个辅助函数：

```go
// momentumCoverage suppresses a technology's momentum when the page lacks
// history, or when the technology is too thin for a share delta to mean
// anything (a 1 -> 3 posting swing must not top the rising board).
func momentumCoverage(count int, history Coverage) Coverage {
	if history.Suppressed {
		return history
	}
	return SampleCoverage(count, MinTechCountForMomentum)
}

// MomentumBoardLimit is how many technologies each momentum board shows.
const MomentumBoardLimit = 10

// momentumBoards splits the unsuppressed rows into rising and falling boards.
func momentumBoards(ranked []TechStat) (rising, falling []TechStat) {
	live := make([]TechStat, 0, len(ranked))
	for _, s := range ranked {
		if !s.Momentum.Suppressed {
			live = append(live, s)
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].MomentumPP > live[j].MomentumPP })
	for _, s := range live {
		if s.MomentumPP > 0 && len(rising) < MomentumBoardLimit {
			rising = append(rising, s)
		}
	}
	for i := len(live) - 1; i >= 0; i-- {
		if live[i].MomentumPP < 0 && len(falling) < MomentumBoardLimit {
			falling = append(falling, live[i])
		}
	}
	return rising, falling
}
```

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/metric/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/metric/tech.go internal/metric/tech_momentum_test.go
git commit -m "feat(metric): add four-week tech momentum in percentage points"
```

---

## Task 9: `/tech` 聚合之三——薪资溢价与入门友好度

溢价基线是滚动 90 天的全体中位数；样本不足 20 条就抑制。溢价**跟随镜头重算**——`?exp=3-5` 下看到的是同经验档内的溢价，这才可行动（spec §3.2 的资历混杂）。入门友好度与薪资透明率共用同一个滚动窗：同一张表里两列各用一套窗口会让数字悄悄失去可比性，且 spec 验收标准 4 要求每处薪资数字旁都有样本量与透明率。

**Files:**
- Modify: `internal/metric/tech.go`
- Test: `internal/metric/tech_pay_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/metric/tech_pay_test.go`:

```go
package metric

import (
	"context"
	"testing"
)

func TestPremiumBaselineIsARealAdvertisedSalary(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.SalaryN == 0 || r.MedianAll == 0 {
		t.Fatalf("no disclosed salaries behind the baseline: n=%d median=%v", r.SalaryN, r.MedianAll)
	}
	if r.SalaryTotal < r.SalaryN || r.SalaryTotal == 0 {
		t.Errorf("transparency denominator %d must cover the disclosed sample %d", r.SalaryTotal, r.SalaryN)
	}
	if p := r.TransparencyPct(); p <= 0 || p > 1 {
		t.Errorf("transparency rate = %v, want (0,1]", p)
	}
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM job
		WHERE is_swe=1 AND salary_hidden=0 AND salary_type='Monthly'
		  AND salary_min IS NOT NULL AND salary_max IS NOT NULL
		  AND (salary_min+salary_max)/2.0 = ?`, r.MedianAll).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Errorf("median %v was never advertised by any posting", r.MedianAll)
	}
}

func TestPremiumSuppressedBelowSampleThreshold(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var shown, hidden int
	for _, s := range r.Ranked {
		if s.Premium.Suppressed {
			hidden++
			if s.Premium.Samples >= MinSalarySamplesPerTech {
				t.Errorf("%s suppressed with n=%d, above the threshold", s.Slug, s.Premium.Samples)
			}
			if s.PremiumPct != 0 {
				t.Errorf("%s carries a premium while suppressed: %v", s.Slug, s.PremiumPct)
			}
		} else {
			shown++
			if s.Premium.Samples < MinSalarySamplesPerTech {
				t.Errorf("%s shown with only n=%d", s.Slug, s.Premium.Samples)
			}
		}
	}
	if shown == 0 || hidden == 0 {
		t.Fatalf("fixture must exercise both sides (shown=%d hidden=%d)", shown, hidden)
	}
}

func TestPremiumFollowsTheLens(t *testing.T) {
	// spec §3.2: a raw premium mixes seniority in, so the number must be
	// recomputed inside the active experience band.
	ctx := context.Background()
	db := seedFixture(t)
	all, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	lens, err := ParseLens("6+", "")
	if err != nil {
		t.Fatal(err)
	}
	senior, err := TechReportFor(ctx, db, fixtureNow, lens)
	if err != nil {
		t.Fatal(err)
	}
	if senior.SalaryN == 0 {
		t.Fatal("6+ band has no disclosed salaries in the fixture")
	}
	// SalaryN comes straight out of salarySample, so a smaller sample under the
	// lens is itself the proof that the lens reaches the salary query. Do not
	// assert the two medians differ — over random fixture salaries that is a
	// coin flip, and a flaky test is worse than no test.
	if senior.SalaryN >= all.SalaryN {
		t.Errorf("lensed salary sample %d must be smaller than %d", senior.SalaryN, all.SalaryN)
	}
	if senior.MedianAll == 0 {
		t.Error("6+ band median is 0 despite a non-empty sample")
	}
}

func TestEntryFriendlyIsAShareOfMentioningPostings(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	nonZero := 0
	for _, s := range r.Ranked {
		if s.EntryFriendly < 0 || s.EntryFriendly > 1 {
			t.Errorf("%s entry-friendliness = %v, want 0..1", s.Slug, s.EntryFriendly)
		}
		if s.EntryFriendly > 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		t.Error("no technology has any entry-level posting; the fixture has 0-2 year rows")
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/metric/ -run 'Premium|EntryFriendly' -v`
Expected: FAIL — `r.SalaryN` 为 0

- [ ] **Step 3: 实现溢价与入门友好度**

在 `internal/metric/tech.go` 的 `TechReportFor` 里，`r.Rising, r.Falling = ...` **之前**插入：

```go
	// Premium is computed over a rolling window, not one week: a single week's
	// disclosed salaries do not survive being split per technology.
	roll := Rolling(now, RollingDays)
	allSalaries, err := salarySample(ctx, db, roll, lens, "")
	if err != nil {
		return nil, err
	}
	r.SalaryN = len(allSalaries)
	r.MedianAll = Percentile(allSalaries, 0.5)
	if r.SalaryTotal, err = sweCount(ctx, db, roll, lens); err != nil {
		return nil, err
	}

	// Entry-friendliness shares the premium's rolling window: two columns of
	// one table computed over different periods would be silently incomparable.
	entry, err := entryShare(ctx, db, roll, lens)
	if err != nil {
		return nil, err
	}
	for i := range r.Ranked {
		slug := r.Ranked[i].Slug
		r.Ranked[i].EntryFriendly = entry[slug]

		vals, err := salarySample(ctx, db, roll, lens, slug)
		if err != nil {
			return nil, err
		}
		r.Ranked[i].Premium = SampleCoverage(len(vals), MinSalarySamplesPerTech)
		if !r.Ranked[i].Premium.Suppressed && r.MedianAll > 0 {
			r.Ranked[i].PremiumPct = Percentile(vals, 0.5)/r.MedianAll - 1
		}
	}
```

在文件末尾追加：

```go
// disclosedSalary limits every salary figure to publicly advertised monthly
// ranges. The share of postings that disclose at all is itself a headline
// number (spec §3.3) — these medians describe only that subset.
const disclosedSalary = `AND j.salary_hidden=0 AND j.salary_type='Monthly'
	AND j.salary_min IS NOT NULL AND j.salary_max IS NOT NULL`

// salarySample returns the ascending midpoint salaries in the window, either
// overall (slug == "") or for postings mentioning one technology.
//
// The per-technology form groups by posting before taking the value: a posting
// carrying the technology from both the rule and LLM layers has two job_tech
// rows, and counting it twice would skew the median toward it.
func salarySample(ctx context.Context, db *store.DB, w Window, lens Lens, slug string) ([]float64, error) {
	q := `SELECT (j.salary_min+j.salary_max)/2.0 ` + swePosted + lens.Where() + ` ` + disclosedSalary + ` ORDER BY 1`
	args := w.Args()
	if slug != "" {
		q = `SELECT min((j.salary_min+j.salary_max)/2.0)
			FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
			WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?` +
			lens.Where() + ` ` + disclosedSalary + ` AND t.tech_slug = ?
			GROUP BY j.uuid ORDER BY 1`
		args = append(args, slug)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// sweCount counts every SWE posting in the window under the lens, disclosed
// salary or not — the denominator of the salary transparency rate.
func sweCount(ctx context.Context, db *store.DB, w Window, lens Lens) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) `+swePosted+lens.Where(), w.Args()...).Scan(&n)
	return n, err
}

// TransparencyPct is the share of SWE postings in the rolling window that
// disclose a monthly salary. It is printed beside every salary figure so a
// median over the disclosing subset cannot read as a market-wide number.
func (r *TechReport) TransparencyPct() float64 {
	if r.SalaryTotal == 0 {
		return 0
	}
	return float64(r.SalaryN) / float64(r.SalaryTotal)
}

// entryShare returns slug -> share of postings mentioning it that are
// entry-level. It answers "what do they actually ask a junior for", which the
// overall ranking cannot. The window is the caller's choice; /tech passes the
// premium's rolling window so the two table columns stay comparable.
func entryShare(ctx context.Context, db *store.DB, w Window, lens Lens) (map[string]float64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.tech_slug, count(DISTINCT j.uuid),
		       count(DISTINCT CASE WHEN `+EntryPredicate+` THEN j.uuid END)
		FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`+lens.Where()+`
		GROUP BY t.tech_slug`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var slug string
		var total, entry int
		if err := rows.Scan(&slug, &total, &entry); err != nil {
			return nil, err
		}
		if total > 0 {
			out[slug] = float64(entry) / float64(total)
		}
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/metric/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/metric/tech.go internal/metric/tech_pay_test.go
git commit -m "feat(metric): add lens-aware salary premium and entry-friendliness per tech"
```

---

## Task 10: `/tech` 页面与路由

**Files:**
- Create: `internal/view/tech.go`
- Create: `internal/web/tech.go`
- Modify: `internal/web/server.go`
- Test: `internal/web/tech_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/web/tech_test.go`:

```go
package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestTechPageRenders(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/tech")
	if rec.Code != http.StatusOK {
		t.Fatalf("/tech = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	for _, want := range []string{"Tech Demand", "Momentum", "Salary premium", "Entry-friendly"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("/tech missing %q", want)
		}
	}
}

func TestTechPageRejectsUnknownLensValues(t *testing.T) {
	s := setupWeb(t)
	for _, path := range []string{
		"/tech?exp=0-3",
		"/tech?exp=junior",
		"/tech?role=backend",
		"/tech?role=Nonexistent",
	} {
		if rec := get(t, s, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}

func TestTechPageAcceptsAllowlistedLens(t *testing.T) {
	s := setupWeb(t)
	for _, path := range []string{"/tech?exp=0-2", "/tech?role=Backend", "/tech?exp=6%2B&role=Data"} {
		if rec := get(t, s, path); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestTechPagesAreCachedPerLens(t *testing.T) {
	s := setupWeb(t)
	get(t, s, "/tech")
	get(t, s, "/tech?exp=0-2")
	get(t, s, "/tech?exp=0-2&role=Backend")
	now := s.now()
	for _, key := range []string{
		"tech:exp=;role=",
		"tech:exp=0-2;role=",
		"tech:exp=0-2;role=Backend",
	} {
		if _, ok := s.cache.get(key, now); !ok {
			t.Errorf("cache missing entry %q", key)
		}
	}
}

func TestTechPageShowsSuppressionInsteadOfZero(t *testing.T) {
	// setupWeb seeds a single posting on one day, so momentum has nowhere near
	// 5 weeks of history: the page must say so rather than draw a flat zero.
	s := setupWeb(t)
	body := get(t, s, "/tech").Body.String()
	if !strings.Contains(body, "needs 5 weeks of history") {
		t.Errorf("/tech must explain short history, got:\n%s", body)
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/web/ -run Tech -v`
Expected: FAIL — `/tech` 返回 404

- [ ] **Step 3: 写模板**

Create `internal/view/tech.go`:

```go
package view

import (
	"bytes"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// techPage is parsed once at init so a template syntax error fails the build's
// tests instead of surfacing as a 500 (the page renders live on every hit).
var techPage = template.Must(template.New("tech").Funcs(template.FuncMap{
	"bar":   Bar,
	"pct":   Pct,
	"pp":    PP,
	"money": Money,
	"sup":   Suppressed,
	"lens":  lensNav,
	"kvs":   techBars,
}).Parse(techTmpl))

// TechPage renders /tech.
func TechPage(r *metric.TechReport) (string, error) {
	var buf bytes.Buffer
	if err := techPage.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// techBars projects the demand ranking onto the bar chart's input.
func techBars(stats []metric.TechStat, n int) []metric.KV {
	out := make([]metric.KV, 0, len(stats))
	for _, s := range stats {
		out = append(out, metric.KV{Key: s.Slug, Value: float64(s.Count)})
	}
	return TopN(out, n)
}

// lensNav renders the experience/role pickers, marking the active values.
func lensNav(page string, active metric.Lens) template.HTML {
	var b bytes.Buffer
	b.WriteString(`<div class="lens">Experience: `)
	writeLensLink(&b, page, metric.Lens{Role: active.Role}, "all", active.Exp == "")
	for _, band := range metric.ExpBands() {
		writeLensLink(&b, page, metric.Lens{Exp: band, Role: active.Role}, band, active.Exp == band)
	}
	b.WriteString(`</div><div class="lens">Role: `)
	writeLensLink(&b, page, metric.Lens{Exp: active.Exp}, "all", active.Role == "")
	for _, role := range metric.RoleFamilies() {
		writeLensLink(&b, page, metric.Lens{Exp: active.Exp, Role: role}, role, active.Role == role)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func writeLensLink(b *bytes.Buffer, page string, l metric.Lens, label string, on bool) {
	q := ""
	if l.Exp != "" {
		q = "?exp=" + template.URLQueryEscaper(l.Exp)
	}
	if l.Role != "" {
		sep := "?"
		if q != "" {
			sep = "&"
		}
		q += sep + "role=" + template.URLQueryEscaper(l.Role)
	}
	class := ""
	if on {
		class = ` class="on"`
	}
	b.WriteString(`<a href="` + page + q + `"` + class + `>` + template.HTMLEscapeString(label) + `</a>`)
}

const techTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Tech Demand · Singapore SWE jobs</title>
<style>` + BaseCSS + SuppressedCSS + `</style>
</head>
<body><div class="wrap">
<h1>Tech Demand</h1>
<div class="sub">What is worth learning · reported week {{.Week}} (last completed ISO week, SGT){{if .Lens.Label}} · {{.Lens.Label}}{{end}}</div>
<nav class="nav"><a href="/">Weekly report</a><a class="on" href="/tech">Tech</a></nav>
{{lens "/tech" .Lens}}

<h2>1. Demand ranking</h2>
<p class="note">{{.Denom}} enriched SWE postings in {{.Week}}. Share = postings mentioning the technology ÷ that number — postings still awaiting enrichment are excluded from the denominator, so a processing backlog cannot depress every share at once.</p>
{{if .Ranked}}{{bar (kvs .Ranked 15) 15}}{{else}}<p class="mut">No enriched postings in {{.Week}}.</p>{{end}}

<h2>2. Momentum (vs the previous 4 weeks)</h2>
{{if .History.Suppressed}}<p class="note">{{sup .History}} — momentum compares the reported week against the 4 completed weeks before it.</p>
{{else}}
<h3>Heating up</h3>
{{if .Rising}}<table><tr><th>Technology</th><th>Share</th><th>Change</th><th>Postings</th></tr>
{{range .Rising}}<tr><td>{{.Slug}}</td><td>{{pct .Share}}</td><td class="up">{{pp .MomentumPP}}</td><td>{{.Count}}</td></tr>{{end}}</table>
{{else}}<p class="mut">Nothing rose this week.</p>{{end}}
<h3>Cooling down</h3>
{{if .Falling}}<table><tr><th>Technology</th><th>Share</th><th>Change</th><th>Postings</th></tr>
{{range .Falling}}<tr><td>{{.Slug}}</td><td>{{pct .Share}}</td><td class="down">{{pp .MomentumPP}}</td><td>{{.Count}}</td></tr>{{end}}</table>
{{else}}<p class="mut">Nothing fell this week.</p>{{end}}
{{end}}
<p class="note">Change is in percentage points of share, not relative percent: a technology going from 1 to 3 postings would otherwise read as +200% and top the board.</p>

<h2>3. Salary premium and entry-friendliness</h2>
<p class="note">Premium compares the median advertised monthly salary of postings mentioning a technology against the overall median, over the trailing 90 days. Baseline: <strong>{{money .MedianAll}}</strong> from {{.SalaryN}} of {{.SalaryTotal}} SWE postings — only {{pct .TransparencyPct}} disclose a monthly salary, and every figure here describes that disclosing subset. Entry-friendly is computed over the same 90-day window. Premium mixes seniority in (senior roles name more infrastructure); pick an experience band above to compare within one.</p>
<table>
<tr><th>Technology</th><th>Kind</th><th>Postings</th><th>Share</th><th>Salary premium</th><th>Entry-friendly</th></tr>
{{range .Ranked}}<tr>
  <td>{{.Slug}}</td><td class="mut">{{.Kind}}</td><td>{{.Count}}</td><td>{{pct .Share}}</td>
  <td>{{if .Premium.Suppressed}}{{sup .Premium}}{{else}}{{pp .PremiumPct}}{{end}}</td>
  <td>{{pct .EntryFriendly}}</td>
</tr>{{end}}
</table>

<div class="foot">Numbers computed by SQL from public MyCareersFuture data; data is refreshed daily, so it lags the live market by up to 24h. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate statistics only, no personal data.</div>
</div></body></html>`
```

- [ ] **Step 4: 写 handler 与路由**

Create `internal/web/tech.go`:

```go
package web

import (
	"context"
	"net/http"

	"github.com/meirongdev/jobs-sg/internal/metric"
	"github.com/meirongdev/jobs-sg/internal/view"
)

// parseLens reads the site-wide lens off the query string. An unknown value is
// a 400, not a silent no-op: rendering numbers that contradict the URL is
// worse than refusing, and free-text values would let a crafted URL mint
// unbounded cache keys.
func parseLens(r *http.Request) (metric.Lens, error) {
	return metric.ParseLens(r.URL.Query().Get("exp"), r.URL.Query().Get("role"))
}

func (s *Server) handleTech(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dailyTimeout)
	defer cancel()

	lens, err := parseLens(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := s.now()
	s.servePage(w, "tech:"+lens.Key(), now, func() (string, error) {
		rep, err := metric.TechReportFor(ctx, s.db, now, lens)
		if err != nil {
			return "", err
		}
		return view.TechPage(rep)
	})
}
```

在 `internal/web/server.go` 的 `Handler()` 里注册（放在 `GET /` 之后）：

```go
	mux.HandleFunc("GET /tech", s.handleTech)
```

- [ ] **Step 5: 确认通过**

Run: `go test ./internal/web/ -v && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/view/tech.go internal/web/tech.go internal/web/server.go internal/web/tech_test.go
git commit -m "feat(web): serve /tech with lens-aware demand, momentum and premium"
```

---

## Task 11: `/daily` 降级为 `/ops`

采集运行日志对求职者零价值。页面和代码保留（它是排障与"数据是否新鲜"的证据），但从主导航移除，旧路径 301 过去。

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/daily.go`
- Modify: `internal/report/daily_render.go`
- Modify: `internal/report/render.go`
- Modify: `internal/web/web_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `internal/web/web_test.go`：

```go
func TestDailyRedirectsToOps(t *testing.T) {
	s := setupWeb(t)
	for from, to := range map[string]string{
		"/daily":                    "/ops",
		"/daily?days=7":             "/ops?days=7",
		"/daily/" + sgtToday():      "/ops/" + sgtToday(),
	} {
		rec := get(t, s, from)
		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("GET %s = %d, want 301", from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != to {
			t.Errorf("GET %s -> %q, want %q", from, got, to)
		}
	}
}

func TestOpsIsNotInTheJobSeekerNav(t *testing.T) {
	// /ops stays reachable — but from the footer as a data-freshness link, not
	// from the nav a job seeker reads. Slice out the nav block and check there,
	// since a plain Contains would also match the footer link.
	s := setupWeb(t)
	body := get(t, s, "/tech").Body.String()
	open := strings.Index(body, `<nav class="nav">`)
	if open < 0 {
		t.Fatal("/tech has no nav block")
	}
	end := strings.Index(body[open:], "</nav>")
	if end < 0 {
		t.Fatal("/tech nav block is unterminated")
	}
	if nav := body[open : open+end]; strings.Contains(nav, "/ops") {
		t.Errorf("nav must not link to /ops, got: %s", nav)
	}
	if !strings.Contains(body, `href="/ops"`) {
		t.Error("/ops must still be reachable from the footer")
	}
}
```

并把既有的 `/daily` 断言全部改为 `/ops`：`TestDailyOverviewRoute`、`TestDailyWindowParam`、`TestDailyDayRoute`、`TestDailyDayRouteRejectsBadDates`、`TestDailyPagesAreCachedUntilTTLExpires` 里的请求路径由 `"/daily"` 改 `"/ops"`、`"/daily/"+today` 改 `"/ops/"+today`、`"/daily?days=..."` 改 `"/ops?days=..."`。`TestRobotsKeepsCrawlersOffDrillDowns` 的期望字符串由 `"Disallow: /daily/"` 改 `"Disallow: /ops/"`。

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/web/ -v`
Expected: FAIL — `/ops` 404，`/daily` 返回 200 而非 301

- [ ] **Step 3: 改路由**

`internal/web/server.go` 的 `Handler()`：

```go
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /tech", s.handleTech)
	mux.HandleFunc("GET /w/{week}", s.handleWeek)
	// Operational pages: kept as troubleshooting and data-freshness evidence,
	// but out of the job-seeker nav. The old /daily paths stay as permanent
	// redirects so existing links and bookmarks survive.
	mux.HandleFunc("GET /ops", s.handleDaily)
	mux.HandleFunc("GET /ops/{date}", s.handleDailyDate)
	mux.HandleFunc("GET /daily", redirectTo("/ops"))
	mux.HandleFunc("GET /daily/{date}", s.redirectDailyDate)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
```

在 `internal/web/server.go` 末尾追加：

```go
// redirectTo permanently redirects a retired path, preserving the query string.
//
// `to` is a per-request copy on purpose: appending to the captured `target`
// would accumulate query strings across requests, so the second visitor to
// /daily?days=7 would be sent to /ops?days=7?days=7.
func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		to := target
		if q := r.URL.RawQuery; q != "" {
			to += "?" + q
		}
		http.Redirect(w, r, to, http.StatusMovedPermanently)
	}
}

// redirectDailyDate maps /daily/{date} onto /ops/{date}. The date is validated
// before it lands in a Location header so the redirect cannot echo arbitrary
// path input back to the client.
func (s *Server) redirectDailyDate(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if _, err := report.ParseDay(date); err != nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/ops/"+date, http.StatusMovedPermanently)
}
```

`server.go` 的 import 加 `"github.com/meirongdev/jobs-sg/internal/report"`。

- [ ] **Step 4: 改 robots 与导航**

`internal/web/daily.go` 的 `handleRobots`：

```go
	w.Write([]byte("User-agent: *\nDisallow: /ops/\nCrawl-delay: 10\n"))
```

并把该函数上方的注释里的 `/daily/{date}` 改为 `/ops/{date}`。

`internal/report/daily_render.go` 的两处 `<nav class="nav">` 改为：

```go
<nav class="nav"><a href="/">Weekly report</a><a href="/tech">Tech</a></nav>
```

（`dailyTmpl` 与 `dayTmpl` 各一处；原来的 `<a href="/daily">Daily crawl stats</a>` 整个删掉——运维页自己不需要在导航里指向自己。）

**导航只列已存在的页面**：`/`、`/pay`、`/companies` 三项要等 Phase A-2 才有，本阶段若照 spec §2.1 的完整导航写死，上线就是两个 404 死链。A-2 建 `/pay`、`/companies` 时一并把导航补全。

`dayTmpl` 底部 `.pager` 里的三个链接由 `/daily/...` 改为 `/ops/...`：

```go
<div class="pager">
  <a href="/ops/{{.Prev}}">← {{.Prev}}</a>
  {{if .Next}}<a href="/ops/{{.Next}}">{{.Next}} →</a>{{end}}
  <a href="/ops">All days</a>
</div>
```

`internal/report/render.go` 周报模板的 `<nav>` 同样改（原来是 `Weekly report` + `Daily crawl stats` 两项）：

```go
<nav class="nav"><a class="on" href="/">Weekly report</a><a href="/tech">Tech</a></nav>
```

并把周报页脚的 `Methodology:` 一句后面补上 `/ops` 链接，与 `/tech` 页脚保持一致：

```go
<div class="foot">Numbers computed by SQL from public MyCareersFuture data. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate stats only, no personal data.</div>
```

- [ ] **Step 5: 确认通过**

Run: `go test ./... && go vet ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/web internal/report/daily_render.go internal/report/render.go
git commit -m "refactor(web): demote daily crawl stats to /ops with permanent redirects"
```

---

## 收尾验证

- [ ] **Step 1: 全量测试与静态检查**

Run: `make test && make vet && make build`
Expected: 全部 PASS，`bin/jobs-sg-{ingest,enrich,report,web}` 生成

- [ ] **Step 2: 本地目视检查 `/tech`**

```bash
rm -rf /tmp/jobs-sg-smoke && mkdir -p /tmp/jobs-sg-smoke
go run ./scripts/genfixture
go test ./internal/metric/ -run TestTechDemandRanksTheReportedWeek -v
./bin/jobs-sg-web --data-dir /tmp/jobs-sg-smoke --addr :8080
```
说明：空 DB 下 `/tech` 应渲染出页面骨架 + "needs 5 weeks of history" 说明，而不是 500 或空白图。这正是 spec §5 要求的一等状态。真实数据需要先跑 `ingest`（需要网络）。

- [ ] **Step 3: 确认未越界**

Run: `git diff --stat main`
Expected: 改动只落在本计划「文件结构」表列出的文件上；`docs/01-requirements.md`、`internal/report/metrics.go` 的指标口径、`internal/report/telegram.go` 均未被改（那些属于 Phase A-2）。

---

## Phase A-2 待办（下一份计划）

1. `/pay`：分位数网格（资历 × 方向）、经验阶梯（含"未标注"档）、薪资透明率 —— spec §3.3。
2. `/`：市场快报卡片 + 12 周新增趋势 + 入门岗绝对数 —— spec §3.4 的首屏部分。
3. `/companies`：持续招聘者、岗位寿命（需先给 fixture 灌 `closed_at`——它不是 MCF JSON 字段，只能在测试里写库）、竞争度分层（日均投递归一化）—— spec §3.5、§3.6。
4. 周报按新顺序重排 + Data Quality 收为页脚一行 + Telegram 改求职者口播 —— spec §4.5。
5. `docs/01-requirements.md` §1/§2/§5 按 spec §1.1 更新。
6. 物化 `tech_share` 与 `swe_enriched` 到 `weekly_metric` —— spec §3.1 的审计载体（口径变更全量重算的依据），随第 4 项一起落在 cmd/report；展示路径不变，仍走现算。
7. `internal/report` 的窗口助手（`sgt`/`WeekBounds`/`DayBounds`/`ISOWeekLabel`）收敛到 `metric.Window` —— 两份实现已有行为差异（report 版信任调用者预本地化，metric 版自己 `.In(SGT)`），不收敛迟早有人只修一份的边界 bug。随第 4 项周报重排一起做。
