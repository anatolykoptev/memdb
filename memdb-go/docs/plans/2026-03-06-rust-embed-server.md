# Rust Embedding Server (embed-server) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace in-process ONNX inference (CGO) in memdb-go with a dedicated Rust sidecar service, eliminating double-FFI overhead and simplifying the Go build.

**Architecture:** A standalone Rust HTTP server (`embed-server`) loads ONNX models via the `ort` crate and exposes an OpenAI-compatible `/v1/embeddings` endpoint. memdb-go replaces its `ONNXEmbedder` with an `HTTPEmbedder` that calls the Rust sidecar over the Docker network. The existing `Embedder` interface and `Registry` remain unchanged — consumers (go-code, memdb-api, etc.) notice nothing.

**Tech Stack:** Rust (axum, ort, tokenizers), Docker, Go (memdb-go HTTPEmbedder)

**Key Benefits:**
- Removes CGO from memdb-go (no libtokenizers.a, no libonnxruntime.so in Go build)
- `tokenizers` crate is native Rust (currently Go calls it via CGO → Rust .so — double FFI)
- `ort` crate wraps ONNX Runtime natively (no Go CGO layer)
- Separate memory accounting: embed-server gets its own container limits
- memdb-go Dockerfile becomes a simple `CGO_ENABLED=0` static build

---

## Task 1: Scaffold Rust project

**Files:**
- Create: `~/src/embed-server/Cargo.toml`
- Create: `~/src/embed-server/src/main.rs`
- Create: `~/src/embed-server/.gitignore`

**Step 1: Create project directory and Cargo.toml**

```toml
# ~/src/embed-server/Cargo.toml
[package]
name = "embed-server"
version = "0.1.0"
edition = "2024"

[dependencies]
axum = "0.8"
tokio = { version = "1", features = ["full"] }
serde = { version = "1", features = ["derive"] }
serde_json = "1"
ort = { version = "2", features = ["load-dynamic"] }
tokenizers = { version = "0.21", default-features = false }
ndarray = "0.16"
tracing = "0.1"
tracing-subscriber = { version = "0.3", features = ["json", "env-filter"] }
tower-http = { version = "0.6", features = ["cors", "trace"] }
```

Key notes on dependencies:
- `ort` with `load-dynamic` — loads libonnxruntime.so at runtime (same as Go does)
- `tokenizers` — native Rust HuggingFace tokenizers (the SAME lib Go calls via CGO!)
- `ndarray` — for mean pooling / L2 norm math
- `axum` — lightweight async HTTP framework

**Step 2: Create minimal main.rs with health endpoint**

```rust
// ~/src/embed-server/src/main.rs
use axum::{routing::get, Router};

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt().json().init();
    let app = Router::new().route("/health", get(|| async { "ok" }));
    let addr = "0.0.0.0:8082";
    tracing::info!("embed-server listening on {addr}");
    let listener = tokio::net::TcpListener::bind(addr).await.unwrap();
    axum::serve(listener, app).await.unwrap();
}
```

**Step 3: Create .gitignore**

```
target/
```

**Step 4: Verify it compiles**

Run: `cd ~/src/embed-server && cargo check`
Expected: compiles without errors (deps download on first run)

**Step 5: Init git and commit**

```bash
cd ~/src/embed-server
git init && git add -A
git commit -m "feat: scaffold embed-server Rust project

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 2: Model configuration and loading

**Files:**
- Create: `~/src/embed-server/src/config.rs`
- Create: `~/src/embed-server/src/model.rs`
- Modify: `~/src/embed-server/src/main.rs`

**Step 1: Create config.rs — model configuration from env vars**

```rust
// ~/src/embed-server/src/config.rs
use std::env;

pub struct ModelDef {
    pub name: String,
    pub dir: String,
    pub dim: usize,
    pub max_len: usize,
    pub pad_id: u32,
    pub has_token_type_ids: bool,
}

pub struct Config {
    pub port: u16,
    pub models: Vec<ModelDef>,
    pub default_model: String,
}

