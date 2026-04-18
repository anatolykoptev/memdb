# Embedding Speedup: e5-small + ONNX Graph Optimization

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Reduce embedding latency from ~47s to ~3-5s per request on ARM Neoverse-N1 (Oracle Cloud).

**Architecture:** Add multilingual-e5-small (384-dim) as a second general-purpose model alongside e5-large. Use e5-small for real-time search/add and keep e5-large for batch re-embedding if needed. The DB schema stays at 1024-dim — e5-small vectors are zero-padded to 1024. This avoids migration while giving immediate speed gains. Later we can migrate to native 384-dim if quality is acceptable.

**Tech Stack:** Python (onnxruntime, optimum), Go (yalue/onnxruntime_go), Docker

**Key constraints:**
- DB columns are `halfvec(1024)` / `vector(1024)` — cannot change without full re-embed
- Zero-padding 384→1024 preserves cosine similarity between same-dim vectors
- Mixed e5-large + e5-small vectors in the same index will have degraded cross-similarity
- Registry already supports multi-model (`embedder.Registry`)

---

## Phase 1: Download and Benchmark e5-small (no code changes)

### Task 1: Download e5-small quantized ONNX model

**Files:**
- Create: `/home/krolik/deploy/krolik-server/models/multilingual-e5-small/` (directory)

**Step 1: Download model from HuggingFace**

```bash
# From Xenova (same source as current e5-large quantized model)
mkdir -p /home/krolik/deploy/krolik-server/models/multilingual-e5-small
cd /home/krolik/deploy/krolik-server/models/multilingual-e5-small

# Download quantized ONNX model + tokenizer
pip install huggingface_hub
python3 -c "
from huggingface_hub import hf_hub_download
repo = 'Xenova/multilingual-e5-small'
for f in ['onnx/model_quantized.onnx', 'tokenizer.json', 'tokenizer_config.json']:
    hf_hub_download(repo, f, local_dir='.', local_dir_use_symlinks=False)
"
# Move files from onnx/ subdirectory to root
mv onnx/model_quantized.onnx . && rmdir onnx
```

**Step 2: Verify files**

```bash
ls -lh /home/krolik/deploy/krolik-server/models/multilingual-e5-small/
```

Expected: `model_quantized.onnx` (~120MB), `tokenizer.json`, `tokenizer_config.json`

### Task 2: Benchmark e5-small in Docker (quick test)

**Step 1: Add volume mount to docker-compose.yml**

In `/home/krolik/deploy/krolik-server/docker-compose.yml`, add to memdb-go volumes:

```yaml
- /home/krolik/deploy/krolik-server/models/multilingual-e5-small:/models-small:ro
```

**Step 2: Add env var**

```yaml
MEMDB_ONNX_MODEL_DIR_SMALL: "/models-small"
```

**Step 3: Rebuild and restart**

```bash
cd /home/krolik/deploy/krolik-server
docker compose build --no-cache memdb-go
docker compose up -d --no-deps --force-recreate memdb-go
```

(This task continues in Phase 2 after code changes)

---

## Phase 2: Add e5-small support to memdb-go

### Task 3: Register e5-small in ONNXModelConfig

**Files:**
- Modify: `internal/embedder/onnx_config.go`

**Step 1: Add e5-small config**

Add to `knownONNXModels` map:

```go
"multilingual-e5-small": {Dim: 384, MaxLen: 512, PadID: 1},
```

**Step 2: Verify build**

```bash
cd ./memdb-go && go build ./...
```

### Task 4: Add zero-padding in Embed output

**Files:**
- Modify: `internal/embedder/onnx.go`

The problem: DB expects 1024-dim, e5-small produces 384-dim.
Solution: ONNXEmbedder gets a `targetDim` field. If set > dim, output is zero-padded.

**Step 1: Add targetDim field and setter**

In `onnx.go`, add field to ONNXEmbedder struct:

```go
targetDim int // if >0 and >dim, zero-pad output to this dimension
```

Add method:

```go
// SetTargetDim sets the output dimension for zero-padding.
// If targetDim > model dim, embeddings are zero-padded after normalization.
func (e *ONNXEmbedder) SetTargetDim(d int) { e.targetDim = d }
```

**Step 2: Add padding to Embed**

After `meanPool` in `Embed()`, before returning:

```go
if e.targetDim > 0 && e.targetDim > e.dim {
    embeddings = padVectors(embeddings, e.dim, e.targetDim)
}
```

**Step 3: Add padVectors helper**

In `internal/embedder/onnx_pool.go` (or new file `onnx_pad.go`):

```go
// padVectors zero-pads each vector from srcDim to dstDim.
func padVectors(vecs [][]float32, srcDim, dstDim int) [][]float32 {
    result := make([][]float32, len(vecs))
    for i, v := range vecs {
        padded := make([]float32, dstDim)
        copy(padded, v)
        result[i] = padded
    }
    return result
}
```

