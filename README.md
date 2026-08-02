# jobs-sg

新加坡 SWE 职位监控与周报系统 —— MyCareersFuture 公开 JSON API → SQLite → 本地 LLM 技术栈富化 → 每周趋势报告（`jobs.meirong.dev` + Telegram）。运行于 k3s-homelab，常驻内存目标 ≤64Mi，外部成本 $0。

- **文档**：[docs/](docs/README.md)（需求 / 设计 / 数据模型 / 运维 / 路线图）
- **状态**：设计完成（v2.1），实现未开始；下一步见 [docs/05-roadmap.md](docs/05-roadmap.md) Phase 0
- **代码**：应用代码与 CI 在本仓库；k8s manifests 落 `meirongdev/homelab` 仓库（原因见 [docs/04-operations.md](docs/04-operations.md) §1）