impl Config {
    /// Reads config from environment variables.
    ///
    /// Required:
    ///   EMBED_MODELS — comma-separated model specs:
    ///     "name:dir:dim:max_len:pad_id:has_tti"
    ///     Example: "multilingual-e5-large:/models:1024:512:1:false,jina-code-v2:/models-code:768:512:0:true"
    ///   EMBED_DEFAULT_MODEL — default model name (default: first in list)
    ///   EMBED_PORT — listen port (default: 8082)
    pub fn from_env() -> Self {
        let port: u16 = env::var("EMBED_PORT")
            .unwrap_or_else(|_| "8082".into())
            .parse()
            .expect("EMBED_PORT must be u16");

        let specs = env::var("EMBED_MODELS")
            .expect("EMBED_MODELS is required");

        let models: Vec<ModelDef> = specs
            .split(',')
            .map(|s| {
                let p: Vec<&str> = s.trim().split(':').collect();
                assert!(p.len() == 6, "EMBED_MODELS format: name:dir:dim:max_len:pad_id:has_tti");
                ModelDef {
                    name: p[0].to_string(),
                    dir: p[1].to_string(),
                    dim: p[2].parse().unwrap(),
                    max_len: p[3].parse().unwrap(),
                    pad_id: p[4].parse().unwrap(),
                    has_token_type_ids: p[5].parse().unwrap(),
                }
            })
            .collect();

        let default_model = env::var("EMBED_DEFAULT_MODEL")
            .unwrap_or_else(|_| models[0].name.clone());

        Config { port, models, default_model }
    }
}
```

**Step 2: Create model.rs — ONNX model wrapper**

```rust
// ~/src/embed-server/src/model.rs
use std::path::Path;
use std::sync::Mutex;

use ort::{session::Session, value::Value};
use tokenizers::Tokenizer;

pub struct EmbedModel {
    session: Mutex<Session>,
    tokenizer: Tokenizer,
    pub dim: usize,
    pub max_len: usize,
    pub pad_id: u32,
    pub has_token_type_ids: bool,
}

impl EmbedModel {
    pub fn load(dir: &str, dim: usize, max_len: usize, pad_id: u32, has_tti: bool) -> Self {
        let model_path = Path::new(dir).join("model_quantized.onnx");
        let tok_path = Path::new(dir).join("tokenizer.json");

        let session = Session::builder()
            .unwrap()
            .with_intra_threads(4)
            .unwrap()
            .with_inter_threads(1)
            .unwrap()
            .commit_from_file(&model_path)
            .unwrap_or_else(|e| panic!("load ONNX model {}: {e}", model_path.display()));

        let tokenizer = Tokenizer::from_file(&tok_path)
            .unwrap_or_else(|e| panic!("load tokenizer {}: {e}", tok_path.display()));

        tracing::info!(
            model = %model_path.display(),
            dim, max_len, pad_id, has_tti,
            "model loaded"
        );

        EmbedModel {
            session: Mutex::new(session),
            tokenizer,
            dim,
            max_len,
            pad_id,
            has_token_type_ids: has_tti,
        }
    }

