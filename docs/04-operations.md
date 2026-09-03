# 部署与运维

> 前置阅读：[02-design](02-design.md)。本文覆盖 GitOps 落地、homelab 踩坑清单、可观测性与告警、备份恢复、集群安全。
> 数据合规红线见 [01-requirements](01-requirements.md) §5。

---

## 1. GitOps 落地

### 1.1 仓库划分

| 内容 | 仓库 | 理由 |
|---|---|---|
| 应用代码 + Dockerfile + 测试 | `meirongdev/jobs-sg`（本仓库） | 独立演进，独立 CI |
| K8s manifests | **`meirongdev/homelab`** | ⚠️ `argocd/projects/homelab.yaml` 的 `sourceRepos` 是**白名单**，只允许 `github.com/meirongdev/homelab`。若把 manifests 放本仓库，必须①往 `sourceRepos` 加条目，且②**手动 `kubectl apply` AppProject**——它不在 root App 托管路径下，`git push` 不生效。为省掉这个长期维护陷阱，manifests 放 homelab 仓库 |

### 1.2 文件清单（homelab 仓库）

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

**用独立 Application 而不并入 `personal-services`**：
① `personal-services` 的 `ResourceQuota` 限死 `count/jobs.batch: "15"`，三个 CronJob 的历史 Job 会挤占该配额（那是为 92-pod 泄漏事故加的护栏，不该稀释）；
② 需要 kustomize 目录 + `ignoreDifferences`，`directory.include` 模式不支持。

### 1.3 ArgoCD Application 骨架

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

### 1.4 镜像构建（本仓库 `.github/workflows/image.yml`）

仿 `homelab/.github/workflows/excalidraw-room-image.yml`：

- 推 `ghcr.io/meirongdev/jobs-sg`，tag `sha-<short>` + `<semver>`
- **`platforms: linux/amd64,linux/arm64`**——homelab 只需 amd64，多构 arm64 是为了留住 oracle 迁移出口（[02](02-design.md) §1），成本几分钟 CI
- 首次构建后把 GHCR 包设为 **Public**（否则需要 imagePullSecret）
- manifests **按 digest 固定**镜像（Kyverno `disallow-latest-tag` 是 Enforce，见 §2）
- **不启用 ArgoCD Image Updater**：集群当前 0 个 ImageUpdater CR；为一个应用引入需补 CR + git write-back 凭据，收益不抵复杂度。手动更新 digest

---

## 2. homelab 特有踩坑清单（实现前必读）

> 以下每条都是 2026-08-02 勘查中**实际确认**的，不是通用建议。证据见 [archive/2026-08-02-site-survey](archive/2026-08-02-site-survey.md)。

1. **⚠️ HTTPRoute 的 `parentRefs.port` 必须是 `80`，不是 8000。**
   `docs/CONVENTIONS.md` 与 `.claude/skills/add-service/SKILL.md` 都写着 homelab 用**端口 8000**，但实测 `homelab-gateway` 的 listener 只有 `{"name":"http","port":80}`，且线上 6 条 HTTPRoute 全部用 port 80 [已验证]。照文档写 8000 会得到一条永远不挂载的路由。
   → 实现时用 80；**并顺手修掉这两处文档**（已列入 [05-roadmap](05-roadmap.md) Phase 1）。

2. **`ReferenceGrant` 必须是 `v1beta1`**。写成 `v1` 会让整个 Application 报 `ComparisonError`。

3. **Kyverno `disallow-latest-tag` 是 Enforce**，不是 Audit——勘查中一个用 `:latest` 的临时 Pod 被准入 webhook **实际拒绝** [已验证]。镜像必须 digest 或明确 tag。

4. **CronJob 必须设 `concurrencyPolicy: Forbid` + `successfulJobsHistoryLimit` + `failedJobsHistoryLimit`**。仓库先例：一个没设这些的 CronJob 泄漏了 92 个卡住的 Job Pod，把单节点推过 110-pod 上限，**阻塞了全集群调度**。

