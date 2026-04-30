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

# D10 skill files — system prompt for the answer-extractor LLM call.
# loadSkillPrompt() resolves /etc/memdb/skills/d10-extractor.md as the
# bundled default; operators can override via MEMDB_D10_SKILL_PATH or
# by volume-mounting on top of /etc/memdb/skills/.
COPY --from=builder /app/internal/search/skills /etc/memdb/skills

EXPOSE 8080

ENTRYPOINT ["memdb-go"]
