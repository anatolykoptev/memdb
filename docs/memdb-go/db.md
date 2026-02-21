# DB — Слой базы данных

Пакет `internal/db/` предоставляет клиенты для работы с PolarDB (PostgreSQL + Apache AGE + pgvector), Qdrant и Redis.

---

## Postgres (PolarDB + pgvector + AGE)

**Файл:** `postgres.go`

```go
type Postgres struct {
    pool   *pgxpool.Pool
    logger *slog.Logger
}
```

### Инициализация

```go
pg, err := db.NewPostgres(ctx, connStr, logger)
```

**Параметры пула:**
- `MaxConns = 8`
- `MinConns = 2`
- `MaxConnLifetime = 30 мин` (с 5-мин jitter для сглаживания переподключений)
- `MaxConnIdleTime = 5 мин`

**На каждое новое соединение (`AfterConnect`):**
```sql
LOAD 'age';
SET search_path = ag_catalog, memos_graph, public;
SET hnsw.iterative_scan = relaxed_order;
SET hnsw.ef_search = 100;
```

`hnsw.iterative_scan = relaxed_order` — критически важно для pgvector 0.8.x с WHERE-фильтрами: HNSW продолжает сканирование за пределами ef_search, пока не найдёт достаточно кандидатов после фильтрации.

**При старте создаёт (идемпотентно):**
- `memory_edges` — рёбра между memory-узлами (graph traversal, bi-temporal: `valid_at` + `invalid_at`)
- `entity_nodes` — именованные сущности с embedding для identity resolution (HNSW cosine index)
- `entity_edges` — entity-to-entity triplets: (subject, predicate, object), bi-temporal
- `user_configs` — конфиги пользователей

---

### Методы поиска

#### VectorSearch

```go
func (p *Postgres) VectorSearch(
    ctx context.Context,
    embedding []float32,
    userName string,
    memoryTypes []string,
    agentID string,
    topK int,
) ([]VectorSearchResult, error)
```

pgvector HNSW ANN поиск по косинусному сходству. Фильтрация по `user_name` и `memory_type`. Возвращает `ID`, `Properties` (JSON), `Score`, `Embedding`.

#### VectorSearchWithCutoff

То же + фильтр `created_at >= cutoff` для темпоральных запросов.

#### FulltextSearch / FulltextSearchWithCutoff

```go
func (p *Postgres) FulltextSearch(
    ctx context.Context,
    tsquery string,
    userName string,
    memoryTypes []string,
    agentID string,
    topK int,
) ([]VectorSearchResult, error)
```

PostgreSQL `to_tsvector` + `tsquery` полнотекстовый поиск. Возвращает те же поля что и VectorSearch.

#### GetWorkingMemory

```go
func (p *Postgres) GetWorkingMemory(
    ctx context.Context,
    userName string,
    limit int,
    agentID string,
) ([]VectorSearchResult, error)
```

Последние `limit` WorkingMemory узлов, отсортированных по `created_at DESC`. Возвращает с embedding для cosine rerank.

---

### Методы graph recall

#### GraphRecallByKey

```go
func (p *Postgres) GraphRecallByKey(
    ctx context.Context,
    userName string,
    memoryTypes []string,
    tokens []string,
    agentID string,
    limit int,
) ([]GraphRecallResult, error)
```

Поиск узлов, у которых поле `key` свойств совпадает с одним из токенов запроса. Score = `GraphKeyScore (0.85)`.

#### GraphRecallByTags

```go
func (p *Postgres) GraphRecallByTags(
    ctx context.Context,
    userName string,
    memoryTypes []string,
    tokens []string,
    agentID string,
    limit int,
) ([]GraphRecallResult, error)
```

Поиск узлов по пересечению тегов. Score = `0.70 + 0.05 * overlapping_tags`.

#### GraphBFSTraversal

```go
func (p *Postgres) GraphBFSTraversal(
    ctx context.Context,
    seedIDs []string,
    userName string,
    memoryTypes []string,
    depth int,
    limit int,
    agentID string,
) ([]GraphRecallResult, error)
```

BFS multi-hop traversal через `memory_edges` таблицу. Начиная с `seedIDs` (top-5 vector hits), обходит граф на `depth` уровней. Находит связанные воспоминания, не захваченные семантическим поиском.

#### GraphRecallByEdge

```go
func (p *Postgres) GraphRecallByEdge(
    ctx context.Context,
    seedIDs []string,
    relation string,
    userName string,
    limit int,
) ([]GraphRecallResult, error)
```

