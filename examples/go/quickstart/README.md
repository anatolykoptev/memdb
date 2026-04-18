# Go Quickstart

Adds 3 memories to MemDB and searches them using Go's `net/http` stdlib.
No external dependencies.

## Prerequisites

- Go 1.21+
- MemDB running: `cd ~/deploy/krolik-server && docker compose up -d`

## Run

```bash
go run main.go
```

With auth enabled:

```bash
MEMDB_API_KEY=your-key go run main.go
```

Custom URL:

```bash
MEMDB_URL=http://my-server:8080 go run main.go
```

## Build

```bash
go build -o memdb-quickstart .
./memdb-quickstart
```

> **Note for monorepo users:** if you have a `go.work` file in a parent
> directory, prefix commands with `GOWORK=off` to avoid workspace conflicts:
> `GOWORK=off go run main.go`

## Expected output

```
MemDB quickstart — http://localhost:8080

1. Adding 3 memories...
  added (200)
  added (200)
  added (200)

2. Searching for "outdoor activities"...

3. Top-1 results:
  [1] score=0.921  User enjoys mountain hiking on weekends.

Done.
```

## API endpoints used

| Endpoint | Purpose |
|---|---|
| `POST /product/add` | Store a conversation as memories |
| `POST /product/search` | Semantic search over stored memories |
