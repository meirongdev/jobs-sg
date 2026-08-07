# 部署 Runbook（首次上线）

> 一次性把 Phase 1 DoD 做绿的操作清单。原理与取舍见 [02-design](02-design.md)、[04-operations](04-operations.md)；本文只讲**照着做什么**、**哪里会踩坑**、**头两天什么算正常**。
> 本仓库不部署自己：manifests 必须同步到 `meirongdev/homelab` 由 ArgoCD 拉取（[04](04-operations.md) §1.1，ArgoCD 的 `sourceRepos` 是白名单）。

---

## 0. 上线前本地闸门

```sh
make test && make vet && make build     # 全绿
make kind-e2e                           # 本地 kind 集群跑一遍 manifests
make kind-down                          # 用完拆掉
```

`kind-e2e` 会建集群 → 构镜像 → 应用 overlay → 建库 → 等 rollout → 冒烟。它验证的是**在真集群里才会暴露的东西**：Service 端口名与 ServiceMonitor 是否对得上、探针路径是否可达、只读挂载下 SQLite 能否打开、`/metrics` 是否只在 9090 上而不在公网端口上。

> `test/integration/kind/reference/` 是 `deploy/` 的镜像副本。**改了 `deploy/` 就要同步 `reference/`**，否则闸门验的是旧清单。

---

## 1. 镜像

推 `main` 会触发 `.github/workflows/image.yml`，构 amd64 + arm64 推到 `ghcr.io/meirongdev/jobs-sg`。

1. **把 GHCR 包设为 Public**（一次性）：GitHub → Packages → `jobs-sg` → Package settings → Change visibility → Public。
   不设的话集群拉不到镜像，且需要额外配 `imagePullSecret`。
2. **取 digest**（Kyverno `disallow-latest-tag` 在 homelab 是 **Enforce**，非 digest 会被准入直接拒绝）：

```sh
docker buildx imagetools inspect ghcr.io/meirongdev/jobs-sg:sha-<short-sha>
```

3. manifests 里四处 `image:` 全部改成 `ghcr.io/meirongdev/jobs-sg@sha256:<digest>`（`web.yaml` 与三个 `cronjob-*.yaml`）。

---

## 2. 同步 manifests 到 homelab

复制到 `meirongdev/homelab` 的 `k8s/helm/manifests/jobs-sg/`：

```
namespace.yaml  limits.yaml  pvc.yaml  rbac.yaml
cronjob-ingest.yaml  cronjob-enrich.yaml  cronjob-report.yaml
web.yaml  monitoring.yaml  kustomization.yaml
```

另需在 homelab 侧新增/修改：

| 文件 | 改动 |
|---|---|
| `argocd/applications/jobs-sg.yaml` | 新建 Application（骨架见 [04](04-operations.md) §1.3，含 `batch/Job` 的 `ignoreDifferences`） |
| `k8s/helm/manifests/jobs-sg/external-secret.yaml` | 新建，Vault → `jobs-sg-secrets`（本仓库无此文件，见下节） |
| `backup/overlays/homelab/backup-script.yaml` | 第 53 行扫描列表加 `jobs-sg-data`；**并把 `raw/` 归档目录加为独立路径** |

**踩坑清单（[04](04-operations.md) §2 的部署相关子集，逐条都是实测过的）**：

1. HTTPRoute 的 `parentRefs.port` 必须是 **80**，不是 8000——homelab 文档写错了，照写会得到一条永不挂载的路由。
2. `ReferenceGrant` 必须是 **v1beta1**，写成 v1 整个 Application 报 `ComparisonError`。
3. PVC 的 `storageClassName` 必须是 **local-path**（`nfs-client` 已于 2026-07-11 卸载，引用它会永久 Pending）。
4. `ServiceMonitor` / `PrometheusRule` 必须带 label **`release: kube-prometheus-stack`**，否则 operator 静默忽略、**没有任何报错**。
5. **`ServiceMonitor` 抓的是 `port: metrics`，不是 `http`**。`/metrics` 现在绑在容器 9090（`--metrics-addr`），Service 有 `http`(80→8080) 与 `metrics`(9090→9090) 两个端口，HTTPRoute 只指向 `http`。**端口名对不上同样是静默不抓**，与第 4 条是同一种失败模式。
6. Gateway 的 `Programmed=False`（`AddressNotAssigned`）**是正常的**，不要去"修"。
7. DNS 不需要任何手工步骤，写 HTTPRoute 就是建 DNS。注意 `policy: upsert-only`——删 HTTPRoute 不会删 DNS 记录。

---

## 3. Vault 密钥

`jobs-sg-secrets` 需要四个键：

