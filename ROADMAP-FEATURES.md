# New Features Roadmap

> Новые фичи, которых нет в Python codebase. Не зависят от миграции.
>
> _Составлен: март 2026._

---

## Recent Milestones

- **2026-04-25 — M7 Compound Lift Sprint**: F1 0.053 → 0.238 (+349%); first MemOS-tier result. answer_style + raw ingest + threshold + embed batching. See `CHANGELOG.md [2.1.0]`.
- **2026-04-24 — v2.0.0**: Full Phase D LoCoMo intelligence stack (10 features). hit@20 = 0.700 (above Mem0/MemOS). See `CHANGELOG.md [2.0.0]`.

---

## 1. Image Memory + Multimodal ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0
**Зависимость:** после Python Deprecation (Фаза 5 в docs/ROADMAP-GO-MIGRATION.md)

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

## 3. Playground Chat ❌ НЕ НАЧАТО

**Суть:** Enhanced two-stage search + references для playground UI.
Сейчас проксируется в Python (`/product/chat/stream/playground`).

**Effort:** S (порт из Python, один endpoint)

---

## 4. Suggestions ❌ НЕ НАЧАТО

**Суть:** Генерация предложений на основе контекста разговора.
Сейчас проксируется в Python (`/product/suggestions`).

**Effort:** S (порт из Python)

---

## 5. RawFileMemory + evolve_to Provenance ❌ НЕ НАЧАТО

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

## 6. Memory Recovery Endpoint ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7 (`RecoverMemoryByRecordIdRequest`)

**Суть:** Explicit endpoint для восстановления soft-deleted memories.

**Зависимость:** Soft-delete (ROADMAP-ADD-PIPELINE.md п.1)
**Effort:** S (status flip + API endpoint)

---

## 7. On-demand Reorganize by IDs ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7 (`MEM_ORGANIZE_TASK_LABEL`)

**Суть:** Триггер reorganizer на конкретном наборе memory IDs (а не всех).

**Effort:** S (новый scheduler task routing к существующему reorganizer)

---

## 8. Memory Lifecycle (5 states) ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7

**Суть:** Полный lifecycle: Generated → Activated → Merged → Archived → Frozen.
Сейчас в Go только 2-3 статуса (activated, merged, expired).

**Зависимость:** Soft-delete (ROADMAP-ADD-PIPELINE.md п.1)
**Effort:** M (DB migration + status machine)

---

## 9. Memory Versioning ❌ НЕ НАЧАТО

**Источник:** MemOS v2.0.7 (`ArchivedTextualMemory`, `version` field)

**Суть:** При UPDATE сохранять предыдущую версию. История изменений memory.

**Effort:** M (new table + version tracking)
**Зависимость:** нет

---

## Что НЕ делаем

- ❌ **Параметрическая память (LoRA)** — требует GPU и fine-tuning инфра, ROI низкий
- ❌ **Активационная память (KV-cache)** — требует специфичного LLM deployment
- ❌ **Миграция на Neo4j** — AGE лучше интегрирован с Postgres ecosystem
- ❌ **Milvus** — Qdrant лучше для self-hosted
