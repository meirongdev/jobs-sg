# 求职者站点 Phase A-2a：导航收敛 + `/pay` 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付第二个求职者页面 `/pay`（薪资分位数网格 + 经验阶梯 + 透明率），并先把 4 处手写导航收敛成 `view.Nav`、把薪资透明率的两份实现合成一份。

**Architecture:** 沿用 A-1 的三层：`internal/metric` 出聚合（新增 `pay.go`）、`internal/view` 出模板（新增 `pay.go` + `Nav` 助手）、`internal/web` 只做路由与镜头解析。`/pay` 现算 + 60s 缓存，缓存键 `pay:` + 镜头。

**Tech Stack:** Go 1.26、标准库 `html/template` + `net/http`、modernc.org/sqlite。

**上游规格:** [spec §3.3](../specs/2026-08-07-jobseeker-facing-site-design.md)（分位数/阶梯/透明率口径）、§2.1（路由）、§2.3（镜头）、§5（抑制）。A-1 计划的 [Phase A-2 待办](2026-08-07-jobseeker-phase-a1-tech-page.md) 第 1、10 项在本计划内结清。

**本计划不做:** `/`（市场快报）与 `/companies` 留 A-2b（两者共用寿命/竞争度聚合，且 `/` 要把首页语义从静态周报换成现算快报）；周报重排、Telegram 改口播、`docs/01` 合规改写、items 6-9 的 report 侧收敛留 A-2c——那些收敛要**在**重写 report 章节时一起做，先收敛再重写等于做两遍。

**分支:** `feat/jobseeker-a2a-pay`（已建，从 `main` 的合并点起）。

**A-1 遗留的一处更正:** A-1 spec §7.1 写"扩 `scripts/genfixture` 掺入已下架岗位"——`closed_at` 是 ingest 生命周期写的 DB 列，MCF JSON 里没有这个字段，genfixture 造不出来。它只能在 `internal/metric/fixture_test.go` 的 `buildFixtureDB` 里灌。本计划不需要它（`/pay` 不看寿命），A-2b 需要时按此更正执行。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/view/nav.go` | **新**：`Nav(active string)` —— 站点主导航唯一来源 |
| `internal/view/nav_test.go` | **新**：导航项集合与高亮断言 |
| `internal/report/render.go` | **改**：周报模板用 `{{nav "/"}}`；FuncMap 注册 |
| `internal/report/daily_render.go` | **改**：两个 ops 模板用 `{{nav ""}}`；FuncMap 注册 |
| `internal/view/tech.go` | **改**：`/tech` 模板用 `{{nav "/tech"}}`；透明率字段改名随 Task 2 |
| `internal/metric/transparency.go` | **新**：`Transparency`（Disclosed/Total + `Pct()`）—— 薪资披露率唯一实现 |
| `internal/metric/tech.go` | **改**：`TechReport` 内嵌 `Transparency`，删本地 `SalaryN`/`SalaryTotal`/`TransparencyPct`；抽出 `salaryMidpoint`/`disclosedSalaryPredicate` |
| `internal/metric/tech_pay_test.go` | **改**：随字段改名更新引用 |
| `internal/classify/classify.go` | **改**：`seniorityLevels` 切片 + 导出 `SeniorityLevels()`，`seniorityRank` 改由切片派生（单一来源） |
| `internal/classify/classify_test.go` | **改**：加 `SeniorityLevels` 与 `seniorityRank` 一致性测试 |
| `internal/metric/window.go` | **改**：加 `Window.RangeLabel()` |
| `internal/metric/lens.go` | **改**：加 `Lens.RoleOnly()` |
| `internal/metric/pay.go` | **新**：`PayReport` 全部聚合（网格 / 阶梯 / 透明率） |
| `internal/metric/pay_test.go` | **新**：口径、抑制边界、镜头行为 |
| `internal/view/pay.go` | **新**：`/pay` 模板与 `PayPage` |
| `internal/web/pay.go` | **新**：`/pay` handler |
| `internal/web/server.go` | **改**：注册 `GET /pay` |
| `internal/web/pay_test.go` | **新**：路由、镜头 400、缓存键、抑制渲染 |

---

## Task 1: `view.Nav` 收敛四处手写导航

现在 4 个模板各自手写 `<nav class="nav">`（`report/render.go:86`、`report/daily_render.go:150` 与 `:204`、`view/tech.go:88`）。`.nav` 的**样式**在 Task 6 已收敛进 `view/css.go`，只有**标记**还是复制的——A-2a/A-2b 各加一页就是 6 份要锁步改。本任务把它收成一处。

**Files:**
- Create: `internal/view/nav.go`
- Test: `internal/view/nav_test.go`
- Modify: `internal/report/render.go`、`internal/report/daily_render.go`、`internal/view/tech.go`

- [ ] **Step 1: 写失败测试**

Create `internal/view/nav_test.go`:

```go
package view

import (
	"strings"
	"testing"
)

func TestNavListsEveryJobSeekerPageOnce(t *testing.T) {
	html := string(Nav("/tech"))
	for _, want := range []string{`href="/"`, `href="/tech"`, `href="/pay"`} {
		if strings.Count(html, want) != 1 {
			t.Errorf("nav must link %s exactly once, got %d: %s", want, strings.Count(html, want), html)
		}
	}
	// /ops is operational telemetry: reachable from page footers, never here.
	if strings.Contains(html, "/ops") {
		t.Errorf("nav must not link /ops: %s", html)
	}
}

func TestNavMarksTheActivePage(t *testing.T) {
	html := string(Nav("/pay"))
	if !strings.Contains(html, `<a class="on" href="/pay">`) {
		t.Errorf("active page must carry class=on: %s", html)
	}
	if strings.Count(html, `class="on"`) != 1 {
		t.Errorf("exactly one active link expected: %s", html)
	}
}