    /// Embed a batch of texts. Returns one Vec<f32> per input text.
    pub fn embed(&self, texts: &[String]) -> Result<Vec<Vec<f32>>, String> {
        if texts.is_empty() {
            return Ok(vec![]);
        }

        let batch_size = texts.len();

        // Tokenize
        let encodings = self.tokenizer
            .encode_batch(texts.to_vec(), true)
            .map_err(|e| format!("tokenize: {e}"))?;

        // Find max seq len (capped at self.max_len)
        let max_seq = encodings.iter()
            .map(|e| e.get_ids().len().min(self.max_len))
            .max()
            .unwrap_or(0);

        if max_seq == 0 {
            return Ok(vec![vec![0.0; self.dim]; batch_size]);
        }

        // Build padded input tensors as flat i64 arrays
        let total = batch_size * max_seq;
        let mut input_ids = vec![self.pad_id as i64; total];
        let mut attention_mask = vec![0i64; total];
        let mut token_type_ids = vec![0i64; total];

        for (b, enc) in encodings.iter().enumerate() {
            let ids = enc.get_ids();
            let mask = enc.get_attention_mask();
            let seq_len = ids.len().min(self.max_len);
            let offset = b * max_seq;

            for s in 0..seq_len {
                input_ids[offset + s] = ids[s] as i64;
                attention_mask[offset + s] = mask[s] as i64;
            }
        }

        // Create ORT tensors
        let shape = [batch_size as i64, max_seq as i64];

        let ids_tensor = Value::from_array(
            ndarray::Array2::from_shape_vec([batch_size, max_seq], input_ids).unwrap()
        ).map_err(|e| format!("ids tensor: {e}"))?;

        let mask_tensor = Value::from_array(
            ndarray::Array2::from_shape_vec([batch_size, max_seq], attention_mask.clone()).unwrap()
        ).map_err(|e| format!("mask tensor: {e}"))?;

        let mut inputs = vec![ids_tensor, mask_tensor];

        if self.has_token_type_ids {
            let tti_tensor = Value::from_array(
                ndarray::Array2::from_shape_vec([batch_size, max_seq], token_type_ids).unwrap()
            ).map_err(|e| format!("tti tensor: {e}"))?;
            inputs.push(tti_tensor);
        }

        // Run inference (serialized via mutex, same as Go impl)
        let session = self.session.lock().map_err(|e| format!("lock: {e}"))?;
        let input_refs: Vec<ort::session::SessionInputValue<'_>> = inputs.iter()
            .map(|v| ort::session::SessionInputValue::from(v))
            .collect();
        let outputs = session.run_raw(input_refs)
            .map_err(|e| format!("inference: {e}"))?;
        drop(session);

        // Extract hidden states [batch, seq, dim]
        let hidden = outputs[0]
            .try_extract_tensor::<f32>()
            .map_err(|e| format!("extract: {e}"))?;

        // Mean pool with attention mask + L2 normalize
        let mut result = Vec::with_capacity(batch_size);
        for b in 0..batch_size {
            let mut vec = vec![0.0f32; self.dim];
            let mut mask_sum: f64 = 0.0;

            for s in 0..max_seq {
                let m = attention_mask[b * max_seq + s];
                if m == 0 { continue; }
                mask_sum += 1.0;
                for d in 0..self.dim {
                    vec[d] += hidden[[b, s, d]];
                }
            }

            if mask_sum > 0.0 {
                let inv = 1.0 / mask_sum as f32;
                for v in vec.iter_mut() { *v *= inv; }
            }

            // L2 normalize
            let norm: f32 = vec.iter().map(|v| v * v).sum::<f32>().sqrt();
            if norm > 0.0 {
                let inv = 1.0 / norm;
                for v in vec.iter_mut() { *v *= inv; }
            }

            result.push(vec);
        }

        Ok(result)
    }
}
```

**Step 3: Update main.rs to load models from config**

```rust
// Add to main.rs
mod config;
mod model;

use std::collections::HashMap;
use std::sync::Arc;

use config::Config;
use model::EmbedModel;

struct AppState {
    models: HashMap<String, Arc<EmbedModel>>,
    default_model: String,
}
```

Load models in main before starting server. Wire into axum state.

**Step 4: Verify it compiles**

Run: `cd ~/src/embed-server && cargo check`
Expected: compiles (may have warnings about unused code — OK)

**Step 5: Commit**

```bash
git add -A
git commit -m "feat: model config and ONNX loading via ort crate

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 3: OpenAI-compatible /v1/embeddings endpoint

**Files:**
- Create: `~/src/embed-server/src/api.rs`
- Modify: `~/src/embed-server/src/main.rs`

**Step 1: Create api.rs with request/response types and handler**

