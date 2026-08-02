# 需求与指标定义

> 本文回答"要什么"；"怎么做"见 [02-design](02-design.md)，字段级计算口径见 [03-data-model](03-data-model.md)。
> 源自 v1 方案的存活部分（分类维度、报告结构、合规原则）+ v2 §5.3。

## 1. 目标

跟踪新加坡 Software Engineer 招聘市场的**招聘趋势**与**技术趋势**，每周一产出一份可信的周报，供个人求职与市场判断使用。

- 用户就是维护者本人；无 SLA，但**数字必须可信**：口径可解释、可回溯、跨周可比。
- 成本约束：外部支出 $0（公开 API + 本地 LLM + 现有 homelab 平台）。

## 2. 周报内容（章节定义）

| 章节 | 回答的问题 | 核心指标 |
|---|---|---|
| Executive Snapshot | 本周市场热度如何 | 新增 SWE 岗位数、环比、在架总量、最热角色 |
| Hiring Trends | 谁在招、招什么级别 | 角色分布、资历分布、公司类型分布、Top 10 招聘公司 |
| Tech Trends | 什么技术在升温/降温 | 技术频次 Top 30、环比升降最快 Top 10 |
| Compensation | 给多少钱 | 按角色/资历的薪资中位数与四分位（仅公开薪资） |
| Demand Signals | 竞争多激烈 | 平均浏览量/投递数、投递竞争度 —— MCF 独有字段，商业职位 API 通常不给，**本系统差异化指标** |
| Skills-first | 门槛在降低吗 | 无学历要求、零经验要求岗位占比 |
| Insights | 怎么解读 | LLM 生成的自然语言解读（只基于已算好的数字，见 §4） |
| Data Quality | 这期数据可信吗 | ingest/enrich 成功率、LLM 缓存命中率、未映射技术词数 |

## 3. 分类维度（标准化目标）

- **角色**：Backend / Frontend / Fullstack / Mobile / Platform / SRE / Data Eng / AI-ML / Security / Other-IT
- **资历**：Intern / Junior / Mid / Senior / Staff+ / Lead / Manager
- **技术栈**：语言、框架、云（AWS/GCP/Azure）、数据库、工具链、AI 相关（LLM/RAG/PyTorch 等）
- **公司类型**：MNC / Local Tech / Bank & FinTech / Startup / Government / Consulting
- **工作模式**：Onsite / Hybrid / Remote

判定规则与数据来源见 [03-data-model](03-data-model.md) §4–§5。

## 4. 指标硬性要求

- 周为最小统计粒度（ISO 周，按 SGT）；历史快照保留，支持时间序列分析。
- **所有数字由 SQL 计算，LLM 不得参与任何数字的产生**（防幻觉污染指标）。
- 去重后计数：同一职位的刷新、重贴、跨源重复不得虚增新增量。
- 口径变更必须全量重算历史，并在周报中标注口径变更点。

## 5. 合规红线

- 只访问公开数据（MCF `robots.txt` 全站放行 **[已验证]**，见 [勘查记录](archive/2026-08-02-site-survey.md)）；不登录、不抓取候选人数据、不做个人画像。
- `User-Agent` 透明声明身份与项目地址（如 `jobs-sg-monitor/1.0 (+https://jobs.meirong.dev)`），不伪装浏览器；限速使负载远低于普通用户浏览。
- 数据仅用于个人趋势分析；**不转售、不再分发原始数据集**；周报只发布聚合统计。
- 发布者个人字段（`createdBy` / `emailRecipient` 等）一律不落库；`postedCompany` 为法人信息，非个人数据。
- LinkedIn / Indeed 爬取**默认不做**（ToS 风险高、成本高、增量低，判定见 [06-multi-source](06-multi-source.md) §1）。

## 6. 非目标

- 不做实时监控（日批足够）；不做候选人匹配、简历分析；不做 SG 以外地区。
- 不追求全源覆盖率 —— MCF 主源的**口径一致性**优先于跨源"大而全"。

## 7. 背景信号（2026-08，定性参考）

软件开发仍是新加坡最热岗位类别之一；大量新增岗位由 AI 驱动；skills-first（弱化学历门槛）趋势明确；AI/ML、Cloud、Platform Engineering、Cybersecurity 需求突出。本系统的价值在于对这些定性信号做**本地化、可持续的量化验证**。