5. **`ServiceMonitor` / `PrometheusRule` 必须带 label `release: kube-prometheus-stack`**，否则 operator 的 selector 静默忽略——指标和告警不生效且**没有任何报错**。

6. **PVC 必须 `storageClassName: local-path`**。`nfs-client` provisioner 已于 2026-07-11 卸载，引用它的 PVC 会**永久 Pending**。

7. **Gateway 的 `Programmed=False`（`AddressNotAssigned`）是正常的** [已验证]——NodePort GatewayClass 挂在 Cloudflare Tunnel 后面没有 LB 地址。路由照常工作，不要去"修"它。

8. **DNS 不需要任何手工步骤**。写 HTTPRoute 就是建 DNS（external-dns + 通配隧道路由）。**不要**改 `cloudflare/terraform`。注意 `policy: upsert-only`——删 HTTPRoute 不会删 DNS 记录。

9. ~~**Bifrost 集群内调用同样需要 virtual key**~~ —— **已作废**：Bifrost 网关 2026-09 退役，enrich 直连 DGX 上的 vLLM，端点不要凭证。换模型/换端点的做法见 [09](09-deploy-runbook.md) §3.2。

10. **homepage 的 ConfigMap 用 `subPath` 挂载，不热加载**。改完必须 `kubectl --context oracle-k3s rollout restart deployment/homepage -n homepage`。

11. **Uptime Kuma monitor 是声明式且会 prune**——不在 `MONITORS` 列表里的会被删除。

12. **`AppProject` 不受 GitOps 管理**。manifests 放 homelab 仓库正是为了绕开这点（§1.1）。

---

## 3. 可观测性与告警

### 3.1 指标（`web` Pod 的 `/metrics`，从 `ingest_run` 与 `job` 表现算）

> **端口**：`/metrics` 绑在 `web` 容器的 **9090**（`--metrics-addr`），与公共站点的 8080 分开；Service 相应开 `http`(80→8080) 与 `metrics`(9090→9090) 两个端口，ServiceMonitor 抓 **`port: metrics`**。HTTPRoute 只指向 `http`，所以 `/metrics` 集群外不可达（理由见 [02](02-design.md) §4.4）。**改 ServiceMonitor 的 `port` 时务必同步 Service 的端口名**——名字对不上 operator 同样是静默不抓。

全部带 `# HELP` / `# TYPE`（各家族恰一次，头在样本之前）：

```
# gauge
jobs_sg_last_success_timestamp_seconds{kind="incremental|full_reconcile|enrich|report"}
jobs_sg_run_duration_seconds{kind=...}
jobs_sg_jobs{state="active|closed"}
jobs_sg_jobs_new                             # 最近一个已物化 ISO 周的新增 SWE 岗位数
jobs_sg_enrich_backlog                       # is_swe=1 且无 job_tech(source='llm') 的数量
jobs_sg_unmapped_tech                        # 未复核的未映射技术词数
# counter
jobs_sg_llm_calls_total / jobs_sg_llm_cache_hits_total / jobs_sg_llm_errors_total
jobs_sg_ingest_errors_total
```

状态在 DB 不在进程内 → web 重启不丢指标。ServiceMonitor **必须带 `release: kube-prometheus-stack` 标签**（§2 第 5 条）。

三条约定：

1. **无值即不输出，绝不补 0**。首次 report 跑之前没有 `jobs_sg_jobs_new`，某 kind 没跑过就没有它那条 `last_success`。补 0 会让「还没有数据」和「真的是 0」不可区分。
2. **任何 DB 错误 → 整个抓取 500**，Prometheus 据此把 target 标为 down（`up == 0`，本身就是信号）。曾经 job 计数用 `_ =` 吞掉错误，于是查询失败时输出 `jobs_sg_jobs_total{state="active"} 0`——和「市场一夜清空」无从分辨，建在它上面的告警会照着假数据响。行数不足（`sql.ErrNoRows`）不算错误，走第 1 条。
3. **标签值必须是闭集**。现存标签只有 `kind` 与 `state`。`jobs_sg_jobs_new` 曾带 `week=` 标签，每周新增一条 series 且永不退休——这是把 Prometheus 撑爆的标准做法；周次由 Prometheus 自己的时间轴回答，不该进标签。

