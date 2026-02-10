# ─── Build stage ─────────────────────────────────────────────────────
FROM golang:1.26rc3-alpine AS builder

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum* ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /memdb-go ./cmd/server

# ─── Runtime stage ───────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /memdb-go /usr/local/bin/memdb-go

EXPOSE 8080

ENTRYPOINT ["memdb-go"]