Возвращает memory-узлы, достижимые из `seedIDs` через рёбра заданного типа. Используется для:
- `CONTRADICTS` → PenalizeContradicts в поиске
- `EXTRACTED_FROM` → связи WM → LTM
- `MERGED_INTO` → аудит слияний

**Bi-temporal фильтр:** `WHERE invalid_at IS NULL` — рёбра с выставленным `invalid_at` не участвуют в recall.

#### InvalidateEdgesByMemoryID

```go
func (p *Postgres) InvalidateEdgesByMemoryID(ctx context.Context, memoryID, invalidAt string) error
```

Выставляет `invalid_at` на всех `memory_edges` где `from_id = memoryID`. Вызывается при DELETE/UPDATE факта.

#### InvalidateEntityEdgesByMemoryID

```go
func (p *Postgres) InvalidateEntityEdgesByMemoryID(ctx context.Context, memoryID, invalidAt string) error
```

Выставляет `invalid_at` на всех `entity_edges` где `memory_id = memoryID`.

---

### Entity Graph (Knowledge Graph)

Entity Graph — это полноценный knowledge graph, реализованный поверх обычных PostgreSQL-таблиц (без Apache AGE). Состоит из трёх компонентов.

#### Схема таблиц

```
entity_nodes
┌─────────────┬───────────┬──────┬─────────────┬────────────┬────────────┬────────────────┐
│ id (PK)     │ user_name │ name │ entity_type │ created_at │ updated_at │ embedding      │
│ TEXT        │ TEXT      │ TEXT │ TEXT        │ TEXT       │ TEXT       │ halfvec(1024)  │
└─────────────┴───────────┴──────┴─────────────┴────────────┴────────────┴────────────────┘
Indexes: user_idx, name_idx, HNSW cosine index на embedding

memory_edges
┌──────────┬───────┬──────────┬────────────┬──────────┬────────────┐
│ from_id  │ to_id │ relation │ created_at │ valid_at │ invalid_at │
│ TEXT     │ TEXT  │ TEXT     │ TEXT       │ TEXT     │ TEXT       │
└──────────┴───────┴──────────┴────────────┴──────────┴────────────┘
Relations: MENTIONS_ENTITY | MERGED_INTO | EXTRACTED_FROM | CONTRADICTS | RELATED
PK: (from_id, to_id, relation)

entity_edges
┌────────────────┬───────────┬──────────────┬───────────┬───────────┬──────────┬────────────┬────────────┐
│ from_entity_id │ predicate │ to_entity_id │ memory_id │ user_name │ valid_at │ invalid_at │ created_at │
│ TEXT           │ TEXT      │ TEXT         │ TEXT      │ TEXT      │ TEXT     │ TEXT       │ TEXT       │
└────────────────┴───────────┴──────────────┴───────────┴───────────┴──────────┴────────────┴────────────┘
Predicates: WORKS_AT | LIVES_IN | KNOWS | PART_OF | CREATED_BY | OWNS | LOCATED_IN | MEMBER_OF
PK: (from_entity_id, predicate, to_entity_id, user_name)
```

#### Bi-temporal модель (по образцу Graphiti)

Каждое ребро хранит два временны́х измерения:

| Поле | Значение | NULL означает |
|---|---|---|
| `valid_at` | Когда факт стал истинным (из LLM `ExtractedFact.ValidAt`) | Время вставки |
| `invalid_at` | Когда факт перестал быть истинным | Ребро активно сейчас |

**Жизненный цикл ребра:**
1. **ADD**: ребро создаётся, `invalid_at = NULL` (активно)
2. **UPDATE**: `invalid_at` выставляется на старых рёбрах → создаются новые рёбра с `linkEntitiesAsync`
3. **DELETE / CONTRADICTS**: `invalid_at` выставляется на всех рёбрах от удаляемой памяти → hard-delete memory node

**Recall фильтрация:** `GetMemoriesByEntityIDs` и `GraphRecallByEdge` содержат `AND e.invalid_at IS NULL` — исторические записи сохраняются для аудита, но не участвуют в поиске.

#### Методы entity_nodes

```go
// Вставка/обновление сущности по нормализованному ID
func (p *Postgres) UpsertEntityNode(ctx, name, entityType, userName, now string) (string, error)

// То же + embedding-based identity resolution (cosine similarity >= 0.92)
// Если найдена похожая сущность — возвращает её ID (merge)
func (p *Postgres) UpsertEntityNodeWithEmbedding(ctx, name, entityType, userName, now, embVec string) (string, error)

// Lookup по нормализованным именам для entity graph recall в поиске
func (p *Postgres) FindEntitiesByNormalizedID(ctx, normalizedIDs []string, userName string) ([]string, error)

// Memories упомянувшие любую из entity_ids через MENTIONS_ENTITY (active edges only)
func (p *Postgres) GetMemoriesByEntityIDs(ctx, entityIDs []string, userName string, limit int) ([]GraphRecallResult, error)
```

