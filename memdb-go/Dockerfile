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

# Stage ONNX Runtime shared libraries from Go module cache
RUN ORT_LIB=$(find /go/pkg/mod -path "*/onnxruntime_go*/lib" -type d 2>/dev/null | head -1) && \
    mkdir -p /ort-lib && \
    if [ -n "$ORT_LIB" ]; then cp -r "$ORT_LIB"/* /ort-lib/; fi

# Build with CGO (needed for tokenizer + ONNX Runtime)
COPY . .
RUN CGO_ENABLED=1 CGO_LDFLAGS="-L/usr/local/lib" \
    go build -ldflags="-s -w" -o /memdb-go ./cmd/server

# Build reembed tool
RUN CGO_ENABLED=1 CGO_LDFLAGS="-L/usr/local/lib" \
    go build -ldflags="-s -w" -o /memdb-reembed ./cmd/reembed

# ─── Runtime stage ───────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata && \
    rm -rf /var/lib/apt/lists/*

# ONNX Runtime shared library (from staged build artifacts)
COPY --from=builder /ort-lib/ /tmp/ort-libs/
ARG TARGETARCH
RUN if [ "$TARGETARCH" = "arm64" ]; then \
      cp /tmp/ort-libs/aarch64/libonnxruntime.so /usr/lib/; \
    else \
      cp /tmp/ort-libs/x86_64/libonnxruntime.so /usr/lib/; \
    fi && \
    rm -rf /tmp/ort-libs && \
    ldconfig

COPY --from=builder /memdb-go /usr/local/bin/memdb-go
COPY --from=builder /memdb-reembed /usr/local/bin/memdb-reembed

EXPOSE 8080

ENTRYPOINT ["memdb-go"]
