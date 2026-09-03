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

> `test/integration/kind/reference/` 是 `deploy/` 的镜像副本。**改了 `deploy/` 就要同步 `reference/`**，否则闸门验的是旧清单。（当前已同步。）

**最近一次实跑**：2026-08-07，全绿。关键断言：`/healthz` → `ok`、metrics 端口 9090 → `HTTP 200`、**公网端口 `/metrics` → `HTTP 404`**（即端口拆分在真集群里确实生效），`deployment "jobs-sg-web" successfully rolled out`（只读挂载下 SQLite 打得开、探针通得过）。

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

## 3. 密钥与 LLM 配置

### 3.1 Vault 密钥

`jobs-sg-secrets` 需要三个键：

| 键 | 用途 | 来源 |
|---|---|---|
| `telegram-bot-token` | 周报推送 | 复用 `secret/homelab/telegram` |
| `telegram-chat-id` | 同上 | 同上 |
| `telegram-thread-id` | **内容话题**，必须与告警话题（`2`）不同 | 见 [02](02-design.md) §4.3 |

> **键名要与 manifests 逐字一致**（`deploy/cronjob-*.yaml` 的 `secretKeyRef.key`）。密钥取不到不会报错：`enrich` 是 fail-open 的，会退回纯规则层继续跑完、退出码 0——**技术栈富化静默失效，而作业看起来是成功的**。上线后务必按 §4 验一次 LLM 层真的在工作。
>
> `telegram-thread-id` 必须是**整数字符串**，非数字会被代码拒绝并报错（宁可失败也不会静默发进 General 话题）。
>
> `bifrost-vk` 已不再需要：Bifrost 网关 2026-09 退役，enrich 直连 DGX 的 vLLM，端点不要凭证。代码仍认 `BIFROST_VK`（发 `x-bf-vk` 头）并打一条弃用告警，只为让残留清单丢吞吐而不是丢鉴权。

### 3.2 LLM 参数（换模型只改这里）

模型相关的东西全部是 `cronjob-enrich.yaml` 里的环境变量，**换模型不需要改代码、不需要发版**：

| 变量 | 默认 | 作用 |
|---|---|---|
| `LLM_BASE_URL` | 空 | OpenAI 兼容端点。**留空就是纯规则层**，enrich 照常跑完退出 0 |
| `LLM_MODELS` | `qwen38-flash-next` | 降级链，逗号分隔，第一个优先。裸 vLLM 认裸 id，网关认 `provider/id` 前缀形式，两者互相 404 |
| `LLM_TIMEOUT` | `300`（秒） | 单次调用预算。端点非流式，这个预算覆盖整个生成过程 |
| `LLM_CONCURRENCY` | `3` | enrich 扇出上限。天花板是共享 GPU 的余量，不是本地 CPU |
| `LLM_THINKING` | 不设 | `false` 关推理、`true` 强制开。**不设时请求体里不出现 `chat_template_kwargs`**——有的后端会拒绝不认识的模板参数 |
| `LLM_THINKING_KWARG` | `enable_thinking` | 模板里那个开关的**键名**。见下方警告 |
| `LLM_EXTRA_BODY` | 空 | 合并进请求体的 JSON 对象（`top_p`、`max_tokens`、`reasoning_effort`……）。这是新模型要什么怪参数都能塞的兜底口子；`model` / `messages` 不可覆盖，写了会被丢弃并告警 |
| `LLM_MAX_DESC_CHARS` | `4000` | 送进模型的描述截断长度，给上下文窗口更小的模型留的 |
| `LLM_API_KEY` | 空 | 端点要鉴权时用。空则完全不发鉴权头 |
| `LLM_AUTH_HEADER` | `Authorization` | 头名。`Authorization` 会自动补 `Bearer ` 前缀（值里已带 scheme 时不补），其他头名原样发送 |
| `LLM_RETRIES` | `1` | 单条岗位失败后的重试次数。超时的重试几乎必然再超时一次，见下方说明 |
| `LLM_MAX_TOKENS` | `0`（不封顶） | 模型输出上限。**设之前先读下面那段**——封低了会把慢成功变成硬失败 |
| `LLM_PROMPT` | 内置抽取提示词 | 覆盖系统提示词。替代品仍必须要求返回那个 JSON 对象，否则解析不出来 |
| `LLM_PROMPT_VERSION` | `v1` | 缓存键的一部分。**改了 `LLM_PROMPT` 但没设它时会自动派生 `custom-<hash>`**，防止拿旧提示词的缓存结果糊弄新提示词 |

