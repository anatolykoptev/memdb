# ─── Build stage ─────────────────────────────────────────────────────
FROM golang:1.26rc3 AS builder

WORKDIR /app

# Install tokenizer static library (pre-built for target arch)
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && \
    ARCH=$([ "$TARGETARCH" = "arm64" ] && echo "aarch64" || echo "x86_64") && \
    curl -L -o libtokenizers.tar.gz \
      "https://github.com/daulet/tokenizers/releases/download/v1.25.0/libtokenizers.linux-${ARCH}.tar.gz" && \
    tar -xzf libtokenizers.tar.gz && \
    mv libtokenizers.a /usr/local/lib/ && \
    rm libtokenizers.tar.gz && \
    rm -rf /var/lib/apt/lists/*

# Cache dependencies
COPY go.mod go.sum* ./
RUN go mod download

# Stage ONNX Runtime shared library from Go module cache (test_data/)
# yalue/onnxruntime_go stores .so files as test_data/onnxruntime_{arch}.so
ARG TARGETARCH
RUN ORT_DIR=$(find /go/pkg/mod -path "*/onnxruntime_go*/test_data" -type d 2>/dev/null | head -1) && \
    mkdir -p /ort-lib && \
    if [ "$TARGETARCH" = "arm64" ] && [ -n "$ORT_DIR" ]; then \
      cp "$ORT_DIR/onnxruntime_arm64.so" /ort-lib/libonnxruntime.so; \
    elif [ -n "$ORT_DIR" ] && [ -f "$ORT_DIR/onnxruntime_amd64.so" ]; then \
      cp "$ORT_DIR/onnxruntime_amd64.so" /ort-lib/libonnxruntime.so; \
    else \
      echo "WARNING: ONNX Runtime .so not found in Go module cache, downloading..."; \
      ORT_VER="1.21.1"; \
      if [ "$TARGETARCH" = "arm64" ]; then ORT_ARCH="aarch64"; else ORT_ARCH="x64"; fi; \
      curl -L -o /tmp/ort.tgz \
        "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VER}/onnxruntime-linux-${ORT_ARCH}-${ORT_VER}.tgz" && \
      tar -xzf /tmp/ort.tgz --strip-components=2 -C /ort-lib/ "*/lib/libonnxruntime.so" && \
      rm /tmp/ort.tgz; \
    fi && \
    ls -la /ort-lib/

# Build with CGO (needed for tokenizer + ONNX Runtime)
COPY . .
RUN CGO_ENABLED=1 CGO_LDFLAGS="-L/usr/local/lib" \
    go build -ldflags="-s -w" -o /memdb-go ./cmd/server

# Build reembed tool
RUN CGO_ENABLED=1 CGO_LDFLAGS="-L/usr/local/lib" \
    go build -ldflags="-s -w" -o /memdb-reembed ./cmd/reembed

# ─── Runtime stage ───────────────────────────────────────────────────
FROM debian:trixie-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata && \
    rm -rf /var/lib/apt/lists/*

# ONNX Runtime shared library
COPY --from=builder /ort-lib/libonnxruntime.so /usr/lib/
RUN ldconfig

COPY --from=builder /memdb-go /usr/local/bin/memdb-go
COPY --from=builder /memdb-reembed /usr/local/bin/memdb-reembed

EXPOSE 8080

ENTRYPOINT ["memdb-go"]