```rust
// ~/src/embed-server/src/api.rs
use std::sync::Arc;

use axum::{extract::State, http::StatusCode, Json};
use serde::{Deserialize, Serialize};

use crate::AppState;

#[derive(Deserialize)]
pub struct EmbedRequest {
    pub input: EmbedInput,
    #[serde(default)]
    pub model: String,
}

#[derive(Deserialize)]
#[serde(untagged)]
pub enum EmbedInput {
    Single(String),
    Batch(Vec<String>),
}

#[derive(Serialize)]
pub struct EmbedResponse {
    pub object: &'static str,
    pub data: Vec<EmbedData>,
    pub model: String,
    pub usage: EmbedUsage,
}

#[derive(Serialize)]
pub struct EmbedData {
    pub object: &'static str,
    pub embedding: Vec<f32>,
    pub index: usize,
}

#[derive(Serialize)]
pub struct EmbedUsage {
    pub prompt_tokens: usize,
    pub total_tokens: usize,
}

#[derive(Serialize)]
struct ErrorResponse {
    error: ErrorDetail,
}

#[derive(Serialize)]
struct ErrorDetail {
    message: String,
    r#type: String,
}

pub async fn embeddings(
    State(state): State<Arc<AppState>>,
    Json(req): Json<EmbedRequest>,
) -> Result<Json<EmbedResponse>, (StatusCode, Json<ErrorResponse>)> {
    let model_name = if req.model.is_empty() {
        &state.default_model
    } else {
        &req.model
    };

    let model = state.models.get(model_name).ok_or_else(|| {
        (StatusCode::BAD_REQUEST, Json(ErrorResponse {
            error: ErrorDetail {
                message: format!("unknown model: {model_name}"),
                r#type: "invalid_request_error".into(),
            },
        }))
    })?;

    let texts: Vec<String> = match req.input {
        EmbedInput::Single(s) => vec![s],
        EmbedInput::Batch(v) => v,
    };

    let embeddings = model.embed(&texts).map_err(|e| {
        (StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse {
            error: ErrorDetail {
                message: e,
                r#type: "server_error".into(),
            },
        }))
    })?;

    let data: Vec<EmbedData> = embeddings.into_iter().enumerate()
        .map(|(i, emb)| EmbedData { object: "embedding", embedding: emb, index: i })
        .collect();

    Ok(Json(EmbedResponse {
        object: "list",
        data,
        model: model_name.clone(),
        usage: EmbedUsage { prompt_tokens: 0, total_tokens: 0 },
    }))
}
```

**Step 2: Wire into main.rs**

```rust
mod api;

// In main():
let cfg = Config::from_env();
let mut models = HashMap::new();
for m in &cfg.models {
    let em = Arc::new(EmbedModel::load(&m.dir, m.dim, m.max_len, m.pad_id, m.has_token_type_ids));
    models.insert(m.name.clone(), em);
}
let state = Arc::new(AppState { models, default_model: cfg.default_model.clone() });

let app = Router::new()
    .route("/health", get(|| async { "ok" }))
    .route("/v1/embeddings", post(api::embeddings))
    .with_state(state);
```

**Step 3: Compile and test locally with model files**

Run: `cargo build --release`
Then test:
```bash
EMBED_MODELS="multilingual-e5-large:/home/krolik/deploy/krolik-server/models/multilingual-e5-large:1024:512:1:false" \
  ./target/release/embed-server &

curl -s http://localhost:8082/v1/embeddings \
  -d '{"input":"Hello world","model":"multilingual-e5-large"}' \
  -H 'Content-Type: application/json' | jq '.data[0].embedding[:5]'
```
Expected: array of 5 float values (normalized, sum of squares ≈ 1.0)

**Step 4: Compare output with Go for identical input**

```bash
# Go (current):
curl -s http://localhost:8080/v1/embeddings \
  -d '{"input":"passage: Hello world","model":"multilingual-e5-large"}' \
  -H 'Content-Type: application/json' -H 'Authorization: Bearer <key>' \
  | jq '.data[0].embedding[:10]'

# Rust (new):
curl -s http://localhost:8082/v1/embeddings \
  -d '{"input":"passage: Hello world","model":"multilingual-e5-large"}' \
  -H 'Content-Type: application/json' \
  | jq '.data[0].embedding[:10]'
```
Expected: **identical vectors** (same ONNX model, same tokenizer, same pooling)

**Step 5: Commit**