| 键 | 用途 | 来源 |
|---|---|---|
| `telegram-bot-token` | 周报推送 | 复用 `secret/homelab/telegram` |
| `telegram-chat-id` | 同上 | 同上 |
| `telegram-thread-id` | **内容话题**，必须与告警话题（`2`）不同 | 见 [02](02-design.md) §4.3 |
| `llm-virtual-key` | Bifrost `x-bf-vk` | Bifrost UI 里创建，**持久化在 PVC 的 SQLite、不在 git**，再手工写进 Vault |

> `telegram-thread-id` 必须是**整数字符串**，非数字会被代码拒绝并报错（宁可失败也不会静默发进 General 话题）。

---

## 4. 上线与验证

```sh
# ArgoCD 同步后
kubectl -n jobs-sg get pods,cronjobs,pvc
kubectl -n jobs-sg logs -l app=jobs-sg-web --tail=50

# 首次 ingest 可以手动触发，不必等 02:15 SGT
kubectl -n jobs-sg create job --from=cronjob/ingest ingest-manual-1
kubectl -n jobs-sg logs -f job/ingest-manual-1
```

验证点：

```sh
curl -sS https://jobs.meirong.dev/healthz                      # ok
curl -sS -o /dev/null -w '%{http_code}\n' https://jobs.meirong.dev/tech    # 200
curl -sS -o /dev/null -w '%{http_code}\n' https://jobs.meirong.dev/metrics # 404 ← 必须是 404
kubectl -n jobs-sg port-forward svc/jobs-sg-web 9090:9090 &                # /metrics 只在集群内
curl -sS localhost:9090/metrics | head
```

Prometheus 侧确认 target 已被发现（`up{namespace="jobs-sg"} == 1`）。若为空，回去查第 4、5 条踩坑。

---

## 5. 头两天什么算正常

首次运行与稳态**长得很不一样**，不知道的话会误判成故障：

| 现象 | 正常吗 | 说明 |
|---|---|---|
| 首次 ingest 跑 **20–25 分钟**、翻约 867 页 | ✅ | 首跑基线是全量扫描（[02](02-design.md) §4.1），之后每日增量只需 2–4 分钟 |
| 首次归档写出 **~120MB gz** | ✅ | 一次性在架快照；之后日增约 4MB |
| `jobs_sg_enrich_backlog` 一上来就是 **上万** | ✅ | 基线把全部在架候选岗位一次灌入 |
| 该积压要 **1–2 个月**才排空 | ✅ | 告警看的是地板抬升而非绝对值，所以**不会**因此报警 |
| `/` 显示「No weekly report yet」 | ✅ | 周报由周一 09:00 SGT 的 CronJob 产出，在那之前首页是说明页 |
| `/tech` 动量区块显示「需 5 周历史」 | ✅ | 抑制是一等状态（spec §5），不是空图也不是 0 |
| `/pay` 大量格子显示 `—(n=3)` | ✅ | 样本不足即抑制，避免伪精度 |
| 周日那轮 `kind=full_reconcile` 而非 `incremental` | ✅ | 告警已同时匹配两个 kind |
| 首个完整周报里角色分布偏 Backend | ⚠️ | 见 [05](05-roadmap.md) Backlog 第 1 条：taxonomy 未核定时未映射的 `251*` 码回落 Backend。**发布首个周报前先做完 Phase 0 核定** |

**加速排空积压**（可选）：给 enrich 临时加环境变量 `LLM_THINKING=false`——实测 18.8x 提速（60.5s → 3.2s/条），代价是抽取略松，但所有词都会过 `tech_taxonomy` 白名单，杂词只会落进 `unmapped_tech`。排空后去掉。

---

## 6. 出问题怎么退

- **Web 有问题**：`kubectl -n jobs-sg scale deploy/jobs-sg-web --replicas=0`。数据不受影响，web 是只读的。
- **采集有问题**：`kubectl -n jobs-sg patch cronjob/ingest -p '{"spec":{"suspend":true}}'`。已归档数据不动。
- **镜像回滚**：manifests 里 digest 改回上一版，ArgoCD 同步。
- **数据回滚**：`jobs.db` 可从 `raw/*.jsonl.gz` 全量重建（archive-before-parse，见 `internal/store/schema.go` 头注释）。**归档本身不可重建**——这是 restic 备份的最高优先级对象。

---

## 7. 完成判据（Phase 1 DoD）

- [ ] `ingest_run` 中有连续 3 条 `kind='incremental'` 且 `status='success'`
- [ ] `https://jobs.meirong.dev/` 可打开，`/tech`、`/pay` 返回 200
- [ ] `https://jobs.meirong.dev/metrics` 返回 **404**（只在集群内 9090 可达）
- [ ] Prometheus 中 `up{namespace="jobs-sg"} == 1`
- [ ] 第一份周报生成并推送到 Telegram **内容话题**（不是告警话题）
