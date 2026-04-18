# ─── Build stage ─────────────────────────────────────────────────────
FROM golang:1.26 AS builder

WORKDIR /app

# Copy source with vendor/ (no go mod download needed)
COPY . .

# Build with CGO disabled (ONNX moved to embed-server sidecar)
RUN CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w" -o /memdb-go ./cmd/server
RUN CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w" -o /memdb-reembed ./cmd/reembed

# ─── Runtime stage ───────────────────────────────────────────────────
FROM debian:trixie-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata curl && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /memdb-go /usr/local/bin/memdb-go
COPY --from=builder /memdb-reembed /usr/local/bin/memdb-reembed

EXPOSE 8080

ENTRYPOINT ["memdb-go"]