func TestNavWithoutAnActivePageHighlightsNothing(t *testing.T) {
	// The ops pages are not in the nav, so they pass "" and no item lights up.
	html := string(Nav(""))
	if strings.Contains(html, `class="on"`) {
		t.Errorf("no item may be active when active is empty: %s", html)
	}
	if !strings.Contains(html, `href="/tech"`) {
		t.Errorf("nav still lists every page: %s", html)
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/view/ -run Nav -v`
Expected: FAIL — `undefined: Nav`

- [ ] **Step 3: 实现 `Nav`**

Create `internal/view/nav.go`:

```go
package view

import (
	"html/template"
	"strings"
)

// navItem is one entry of the site's main navigation.
type navItem struct {
	Href  string
	Label string
}

// navItems is the job-seeker navigation, in reading order. This slice is the
// single source of truth: every page renders it through Nav, so adding a page
// is one edit here instead of one per template. Operational pages (/ops) stay
// out on purpose — they are linked from page footers as data-freshness
// evidence, not offered to someone looking for work.
var navItems = []navItem{
	{"/", "Weekly report"},
	{"/tech", "Tech"},
	{"/pay", "Pay"},
}

// Nav renders the main navigation, marking active as the current page. Pass ""
// from pages that are not in the nav (the ops pages), and nothing lights up.
func Nav(active string) template.HTML {
	var b strings.Builder
	b.WriteString(`<nav class="nav">`)
	for _, it := range navItems {
		if it.Href == active {
			b.WriteString(`<a class="on" href="` + it.Href + `">`)
		} else {
			b.WriteString(`<a href="` + it.Href + `">`)
		}
		b.WriteString(template.HTMLEscapeString(it.Label) + `</a>`)
	}
	b.WriteString(`</nav>`)
	return template.HTML(b.String())
}
```

（`Href` 不经转义是安全的：它们是本文件内的编译期常量，从不来自请求；`Label` 仍走转义，因为将来有人加带 `&` 的标签是可信的失误。）

- [ ] **Step 4: 四处调用点改为 `{{nav …}}`**

`internal/view/tech.go`：FuncMap 加 `"nav": Nav,`；模板第 88 行整行替换为：

```
{{nav "/tech"}}
```

`internal/report/render.go`：`RenderHTML` 的 FuncMap 加 `"nav": view.Nav,`；模板里 `<nav class="nav">…</nav>` 那行替换为：

```
{{nav "/"}}
```

`internal/report/daily_render.go`：`newDailyTemplate` 的 FuncMap 加 `"nav": view.Nav,`；`dailyTmpl` 与 `dayTmpl` 各自的 `<nav class="nav">…</nav>` 行都替换为：

```
{{nav ""}}
```

- [ ] **Step 5: 确认全绿**

Run: `go test ./... -count=1 && go vet ./... && gofmt -l internal/`
Expected: 全部 PASS，gofmt 空。**注意** `internal/web/web_test.go` 的 `TestOpsIsNotInTheJobSeekerNav` 会切出 `<nav class="nav">` 块检查里面没有 `/ops` —— `Nav` 的输出仍是这个结构，该测试必须原样通过；若它红了说明 `Nav` 的包装标记写错了。

再跑一次断言导航现在只有一处定义：

Run: `grep -rn '<nav class="nav">' internal/ --include='*.go'`
Expected: 只有 `internal/view/nav.go` 一处命中。

- [ ] **Step 6: Commit**

```bash
git add internal/view/nav.go internal/view/nav_test.go internal/view/tech.go internal/report/render.go internal/report/daily_render.go
git commit -m "refactor(view): collapse four hand-written navs into view.Nav" -- internal/view/nav.go internal/view/nav_test.go internal/view/tech.go internal/report/render.go internal/report/daily_render.go
```

---

## Task 2: `metric.Transparency` —— 薪资披露率的唯一实现

`TechReport` 现在自带 `SalaryN`/`SalaryTotal`/`TransparencyPct()`。`/pay` 需要同一个数（spec §3.3 要求整体 + 按公司类型拆分），照抄一份就是 A-2 待办 7/8/9/10 反复记录的那个病。趁第二个消费者出现时收敛。

**Files:**
- Create: `internal/metric/transparency.go`
- Modify: `internal/metric/tech.go`、`internal/metric/tech_pay_test.go`、`internal/view/tech.go`
- Test: `internal/metric/transparency_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/metric/transparency_test.go`:

```go
package metric

import "testing"

func TestTransparencyPct(t *testing.T) {
	if got := (Transparency{Disclosed: 288, Total: 360}).Pct(); got != 0.8 {
		t.Errorf("Pct = %v, want 0.8", got)
	}
	// A window with no postings must read 0, not NaN: the page prints this
	// next to every salary figure, and NaN renders as "NaN%".
	if got := (Transparency{}).Pct(); got != 0 {
		t.Errorf("empty window Pct = %v, want 0", got)
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/metric/ -run Transparency -v`
Expected: FAIL — `undefined: Transparency`

- [ ] **Step 3: 实现并让 `TechReport` 内嵌它**

Create `internal/metric/transparency.go`:

```go
package metric

// Transparency is the salary-disclosure rate over a window: how many postings
// advertise a monthly salary out of how many exist at all.
//
// Every median in this package describes only the disclosed subset, so the
// pair travels with the number rather than being recomputed per page — two
// hand-rolled copies would drift the moment "disclosed" changes meaning.
type Transparency struct {
	Disclosed int
	Total     int
}

// Pct is the disclosed share, or 0 for an empty window (never NaN — this value
// is printed beside every salary figure).
func (t Transparency) Pct() float64 {
	if t.Total == 0 {
		return 0
	}
	return float64(t.Disclosed) / float64(t.Total)
}
```

`internal/metric/tech.go` 的 `TechReport`：把

```go
	MedianAll        float64    // rolling-90d median monthly salary, the premium baseline
	SalaryN          int        // disclosed monthly salaries behind MedianAll
	SalaryTotal      int        // every SWE posting in the same window — the transparency denominator
```

替换为

```go
	MedianAll        float64      // rolling-90d median monthly salary, the premium baseline
	Salary           Transparency // disclosed vs all postings behind MedianAll
```

`TechReportFor` 里的两处赋值：

```go
	r.Salary.Disclosed = len(allSalaries)
	r.MedianAll = Percentile(allSalaries, 0.5)
	if r.Salary.Total, err = sweCount(ctx, db, roll, lens); err != nil {
		return nil, err
	}
```

删除文件末尾的 `TransparencyPct` 方法（连注释）——它的职责已经搬进 `Transparency.Pct`。

- [ ] **Step 4: 顺带抽出两个共享 SQL 片段**

同文件内，`disclosedSalary` 常量替换为三个常量（`/pay` 的 `CASE WHEN` 需要不带前缀 `AND` 的裸谓词，而中点表达式即将出现在第三处）：

```go
// salaryMidpoint is the advertised range's midpoint — the single value every
// salary statistic in this package is computed over.
const salaryMidpoint = `(j.salary_min+j.salary_max)/2.0`

// disclosedSalaryPredicate limits salary figures to publicly advertised
// monthly ranges. disclosedSalary is its WHERE-appendable form; both come from
// one definition so a change to what counts as disclosed cannot land in only
// one of them. The share of postings that disclose at all is itself a headline
// number (spec §3.3) — these medians describe only that subset.
const disclosedSalaryPredicate = `j.salary_hidden=0 AND j.salary_type='Monthly'
	AND j.salary_min IS NOT NULL AND j.salary_max IS NOT NULL`

const disclosedSalary = `AND ` + disclosedSalaryPredicate
```

`salarySample` 里两处 `(j.salary_min+j.salary_max)/2.0` 改用常量拼接：

```go
	q := `SELECT ` + salaryMidpoint + ` ` + swePosted + lens.Where() + ` ` + disclosedSalary + ` ORDER BY 1`
	args := w.Args()
	if slug != "" {
		q = `SELECT min(` + salaryMidpoint + `)
			FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
			WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?` +
			lens.Where() + ` ` + disclosedSalary + ` AND t.tech_slug = ?
			GROUP BY j.uuid ORDER BY 1`
```

- [ ] **Step 5: 更新两处消费者**

`internal/metric/tech_pay_test.go` 的 `TestPremiumBaselineIsARealAdvertisedSalary` 中，`r.SalaryN` → `r.Salary.Disclosed`、`r.SalaryTotal` → `r.Salary.Total`、`r.TransparencyPct()` → `r.Salary.Pct()`（三处断言的语义与消息不变）。`TestPremiumFollowsTheLens` 中 `senior.SalaryN`/`all.SalaryN` 同样改为 `.Salary.Disclosed`。

`internal/view/tech.go` 的 Section 3 note：`{{.SalaryN}} of {{.SalaryTotal}}` → `{{.Salary.Disclosed}} of {{.Salary.Total}}`，`{{pct .TransparencyPct}}` → `{{pct .Salary.Pct}}`。

- [ ] **Step 6: 确认全绿**

Run: `go test ./... -count=1 && go vet ./... && gofmt -l internal/`
Expected: PASS。`grep -rn 'SalaryN\|SalaryTotal\|TransparencyPct' internal/` 应零命中。

- [ ] **Step 7: Commit**

```bash
git add internal/metric/transparency.go internal/metric/transparency_test.go internal/metric/tech.go internal/metric/tech_pay_test.go internal/view/tech.go
git commit -m "refactor(metric): one Transparency type behind every salary figure" -- internal/metric/transparency.go internal/metric/transparency_test.go internal/metric/tech.go internal/metric/tech_pay_test.go internal/view/tech.go
```

---

## Task 3: 资历顺序单一来源 + 两个窗口/镜头助手

`/pay` 的网格按资历排行、按方向排列，还要一个"滚动 90 天"的人类可读标签和一个"只保留方向维度"的镜头。三样都不该在 `pay.go` 里就地拼。

`classify` 里 `seniorityRank` 用 switch 写死了 7 个字面量，而 `/pay` 需要同样的顺序——照抄进 metric 就是又一处漂移源。改成切片派生，行为等价（`slices.Index` 缺失时返回 -1，与原 default 一致）。

**Files:**
- Modify: `internal/classify/classify.go`、`internal/classify/classify_test.go`
- Modify: `internal/metric/window.go`、`internal/metric/lens.go`
- Test: `internal/metric/window_test.go`、`internal/metric/lens_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `internal/classify/classify_test.go`:

```go
// TestSeniorityLevelsIsTheRankingsSingleSource pins that the exported
// vocabulary and the internal ranking cannot disagree: /pay renders rows in
// SeniorityLevels order and relies on it matching the ranking classify uses
// when a title and a stated experience conflict.
func TestSeniorityLevelsIsTheRankingsSingleSource(t *testing.T) {
	levels := SeniorityLevels()
	if len(levels) != 7 {
		t.Fatalf("levels = %v, want 7 entries", levels)
	}
	for i, l := range levels {
		if got := seniorityRank(l); got != i {
			t.Errorf("seniorityRank(%q) = %d, want %d (its index)", l, got, i)
		}
	}
	if got := seniorityRank("Nonexistent"); got != -1 {
		t.Errorf("unknown level rank = %d, want -1", got)
	}
	// Mutating the returned slice must not corrupt the package's own order.
	levels[0] = "Tampered"
	if SeniorityLevels()[0] != "Intern" {
		t.Error("SeniorityLevels must hand back a copy")
	}
}
```

追加到 `internal/metric/window_test.go`:

```go
func TestRangeLabelNamesTheInclusiveSGTDays(t *testing.T) {
	// End is exclusive, so the label's last day is End − 1 day: a 90-day
	// window ending at today's SGT day end reads through today, not tomorrow.
	w := Rolling(time.Date(2026, 8, 10, 9, 0, 0, 0, SGT), 90)
	if got := w.RangeLabel(); got != "2026-05-13 → 2026-08-10" {
		t.Errorf("RangeLabel = %q, want 2026-05-13 → 2026-08-10", got)
	}
}
```

追加到 `internal/metric/lens_test.go`:

```go
func TestRoleOnlyDropsTheExperienceBand(t *testing.T) {
	// The /pay experience ladder IS the experience dimension, so it must not be
	// filtered by the experience lens — only by role.
	l, err := ParseLens("3-5", "Backend")
	if err != nil {
		t.Fatal(err)
	}
	ro := l.RoleOnly()
	if ro.Exp != "" || ro.Role != "Backend" {
		t.Errorf("RoleOnly() = %+v, want only Role=Backend", ro)
	}
	if strings.Contains(ro.Where(), "min_years_exp") {
		t.Errorf("RoleOnly().Where() = %q, must not constrain experience", ro.Where())
	}
	if l.Exp != "3-5" {
		t.Error("RoleOnly must not mutate its receiver")
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/classify/ ./internal/metric/ -run 'Seniority|RangeLabel|RoleOnly' -v`
Expected: FAIL — `undefined: SeniorityLevels`、`RangeLabel`、`RoleOnly`

- [ ] **Step 3: 实现**

`internal/classify/classify.go`：把

```go
func seniorityRank(s string) int {
	switch s {
	case "Intern":
		return 0
	...
	}
	return -1
}
```

整段替换为：

```go
// seniorityLevels is the seniority vocabulary in career order. It is the one
// definition: seniorityRank derives from it, and SeniorityLevels exports it for
// the pages that render seniority rows, so a level added here appears
// everywhere instead of being re-listed per consumer.
var seniorityLevels = []string{"Intern", "Junior", "Mid", "Senior", "Staff+", "Lead", "Manager"}

// SeniorityLevels returns the seniority vocabulary in career order.
func SeniorityLevels() []string { return slices.Clone(seniorityLevels) }

func seniorityRank(s string) int { return slices.Index(seniorityLevels, s) }
```

import 加 `"slices"`。（行为等价：原 switch 对这 7 个值返回 0..6、其余 -1，与 `slices.Index` 逐一相同。）

`internal/metric/window.go` 末尾追加：

```go
// RangeLabel describes the window as inclusive SGT calendar dates, for page
// headers. End is exclusive, so the last named day is End − 1 day.
func (w Window) RangeLabel() string {
	first := w.Start.In(SGT).Format("2006-01-02")
	last := w.End.In(SGT).AddDate(0, 0, -1).Format("2006-01-02")
	return first + " → " + last
}
```

`internal/metric/lens.go` 末尾追加：

```go
// RoleOnly drops the experience band, keeping the role family. The /pay
// experience ladder is itself the experience breakdown, so filtering it by the
// experience lens would collapse it to a single rung.
func (l Lens) RoleOnly() Lens { return Lens{Role: l.Role} }
```

（`lens_test.go` 已 import `strings`，`window_test.go` 已 import `time`——两个新测试不需要动 import 块。）

- [ ] **Step 4: 确认全绿**

Run: `go test ./... -count=1 && go vet ./... && gofmt -l internal/`
Expected: PASS。特别确认 `internal/classify` 的既有测试（`TestSeniority*`、`TestFixtureReplay`）原样通过——这是 `seniorityRank` 重构行为等价的证据。

- [ ] **Step 5: Commit**

```bash
git add internal/classify/classify.go internal/classify/classify_test.go internal/metric/window.go internal/metric/window_test.go internal/metric/lens.go internal/metric/lens_test.go
git commit -m "refactor(classify,metric): derive seniority order from one slice, add window/lens helpers" -- internal/classify/classify.go internal/classify/classify_test.go internal/metric/window.go internal/metric/window_test.go internal/metric/lens.go internal/metric/lens_test.go
```

---

## Task 4: `/pay` 聚合 —— 分位数网格、经验阶梯、透明率

spec §3.3 的三块。三条口径决定必须原样落进注释：

1. **分位数用最近秩**（复用 `Percentile`）——报出的每个数字都是真实登过的薪资。`Percentile` 对乱序输入 panic，所以每个取值查询都必须 `ORDER BY` 到分组内升序。
2. **单元格 `n < MinSalarySamplesPerCell`（5）抑制**——既防伪精度，也避免一个格子等于公开某个雇主的挂牌薪资。
3. **经验阶梯的档位与镜头档位不同**：阶梯是 `0 / 1-2 / 3-5 / 6+ / unstated`（把"明确不要求经验"与"1-2 年"分开，spec §3.7-1），镜头是 `0-2 / 3-5 / 6+ / unstated`（粗档，够筛选用）。**不要"统一"它们**——阶梯要回答"第一年值多少钱"，镜头不需要那个分辨率。
4. **阶梯忽略经验镜头**（走 `Lens.RoleOnly()`），因为它本身就是经验维度；网格与透明率两个维度都跟随镜头。

**Files:**
- Create: `internal/metric/pay.go`
- Test: `internal/metric/pay_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/metric/pay_test.go`:

```go
package metric

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

func TestPayGridPercentilesAreRealAdvertisedSalaries(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := PayReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Overall.Coverage.Suppressed {
		t.Fatalf("overall cell suppressed with the full fixture: %+v", r.Overall.Coverage)
	}
	for _, q := range []float64{r.Overall.P25, r.Overall.P50, r.Overall.P75} {
		var n int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM job
			WHERE is_swe=1 AND salary_hidden=0 AND salary_type='Monthly'
			  AND salary_min IS NOT NULL AND salary_max IS NOT NULL
			  AND (salary_min+salary_max)/2.0 = ?`, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("percentile %v was never advertised by any posting", q)
		}
	}
	if !(r.Overall.P25 <= r.Overall.P50 && r.Overall.P50 <= r.Overall.P75) {
		t.Errorf("percentiles out of order: %v / %v / %v", r.Overall.P25, r.Overall.P50, r.Overall.P75)
	}
}

func TestPayGridShapeFollowsTheVocabularies(t *testing.T) {
	r, err := PayReportFor(context.Background(), seedFixture(t), fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Roles) != len(RoleFamilies()) {
		t.Errorf("grid has %d role columns, want %d", len(r.Roles), len(RoleFamilies()))
	}
	if len(r.RoleTotals) != len(r.Roles) {
		t.Errorf("%d column totals for %d columns", len(r.RoleTotals), len(r.Roles))
	}
	if len(r.Grid) != len(classify.SeniorityLevels()) {
		t.Errorf("grid has %d seniority rows, want %d", len(r.Grid), len(classify.SeniorityLevels()))
	}
	for i, row := range r.Grid {
		if row.Seniority != classify.SeniorityLevels()[i] {
			t.Errorf("row %d = %q, want %q (career order)", i, row.Seniority, classify.SeniorityLevels()[i])
		}
		if len(row.Cells) != len(r.Roles) {
			t.Fatalf("row %q has %d cells, want %d", row.Seniority, len(row.Cells), len(r.Roles))
		}
	}
}

func TestPayCellSuppressionBoundary(t *testing.T) {
	// Four disclosed postings in one (seniority, role) cell must suppress; five
	// must not. Seeded rather than fixture-derived so the boundary is exact.
	ctx := context.Background()
	for _, tc := range []struct {
		n          int
		suppressed bool
	}{{4, true}, {5, false}} {
		db := seedControlledPay(t, tc.n)
		r, err := PayReportFor(ctx, db, fixtureNow, Lens{})
		if err != nil {
			t.Fatal(err)
		}
		cell, ok := findCell(r, "Senior", classify.FamilyBackend)
		if !ok {
			t.Fatalf("n=%d: seeded cell missing from the grid", tc.n)
		}
		if cell.Coverage.Suppressed != tc.suppressed {
			t.Errorf("n=%d: suppressed = %v, want %v (samples=%d)",
				tc.n, cell.Coverage.Suppressed, tc.suppressed, cell.Coverage.Samples)
		}
		if cell.Coverage.Samples != tc.n {
			t.Errorf("n=%d: samples = %d, want the real count", tc.n, cell.Coverage.Samples)
		}
		if tc.suppressed && (cell.P50 != 0 || cell.P25 != 0 || cell.P75 != 0) {
			t.Errorf("n=%d: suppressed cell carries values %v/%v/%v", tc.n, cell.P25, cell.P50, cell.P75)
		}
	}
}

func TestLadderKeepsZeroAndOneToTwoApart(t *testing.T) {
	// spec §3.7-1: "no experience required" (0) and "1-2 years" are different
	// answers to "can I apply", and both differ from "did not say".
	r, err := PayReportFor(context.Background(), seedFixture(t), fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0", "1-2", "3-5", "6+", "unstated"}
	if len(r.Ladder) != len(want) {
		t.Fatalf("ladder has %d rungs, want %d", len(r.Ladder), len(want))
	}
	for i, label := range want {
		if r.Ladder[i].Label != label {
			t.Errorf("rung %d = %q, want %q", i, r.Ladder[i].Label, label)
		}
	}
	var zero, oneTwo *PayBand
	for i := range r.Ladder {
		switch r.Ladder[i].Label {
		case "0":
			zero = &r.Ladder[i]
		case "1-2":
			oneTwo = &r.Ladder[i]
		}
	}
	if zero.Postings == 0 || oneTwo.Postings == 0 {
		t.Fatalf("fixture must populate both rungs: 0=%d, 1-2=%d", zero.Postings, oneTwo.Postings)
	}
	// A merged "0-2" band would carry both rungs' postings; keeping them apart
	// means neither rung can equal the sum.
	var unstated *PayBand
	for i := range r.Ladder {
		if r.Ladder[i].Label == "unstated" {
			unstated = &r.Ladder[i]
		}
	}
	if unstated.Postings == 0 {
		t.Error("fixture has null-experience rows; the unstated rung must not be empty")
	}
	if zero.Postings == zero.Postings+oneTwo.Postings {
		t.Error("rung 0 carries 1-2's postings; the bands were merged")
	}
}

func TestLadderIgnoresTheExperienceLensButFollowsRole(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	all, err := PayReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	expLens, err := ParseLens("6+", "")
	if err != nil {
		t.Fatal(err)
	}
	lensed, err := PayReportFor(ctx, db, fixtureNow, expLens)
	if err != nil {
		t.Fatal(err)
	}
	for i := range all.Ladder {
		if all.Ladder[i].Postings != lensed.Ladder[i].Postings {
			t.Errorf("rung %q changed under an experience lens (%d -> %d); the ladder IS that dimension",
				all.Ladder[i].Label, all.Ladder[i].Postings, lensed.Ladder[i].Postings)
		}
	}
	// The grid, by contrast, must narrow.
	if lensed.Salary.Total >= all.Salary.Total {
		t.Errorf("lensed window total %d must be smaller than %d", lensed.Salary.Total, all.Salary.Total)
	}
	roleLens, err := ParseLens("", classify.FamilyBackend)
	if err != nil {
		t.Fatal(err)
	}
	byRole, err := PayReportFor(ctx, db, fixtureNow, roleLens)
	if err != nil {
		t.Fatal(err)
	}
	var allTotal, roleTotal int
	for i := range all.Ladder {
		allTotal += all.Ladder[i].Postings
		roleTotal += byRole.Ladder[i].Postings
	}
	if roleTotal >= allTotal || roleTotal == 0 {
		t.Errorf("ladder totals under a role lens = %d, want 0 < n < %d", roleTotal, allTotal)
	}
}

func TestTransparencyByCompanyTypeSuppressesThinTypes(t *testing.T) {
	r, err := PayReportFor(context.Background(), seedFixture(t), fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Salary.Total == 0 {
		t.Fatal("no postings in the window")
	}
	if p := r.Salary.Pct(); p <= 0 || p > 1 {
		t.Errorf("overall transparency = %v, want (0,1]", p)
	}
	var shown, hidden int
	for _, row := range r.ByCompany {
		if row.Coverage.Suppressed {
			hidden++
			if row.Total >= MinPostingsPerCompanyStat {
				t.Errorf("%s suppressed with %d postings", row.CompanyType, row.Total)
			}
		} else {
			shown++
			if row.Total < MinPostingsPerCompanyStat {
				t.Errorf("%s shown with only %d postings", row.CompanyType, row.Total)
			}
			if row.Disclosed > row.Total {
				t.Errorf("%s discloses %d of %d", row.CompanyType, row.Disclosed, row.Total)
			}
		}
	}
	if shown == 0 {
		t.Error("no company type cleared the threshold; the fixture has several")
	}
	_ = hidden
}

// seedControlledPay builds a DB with exactly n disclosed Senior/Backend
// postings in the rolling window, for exact-boundary assertions.
func seedControlledPay(t *testing.T, n int) *store.DB {
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
	cl := classify.New(map[string]string{"25121": classify.FamilyBackend})
	day := LastCompletedWeek(fixtureNow).Start.In(SGT).Format("2006-01-02")
	for i := 0; i < n; i++ {
		j := mcf.Job{
			UUID: fmt.Sprintf("ctl-%03d", i), Title: "Senior Backend Engineer",
			Description: "d",
			Metadata: mcf.Metadata{JobPostID: fmt.Sprintf("MCF-ctl-%03d", i),
				NewPostingDate: day, ExpiryDate: "2026-12-31"},
			SSOCCode:   "25121",
			Categories: []mcf.Category{{Category: "Information Technology"}},
			Salary: &mcf.Salary{Minimum: float64(6000 + i*100), Maximum: float64(8000 + i*100),
				Type: mcf.SalaryType{SalaryType: "Monthly"}},
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/ctl#0"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func findCell(r *PayReport, seniority, role string) (PayCell, bool) {
	for _, row := range r.Grid {
		if row.Seniority != seniority {
			continue
		}
		for i, col := range r.Roles {
			if col == role {
				return row.Cells[i], true
			}
		}
	}
	return PayCell{}, false
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/metric/ -run Pay -v`
Expected: FAIL — `undefined: PayReportFor`

- [ ] **Step 3: 实现聚合**

Create `internal/metric/pay.go`:

```go
package metric

import (
	"context"
	"sort"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/store"
)

// PayCell is one (seniority, role) cell of the percentile grid, or a total.
type PayCell struct {
	P25, P50, P75 float64
	Coverage      Coverage // suppressed below MinSalarySamplesPerCell
}

// PayRow is one seniority row of the grid.
type PayRow struct {
	Seniority string
	Cells     []PayCell // index-aligned with PayReport.Roles
	All       PayCell   // the row across every role
}

// PayBand is one rung of the experience ladder.
type PayBand struct {
	Label         string // "0" | "1-2" | "3-5" | "6+" | "unstated"
	P25, P50, P75 float64
	Postings      int      // every SWE posting in the band, disclosed or not
	Coverage      Coverage // suppressed below MinSalarySamplesPerCell
}

// TransparencyRow is one company type's disclosure rate.
type TransparencyRow struct {
	CompanyType string
	Transparency
	Coverage Coverage // suppressed below MinPostingsPerCompanyStat
}

// PayReport is the /pay page model.
type PayReport struct {
	Window    string   // inclusive SGT date range of the rolling window
	Days      int      // RollingDays, so the page can state its own window
	Roles      []string  // grid columns, career-neutral alphabetical order
	Grid       []PayRow  // rows in classify.SeniorityLevels order
	RoleTotals []PayCell // each role across every seniority, index-aligned with Roles
	Overall    PayCell
	Ladder    []PayBand
	Salary    Transparency // the whole window: disclosed vs all postings
	ByCompany []TransparencyRow
	Lens      Lens
}

// ladderBands are the experience rungs, in career order.
//
// Deliberately finer than the lens bands (spec §2.3 uses 0-2 as one band):
// the ladder answers "what does the first year buy", so 0 and 1-2 must stay
// apart, and "unstated" is never folded into "no requirement" (spec §3.7-1).
var ladderBands = []struct {
	Label     string
	Predicate string
}{
	{"0", `j.min_years_exp = 0`},
	{"1-2", `j.min_years_exp BETWEEN 1 AND 2`},
	{"3-5", `j.min_years_exp BETWEEN 3 AND 5`},
	{"6+", `j.min_years_exp >= 6`},
	{"unstated", `j.min_years_exp IS NULL`},
}

// PayReportFor builds the /pay model over the trailing RollingDays window.
//
// A rolling window, not one week: a single week's disclosed salaries do not
// survive being split across a seniority × role grid.
func PayReportFor(ctx context.Context, db *store.DB, now time.Time, lens Lens) (*PayReport, error) {
	w := Rolling(now, RollingDays)
	r := &PayReport{
		Window: w.RangeLabel(),
		Days:   RollingDays,
		Roles:  RoleFamilies(),
		Lens:   lens,
	}

	cells, err := gridSamples(ctx, db, w, lens)
	if err != nil {
		return nil, err
	}
	var overall []float64
	for _, row := range cells {
		for _, vals := range row {
			overall = append(overall, vals...)
		}
	}
	sort.Float64s(overall)
	r.Overall = cellOf(overall)

	perRole := make([][]float64, len(r.Roles))
	for _, sen := range classify.SeniorityLevels() {
		row := PayRow{Seniority: sen, Cells: make([]PayCell, len(r.Roles))}
		var rowVals []float64
		for i, role := range r.Roles {
			vals := cells[sen][role]
			row.Cells[i] = cellOf(vals)
			rowVals = append(rowVals, vals...)
			perRole[i] = append(perRole[i], vals...)
		}
		sort.Float64s(rowVals)
		row.All = cellOf(rowVals)
		r.Grid = append(r.Grid, row)
	}
	// Column totals: a role's pay across every level. Re-sorted because the
	// per-cell samples were concatenated, and Percentile panics on unsorted
	// input rather than quietly returning a plausible wrong number.
	r.RoleTotals = make([]PayCell, len(r.Roles))
	for i := range perRole {
		sort.Float64s(perRole[i])
		r.RoleTotals[i] = cellOf(perRole[i])
	}

	// The ladder is the experience dimension itself, so it drops the
	// experience lens and keeps only the role filter.
	if r.Ladder, err = ladder(ctx, db, w, lens.RoleOnly()); err != nil {
		return nil, err
	}
	if r.Salary, err = windowTransparency(ctx, db, w, lens); err != nil {
		return nil, err
	}
	if r.ByCompany, err = transparencyByCompanyType(ctx, db, w, lens); err != nil {
		return nil, err
	}
	return r, nil
}

// cellOf turns an ascending sample into a cell, suppressing thin ones. A
// suppressed cell carries no values: a percentile over four salaries is both
// pseudo-precise and close to publishing one employer's advertised range.
func cellOf(sorted []float64) PayCell {
	c := PayCell{Coverage: SampleCoverage(len(sorted), MinSalarySamplesPerCell)}
	if c.Coverage.Suppressed {
		return c
	}
	c.P25 = Percentile(sorted, 0.25)
	c.P50 = Percentile(sorted, 0.5)
	c.P75 = Percentile(sorted, 0.75)
	return c
}

// gridSamples returns seniority -> role -> ascending disclosed midpoints.
//
// ORDER BY seniority, role_family, midpoint is load-bearing: Percentile panics
// on unsorted input, and grouping in Go preserves the per-group ascending order
// this ORDER BY produces.
func gridSamples(ctx context.Context, db *store.DB, w Window, lens Lens) (map[string]map[string][]float64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT coalesce(j.seniority,''), coalesce(j.role_family,''), `+salaryMidpoint+` `+
		swePosted+lens.Where()+` `+disclosedSalary+`
		ORDER BY 1, 2, 3`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string][]float64{}
	for rows.Next() {
		var sen, role string
		var v float64
		if err := rows.Scan(&sen, &role, &v); err != nil {
			return nil, err
		}
		if out[sen] == nil {
			out[sen] = map[string][]float64{}
		}
		out[sen][role] = append(out[sen][role], v)
	}
	return out, rows.Err()
}

// ladder returns one rung per experience band: the disclosed-salary
// percentiles plus the band's full posting count, so a reader can see that a
// median rests on a fraction of the rung.
func ladder(ctx context.Context, db *store.DB, w Window, lens Lens) ([]PayBand, error) {
	out := make([]PayBand, 0, len(ladderBands))
	for _, b := range ladderBands {
		band := PayBand{Label: b.Label}
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) `+swePosted+lens.Where()+` AND `+b.Predicate,
			w.Args()...).Scan(&band.Postings); err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx,
			`SELECT `+salaryMidpoint+` `+swePosted+lens.Where()+` `+disclosedSalary+
				` AND `+b.Predicate+` ORDER BY 1`, w.Args()...)
		if err != nil {
			return nil, err
		}
		var vals []float64
		for rows.Next() {
			var v float64
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			vals = append(vals, v)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		cell := cellOf(vals)
		band.P25, band.P50, band.P75, band.Coverage = cell.P25, cell.P50, cell.P75, cell.Coverage
		out = append(out, band)
	}
	return out, nil
}

// windowTransparency is the disclosure rate over the whole window.
func windowTransparency(ctx context.Context, db *store.DB, w Window, lens Lens) (Transparency, error) {
	var t Transparency
	err := db.QueryRowContext(ctx, `
		SELECT count(*), coalesce(sum(CASE WHEN `+disclosedSalaryPredicate+` THEN 1 ELSE 0 END),0) `+
		swePosted+lens.Where(), w.Args()...).Scan(&t.Total, &t.Disclosed)
	return t, err
}

// transparencyByCompanyType answers "who tells you the number before you
// apply", ordered by disclosure rate so the transparent employers surface.
// Types below MinPostingsPerCompanyStat are suppressed rather than ranked on a
// handful of postings.
func transparencyByCompanyType(ctx context.Context, db *store.DB, w Window, lens Lens) ([]TransparencyRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT coalesce(c.company_type,'Other'), count(*),
		       coalesce(sum(CASE WHEN `+disclosedSalaryPredicate+` THEN 1 ELSE 0 END),0)
		FROM job j LEFT JOIN company c ON c.uen=j.company_uen
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`+lens.Where()+`
		GROUP BY 1`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TransparencyRow
	for rows.Next() {
		var row TransparencyRow
		if err := rows.Scan(&row.CompanyType, &row.Total, &row.Disclosed); err != nil {
			return nil, err
		}
		row.Coverage = SampleCoverage(row.Total, MinPostingsPerCompanyStat)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Coverage.Suppressed != out[j].Coverage.Suppressed {
			return !out[i].Coverage.Suppressed
		}
		if out[i].Pct() != out[j].Pct() {
			return out[i].Pct() > out[j].Pct()
		}
		return out[i].CompanyType < out[j].CompanyType
	})
	return out, nil
}
```

import 里加 `"time"`（`PayReportFor` 的参数用到）。

- [ ] **Step 4: 确认通过**

Run: `go test ./internal/metric/ -v -count=1`
Expected: PASS（A-1 的 37 + 本任务 6 ≈ 43）

- [ ] **Step 5: Commit**

```bash
git add internal/metric/pay.go internal/metric/pay_test.go
git commit -m "feat(metric): add pay percentile grid, experience ladder and transparency" -- internal/metric/pay.go internal/metric/pay_test.go
```

---

## Task 5: `/pay` 页面与路由

**Files:**
- Create: `internal/view/pay.go`、`internal/web/pay.go`、`internal/web/pay_test.go`
- Modify: `internal/web/server.go`

- [ ] **Step 1: 写失败测试**

Create `internal/web/pay_test.go`:

```go
package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestPayPageRenders(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/pay")
	if rec.Code != http.StatusOK {
		t.Fatalf("/pay = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	for _, want := range []string{"What you are worth", "Seniority", "Experience ladder", "Who discloses"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("/pay missing %q", want)
		}
	}
}

func TestPayPageRejectsUnknownLensValues(t *testing.T) {
	s := setupWeb(t)
	for _, path := range []string{"/pay?exp=0-3", "/pay?role=backend", "/pay?exp=junior"} {
		if rec := get(t, s, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}

func TestPayPagesAreCachedPerLens(t *testing.T) {
	s := setupWeb(t)
	get(t, s, "/pay")
	get(t, s, "/pay?exp=0-2&role=Backend")
	now := s.now()
	for _, key := range []string{"pay:exp=;role=", "pay:exp=0-2;role=Backend"} {
		if _, ok := s.cache.get(key, now); !ok {
			t.Errorf("cache missing entry %q", key)
		}
	}
}

func TestPayPageSuppressesInsteadOfShowingZero(t *testing.T) {
	// setupWeb seeds one posting with no salary, so every cell is thin: the
	// page must say so rather than print S$0.
	s := setupWeb(t)
	body := get(t, s, "/pay").Body.String()
	if !strings.Contains(body, "n=") {
		t.Errorf("/pay must show sample-size suppression markers, got:\n%s", body)
	}
	if strings.Contains(body, "S$0") {
		t.Error("/pay must never render a zero salary")
	}
}

func TestPayPageCarriesTheSharedNav(t *testing.T) {
	s := setupWeb(t)
	body := get(t, s, "/pay").Body.String()
	if !strings.Contains(body, `<a class="on" href="/pay">Pay</a>`) {
		t.Error("/pay must mark itself active in the shared nav")
	}
	if !strings.Contains(body, `href="/tech"`) {
		t.Error("/pay nav must link the sibling pages")
	}
}
```

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/web/ -run Pay -v`
Expected: FAIL — `/pay` 返回 404

- [ ] **Step 3: 写模板**

Create `internal/view/pay.go`:

```go
package view

import (
	"bytes"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// payPage is parsed once at init so a syntax error fails the build's tests
// instead of surfacing as a 500 (the page renders live on every hit).
var payPage = template.Must(template.New("pay").Funcs(template.FuncMap{
	"bar":   Bar,
	"pct":   Pct,
	"money": Money,
	"sup":   Suppressed,
	"nav":   Nav,
	"lens":  lensNav,
	"cell":  payCell,
	"bars":  ladderBars,
}).Parse(payTmpl))

// PayPage renders /pay.
func PayPage(r *metric.PayReport) (string, error) {
	var buf bytes.Buffer
	if err := payPage.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// payCell renders one grid cell: the median with its quartiles, or the
// suppression marker. Never a zero — an unmeasured cell must not read as a
// measured S$0.
func payCell(c metric.PayCell) template.HTML {
	if c.Coverage.Suppressed {
		return Suppressed(c.Coverage)
	}
	return template.HTML(`<strong>` + Money(c.P50) + `</strong><br><span class="sup">` +
		Money(c.P25) + `–` + Money(c.P75) + `</span>`)
}

// ladderBars charts the ladder's medians, skipping suppressed rungs so the
// chart cannot imply a measured zero.
func ladderBars(bands []metric.PayBand) []metric.KV {
	out := make([]metric.KV, 0, len(bands))
	for _, b := range bands {
		if b.Coverage.Suppressed {
			continue
		}
		out = append(out, metric.KV{Key: b.Label, Value: b.P50})
	}
	return out
}

const payTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pay · Singapore SWE jobs</title>
<style>` + BaseCSS + SuppressedCSS + `</style>
</head>
<body><div class="wrap wide">
<h1>What you are worth</h1>
<div class="sub">Advertised monthly salaries · trailing {{.Days}} days ({{.Window}}, SGT){{if .Lens.Label}} · {{.Lens.Label}}{{end}}</div>
{{nav "/pay"}}
{{lens "/pay" .Lens}}

<p class="note">Every figure below is a salary a posting actually advertised — quartiles are picked from the sample, never interpolated. Only {{pct .Salary.Pct}} of postings disclose a monthly salary ({{.Salary.Disclosed}} of {{.Salary.Total}}), so these describe that disclosing subset, not the market. Cells with fewer than 5 disclosed salaries are withheld rather than shown: a quartile over four postings is both false precision and close to publishing one employer's range.</p>

<h2>1. Median by seniority and role</h2>
<p class="note">Each cell: median on top, 25th–75th percentile below.</p>
<div class="scroll">
<table class="detail">
<tr><th>Seniority</th>{{range .Roles}}<th>{{.}}</th>{{end}}<th>All roles</th></tr>
{{range .Grid}}<tr>
  <td><strong>{{.Seniority}}</strong></td>
  {{range .Cells}}<td>{{cell .}}</td>{{end}}
  <td>{{cell .All}}</td>
</tr>{{end}}
<tr><td><strong>All levels</strong></td>{{range .RoleTotals}}<td>{{cell .}}</td>{{end}}<td>{{cell .Overall}}</td></tr>
</table>
</div>

<h2>2. Experience ladder</h2>
<p class="note">What another year of experience is worth. "0" means the posting explicitly asks for no experience; "unstated" means it did not say — for someone deciding whether to apply those are different answers, so they are never merged. This ladder is the experience dimension itself, so the experience filter above does not narrow it (the role filter does).</p>
{{if bars .Ladder}}{{bar (bars .Ladder) 5}}{{end}}
<table>
<tr><th>Years required</th><th>Postings</th><th>p25</th><th>Median</th><th>p75</th></tr>
{{range .Ladder}}<tr>
  <td>{{.Label}}</td><td>{{.Postings}}</td>
  {{if .Coverage.Suppressed}}<td colspan="3">{{sup .Coverage}}</td>
  {{else}}<td>{{money .P25}}</td><td><strong>{{money .P50}}</strong></td><td>{{money .P75}}</td>{{end}}
</tr>{{end}}
</table>

<h2>3. Who discloses pay</h2>
<p class="note">Salary transparency by employer type. A type with fewer than 5 postings in the window is withheld rather than ranked on a handful.</p>
<table>
<tr><th>Employer type</th><th>Postings</th><th>Disclose a salary</th></tr>
{{range .ByCompany}}<tr>
  <td>{{.CompanyType}}</td><td>{{.Total}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{pct .Pct}} ({{.Disclosed}}){{end}}</td>
</tr>{{end}}
</table>

<div class="foot">Numbers computed by SQL from public MyCareersFuture data; data is refreshed daily, so it lags the live market by up to 24h. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate statistics only, no personal data.</div>
</div></body></html>`
```

模板用了 `bar`（阶梯图）与 `.wide`/`.scroll`/`.detail`（宽表样式）。前者要在 FuncMap 里加 `"bar": Bar,`；后三个 CSS 类目前只在 `internal/report/daily_render.go` 的 `dailyCSS` 里，`/pay` 用不到那整块——在 `internal/view/css.go` 的 `SuppressedCSS` 末尾追加这三条（ops 页的 `dailyCSS` 里同名规则保留不动，重复无害且 A-2c 收敛 report 时一并处理）：

```
.wide{max-width:1160px}.scroll{overflow-x:auto;-webkit-overflow-scrolling:touch}
.scroll table{min-width:840px}table.detail td,table.detail th{padding:5px 8px;font-size:14px}
```

- [ ] **Step 4: 写 handler 与路由**

Create `internal/web/pay.go`:

```go
package web

import (
	"context"
	"net/http"

	"github.com/meirongdev/jobs-sg/internal/metric"
	"github.com/meirongdev/jobs-sg/internal/view"
)

func (s *Server) handlePay(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dailyTimeout)
	defer cancel()

	lens, err := parseLens(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := s.now()
	s.servePage(w, "pay:"+lens.Key(), now, func() (string, error) {
		rep, err := metric.PayReportFor(ctx, s.db, now, lens)
		if err != nil {
			return "", err
		}
		return view.PayPage(rep)
	})
}
```

`internal/web/server.go` 的 `Handler()`，在 `GET /tech` 之后加一行：

```go
	mux.HandleFunc("GET /pay", s.handlePay)
```

- [ ] **Step 5: 确认全绿**

Run: `go test ./... -count=1 && go vet ./... && gofmt -l internal/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/view/pay.go internal/view/css.go internal/web/pay.go internal/web/server.go internal/web/pay_test.go
git commit -m "feat(web): serve /pay with the percentile grid, ladder and disclosure rates" -- internal/view/pay.go internal/view/css.go internal/web/pay.go internal/web/server.go internal/web/pay_test.go
```

---

## 收尾验证

- [ ] **Step 1: 全量**

Run: `make test && make vet && make build`
Expected: 全绿

- [ ] **Step 2: 空库冒烟**

用与 A-1 收尾相同的做法（临时 in-module 程序建一个 migrate+seed 过的空库，跑完删掉），然后：

```bash
./bin/jobs-sg-web --data-dir <空库目录> --addr 127.0.0.1:18099 &
curl -sS http://127.0.0.1:18099/pay | grep -oE 'What you are worth|n=[0-9]+|S\$0' | sort -u
curl -sS -o /dev/null -w '%{http_code}\n' 'http://127.0.0.1:18099/pay?exp=0-3'   # 400
curl -sS http://127.0.0.1:18099/tech | grep -c 'href="/pay"'                      # 1（共享导航生效）
```
Expected: 出现 `What you are worth` 与 `n=` 抑制标记，**不出现 `S$0`**；非法镜头 400；`/tech` 的导航里有一条 `/pay` 链接。

- [ ] **Step 3: 越界检查**

Run: `git diff --name-only main..HEAD | sort`
Expected: 只落在本计划「文件结构」表列出的文件上。特别确认**未**触碰：`internal/report/metrics.go`（周报口径，A-2c）、`internal/report/telegram.go`、`docs/01-requirements.md`、`internal/metric/tech.go` 的动量/溢价逻辑（Task 2 只改透明率字段与 SQL 片段常量）。

---

## 交接给 A-2b / A-2c

- **A-2b（`/` + `/companies`）**：需要 `buildFixtureDB` 灌 `closed_at`（见本计划开头的 A-1 更正），寿命指标按 spec §3.5 只统计已下架岗位并标注右删失；竞争度按 §3.6 归一化为日均投递。`/` 会把首页语义从静态周报换成现算快报——那时 `view.navItems` 的第一项要从 `Weekly report` 改成 `Market`，并给周报另找入口（`/w/{week}` 已存在），这正是 Task 1 收敛导航要付的红利。
- **仍在 A-1 待办上、本计划未动的**：`lensNav` 的四处 `metric.Lens{...}` 从头构造字面量改 copy-and-override（触发条件是"加第三个镜头维度"，`/pay` 仍只用 exp+role，未触发）；`EntryFriendly` 是否需要自己的 `Coverage`（`/tech` 的模型问题）；`redirectDailyDate` 的未来日期绕路。三项保持记账。
- **A-2c（对外口径）**：A-1 待办 6-9（`weekly_metric` 物化 `tech_share`/`swe_enriched`、report 的窗口助手收敛到 `metric.Window`、`pct`/`money`/`topn` 换成 `view` 版、`salaryMedian`/`salaryByRole` 改走 `metric` 的薪资口径）全部随周报重排一起做。本计划已提前结清其中的第 1、10 项，并把 `disclosedSalaryPredicate`/`salaryMidpoint` 抽成常量、`Transparency` 收成一份，第 9 项的收敛面因此变小。