> **命名约定**：`_total` 后缀只给 counter。`jobs_sg_jobs`、`jobs_sg_unmapped_tech`、`jobs_sg_jobs_new`、`jobs_sg_enrich_backlog` 都是 gauge（会双向变动），故均无后缀。三者的改名都在部署之前完成，此后再改就要破坏已有查询了。

### 3.2 告警规则（PrometheusRule，同样需 `release` 标签）

| 告警 | 条件 | severity | 说明 |
|---|---|---|---|
| `JobsSgIngestStale` | `min without(kind) (time() - jobs_sg_last_success_timestamp_seconds{kind=~"incremental\|full_reconcile"}) > 36h` | warning | **最重要的告警**——静默失效比崩溃更危险 |
| `JobsSgReconcileStale` | 同上，`full_reconcile > 10d` | warning | 在架量指标失真 |

> **为什么 enrich 积压看的是「地板抬升」而非绝对值**：首跑基线一次性把全部在架候选岗位（≈11k）灌进来，而 LLM 每晚只能排掉约 420 条、每天又新进约 300 条——绝对阈值会在上线第一晚就响，并持续响一到两个月直到烧完。而积压在稳态下是锯齿形（02:15 ingest 加、03:10 enrich 减），**每日低谷**才是信号所在：拿今天的低谷比昨天的低谷，问的正是「管线还跟不跟得上」。DGX 停机、虚拟密钥过期、enrich 作业没跑，都会把地板顶高一天的进量；而一个正在稳步排空的大积压会把地板压低，保持安静。

> **为什么 `JobsSgIngestStale` 要同时匹配两个 kind**：周日那轮 ingest 把自己记成 `full_reconcile`（`cmd/ingest` 按 SGT 星期几判定），`incremental` 系列因此每周固定断档 48h > 36h 阈值。只匹配 `incremental` 会让这条告警每周误报约 12 小时（周日 06:45 UTC 起至当日 18:15 UTC 下一轮增量），而它恰恰是本系统最不该被噪音淹没的一条。`min without(kind)` 取两者中最近的一次成功，语义即"任何形式的采集都超过 36h 没成功过"。
| `JobsSgIngestErrors` | `increase(jobs_sg_ingest_errors_total[1d]) > 20` | warning | API 语义变更的早期信号 |
| `JobsSgEnrichBacklogGrowing` | `min_over_time(jobs_sg_enrich_backlog[1d]) - min_over_time(…[1d] offset 1d) > 500` | warning | LLM 不可用或跟不上 |
| `JobsSgCronJobFailed` | `kube_job_status_failed{namespace="jobs-sg"} > 0` | warning | 兜底（kube-state-metrics 现成） |

均走现有 Alertmanager → Telegram（`severity=warning|critical` 路由已在线）。

### 3.3 日志与追踪

- 结构化 JSON 日志到 stdout → 现有 OTel Collector → Loki，零额外配置。
- **Phase 1 不接 Tempo**：批作业的分布式追踪收益低于配置成本。日后需要时注入 `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.monitoring.svc:4317` 即可。

### 3.4 Grafana 面板

`k8s/helm/manifests/jobs-sg-dashboard.yaml`：ConfigMap 带 `grafana_dashboard: "1"` label + `grafana_folder: Platform` annotation，文件名加入 `argocd/applications/monitoring-dashboards.yaml` 的 `directory.include`。

---

## 4. 备份与恢复

`local-path` **无冗余、无快照**，备份是强制项。

现有 restic 夜备脚本（`backup/overlays/homelab/backup-script.yaml`）已在扫 `*.db` / `*.db-wal` / `*.db-shm`，接入只需一行：

