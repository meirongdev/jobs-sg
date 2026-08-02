# 新加坡 SWE 职位监控与趋势分析系统 — 架构方案

> 面向：招聘趋势 + 技术趋势监控  
> 时间背景：2026 年 8 月  
> 目标：爬取新加坡 Software Engineer 相关职位，进行分类统计分析，输出 Weekly 报告

---

## 1. 数据源优先级与合规机制

### 主要数据源

| 优先级 | 来源 | 特点 | 推荐访问方式 | 风险等级 |
|--------|------|------|--------------|----------|
| 高 | **MyCareersFuture.gov.sg** | 官方平台，SWE 岗位量大，结构相对规范 | 结构化页面 / 现有 Apify Actor / 自建 Playwright 爬虫 | 低-中 |
| 中 | JobStreet / Indeed SG | 体量较大 | 爬虫 + 代理 | 中 |
| 中-低 | LinkedIn | 高质量，但反爬严格 | 优先商业数据 API（Bright Data、TheirStack、Coresignal 等） | 高 |
| 补充 | Wellfound、公司 Career Page、YC | 创业公司信号 | 按需抓取 | 低-中 |

### 核心原则（2026 Best Practice）

- 优先官方/公开数据，尊重 `robots.txt` 与站点 ToS。
- LinkedIn **无公开 Jobs Search API**，官方仅提供 Partner-only 的 Job Posting API，大规模自建爬虫风险高、成本高。
- 自建爬虫时必须做好：
  - Rate limiting（随机 1–3 秒以上延迟）
  - User-Agent 轮换
  - Residential Proxy 轮换（必要时）
  - Playwright 处理 JS 渲染页面
- 法律与伦理：只抓取公开 listing，不做登录后深度抓取；数据仅用于分析与趋势洞察。

**推荐地基**：以 MyCareersFuture 作为主数据源，其他来源做交叉验证与补充。

---

## 2. 整体架构

```
[Ingestion Layer]
  ├── MyCareersFuture Scraper (Playwright / Scrapy + Playwright)
  ├── JobStreet / Indeed Scrapers
  └── Optional: Commercial Job API（LinkedIn 补全）

[Storage]
  ├── Raw: Object Storage (S3 / MinIO) + 原始 HTML/JSON
  ├── Structured: PostgreSQL（岗位元数据、公司、薪资、时间戳）
  └── Enrichment: Vector DB (pgvector / Chroma) 存 description embedding

[Processing / ETL]
  ├── Airflow / Prefect / Dagster 调度（每日增量 + 每周全量校验）
  ├── Deduplication（job_id + 公司 + 标题 + 模糊哈希）
  ├── LLM / NLP 分类与技能提取
  └── dbt 或 SQL 转换层

[Analytics & Reporting]
  ├── 聚合表（周/月 volume、tech frequency、seniority distribution）
  ├── Dashboard（Metabase / Superset / Streamlit）
  └── 自动化 Weekly Report（Markdown → PDF / Slack / Email）
```

### 技术选型理由

- **爬虫**：Playwright（现代、多浏览器、异步友好）+ Scrapy 编排。比纯 Selenium 更轻、更稳定。
- **调度**：Prefect 或 Dagster（比 Airflow 更 Python-native，可观测性更好）。
- **分类**：LLM-first（Claude / GPT-4o / Grok 等）做 skill extraction + seniority / domain 分类，辅以规则 + spaCy。2026 年 skills-first 招聘信号强烈，LLM 对非结构化 JD 的提取效果明显优于纯关键词。
- **存储**：PostgreSQL + pgvector 足够起步；数据量增大后再考虑 Iceberg / Parquet + DuckDB。
- **去重机制**：优先使用官方 `job_id` / `jobPostId`；缺失时采用 `(company_normalized + title_normalized + location + 发布日期窗口)` + MinHash / embedding 相似度。

---

## 3. 分类与统计分析维度

### 核心分类字段（建议标准化）

