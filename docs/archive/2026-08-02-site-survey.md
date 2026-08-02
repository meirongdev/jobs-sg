# 现场勘查记录（2026-08-02）

> **时点快照** — 本文记录设计定稿当日的实测勘查结果与完整命令证据，是各文档中 **[已验证]** 标记的唯一出处。
> 数字（在架总量、节点内存、日增量等）会随时间失效；设计结论已吸收进 [../02-design.md](../02-design.md)。
> 复核时请另存新日期文件，勿原地改写本文。

---

## 1. 数据源实测（MyCareersFuture）

| 探测项 | 结果 |
|---|---|
| `robots.txt` | `User-agent: *` / `Disallow:`（空 = 全站允许）；含 `sitemap-index.xml` |
| `POST /v2/jobs`（旧写法） | **401 Unauthorized** — 网上流传的 POST search 已关闭 |
| `GET /v2/jobs?limit=100&page=0&sortBy=new_posting_date` | **200**，返回 `{results[], total, _links, countWithoutFilters}` |
| `GET /v2/jobs/{uuid}` | **200**，单条完整对象 |
| 列表项与详情项字段 | **完全一致**（列表已含 `description`）→ 无需二次详情抓取 |
| `limit` 上限 | **100**（`limit=200` → 400） |
| `categories=21` | **400** — 类目过滤参数格式不通，改为客户端过滤 |
| `search=software engineer` | 200，`total=902` — 全文检索可用 |
| 全站在架职位 `total` | **86,678**（2026-08-02） |
| 单条 JSON 体积 | **≈ 7.0 KB** |
| 日增量（按分页反查） | page0=08-02、page15=08-01、page30/60=07-31 → **工作日 ≥3,000/天** |
| IT 类目占比（page 0 抽样） | **13%** → 估算 IT 日增 ~400、在架 ~11,000 |
| sitemap | 6 个分片，`sitemap-1.xml` = 45,000 URL / 9.58MB，**不支持 gzip**，含 `<lastmod>` |
| sitemap URL 结构 | `/job/<category-slug>/<title-slug>-<uuid>` → 免解析即得类目与主键 |

**单条职位可用字段**（实测样本，非推测）：

```
uuid, metadata.jobPostId(MCF-2026-1162108), title, description(HTML)
ssocCode(5位), occupationId, ssocVersion          → 官方职业分类
positionLevels[].position, minimumYearsExperience → 资历
salary.{minimum,maximum,type}, metadata.isHideSalary → 薪资
employmentTypes[], categories[], schemes[], flexibleWorkArrangements[]
skills[].{skill,isKeySkill}                       → 官方技能标签
postedCompany.{uen,name,ssicCode,employeeCount}   → 公司唯一键 + 行业码
address.{postalCode,districts,lat,lng,isOverseas}
metadata.{newPostingDate,originalPostingDate,expiryDate,
          repostCount,totalNumberOfView,totalNumberJobApplication}
status.jobStatus, numberOfVacancies, screeningQuestions[]
```

> `totalNumberOfView` / `totalNumberJobApplication` 是**需求侧信号**——商业职位 API 通常不给，值得作为本系统的差异化指标。
> MCF 的 `skills[]` 是业务技能（实测样本："Liaising with cross functional teams"），**不是技术栈**——这是 enrich 组件存在的直接依据。

## 2. 集群容量实测

| | `k3s-homelab` | `oracle-k3s` |
|---|---|---|
| 架构 | **amd64** | **arm64**（Ampere A1） |
| 位置 | 家宽住宅 IP（Proxmox 笔记本） | OCI **ap-osaka-1**（日本，机房 IP） |
| CPU | 8C（allocatable 7600m） | 4C |
| 内存 | 13.27GB（allocatable 11918Mi） | 24.55GB（allocatable 22453Mi） |
| 内存 requests / 实测 | 6118Mi (52%) / **9095Mi (78%)** | 4073Mi (18%) / 8417Mi (38%) |
| CPU requests / 实测 | 2722m (35%) / 1805m (23%) | 2005m (50%) / 302m (7%) |
| Pod | 47 / 110 | — |
| 磁盘 | 123.7GB，**余 42.7GB** | 203GB |
| StorageClass | 仅 `local-path` | 仅 `local-path` |
| PostgreSQL | 无 operator | **CNPG 就绪**（`zitadel-pg` 运行中） |
| Prometheus Operator | ✅ 原生（ServiceMonitor 可用） | ❌ 走 otel-collector |
| Vault / ESO | ✅ 本体在此 | ESO 有，`vault-backend` 跨 Tailscale 可用 |
| Bifrost LLM 网关 | ✅ 本体在此 | ❌ |

节点为过热笔记本（idle ~74°C）——这是给批作业设 cpu limit 的依据（[../02-design.md](../02-design.md) §5.2）。

## 3. Bifrost LLM 网关现状

集群内 `bifrost.bifrost.svc.cluster.local:8080`，两个 **keyless** 自定义 OpenAI provider：

