# MemDB — архитектурные диаграммы

LikeC4 source. Build → static SPA → `https://m.krolik.run/c4/MemDB/`.

Self-hosted long-term memory DB для AI agents. Pure Go + Rust embedder sidecar. PostgreSQL 17 + pgvector + Apache AGE + Qdrant + Redis.

## Файлы
- `catalog-info.yaml` — Backstage service descriptor
- `memdb.c4` — system + containers + components + deployment + dynamic views
- `reward-loop.md` — отдельная нота про reward loop architecture

## Edit + publish
```bash
$EDITOR memdb.c4
~/bin/c4-publish MemDB
```