```bash
git add -A
git commit -m "feat: OpenAI-compatible /v1/embeddings endpoint

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 4: Dockerfile for embed-server

**Files:**
- Create: `~/src/embed-server/Dockerfile`

**Step 1: Create multi-stage Dockerfile**

```dockerfile
# ~/src/embed-server/Dockerfile
# --- Build stage ---
FROM rust:1.87 AS builder

WORKDIR /app

# Install ONNX Runtime shared lib
ARG TARGETARCH
RUN ORT_VER="1.24.1" && \
    if [ "$TARGETARCH" = "arm64" ]; then ORT_ARCH="aarch64"; else ORT_ARCH="x64"; fi && \
    curl -L -o /tmp/ort.tgz \
      "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VER}/onnxruntime-linux-${ORT_ARCH}-${ORT_VER}.tgz" && \
    tar -xzf /tmp/ort.tgz -C /tmp/ && \
    cp /tmp/onnxruntime-linux-${ORT_ARCH}-${ORT_VER}/lib/libonnxruntime.so /usr/lib/ && \
    ldconfig && \
    rm -rf /tmp/ort.tgz /tmp/onnxruntime-linux-*

COPY Cargo.toml Cargo.lock ./
# Fetch deps (cached layer)
RUN mkdir src && echo "fn main(){}" > src/main.rs && cargo build --release && rm -rf src

COPY src/ src/
RUN cargo build --release

# --- Runtime stage ---
FROM debian:trixie-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /usr/lib/libonnxruntime.so /usr/lib/
RUN ldconfig

COPY --from=builder /app/target/release/embed-server /usr/local/bin/

EXPOSE 8082

ENTRYPOINT ["embed-server"]
```

**Step 2: Build and test**

```bash
cd ~/src/embed-server && docker build -t embed-server .
```
Expected: builds successfully

**Step 3: Commit**

```bash
git add Dockerfile
git commit -m "feat: multi-stage Dockerfile for embed-server

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 5: Add embed-server to docker-compose.yml

**Files:**
- Modify: `~/deploy/krolik-server/docker-compose.yml`

**Step 1: Add embed-server service (before memdb-go)**

```yaml
  embed-server:
    build:
      context: /home/krolik/src/embed-server
      dockerfile: Dockerfile
    container_name: embed-server
    restart: unless-stopped
    labels:
      dozor.group: "memdb"
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    read_only: true
    tmpfs:
      - /tmp
    logging: *default-logging
    environment:
      EMBED_PORT: "8082"
      EMBED_MODELS: "multilingual-e5-large:/models:1024:512:1:false,jina-code-v2:/models-code:768:512:0:true"
      EMBED_DEFAULT_MODEL: "multilingual-e5-large"
      ORT_DYLIB_PATH: "/usr/lib/libonnxruntime.so"
      RUST_LOG: "info"
    volumes:
      - /home/krolik/deploy/krolik-server/models/multilingual-e5-large:/models:ro
      - /home/krolik/deploy/krolik-server/models/jina-code-v2:/models-code:ro
    deploy:
      resources:
        limits:
          memory: 1536M  # 2 ONNX models (~550+300MB) + Rust runtime (no GC)
    healthcheck:
      test: ["CMD", "curl", "-sf", "http://localhost:8082/health"]
      interval: 15s
      timeout: 5s
      retries: 3
      start_period: 10s
    networks:
      - backend
```

**Step 2: Update memdb-go to point to embed-server**

In memdb-go environment, change:
```yaml
      # Replace local ONNX with Rust sidecar
      MEMDB_EMBEDDER_TYPE: "http"
      MEMDB_EMBED_URL: "http://embed-server:8082"
```

Remove from memdb-go:
```yaml
      # Remove (no longer needed — models are in embed-server):
      # MEMDB_ONNX_MODEL_DIR: "/models"
      # MEMDB_ONNX_MODEL_DIR_CODE: "/models-code"
```

Remove volumes from memdb-go:
```yaml
      # Remove:
      # - /home/krolik/.../multilingual-e5-large:/models:ro
      # - /home/krolik/.../jina-code-v2:/models-code:ro
```

