# Contributing to MemDB

Thank you for your interest in contributing!

## How to ask questions

- **Usage questions** — open a [GitHub Discussion](https://github.com/anatolykoptev/memdb/discussions).
- **Bug reports** — open a [GitHub Issue](https://github.com/anatolykoptev/memdb/issues).
- **Feature proposals** — open a Discussion first, get buy-in from a maintainer, then open a PR.

## Development Setup

### Prerequisites

- Go 1.26+ (the version in `memdb-go/go.mod`)
- Docker + Docker Compose
- PostgreSQL 17 with pgvector and Apache AGE (via Docker is fine)
- Python 3.10+ only for `examples/python*` and pre-commit; the service itself is pure Go

### Clone and Build

```bash
git clone https://github.com/anatolykoptev/memdb.git
cd memdb

# Go service (the Makefile is at the repository root)
make build
```

### Running the Stack Locally

```bash
cp .env.example .env
# Edit .env: set MEMDB_LLM_API_KEY and POSTGRES_PASSWORD
docker compose -f docker/docker-compose.yml up -d
```

## Running Tests

Before opening a PR, run the gate CI runs (`.github/workflows/preflight.yml`):

```bash
make preflight    # go vet, go build, go test and a gofmt check, all in memdb-go
make lint         # golangci-lint, run by the CI Lint job
```

### Integration tests (require Postgres + Apache AGE)

Tests tagged `//go:build livepg` need a real database instance:

```bash
# Start the stack
docker compose up -d postgres

# Run with live DB
cd memdb-go
go test -tags livepg ./...
```

## Code Style

### Go

- Format: `gofmt -w .`
- Lint: `make lint` (runs golangci-lint)
- Files target ≤200 lines; hard limit 300 lines
- No `panic`/`unwrap` in request handlers

### Pre-commit Hooks

```bash
pip install pre-commit
pre-commit install
# Manual run:
pre-commit run --all-files
```

## Commit Conventions

We use [Conventional Commits](https://www.conventionalcommits.org/):

| Prefix | When to use |
|--------|-------------|
| `feat:` | New feature |
| `fix:` | Bug fix |
| `docs:` | Documentation only |
| `chore:` | Build/tooling/deps |
| `test:` | Tests only |
| `refactor:` | Code restructure, no behavior change |

Example: `feat(api): add memory search pagination`

## PR Process

1. Branch from `main`: `git checkout -b feat/short-description`
2. One concern per PR — keep scope focused
3. Link the related issue in the PR description: `Fixes #123`
4. CI must pass: preflight, build and test, lint, the live-Postgres job and the secret scan. A pull request that touches only documents skips the heavy jobs
5. Request review from a maintainer

PRs that mix unrelated concerns will be asked to split.

## Issue Triage Labels

| Label | Meaning |
|-------|---------|
| `bug` | Confirmed defect |
| `feature` | New capability request |
| `docs` | Documentation gap |
| `question` | Needs clarification |

## Reporting bugs

Include a minimal reproducer:
1. `docker compose` snippet or exact image tag
2. The `curl` command (or MCP call) that triggered the issue
3. Relevant log output (`docker compose logs memdb-go`)
4. What you expected vs. what actually happened

## Sign-off

If your contribution was AI-assisted, add a `Co-Authored-By:` footer to the commit message:

```
Co-Authored-By: Claude <noreply@anthropic.com>
```

DCO sign-off is not required.

## Questions?

Open a [GitHub Discussion](https://github.com/anatolykoptev/memdb/discussions).
