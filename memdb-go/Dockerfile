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

# Stage ONNX Runtime shared library (download — vendor/ doesn't include test_data .so files)
ARG TARGETARCH
RUN mkdir -p /ort-lib && \
    ORT_VER="1.24.1" && \
    if [ "$TARGETARCH" = "arm64" ]; then ORT_ARCH="aarch64"; else ORT_ARCH="x64"; fi && \
    curl -L -o /tmp/ort.tgz \
      "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VER}/onnxruntime-linux-${ORT_ARCH}-${ORT_VER}.tgz" && \
    tar -xzf /tmp/ort.tgz -C /tmp/ && \
    cp /tmp/onnxruntime-linux-${ORT_ARCH}-${ORT_VER}/lib/libonnxruntime.so /ort-lib/ && \
    rm -rf /tmp/ort.tgz /tmp/onnxruntime-linux-* && \
    ls -la /ort-lib/

# Copy source with vendor/ (no go mod download needed)
COPY . .

# Build with CGO (needed for tokenizer + ONNX Runtime)
RUN CGO_ENABLED=1 CGO_LDFLAGS="-L/usr/local/lib" \
    go build -mod=vendor -ldflags="-s -w" -o /memdb-go ./cmd/server

# Build reembed tool
RUN CGO_ENABLED=1 CGO_LDFLAGS="-L/usr/local/lib" \
    go build -mod=vendor -ldflags="-s -w" -o /memdb-reembed ./cmd/reembed

# ─── Runtime stage ───────────────────────────────────────────────────
FROM debian:trixie-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata curl && \
    rm -rf /var/lib/apt/lists/*

# ONNX Runtime shared library
COPY --from=builder /ort-lib/libonnxruntime.so /usr/lib/
RUN ldconfig

COPY --from=builder /memdb-go /usr/local/bin/memdb-go
COPY --from=builder /memdb-reembed /usr/local/bin/memdb-reembed

EXPOSE 8080

ENTRYPOINT ["memdb-go"]