Reduce memdb-go memory (no more ONNX models in-process):
```yaml
    deploy:
      resources:
        limits:
          memory: 512M  # Down from 3072M — no ONNX models
    environment:
      GOMEMLIMIT: "400MiB"  # Down from 2500MiB
```

Add dependency:
```yaml
    depends_on:
      embed-server:
        condition: service_healthy
```

**Step 3: Don't deploy yet — memdb-go HTTPEmbedder needed first (Task 6)**

**Step 4: Commit**

```bash
cd ~/deploy/krolik-server
git add docker-compose.yml
git commit -m "feat: add embed-server to docker-compose, reconfigure memdb-go

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 6: HTTPEmbedder in memdb-go

**Files:**
- Create: `~/src/MemDB/memdb-go/internal/embedder/http.go`
- Modify: `~/src/MemDB/memdb-go/internal/embedder/factory.go`
- Modify: `~/src/MemDB/memdb-go/internal/server/server.go`
- Create: `~/src/MemDB/memdb-go/internal/embedder/http_test.go`

**Step 1: Write the failing test**

```go
// http_test.go
package embedder

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestHTTPEmbedder_Embed(t *testing.T) {
    // Mock server returning OpenAI-compatible response
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var req struct {
            Input []string `json:"input"`
            Model string   `json:"model"`
        }
        json.NewDecoder(r.Body).Decode(&req)

        data := make([]map[string]any, len(req.Input))
        for i := range req.Input {
            data[i] = map[string]any{
                "object":    "embedding",
                "embedding": []float32{0.1, 0.2, 0.3},
                "index":     i,
            }
        }
        json.NewEncoder(w).Encode(map[string]any{
            "object": "list",
            "data":   data,
            "model":  req.Model,
        })
    }))
    defer srv.Close()

    emb := NewHTTPEmbedder(srv.URL, "test-model", 3, nil)
    vecs, err := emb.Embed(context.Background(), []string{"hello", "world"})
    if err != nil {
        t.Fatal(err)
    }
    if len(vecs) != 2 {
        t.Fatalf("expected 2 vectors, got %d", len(vecs))
    }
    if len(vecs[0]) != 3 {
        t.Fatalf("expected dim=3, got %d", len(vecs[0]))
    }
}
```

**Step 2: Run test to verify it fails**

Run: `cd ~/src/MemDB/memdb-go && go test ./internal/embedder/ -run TestHTTPEmbedder -v`
Expected: FAIL — `NewHTTPEmbedder` not defined

**Step 3: Implement HTTPEmbedder**

```go
// http.go
package embedder

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "time"
)

// HTTPEmbedder calls a remote OpenAI-compatible /v1/embeddings endpoint.
// Used to delegate inference to a Rust sidecar (embed-server).
type HTTPEmbedder struct {
    baseURL string
    model   string
    dim     int
    client  *http.Client
    logger  *slog.Logger
}

// NewHTTPEmbedder creates an embedder that calls baseURL/v1/embeddings.
func NewHTTPEmbedder(baseURL, model string, dim int, logger *slog.Logger) *HTTPEmbedder {
    if logger == nil {
        logger = slog.Default()
    }
    return &HTTPEmbedder{
        baseURL: baseURL,
        model:   model,
        dim:     dim,
        client: &http.Client{
            Timeout: 30 * time.Second,
        },
        logger: logger,
    }
}

type httpEmbedRequest struct {
    Input []string `json:"input"`
    Model string   `json:"model"`
}

type httpEmbedResponse struct {
    Data []struct {
        Embedding []float32 `json:"embedding"`
        Index     int       `json:"index"`
    } `json:"data"`
}

