# Features Backlog

> Pending feature work. For shipped features see CHANGELOG.md.
>
> Derived from MemOS v2.0.x competitive analysis (March 2026).
> Not migration work — see `docs/ROADMAP-GO-MIGRATION.md` (closed) for past Python→Go work.
>
> Master roadmap: [ROADMAP.md](../../ROADMAP.md)

---

## 1. Image Memory + Multimodal ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0

**Суть:** Нативная поддержка изображений — caption, CLIP embedding, image+text co-retrieval.

```go
// internal/handlers/image_memory.go
type ImageMemory struct {
    ID        string
    URL       string      // или base64
    Caption   string      // LLM-generated
    Embedding []float32   // CLIP embedding
    Tags      []string
}
// Требует: CLIP модель (ONNX) для image embeddings
// Storage: Qdrant (image vectors) + Postgres (metadata)
```

**Effort:** L (4-6 недель)
**Метрика:** Image + text co-retrieval работает.

---

## 2. MemCube Cross-Sharing ❌ НЕ НАЧАТО

**Источник:** MemOS

**Суть:** Управление правами доступа между кубами. Один куб может читать/писать в другой.

```go
// POST /product/share_cube
// {"owner_cube_id": "alice", "target_cube_id": "bob", "permission": "read"}
// Search: readable_cube_ids = own + shared_with_me
```

**Effort:** M
**Зависимость:** нет

---

## 3. RawFileMemory + evolve_to Provenance ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7

**Суть:** Хранение raw document chunks как отдельных graph nodes с lineage tracking.

```go
// memory_type = "RawFileMemory", capacity = 1500
// is_fast: bool — marks nodes created in fast mode
// evolve_to: []string — IDs of LTM nodes this raw chunk evolved into
// Graph edge: EVOLVED_FROM (LTM → RawFileMemory)
```

**Зачем:**
- Querying: какие LTM memories пришли из какого документа
- Re-extraction: при обновлении документа — selective re-extract
- Deletion: "забудь этот документ" — удалить raw + все evolved LTM

**Effort:** M
**Зависимость:** нет

---

## 4. Memory Recovery Endpoint ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7 (`RecoverMemoryByRecordIdRequest`)

**Суть:** Explicit endpoint для восстановления soft-deleted memories.

**Зависимость:** Soft-delete ([docs/backlog/add-pipeline.md](add-pipeline.md) item 1)
**Effort:** S (status flip + API endpoint)

---

## 5. On-demand Reorganize by IDs ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7 (`MEM_ORGANIZE_TASK_LABEL`)

**Суть:** Триггер reorganizer на конкретном наборе memory IDs (а не всех).

**Effort:** S (новый scheduler task routing к существующему reorganizer)

---

## 6. Memory Lifecycle (5 states) ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7

**Суть:** Полный lifecycle: Generated → Activated → Merged → Archived → Frozen.
Сейчас в Go только 2-3 статуса (activated, merged, expired).

**Зависимость:** Soft-delete ([docs/backlog/add-pipeline.md](add-pipeline.md) item 1)
**Effort:** M (DB migration + status machine)

---

## 7. Memory Versioning ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7 (`ArchivedTextualMemory`, `version` field)

**Суть:** При UPDATE сохранять предыдущую версию. История изменений memory.

**Effort:** M (new table + version tracking)
**Зависимость:** нет
