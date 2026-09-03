# jobs-sg

新加坡 SWE 职位监控与周报系统 —— MyCareersFuture 公开 JSON API → SQLite → 本地 LLM 技术栈富化 → 每周趋势报告（`jobs.meirong.dev` + Telegram）。运行于 k3s-homelab，常驻内存目标 ≤64Mi，外部成本 $0。

## 状态

- **MVP（Phase 1）代码已实现**：`ingest`（增量/首跑基线/周全量对账 + closed_at 生命周期）、`enrich`（规则层 + LLM 接口化 fail-open + 缓存）、`report`（weekly_metric 物化 + 自包含 HTML/MD）、`web`（**两个页面**：静态周报 + 现算的每日采集统计，外加 `/metrics` + `/healthz`）。
- 测试：SQLite 生命周期、采集回放、分类口径、规则层/LLM 富化、指标口径、日统计 SGT 分桶、web 路由；含 ~100 条字段结构对齐的 [fixture](testdata/fixture/jobs.jsonl) 回放测试。
- 容器化：多阶段 [Dockerfile](Dockerfile)（scratch、非 root、多架构 CI）；k8s manifests 在 [deploy/](deploy/)（**按 docs/04 需同步到 `meirongdev/homelab` 仓库经 ArgoCD 部署**，勿指向本仓库）。
- **待部署**：构建镜像定 digest → 同步 manifests 到 homelab → Vault 密钥 → ArgoCD sync → 连续 3 天增量 success（Phase 1 DoD）。进度见 [docs/05-roadmap.md](docs/05-roadmap.md)。

## 快速开始

```sh
make build            # bin/jobs-sg-{ingest,enrich,report,web}
make test             # go test ./...
make vet
```

本地冒烟（需要网络访问 MCF API 时才可完整运行）：

```sh
./bin/jobs-sg-ingest --data-dir ./data --delay-ms 1500      # 首跑=全量基线
./bin/jobs-sg-enrich --data-dir ./data                       # 无 LLM_BASE_URL 时纯规则层
LLM_BASE_URL=http://<endpoint>:8000 LLM_MODELS=qwen38-flash-next \
  ./bin/jobs-sg-enrich --data-dir ./data                     # 接 LLM 层；全部参数见 docs/09 §3.2
./bin/jobs-sg-report --data-dir ./data --week 2026-08-03 --base-url http://localhost:8080
./bin/jobs-sg-web --data-dir ./data --addr :8080             # 打开 http://localhost:8080
```

web 提供两个页面：

| 路由 | 页面 | 产出 |
|---|---|---|
| `/`、`/w/{YYYY-Www}` | 周报（招聘/技术趋势、薪资、需求信号） | `report` CronJob 预生成的静态 HTML |
| `/daily`、`/daily/{YYYY-MM-DD}` | 每日采集明细统计（运行计数、入库/下架量、当日技术栈、单日职位列表） | 请求时按 SGT 日从 DB 现算 |

## 架构

```
MCF 公开 JSON API → ingest(归档 raw/*.jsonl.gz + 候选入 SQLite) → enrich(规则+LLM 技术栈)
→ report(周度指标 + 自包含 HTML/MD) → web(只读托管 /metrics) + Telegram
```

- 文档：[docs/](docs/README.md)（需求 / 设计 / 数据模型 / 运维 / 路线图）
- 合规：UA 透明、只存公开法人/职位数据、不落个人字段、周报只发聚合统计（docs/01 §5）
- 实现说明：分类口径、schema、指标口径分别见 [docs/03-data-model.md](docs/03-data-model.md)、[docs/08-bdd.md](docs/08-bdd.md)

## 目录

```
cmd/{ingest,enrich,report,web}   四命令入口
internal/{mcf,store,classify,tech,llm,report,web}  共享库
testdata/fixture/                ~100 条字段结构对齐 fixture + 回放测试
deploy/                          kustomize manifests（同步 homelab 仓库的参照）
scripts/genfixture/              fixture 生成器（确定性、可复现）
```