func (h *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    if len(texts) == 0 {
        return nil, nil
    }

    body, err := json.Marshal(httpEmbedRequest{Input: texts, Model: h.model})
    if err != nil {
        return nil, fmt.Errorf("http embedder: marshal: %w", err)
    }

    req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/v1/embeddings", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("http embedder: create request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := h.client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("http embedder: request to %s: %w", h.baseURL, err)
    }
    defer resp.Body.Close()

    respBody, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("http embedder: read response: %w", err)
    }

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("http embedder: status %d: %s", resp.StatusCode, string(respBody))
    }

    var result httpEmbedResponse
    if err := json.Unmarshal(respBody, &result); err != nil {
        return nil, fmt.Errorf("http embedder: unmarshal: %w", err)
    }

    vecs := make([][]float32, len(texts))
    for _, d := range result.Data {
        if d.Index >= 0 && d.Index < len(texts) {
            vecs[d.Index] = d.Embedding
        }
    }

    h.logger.Debug("http embed complete",
        slog.Int("texts", len(texts)),
        slog.String("model", h.model),
    )
    return vecs, nil
}

func (h *HTTPEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
    return EmbedQueryViaEmbed(ctx, h, text)
}

func (h *HTTPEmbedder) Dimension() int { return h.dim }

func (h *HTTPEmbedder) Close() error { return nil }
```

**Step 4: Run test to verify it passes**

Run: `cd ~/src/MemDB/memdb-go && go test ./internal/embedder/ -run TestHTTPEmbedder -v`
Expected: PASS

**Step 5: Add "http" type to factory.go**

In `factory.go`, add case to the switch in `New()`:

```go
    case "http":
        if cfg.HTTPBaseURL == "" {
            return nil, errors.New("embedder: http requires MEMDB_EMBED_URL")
        }
        dim := cfg.HTTPDim
        if dim == 0 {
            dim = 1024
        }
        model := cfg.Model
        if model == "" {
            model = "multilingual-e5-large"
        }
        e := NewHTTPEmbedder(cfg.HTTPBaseURL, model, dim, logger)
        logger.Info("embedder: http", slog.String("url", cfg.HTTPBaseURL), slog.String("model", model))
        return e, nil
```

Add fields to `Config` struct:
```go
    HTTPBaseURL string // for type="http"
    HTTPDim     int    // dimension override for HTTP embedder
```

**Step 6: Update server.go initEmbedder for HTTP multi-model**

When `cfg.EmbedderType == "http"` and `cfg.EmbedURL != ""`:
- Create Registry with two HTTPEmbedders pointing to the same URL but different model names
- e5-large (dim=1024) + jina-code-v2 (dim=768)

```go
    // In initEmbedder, after setting h.SetEmbedder(e):
    if cfg.EmbedderType == "http" && cfg.EmbedURL != "" {
        registry := embedder.NewRegistry("multilingual-e5-large")
        registry.Register("multilingual-e5-large", e)

        codeEmb := embedder.NewHTTPEmbedder(cfg.EmbedURL, "jina-code-v2", 768, logger)
        registry.Register("jina-code-v2", codeEmb)
        h.SetEmbedRegistry(registry)
        logger.Info("http embed registry created", slog.Int("models", 2))
    }
```

**Step 7: Add config fields for EmbedURL**

In `~/src/MemDB/memdb-go/internal/config/config.go`, add:
```go
    EmbedURL string `env:"MEMDB_EMBED_URL"` // URL of embed-server (Rust sidecar)
```

Map it in factory Config:
```go
    HTTPBaseURL: cfg.EmbedURL,
```

**Step 8: Run all tests**

Run: `cd ~/src/MemDB/memdb-go && go test ./... -count=1`
Expected: all pass

**Step 9: Commit**

```bash
cd ~/src/MemDB/memdb-go
git add internal/embedder/http.go internal/embedder/http_test.go internal/embedder/factory.go internal/server/server.go internal/config/config.go
git commit -m "feat: HTTPEmbedder for Rust sidecar delegation

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 7: Simplify memdb-go Dockerfile (remove CGO)

**Files:**
- Modify: `~/src/MemDB/memdb-go/Dockerfile`

**Step 1: Rewrite Dockerfile without CGO dependencies**

```dockerfile
# --- Build stage ---
FROM golang:1.26rc3 AS builder

WORKDIR /app
COPY . .

# Pure Go build — no CGO needed (ONNX inference moved to embed-server)
RUN CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w" -o /memdb-go ./cmd/server

# --- Runtime stage ---
FROM debian:trixie-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata curl && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /memdb-go /usr/local/bin/memdb-go

EXPOSE 8080

ENTRYPOINT ["memdb-go"]
```