#### NormalizeEntityID

```go
func NormalizeEntityID(name string) string  // lowercase + trim
```

Primary key для `entity_nodes`. `"Yandex"` → `"yandex"`. Цель: одно хранилище для entity независимо от регистра. Aliases (`"Яндекс"` vs `"Yandex"`) разрешаются через `UpsertEntityNodeWithEmbedding` (cosine similarity threshold 0.92).

#### UpsertEntityEdge

```go
func (p *Postgres) UpsertEntityEdge(ctx, fromEntityID, predicate, toEntityID, memoryID, userName, validAt, createdAt string) error
```

Вставляет directed triplet (subject, predicate, object) из LLM-извлечённых `EntityRelation`. Idempotent через `ON CONFLICT DO NOTHING`.

#### Pipeline entity linking (async)

При каждом `ADD`/`UPDATE` в fine-mode:

```
ExtractedFact.Entities + Relations
         │
         ▼
linkEntitiesAsync (goroutine)
         │
         ├─ Batch embed entity names (1 ONNX forward pass)
         │
         ├─ for each entity:
         │    UpsertEntityNodeWithEmbedding → entity_id
         │    CreateMemoryEdge(ltmID → entity_id, MENTIONS_ENTITY, valid_at)
         │
         └─ for each relation:
              UpsertEntityEdge(from, predicate, to, memory_id, valid_at)
```

---

### Методы вставки

#### InsertMemoryNodes

```go
func (p *Postgres) InsertMemoryNodes(ctx context.Context, nodes []MemoryInsertNode) error
```

Batch insert через Apache AGE (`CREATE (:MemNode {props})` + pgvector embedding). Одна транзакция на весь батч.

```go
type MemoryInsertNode struct {
    ID             string
    PropertiesJSON []byte
    EmbeddingVec   string  // pgvector формат: "[0.1,0.2,...]"
}
```

#### FilterExistingContentHashes

```go
func (p *Postgres) FilterExistingContentHashes(
    ctx context.Context,
    hashes []string,
    cubeID string,
) (map[string]bool, error)
```

Один запрос к Postgres: какие из переданных SHA-256 хэшей уже существуют в кубе. Возвращает `map[hash]bool` для O(1) lookup. Используется для exact-duplicate dedup перед embed.

---

### Методы обновления

#### UpdateMemoryNodeFull

```go
func (p *Postgres) UpdateMemoryNodeFull(
    ctx context.Context,
    propertyID string,
    memory string,
    embeddingVec string,
    updatedAt string,
) error
```

Полное обновление: новый текст + новый вектор + `updated_at`. Используется при `action=update` в fine-mode.

#### UpdateMemoryContent

```go
func (p *Postgres) UpdateMemoryContent(
    ctx context.Context,
    propertyID string,
    memory string,
) error
```

Обновление только текста (без re-embed). Используется из MCP `update_memory`.

#### IncrRetrievalCount

```go
func (p *Postgres) IncrRetrievalCount(
    ctx context.Context,
    ids []string,
    now string,
) error
```

Batch-инкремент `retrieval_count + 1` и `importance_score + 0.1` для всех переданных IDs. Вызывается async после каждого поиска.

#### DecayAndArchiveImportance

```go
func (p *Postgres) DecayAndArchiveImportance(
    ctx context.Context,
    cubeID string,
    decayFactor float64,
    archiveThreshold float64,
    now string,
) (int64, error)
```

Один запрос:
1. `importance_score *= decayFactor` для всех LTM/UserMemory куба
2. Архивирует (soft-delete) узлы с `importance_score < archiveThreshold`

---

### Методы удаления

#### DeleteByPropertyIDs

```go
func (p *Postgres) DeleteByPropertyIDs(
    ctx context.Context,
    ids []string,
    userName string,
) (int64, error)
```

Hard delete по property UUIDs (не AGE graph IDs). Безопасно: фильтр по `user_name`.

#### DeleteAllByUser

```go
func (p *Postgres) DeleteAllByUser(ctx context.Context, userName string) (int64, error)
```

Удаляет все узлы пользователя. Нужно подтверждение в MCP.

#### CleanupWorkingMemory / CleanupWorkingMemoryWithIDs

Удаляет старые WorkingMemory узлы, оставляя только `limit` новейших. `WithIDs` версия возвращает удалённые IDs для evict из VSET.

---

### Пользовательские методы

