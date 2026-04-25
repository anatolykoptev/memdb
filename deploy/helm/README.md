# MemDB Helm Chart

Single-namespace Helm chart that brings up the full MemDB stack:
**postgres + redis + qdrant + embed-server + memdb-go + memdb-mcp**

No external subchart dependencies. All manifests are owned by this chart.

## Prerequisites

- Kubernetes 1.26+
- Helm 3.12+
- A default StorageClass (or set `*.persistence.storageClass` explicitly)

## Quick Install

### 1. Create the required secret

Secrets are never stored in `values.yaml`. Create them before install:

```bash
kubectl create namespace memdb

kubectl create secret generic memdb-secrets \
  --namespace memdb \
  --from-literal=postgresPassword=<STRONG_PASSWORD> \
  --from-literal=llmApiKey=<YOUR_LLM_API_KEY> \
  --from-literal=masterKeyHash="" \
  --from-literal=internalServiceSecret=""
```

### 2. Install the chart

```bash
helm install memdb ./deploy/helm \
  --namespace memdb \
  --create-namespace
```

### 3. Verify

```bash
kubectl -n memdb get pods
kubectl -n memdb port-forward svc/memdb-memdb-go 8080:8080
curl http://localhost:8080/health
```

## Upgrade

```bash
helm upgrade memdb ./deploy/helm --namespace memdb
```

## Uninstall

```bash
helm uninstall memdb --namespace memdb
# PVCs are not deleted automatically — remove manually if desired:
kubectl -n memdb delete pvc --all
```

## Values Reference

| Key | Default | Description |
|-----|---------|-------------|
| `existingSecret` | `memdb-secrets` | Name of the K8s Secret with sensitive values |
| `nameOverride` | `""` | Override chart name |
| `fullnameOverride` | `""` | Override full name prefix |
| **memdb-go** | | |
| `memdbGo.image.repository` | `ghcr.io/anatolykoptev/memdb-go` | Image repository |
| `memdbGo.image.tag` | `latest` | Image tag — **pin in production** |
| `memdbGo.replicaCount` | `1` | Number of replicas |
| `memdbGo.port` | `8080` | Container port |
| `memdbGo.resources.requests.cpu` | `100m` | CPU request |
| `memdbGo.resources.requests.memory` | `512Mi` | Memory request |
| `memdbGo.resources.limits.cpu` | `1000m` | CPU limit |
| `memdbGo.resources.limits.memory` | `2Gi` | Memory limit |
| `memdbGo.env` | see values.yaml | Extra env vars (non-secret) |
| **memdb-mcp** | | |
| `memdbMcp.image.tag` | `latest` | Image tag — **pin in production** |
| `memdbMcp.port` | `8001` | MCP HTTP port |
| **embed-server** | | |
| `embedServer.enabled` | `true` | Deploy embed-server sidecar |
| `embedServer.image.tag` | `latest` | Image tag |
| `embedServer.persistence.size` | `5Gi` | Model cache PVC size |
| **postgres** | | |
| `postgres.image.tag` | `pg17` | pgvector image tag |
| `postgres.database` | `memdb` | Database name |
| `postgres.user` | `memdb` | Database user (password via secret) |
| `postgres.persistence.enabled` | `true` | Enable PVC for data |
| `postgres.persistence.size` | `10Gi` | PVC size |
| **redis** | | |
| `redis.image.tag` | `7-alpine` | Redis image tag |
| `redis.persistence.enabled` | `true` | Enable PVC for AOF data |
| `redis.persistence.size` | `2Gi` | PVC size |
| **qdrant** | | |
| `qdrant.image.tag` | `v1.15.3` | Qdrant image tag |
| `qdrant.persistence.enabled` | `true` | Enable PVC for `/qdrant/storage` |
| `qdrant.persistence.size` | `10Gi` | PVC size |
| **ingress** | | |
| `ingress.enabled` | `false` | Create Ingress resource |
| `ingress.className` | `""` | IngressClass name (e.g. `nginx`) |
| `ingress.hosts` | see values.yaml | Ingress host/path rules |
| `ingress.tls` | `[]` | TLS configuration |

## Disabling Persistence

For ephemeral dev clusters (no StorageClass):

```bash
helm install memdb ./deploy/helm \
  --namespace memdb \
  --set postgres.persistence.enabled=false \
  --set redis.persistence.enabled=false \
  --set qdrant.persistence.enabled=false \
  --set embedServer.persistence.enabled=false
```

## Ingress Example (nginx)

```bash
helm upgrade memdb ./deploy/helm \
  --namespace memdb \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set "ingress.hosts[0].host=memdb.example.com" \
  --set "ingress.hosts[0].paths[0].path=/" \
  --set "ingress.hosts[0].paths[0].pathType=Prefix" \
  --set "ingress.hosts[0].paths[0].service=memdb-go"
```

## Disabling embed-server

If you use a hosted embedding provider (Voyage, Ollama, etc.):

```bash
helm install memdb ./deploy/helm \
  --namespace memdb \
  --set embedServer.enabled=false \
  --set memdbGo.env.MEMDB_EMBEDDER_TYPE=voyage
```

## Production Notes

- **Always pin image tags** — `latest` is for dev only.
- **Scale resources** — default limits are sized for a 4-core dev box.
- **Backup PVCs** — use Velero or your cloud provider's snapshot mechanism.
- **Secret rotation** — update the K8s Secret and do a rolling restart (`kubectl rollout restart deployment -n memdb`).