Note: `cmd/reembed` still uses ONNXEmbedder directly. Options:
- (a) Keep reembed building with CGO separately (rarely used)
- (b) Rewrite reembed to use HTTPEmbedder (call embed-server)
- Choose (b) — simpler, no CGO anywhere

**Step 2: Update reembed to use HTTPEmbedder**

Modify `cmd/reembed/main.go` to accept `--embed-url` flag and use HTTPEmbedder instead of ONNXEmbedder. This is a separate commit.

**Step 3: Verify build**

```bash
cd ~/src/MemDB/memdb-go && docker build -t memdb-go-test .
```
Expected: builds faster (no libtokenizers download, no libonnxruntime download)

**Step 4: Commit**

```bash
git add Dockerfile
git commit -m "feat: remove CGO from Dockerfile (ONNX moved to embed-server)

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 8: Integration test and deploy

**Step 1: Build both containers**

```bash
cd ~/deploy/krolik-server
docker compose build --no-cache embed-server memdb-go
```

**Step 2: Start embed-server first, verify health**

```bash
docker compose up -d embed-server
docker compose logs embed-server | head -20
curl http://127.0.0.1:8082/health  # (if port exposed, or exec into container)
```

**Step 3: Restart memdb-go with new config**

```bash
docker compose up -d --no-deps --force-recreate memdb-go
docker compose logs memdb-go 2>&1 | grep -i embed | head -10
```
Expected: `"embedder: http" url=http://embed-server:8082 model=multilingual-e5-large`

**Step 4: Verify embedding quality (compare vectors)**

```bash
# Test e5-large
curl -s http://127.0.0.1:8080/v1/embeddings \
  -d '{"input":"passage: Тестовое предложение на русском языке","model":"multilingual-e5-large"}' \
  -H 'Content-Type: application/json' -H 'Authorization: Bearer <key>' \
  | jq '.data[0].embedding[:5]'

# Test jina-code-v2
curl -s http://127.0.0.1:8080/v1/embeddings \
  -d '{"input":"func main() { fmt.Println(\"hello\") }","model":"jina-code-v2"}' \
  -H 'Content-Type: application/json' -H 'Authorization: Bearer <key>' \
  | jq '.data[0].embedding[:5]'
```
Expected: float vectors of correct dimensionality (1024 and 768)

**Step 5: Verify downstream services**

```bash
# go-code semantic search still works
curl http://127.0.0.1:8897/health

# memdb search
curl -s http://127.0.0.1:8080/search \
  -d '{"query":"test","cube":"memos","user":"memos"}' \
  -H 'Content-Type: application/json' -H 'Authorization: Bearer <key>'
```

**Step 6: Check memory savings**

```bash
docker stats --no-stream embed-server memdb-go
```
Expected: embed-server ~800-1000MB, memdb-go ~200-300MB. Total < previous 3072MB.

**Step 7: Commit docker-compose changes**

```bash
cd ~/deploy/krolik-server
git add docker-compose.yml
git commit -m "deploy: switch memdb-go to embed-server sidecar

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Summary: Resource Impact

| Metric | Before (Go+ONNX) | After (Rust sidecar) |
|--------|-------------------|----------------------|
| memdb-go memory limit | 3072M | 512M |
| embed-server memory | — | 1536M |
| **Total** | **3072M** | **2048M** |
| memdb-go build time | ~3min (CGO+deps) | ~30s (pure Go) |
| FFI layers | Go→CGO→libort + Go→CGO→libtok(Rust) | Rust→libort (native) + Rust tokenizers (native) |
| Model files mounted to | memdb-go | embed-server |

## Rollback Plan

If issues arise, revert memdb-go to `MEMDB_EMBEDDER_TYPE=onnx` in docker-compose and restore the old Dockerfile + model volumes. The ONNX code remains in memdb-go (behind build tag `cgo`), just unused.