| provider | base_url | 说明 |
|---|---|---|
| `custom_dgx` | `http://100.97.87.120:8000` | DGX Spark，vLLM |
| `custom_m2` | `http://100.89.15.120:8000` | MacBook 本地推理 |

- 两者均 `chat_completion: true` / `chat_completion_stream: true`
- 两者均 **`embedding: false`** → 集群内当前没有 embedding 端点，**这是不引入向量库的直接依据**
- 网关开了 `enforce_auth_on_inference` + governance routing rule，**集群内调用同样需要 virtual key**（governance PreHook 在 ingress 之前，不区分来源）

## 4. ATS 公开接口实证（多源化依据）

- `GET https://boards-api.greenhouse.io/v1/boards/stripe/jobs` → 免鉴权 `{"jobs":[...]}`
  字段：`id, title, location.name, first_published, updated_at, absolute_url, data_compliance[].type, company_name, internal_job_id`
- `GET https://api.lever.co/v0/postings/leverdemo?mode=json` → 免鉴权数组
  字段：`id, text, categories{department,location,team}, workplaceType, descriptionPlain, hostedUrl`，部分含 `salaryRange{min,max,currency,interval}`

## 5. 完整命令证据

```bash
# ── 数据源 ────────────────────────────────────────────────────────────
curl -s https://www.mycareersfuture.gov.sg/robots.txt
#   → User-agent: * / Disallow:  (全站允许) + sitemap-index.xml

curl -s -X POST 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=2&page=0' \
     -H 'Content-Type: application/json' -d '{"search":"software engineer"}'
#   → 401 Unauthorized

curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=100&page=0&sortBy=new_posting_date'
#   → 200, {"results":[...100 条完整对象...],"total":86678,...}
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=200&page=0'      # → 400 (limit 上限 100)
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=3&categories=21' # → 400 (类目参数不通)
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs?limit=3&search=software%20engineer'  # → 200, total=902
curl -s 'https://api.mycareersfuture.gov.sg/v2/jobs/0002d6e300a67475b8b4be8b94db6189'    # → 200 单条

# 日增量反查：page 0 → 2026-08-02 ×100；page 15 → 08-01；page 30/60 → 07-31
# IT 类目占比：page 0 的 100 条中 13 条 categories[0].category == 'Information Technology'
# 单条 JSON ≈ 7,024 bytes

curl -s https://www.mycareersfuture.gov.sg/sitemap-1.xml    # 9,579,317 bytes, 45,000 <loc>, 3,185 IT
#   Accept-Encoding: gzip 无效（返回 application/octet-stream 全量）

# ── ATS 公开接口（多源化实证）─────────────────────────────────────────
curl -s https://boards-api.greenhouse.io/v1/boards/stripe/jobs   # 免鉴权 {"jobs":[...]}
curl -s https://api.lever.co/v0/postings/leverdemo?mode=json     # 免鉴权 [...]

# ── 集群 ──────────────────────────────────────────────────────────────
kubectl --context k3s-homelab describe node k8s-node   # 8C/13.27GB; req 2722m/6118Mi; 47 pods
kubectl --context k3s-homelab top node                 # 1805m (23%) / 9095Mi (78%)
kubectl --context k3s-homelab get sc                   # local-path (default) 唯一
kubectl --context k3s-homelab get --raw \
  "/api/v1/nodes/k8s-node/proxy/stats/summary"         # 123.7GB 容量 / 42.7GB 可用

kubectl --context oracle-k3s describe node oracle-k3s  # 4C/24.55GB; req 2005m/4073Mi
kubectl --context oracle-k3s get cluster.postgresql.cnpg.io -A   # zitadel-pg healthy

# ── Gateway 端口（homelab 文档与实际不一致的证据）──────────────────────
kubectl --context k3s-homelab get gateway homelab-gateway -n kube-system -o jsonpath='{.spec.listeners}'
#   → [{"name":"http","port":80,"protocol":"HTTP",...}]     ← 只有 80
kubectl --context k3s-homelab get httproute -A \
  -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,PORT:.spec.parentRefs[*].port'
#   → 6 条路由全部 PORT=80
kubectl --context k3s-homelab get gateway homelab-gateway -n kube-system -o jsonpath='{.status.conditions}'
#   → Programmed=False / AddressNotAssigned（NodePort + Tunnel 下属正常）

# ── Kyverno Enforce（实测拒绝）─────────────────────────────────────────
kubectl --context k3s-homelab run curltest --image=curlimages/curl:latest ...
#   → admission webhook "validate.kyverno.svc-ignore" denied the request:
#     disallow-latest-tag: 禁止使用 :latest tag

# ── Bifrost ───────────────────────────────────────────────────────────
curl -s http://bifrost.bifrost.svc.cluster.local:8080/api/providers   # 集群内
#   → custom_dgx (http://100.97.87.120:8000), custom_m2 (http://100.89.15.120:8000)
#     两者 chat_completion=true, embedding=FALSE
```
