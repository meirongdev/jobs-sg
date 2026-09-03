# jobs-sg deploy manifests (reference)

These kustomize manifests implement docs/04. **They are intentionally synced to
the `meirongdev/homelab` repo** (ArgoCD AppProject `sourceRepos` whitelist only
allows `github.com/meirongdev/homelab`, docs/04 §1.1) — do not point ArgoCD at
this repo.

## Steps to deploy

1. Build the image and pin the digest (Kyverno rejects non-digest tags):

   ```sh
   docker buildx imagetools inspect ghcr.io/meirongdev/jobs-sg:sha-<sha>
   # set the digest in kustomization.yaml (images.newTag -> newDigest)
   ```

2. In `meirongdev/homelab`, copy this `deploy/` directory to
   `k8s/helm/manifests/jobs-sg/`, then create
   `argocd/applications/jobs-sg.yaml` from the skeleton in docs/04 §1.3
   (with `ignoreDifferences` for batch/Job).

3. Create the Vault secret `secret/homelab/jobs-sg` with keys:
   `telegram-bot-token`, `telegram-chat-id`, `telegram-thread-id`
   and an ExternalSecret + `jobs-sg-secrets` (docs/04 §5).

4. `kubectl apply -n argocd -f argocd/applications/jobs-sg.yaml` and let
   ArgoCD sync.

## Known homelab gotchas (docs/04 §2)

- HTTPRoute `parentRefs.port: 80` (not 8000) — also fix the two docs that
  still say 8000 (`docs/CONVENTIONS.md`, `.claude/skills/add-service/SKILL.md`).
- `ReferenceGrant` is `gateway.networking.k8s.io/v1beta1`.
- ServiceMonitor/PrometheusRule need `release: kube-prometheus-stack`.
- RWO PVC depends on single-node; add nodeAffinity if the cluster grows.