| Метод | Описание |
|---|---|
| `ListUsers(ctx)` | Список уникальных user_name |
| `ExistUser(ctx, userID)` | Проверка существования |
| `CountDistinctUsers(ctx)` | Кол-во уникальных пользователей |
| `GetUserConfig(ctx, userID)` | JSON конфиг из `user_configs` |
| `UpdateUserConfig(ctx, userID, cfg)` | Upsert конфига |
| `GetUserNamesByMemoryIDs(ctx, ids)` | Маппинг memory_id → user_name |
| `GetMemoryByPropertyID(ctx, id)` | Один узел по property UUID |
| `GetMemoriesByPropertyIDs(ctx, ids)` | Батч по property UUIDs |
| `GetAllMemories(ctx, cube, type, page, size)` | Все воспоминания типа с пагинацией |
| `GetAllMemoriesByTypes(ctx, cube, types, page, size)` | Несколько типов |
| `FindNearDuplicates(ctx, cubeID, threshold, limit)` | Пары узлов с cosine > threshold |

---

## Qdrant

**Файл:** `qdrant.go`

```go
type Qdrant struct {
    client *qdrant.Client
    logger *slog.Logger
}
```

Используется для хранения preference memories (explicit_preference, implicit_preference collections).

### Методы

#### SearchByVector

```go
func (q *Qdrant) SearchByVector(
    ctx context.Context,
    collection string,
    vector []float32,
    limit uint64,
    userFilter string,
) ([]QdrantSearchResult, error)
```

ANN поиск в Qdrant коллекции с фильтром по `user_id`.

#### Scroll

```go
func (q *Qdrant) Scroll(
    ctx context.Context,
    collection string,
    userFilter string,
    limit uint64,
) ([]QdrantSearchResult, error)
```

Постраничное получение всех preference points для пользователя (для `get_memory` endpoint).

---

## Redis

**Файл:** `redis.go`

```go
type Redis struct {
    client *redis.Client
}
```

Используется для:
- VSET (WorkingMemory hot cache)
- Scheduler streams consumer
- Profiler cache (`profile:{cube_id}`)
- DB-level response cache (`memdb:db:*`)

### Методы

```go
func (r *Redis) Client() *redis.Client   // прямой доступ к клиенту
func (r *Redis) Get(ctx, key) (string, error)
func (r *Redis) Set(ctx, key, value, ttl) error
func (r *Redis) Ping(ctx) error
func (r *Redis) Close() error
```

---

## WorkingMemoryCache (VSET)

**Файл:** `vset.go`

Redis HNSW in-memory VSET для WorkingMemory. Требует Redis 8+ (Redis Stack) с векторными командами `VSET.*`.

```go
type WorkingMemoryCache struct {
    redis *Redis
}
```

**Ключи:** `wm:v:{cubeID}` — отдельный VSET на каждый куб.

### Методы

#### VAdd

```go
func (c *WorkingMemoryCache) VAdd(
    ctx context.Context,
    cubeID string,
    id string,
    memory string,
    embedding []float32,
    ts int64,
) error
```

Добавляет WM узел в VSET с CAS (Compare-And-Swap): добавляет только если не существует или timestamp новее. Атрибуты: `id`, `memory`, `ts`.

#### VSim

```go
func (c *WorkingMemoryCache) VSim(
    ctx context.Context,
    cubeID string,
    embedding []float32,
    topK int,
) ([]VSimResult, error)
```

HNSW ANN поиск в VSET. Возвращает `[]VSimResult{ID, Memory, Score}`. Latency ~1–5ms (in-memory).

#### VRem / VRemBatch

```go
func (c *WorkingMemoryCache) VRem(ctx, cubeID, id string) error
func (c *WorkingMemoryCache) VRemBatch(ctx, cubeID string, ids []string) error
```

Evict из VSET. `VRemBatch` использует pipeline для минимального latency.

#### Sync

```go
func (c *WorkingMemoryCache) Sync(ctx, pg *Postgres, logger *slog.Logger)
```

Warm-up при старте: загружает все WorkingMemory из Postgres → записывает в VSET. Запускается в background goroutine, нефатальный.

### Зачем VSET?

При fine-mode add нужен быстрый поиск WM-кандидатов для dedup-контекста LLM:
- Postgres pgvector: ~20–100ms
- Redis VSET HNSW: ~1–5ms

Двухуровневая стратегия: сначала VSET (WorkingMemory), потом pgvector (LTM + UserMemory).

---

## FormatVector

```go
func FormatVector(embedding []float32) string
```

Конвертирует `[]float32` в pgvector-совместимую строку `"[0.1,0.2,...]"` для INSERT запросов.