```sh
# 第 53 行
for pat in bifrost-data calibre-web-automated-config jobs-sg-data; do
```

- 覆盖：`jobs.db`（回滚日志下无 WAL/SHM 文件；restore 时 SQLite 自恢复）。
- **归档目录 `raw/` 必须纳入 restic 独立路径**（类似 calibre 书库的整目录做法）：历史快照**不可重建**——API 只返回当前在架职位，下架的永远拿不回来。这是本系统最不可替代的数据资产。全类目归档后年增 ~1.5GB（[03](03-data-model.md) §3），restic 去重后成本可控（每日新增文件，一次存储）。
- PVC 加 `argocd.argoproj.io/sync-options: Prune=false`。
- **恢复演练是 Phase 2 DoD**：实际 restore + `PRAGMA integrity_check` 通过。

---

## 5. 集群安全

| 项 | 做法 |
|---|---|
| Namespace PSA | `restricted`（而非其他 ns 的 `baseline`）——全新 Go 应用可轻松满足，白拿一档 |
| Pod 安全上下文 | `runAsNonRoot: true`、`runAsUser: 10001`、`allowPrivilegeEscalation: false`、`capabilities.drop: [ALL]`、`seccompProfile: RuntimeDefault`、`readOnlyRootFilesystem: true`（`/tmp` 用 emptyDir） |
| 镜像 | `ghcr.io/meirongdev/jobs-sg`，**按 digest 固定**（Kyverno Enforce） |
| 密钥 | Telegram bot token 走 Vault → ESO；**不进 git**。LLM 端点当前无凭证 |
| 网络 | 集群当前无 CiliumNetworkPolicy（有意延后），本项目不单独引入，保持一致 |

**Vault 路径**：`secret/homelab/jobs-sg`，键 `telegram-bot-token`、`telegram-chat-id`、`telegram-thread-id`。
`bifrost-vk` 自 Bifrost 网关 2026-09 退役后不再被消费——enrich 直连 DGX vLLM，端点不要凭证（见 [09](09-deploy-runbook.md) §3.1）。

web 服务**不做认证**——公开就业市场统计，无个人数据（[02](02-design.md) §4.4）；若日后需要，再加 oauth2-proxy。

---

## 6. 2026-08-03 故障记录：codex apply_patch incompatible payload

**症状**：codex（0.146.0）调用 `apply_patch` 工具一律失败，日志报
`Fatal error: tool apply_patch invoked with incompatible payload`（`codex_core::tools::router`）。

**根因**：2026-07-31 22:49 把 `~/.codex/dgx-models.json` 重做（配合 DGX 模型升级
DeepSeek-V4-Flash-0731）时新增了 `apply_patch_tool_type: "freeform"`。freeform 模式下
客户端期望 apply_patch 参数为**裸补丁文本**，而 0731 模型经 vLLM 实际返回
JSON 包裹（`{"patch":...}` / `{"input":...}`）→ 客户端判 incompatible。

**证据**：7-24（旧快照，无 freeform 字段）60 次调用 0 错误；7-31 白天 21 次调用
0 错误；8-01 起（0731 + freeform）100% 失败（105+ 次）。实测客户端 `apply_patch_tool_type`
只接受 `freeform` 一个值（改 `json` 报 `unknown variant`），无法用改值兼容。

**修复**：备份后删除 `~/.codex/dgx-models.json` 中 `models[0].apply_patch_tool_type`
字段 → 客户端回退到 7-31 上午验证过的默认（command 数组）形式。
备份：`~/.codex/dgx-models.json.bak-20260803-071940`（恢复：`cp` 回去即可）。
验证：`codex -c model_catalog_json=~/.codex/dgx-models.json -c model=deepseek-v4-flash debug models` 解析正常，字段为 None。

**注意**：修改需重启 codex 会话生效。若日后换回官方 freeform 行为，需先确认
vLLM 侧对 freeform 工具的响应序列化（`--tool-call-parser deepseek_v4`）是否已对齐。
