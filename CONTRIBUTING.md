# Contributing to MemDB

Thank you for your interest in contributing! This guide covers everything you need to get started.

## Development Setup

### Prerequisites

- Go 1.26+
- Docker + Docker Compose
- PostgreSQL 17 (via Docker is fine)
- Python 3.10+ (for the Python layer)

### Clone and Build

```bash
git clone https://github.com/anatolykoptev/memdb.git
cd memdb

# Go service
cd memdb-go
go mod download
make build

# Python layer
pip install -r docker/requirements.txt
```

### Running the Stack Locally

```bash
cp .env.example .env
# Edit .env with your credentials
cd docker && docker compose up
```

## Running Tests

```bash
# Go service
cd memdb-go
make test
# or: go test ./...

# Python layer
pytest tests/
```

## Code Style

### Go

- Format: `gofmt -w .`
- Lint: `make lint` (runs golangci-lint)
- Files target ≤200 lines; hard limit 300 lines
- No `panic`/`unwrap` in request handlers

### Python

- Formatter: `ruff format .`
- Linter: `ruff check .`
- Run both before committing: `ruff check . && ruff format --check .`

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
4. CI must pass (16-matrix builds: 4 OS × 4 Python versions)
5. Request review from a maintainer

PRs that mix unrelated concerns will be asked to split.

## Issue Triage Labels

| Label | Meaning |
|-------|---------|
| `bug` | Confirmed defect |
| `feature` | New capability request |
| `docs` | Documentation gap |
| `question` | Needs clarification |

## Questions?

Open a [GitHub Discussion](https://github.com/anatolykoptev/memdb/discussions) or join Discord (link in README).