---

## Queries

**Директория:** `internal/db/queries/`

Содержит константы имён граф-нод и SQL-запросы. `DefaultGraphName = "memos_graph"` — имя Apache AGE графа (фиксированное для всего приложения).

---

## Сравнение с конкурентами: Database Layer

### mem0 — хранилища векторов

| Аспект | mem0 | memdb-go |
|---|---|---|
| Vector backends | Qdrant, Chroma, Pinecone, FAISS, Weaviate, PGVector, 10+ | **PolarDB (pgvector) + Qdrant** |
| Graph backends | Neo4j, AWS Neptune | **Apache AGE** (встроен в PostgreSQL) |
| SQL storage | Нет отдельного SQL | **PolarDB (PostgreSQL-compatible)** |
| Pluggable interface | ✅ VectorStore ABC | ❌ Жёстко привязаны к PolarDB+Qdrant |
| Managed cloud storage | AWS ElastiCache + Neptune | ❌ Self-hosted only |

**Где mem0 сильнее:** 10+ vector store backends — это критично для enterprise, где уже есть Pinecone/Weaviate. Наш `Postgres.VectorSearch` нельзя переключить на другой бэкенд без переписывания.

**Цель Go-миграции:** Ввести интерфейс `VectorStore`:
```go
type VectorStore interface {
    VectorSearch(ctx, vec, user, types, agentID string, topK int) ([]VectorSearchResult, error)
    InsertMemoryNodes(ctx, nodes []MemoryInsertNode) error
}
```
Первая реализация — текущий PolarDB. Вторая — Qdrant как основное хранилище (убрав зависимость от AGE).

---

### Graphiti/Zep — граф БД

| Аспект | Graphiti | memdb-go |
|---|---|---|
| Graph DB | Neo4j | **Apache AGE (внутри PostgreSQL)** |
| Entity nodes | Явные (PERSON, PLACE, ORG) | ❌ Нет entity extraction |
| Temporal edges | ✅ valid_from / valid_to на рёбрах | ⚠️ Только flat memory_edges |
| Community detection | ✅ Graph algorithms (PageRank, Community) | ❌ Нет |
| Query language | Cypher | **SQL + openCypher (AGE)** |
| Scaling | Dedicated Neo4j cluster | **Единый PostgreSQL** |

**Где Graphiti сильнее:** Neo4j — это mature graph database с Cypher, built-in алгоритмами (PageRank, community detection, shortest path). AGE — это расширение для PostgreSQL, функционал значительно беднее.

**Наше преимущество:** Apache AGE внутри PostgreSQL = **один сервис** вместо PostgreSQL + отдельный Neo4j. Это ключевое операционное преимущество для self-hosted.

**Цель Go-миграции:** Воспользоваться `apoc` процедурами AGE для community detection или реализовать Union-Find + community summaries на уровне Go (уже частично есть в Reorganizer).

---

### Redis Agent Memory Server — Redis VSET

| Аспект | redis/agent-memory-server | memdb-go |
|---|---|---|
| Vector index | Redis Stack HNSW (VSET) | **Redis Stack HNSW (VSET) + pgvector** |
| Working Memory | Redis только для WM | **VSET (WM hot cache) + Postgres (LTM)** |
| Warm-up при старте | ❌ | ✅ `wmCache.Sync()` из Postgres |
| Batch eviction | ❌ | ✅ `VRemBatch` pipeline |
| Two-tier retrieval | ❌ (только Redis) | ✅ VSET → pgvector fallback |

**Наше преимущество:** Двухуровневая стратегия поиска WM-кандидатов (VSET → pgvector) даёт лучший recall при fine-mode dedup — VSET покрывает только WorkingMemory, pgvector добирает LTM.

**Цель Go-миграции:** Добавить `VSet.TTL` — автоэкспирация VSET ключей для неактивных кубов (сейчас при `CleanupWorkingMemory` evict идёт через `VRemBatch`, но куб-ключ живёт вечно).

---

### MemOS — MemCube абстракция

MemOS использует `MemCube` как универсальное хранилище для всех типов памяти (parametric + activation + plaintext). Каждый MemCube — независимая единица с версионированием.

**Где MemOS сильнее:**
- **Версионирование MemCube** — можно откатить память куба к предыдущей версии
- **MemCube migration** — перенос памяти между кубами с конвертацией типов
- **Federated куб** — несколько кубов объединяются в federation с unified search

**Цель Go-миграции:** Реализовать `cube_version` в `user_configs` таблице + snapshotting через `pg_dump` подмножества узлов по `user_name`. Federated search уже частично реализован через `readable_cube_ids`.