- **角色类型**：SWE / Full-stack / Backend / Frontend / Mobile / Platform / SRE / Data Eng / AI-ML Eng / Security 等
- **资历**：Intern / Junior / Mid / Senior / Staff+ / Lead / Manager
- **技术栈**：语言、框架、云（AWS / GCP / Azure）、AI 相关（LLM、RAG、LangChain、PyTorch 等）、数据库、工具链
- **公司类型**：MNC / Local Tech / Bank & FinTech / Startup / Government / Consulting
- **工作模式**：Onsite / Hybrid / Remote
- **其他**：薪资区间、签证/经验要求、是否为新创建岗位

### Weekly 统计与趋势指标

- 新发布量 vs 累计活跃量（按角色、资历）
- 技术出现频率排名 + 周环比 / 同比
- AI/ML 相关岗位占比变化（2026 核心驱动因素）
- 公司分布（Top hirers）
- 薪资中位数趋势（数据可得时）
- Skills-first 信号：无学历要求、无经验要求的岗位比例

**机制建议**：每周跑一次全量聚合 + 增量更新，保留历史快照，支持时间序列分析。

---

## 4. Weekly 报告内容建议

1. **Executive Snapshot**  
   本周新岗位数、环比变化、最热角色。

2. **Hiring Trends**  
   总量趋势图、资历分布、公司类型分布、热招公司 Top 10。

3. **Tech Trends**  
   最常出现的语言 / 框架 / 云 / AI 工具排名 + 上升 / 下降最快的技术。

4. **Insights**  
   结合外部信号（LinkedIn Jobs on the Rise、Robert Half 薪资指南、政府相关数据）进行解读。例如：AI Engineer 持续高需求、skills-first 继续强化、云与平台工程需求稳定等。

5. **Actionable**  
   对候选人 / 招聘方的简要建议。

**输出形式**：Markdown 源文件 → PDF / HTML Dashboard + Slack / Email 推送。可用 LLM 生成自然语言解读，再对关键数字进行人工或规则校验。

---

## 5. 实施路线图

### Phase 1（2–4 周）— MVP
- 仅做 MyCareersFuture 每日增量爬取
- 基础结构化存储 + 简单关键词 / 规则分类
- 手动或半自动生成 Weekly 报告
- 重点验证数据质量与合规性

### Phase 2（4–8 周）— 生产化
- 增加 JobStreet / Indeed
- 完善去重与 LLM 技能提取
- 自动化调度 + 基础 Dashboard
- 引入 Proxy 与监控（失败率、封锁检测）

### Phase 3（持续演进）
- 商业数据补全 LinkedIn
- 时间序列预测、异常检测（突然爆发的技术 / 公司）
- 多维度切片（按行业、薪资、经验要求）
- 可选：向量检索相似岗位、候选人匹配实验

---

## 6. 风险与缓解

| 风险 | 缓解措施 |
|------|----------|
| 反爬 / 封锁 | 住宅代理 + 智能限速 + 失败重试 + 监控告警；优先稳定源 |
| 数据漂移（JD 格式变化） | LLM 分类比硬编码规则更抗变化 |
| 成本 | 自建爬虫 + LLM 调用可控；大规模 LinkedIn 数据建议走商业 API |
| 法律合规 | 持续关注站点 ToS；仅公共数据；不用于商业转售 |
| 准确性 | 多源交叉验证 + 人工抽检关键指标 |

---

## 7. 2026 趋势背景（参考公开数据）

- 新加坡软件开发者仍是最热门岗位类别之一。
- 大量新岗位由 AI 驱动产生。
- Skills-first 趋势明显（大量岗位不再把学历作为主要门槛）。
- AI Engineer、Machine Learning、Cloud、Platform Engineering、Cybersecurity 相关需求突出。

系统应重点跟踪以上信号。

---

## 总结推荐起点

以 **MyCareersFuture 为主源 + Playwright 爬虫 + PostgreSQL + LLM 分类 + Prefect / Dagster 调度** 搭建 MVP。

该架构具备以下特点：
- 足够模块化，便于后续扩展
- 合规优先
- 符合 2026 年技术实践（LLM 增强分类 + 现代浏览器自动化）
- 可观测性与可维护性较好

---

*文档版本：2026-08-02*  
*适用场景：新加坡 SWE 职位招聘趋势与技术趋势监控系统*