# 扩展设计：多源化、跨源身份与选择策略

> **状态：设计存档，Phase 3 素材。** Phase 1 只落 schema 预留（`job.source` / `job.canonical_fp` / `job_source_xref` / `job_repost`，成本低、避免后补重灌），其余一律不实现。
> 源自 v2 §15（2026-08-02 评审后新增）；v2.1 补充 §3 指纹重审警告。

---

## 1. 来源分级（实测判定，2026-08-02）

| 来源 | 拿法 | 鉴权/反爬 | 合规 | 薪资填充 | 优先级 |
|---|---|---|---|---|---|
| **MCF** | 公开 JSON API（已在用） | 无 / 无 | 干净 | 高（结构化） | ✅ MVP |
| **公司官网 → ATS**（Greenhouse/Lever/Ashby/Workday） | **公开 JSON API**（GH/Lever 已实测免鉴权） | 无 / 低 | 自家发布接口 | 中→低 | 🥇 多源第一期 |
| **Indeed** | 无干净公开 API；Publisher API 需合作 | 反爬重 | 中 | 低 | 末位/仅校验 |
| **LinkedIn** | 公开 API 需企业合作；刮取违反 ToS | Cloudflare+登录墙+住宅代理 | **高风险** | 中 | **跳过/最后** |

**实证**（完整命令见 [archive/2026-08-02-site-survey](archive/2026-08-02-site-survey.md) §4）：

- `GET https://boards-api.greenhouse.io/v1/boards/{company}/jobs` → 免鉴权 `{jobs:[{id,title,location,first_published,updated_at,absolute_url,data_compliance}]}`
- `GET https://api.lever.co/v0/postings/{company}?mode=json` → 免鉴权数组 `[{id,text,categories{...},workplaceType,descriptionPlain,hostedUrl,salaryRange?}]`

**判断**：多源化该**接 ATS 公开接口**，而非爬 LinkedIn/Indeed——后者成本高、收益低、ToS/GDPR 风险大，其职位 MCF+ATS 基本都有。

## 2. Source 策略接口（策略模式落地）

```
Source 接口
  name():  str
  fetch(window) -> SourceJob[]        # 各源自己的抓取（日期窗/分页/增量）
  normalize(j) -> UnifiedJob          # 映射到统一 schema + canonical_fp
  classify(j) -> kept: bool           # 选择谓词（可选，缺省恒真）
注册表: { "mcf":..., "greenhouse":..., "lever":..., "ashby":... }
Driver: 遍历启用源 → fetch → normalize → ingest(upsert + 去重)
```

- 每源一个 `ingest_run.kind`（`ingest_mcf` / `ingest_greenhouse`…）与各自的 freshness/error 指标、独立告警。
- **添加新源 = 加一个 `sources/<name>.go` 文件**（实现 `Source` 接口 + 注册），不动 ingest 主流程——这是策略模式的核心收益。

## 3. canonical 跨源身份与去重

`uuid` 仅 MCF 有，跨源无通用主键 → 用**逻辑指纹**做 canonical 身份。v2 原设计：

```
canonical_fp = sha256(normalize(title) + "|" + uen_or_domain + "|" + normalize(description))
```

> **⚠️ v2.1 评审结论 — 多源动工前必须重设计**
> 上式把正文纳入哈希，而同一职位在 MCF 与 ATS 上的正文几乎必然存在措辞/boilerplate 差异（MCF 的 EA licence 行、模板尾注、HTML 结构差异等）→ **跨源命中率趋近于 0**。该指纹实际只对"同源复制成新 uuid"的重贴有效，对跨源归并近似空转。
> 重设计方向：blocking key = `normalize(title) + 公司标识（UEN↔域名映射表）`产生**候选对** → 候选对再做描述相似度打分或人工确认（`job_repost.basis='human_review'` 已预留）。
> 保守方向不变：**宁可少并、勿误并**——漏并只是多算一行，误并会污染两个源的生命周期。

- 归并结果：同一职位跨源 → 同一 canonical job；来源证据落 `job_source_xref(source, source_id, canon_uuid)`。
- 指标按 **canonical** 去重（不然出现在 3 个源就翻 3 倍，口径见 [03](03-data-model.md) §6）。
- 交叉验证信号：某职位仅出现在 ATS 而未进 MCF → 本身是需求侧信号。

**刷新去重（HR 只刷新不动内容）**：MCF 刷新通常在同一 uuid 上顶 `newPostingDate` + `repostCount++` → 主键 upsert 已是 UPDATE 非新行。叠加规则见 [03](03-data-model.md) §6 信号表：指标按 `original_posting_date` 归属；hash 不变 → enrich_cache 命中跳过 LLM；复制成新 uuid 且 fp 命中 → `job_repost` 归组不计新。

**已知边界**：改了关键字的复制到新 uuid 抓不住（需模糊匹配，本设计不引入向量库）；靠 `repost_count` + "极端相似 JD 周报"暴露给人工判断。

## 4. 选择策略（"≥10k 薪资"等可插拔谓词）

- **不放 ingest 硬过滤**：服务器端过滤不了薪资；抓取层过滤省不下流量（近窗整段都要拉），却带来不可逆 + 漏掉「隐藏高薪岗」（`isHideSalary=1` 常是高薪岗）。
- **放分析/指标层**：全部 SWE 候选落库（schema 已存 `salary_min/max/type`），阈值作为 `selection_strategy` 谓词在 `weekly_metric` 物化时按 SQL 应用：

```sql
WHERE (salary_type='Monthly' AND salary_max >= 10000)
   OR (salary_type='Annual'  AND salary_max >= 120000)
   OR salary_hidden=1                     -- 保留、单独计数、不直接排除
```

- 阈值可调、历史可重算；用**组合规则**而非单一薪资谓词，否则系统性漏掉隐藏高薪岗。

## 5. 跨源薪资归一化

- 薪资口径只对**有结构化薪资的源**成立（主要是 MCF；ATS 部分有 `salaryRange.interval/currency`）。
- `source ∈ {greenhouse, lever}` 且无 `salaryRange` → 标 `salary_hidden=1`（视为未知，不参与 10k 选择策略）。
- 跨源汇总前把 `salaryRange.interval`（monthly/annual）+ `currency` 归一成 MCF 一致的 `salary_type`；非 SGD 单独统计不混算。

## 6. 落地顺序

1. **Phase 1（当前）**：仅 MCF；schema 预留已完成即止。
2. **Phase 3 第一期**：重审 §3 指纹 → 接 Greenhouse / Lever / Ashby 三个 ATS 源。
3. **最后才评估** Indeed / LinkedIn，且只做交叉验证/补漏，不依赖做核心口径。