> **⚠️ 换模型必查：思考开关的键名会变，而且写错不报错。**
> 2026-09-02 DGX 把 `deepseek-v4-flash` 换成 `qwen38-flash-next` 之后，旧的 `{"thinking": false}` 依然返回 200、依然被接受、**依然完全不生效**。唯一判据是响应里的 `usage.completion_tokens_details.reasoning_tokens`：
>
> ```sh
> curl -sS http://<endpoint>/v1/chat/completions -H 'Content-Type: application/json' \
>   -d '{"model":"qwen38-flash-next","messages":[{"role":"user","content":"hi"}],
>        "chat_template_kwargs":{"enable_thinking":false}}' |
>   python3 -c 'import sys,json; print(json.load(sys.stdin)["usage"])'
> ```
>
> 2026-09-03 在生产端点实测同一条岗位：不带参数 792 个 reasoning token，`{"thinking":false}` 1108 个，`{"enable_thinking":false}` 0 个。
> 代码现在会自己发现这件事：设了 `LLM_THINKING=false` 但模型仍然吐 reasoning token 时，enrich 每进程打一条 `reasoning was not disabled` 的 WARN，点名让你改 `LLM_THINKING_KWARG`。

**换模型的完整清单**：

1. 改 `LLM_MODELS`（必要时连 `LLM_BASE_URL` 一起改）。
2. 本机对着新端点跑一次验收——**这条用例默认跳过，不进 CI**：

   ```sh
   LLM_LIVE_URL=http://<endpoint>:8000 LLM_LIVE_MODEL=<新模型 id> \
     go test ./internal/llm -run Live -v
   ```

   它会实际调一次模型并断言推理确实被关掉，同时打印本次耗时。挂了就换 `LLM_LIVE_KWARG=<别的键名>` 重试，**试通的那个值就是要写进清单的 `LLM_THINKING_KWARG`**。
3. 跑一次 `enrich`，看首行 `llm enabled` 日志里的 `models` / `thinking` / `timeout` / `extra_body` 是不是预期值，以及有没有 `llm config` 的告警（参数写错会被丢弃，只在这里现形）。
4. 按 §5 重测一次耗时并更新那张表。注意小样本测不出尾巴——真正要看的是跑满一轮之后日志里有多少条 `context deadline exceeded`，比例上到百分之几就该按 §5 的警告封顶生成，而不是调大 `LLM_TIMEOUT`。

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

**确认 LLM 层真的接上了**（fail-open 会把密钥错配伪装成正常）：

```sh
kubectl -n jobs-sg create job --from=cronjob/enrich enrich-manual-1
kubectl -n jobs-sg logs -f job/enrich-manual-1     # 看 llm_calls 是否 > 0
# 或从指标看：llm_calls 一直为 0 而 backlog 不降 = 没接上
curl -sS localhost:9090/metrics | grep -E 'jobs_sg_llm_calls_total|jobs_sg_enrich_backlog'
```

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

**加速排空积压**（可选）：给 enrich 临时加环境变量 `LLM_THINKING=false`，排空后去掉。代价是抽取略松，但所有词都会过 `tech_taxonomy` 白名单，杂词只会落进 `unmapped_tech`。

2026-09-03 在 `qwen38-flash-next` 上用 16 条真实岗位、并发 8 实测（旧模型的数字已作废）：

| | 单次均值 | 单次最坏 | 吞吐（并发 8） |
|---|---|---|---|
| 开推理（当前默认） | 18.7s | 58.7s | 15 条/分钟 |
| `LLM_THINKING=false` | 2.8s | 6.1s | 154 条/分钟 |

> **⚠️ 300s 不是充足余量。** 上面的最坏值来自 16 条样本，尾巴比这重得多：生产日志里约 **1.3% 的调用会超过 300s**
> （2026-09-02 那轮 3/204、09-03 那轮 3/242，两轮是不同岗位，新旧模型都一样）。这不是端点慢，是推理模型在个别岗位上跑飞。
>
> **别急着用 `LLM_MAX_TOKENS` 封顶。** 2026-09-03 用 24 条真实岗位实测，开推理时正常抽取的 completion token 分布是
> p50 475 / p90 2631 / **max 4359**；而并发 8 下要烧满 300s 大约得跑到 7000 token 以上。封顶值必须落在这条窄带里才有用，
> 封低了模型会先把推理写满、`content` 直接是 null，一次慢成功就变成硬失败（代码会明确报"截断"而不是"bad JSON"）。
>
> **更划算的是 `LLM_RETRIES=0`。** 超时是 fail-open 的，这些岗位留在积压里，第二天那轮本来就会重试；而当场重试同一条岗位、
> 同一个模型，几乎必然再烧一个完整的 `LLM_TIMEOUT`。关掉当场重试等于把跑飞的代价直接砍半，且没有任何截断风险。
> 想彻底消掉跑飞就 `LLM_THINKING=false`，代价是抽取略松。

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