**Step 4: Update Dimension() to return targetDim if set**

```go
func (e *ONNXEmbedder) Dimension() int {
    if e.targetDim > 0 {
        return e.targetDim
    }
    return e.dim
}
```

**Step 5: Build and test**

```bash
go build ./...
```

### Task 5: Add config and wiring for e5-small

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/server/server.go`

**Step 1: Add config field**

In `config.go`, add to Config struct:

```go
ONNXModelDirSmall string `json:"onnx_model_dir_small"` // path to small ONNX model (optional, fast)
```

In `Load()`:

```go
ONNXModelDirSmall: envStr("MEMDB_ONNX_MODEL_DIR_SMALL", ""),
```

**Step 2: Wire in server.go initEmbedder**

After the code model registry block, add:

```go
// Fast small model: if configured, register and use as default embedder.
if cfg.ONNXModelDirSmall != "" {
    smallCfg, ok := embedder.KnownONNXModels()["multilingual-e5-small"]
    if !ok {
        smallCfg = embedder.ONNXModelConfig{Dim: 384, MaxLen: 512, PadID: 1}
    }
    smallEmb, smallErr := embedder.NewONNXEmbedder(cfg.ONNXModelDirSmall, smallCfg, logger)
    if smallErr != nil {
        logger.Warn("small embedder init failed", slog.Any("error", smallErr))
    } else {
        // Zero-pad to match DB schema (1024-dim)
        smallEmb.SetTargetDim(1024)
        // Replace default embedder with fast small model
        h.SetEmbedder(smallEmb)
        e = smallEmb
        logger.Info("small embedder loaded (now default)",
            slog.String("model", "multilingual-e5-small"),
            slog.Int("native_dim", smallCfg.Dim),
            slog.Int("output_dim", 1024),
        )
        if registry := h.EmbedRegistry(); registry != nil {
            registry.Register("multilingual-e5-small", smallEmb)
        }
    }
}
```

**Step 3: Build**

```bash
go build ./...
```

**Step 4: Commit**

```bash
git add internal/embedder/onnx_config.go internal/embedder/onnx.go \
        internal/embedder/onnx_pad.go internal/config/config.go \
        internal/server/server.go
git commit -m "feat(embedder): add multilingual-e5-small with zero-padding to 1024-dim"
```

---

## Phase 3: Deploy and Benchmark

### Task 6: Deploy and compare

**Step 1: Update docker-compose.yml**

Add to memdb-go service:

```yaml
volumes:
  - /home/krolik/deploy/krolik-server/models/multilingual-e5-small:/models-small:ro
environment:
  MEMDB_ONNX_MODEL_DIR_SMALL: "/models-small"
```

Update GOMEMLIMIT if needed (e5-small is ~120MB, total now ~970MB for 3 models).

**Step 2: Build and deploy**

```bash
cd /home/krolik/deploy/krolik-server
docker compose build --no-cache memdb-go
docker compose up -d --no-deps --force-recreate memdb-go
```

**Step 3: Verify startup logs**

```bash
docker logs memdb-go --tail 20 2>&1 | grep -i 'embedder\|small\|onnx'
```

Expected: "small embedder loaded (now default)" with native_dim=384, output_dim=1024

**Step 4: Benchmark**

```bash
# Single request
time curl -s -X POST http://127.0.0.1:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{"input":"test embedding speed","model":"multilingual-e5-small"}'

# Compare with large (should still be available via registry)
time curl -s -X POST http://127.0.0.1:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{"input":"test embedding speed","model":"multilingual-e5-large"}'
```

Expected: e5-small ~3-5s vs e5-large ~47s on ARM

**Step 5: Test search quality**

```bash
# Test actual memory search
curl -s http://127.0.0.1:8080/search -H "Authorization: Bearer $TOKEN" \
  -d '{"query":"test search quality","user_id":"memos","limit":5}'
```

Verify results are relevant (cosine scores may be lower due to zero-padding but ranking should be similar).

---

## Notes

### Why zero-padding works
Cosine similarity between two zero-padded vectors equals cosine similarity of their original shorter vectors, because the zero dimensions contribute nothing to the dot product or norms. So e5-small vectors compared to other e5-small vectors will have identical rankings.

### Cross-model limitation
Comparing an e5-large vector (1024 real dims) with an e5-small vector (384 real + 640 zeros) will give poor results because the models produce vectors in different embedding spaces. During transition, search quality degrades for old memories until re-embedded.

### Future: full migration to e5-small
If quality is acceptable after testing:
1. Add migration script using `cmd/reembed/` (already exists)
2. Migrate DB columns from `halfvec(1024)` to `halfvec(384)`
3. Remove zero-padding
4. Free ~400MB RAM from e5-large
