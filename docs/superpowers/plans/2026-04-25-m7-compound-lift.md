# M7 — Compound Lift Sprint (prompt × ingest × window × full-system validation)

> **Цель дня:** доказать что compound'ный эффект трёх ортогональных фиксов (prompt, ingest granularity, sliding window) выводит MemDB LoCoMo aggregate F1 с 0.053 на 0.15+ (MemOS tier), не ломая vaelor / go-nerv / oxpulse-chat / piter.now, и делает измерение воспроизводимым.
>
> **Controller (ты):** координатор, dispatcher, merger. Ты не пишешь код — ты направляешь субагентов. Код пишут специалисты.
>
> **Команда:** 6 senior субагентов параллельно. Все из профильных направлений. Полный доступ к полной системе (Postgres, Redis, embed-server, cliproxyapi, все клиенты). Время на тесты не ограничено.
>
> **Основа:** M6 ablation от 2026-04-24 доказала что default chatbot prompt — #1 bottleneck (+51% F1 от 10-строчного override). Этот план compound'ит это с двумя другими ортогональными правками и валидирует на полном стеке.

---

## 0. Состав команды

| Агент | Роль | Модель | Границы компетенции |
|-------|------|--------|---------------------|
| **TL** | Controller | Opus 4.7 | Вся координация. Пишет ТОЛЬКО planning docs и merge-commits. |
| **BE-1** | Go Backend (chat/prompts) | Opus | `memdb-go/internal/handlers/chat*.go`, `internal/search/answer_*` |
| **BE-2** | Go Backend (ingest pipeline) | Sonnet | `memdb-go/internal/handlers/add*.go`, `internal/handlers/add_windowing.go` |
| **BE-3** | Product Design + Go Backend (windowing) | Opus | `add_windowing.go`, client-side configurability |
| **QA** | Integration + Regression Engineer | Sonnet | `memdb-go/internal/**/*_test.go`, livepg tests, client smoke |
| **ML-Eval** | LoCoMo Harness + scoring | Sonnet | `evaluation/locomo/**`, ingest/query/score, measurement reproducibility |
| **Perf** | Performance/Latency Engineer | Sonnet | Pprof, metrics, p50/p95 latencies, OTel dashboards |
| **Reviewer** | Spec + Code Quality | Opus | Two-stage review per `superpowers:subagent-driven-development` |

> TL — единственный с правом мерджа в `main`. Субагенты останавливаются на `gh pr create`.

---

## 1. Высокоуровневая стратегия

```
                              ┌─────────────────────┐
                              │  TL starts session  │
                              │  reads this plan    │
                              └──────────┬──────────┘
                                         │
                ┌────────────────────────┼────────────────────────┐
                │                        │                        │
                ▼                        ▼                        ▼
        ┌──────────────┐        ┌──────────────┐         ┌──────────────┐
        │  Stream A    │        │  Stream B    │         │  Stream C    │
        │  (BE-1)      │        │  (BE-2)      │         │  (BE-3)      │
        │              │        │              │         │              │
        │ answer_style │        │  ingest mode │         │ window config│
        │  = factual   │        │   = raw      │         │ / redesign   │
        └───────┬──────┘        └───────┬──────┘         └───────┬──────┘
                │                       │                        │
                │    [two-stage reviews, merge to main]          │
                └───────────────────────┼────────────────────────┘
                                        ▼
                              ┌──────────────────┐
                              │  Stream D (QA)   │
                              │  regression      │
                              │  suite re-run    │
                              └─────────┬────────┘
                                        ▼
                              ┌──────────────────┐
                              │ Stream E (ML-E)  │
                              │ LoCoMo 50 → 250  │
                              │ → 2000 QA        │
                              └─────────┬────────┘
                                        ▼
                              ┌──────────────────┐
                              │ Stream F (Perf)  │
                              │ p50/p95 + prod   │
                              │ memos cube probe │
                              └─────────┬────────┘
                                        ▼
                              ┌──────────────────┐
                              │ Final merge +    │
                              │ MILESTONES entry │
                              └──────────────────┘
```

Streams A, B, C — строго **параллельны** (disjoint file sets, разные ветки). Streams D, E, F — после merge A+B+C.

**Disjoint file sets (проверено на пересечения):**
- Stream A: `chat_prompt*.go`, `chat.go`, `handlers/types.go` (добавление `AnswerStyle`)
- Stream B: `evaluation/locomo/ingest.py` + `query.py` (Python harness, не Go)
- Stream C: `add_windowing.go`, `add.go` (константы), `types.go` (добавление `WindowChars` param) — **пересекается с A по types.go**, поэтому A → C последовательны

Финал: A в merge → C dispatched. B идёт полностью параллельно (Python harness).

---

## 2. Stream A — Port QA prompt в Go (BE-1 + Reviewer)

### Цель
Перенести `exp/locomo-qa-prompt` Fix 1 из Python harness на сервер. Клиенты (включая future vaelor, go-nerv) получают factual-extraction prompt через один bool/enum param, не зная про LoCoMo.

### Технические решения

- API: новое поле `answer_style *string` в `fullAddRequest` / chat complete request body. Допустимые значения: `""` (default = существующий `cloudChatPromptEN`), `"factual"` (новый QA-режим), `"conversational"` (явное default, для будущего expansion).
- Валидация: unknown value → 400 с понятным сообщением "unknown answer_style 'X', valid: factual, conversational".
- Default behavior: `answer_style == ""` = текущий `cloudChatPromptEN` (zero regression для existing clients).
- Шаблоны:
  - `factualQAPromptEN` — порт `QA_SYSTEM_PROMPT` из `exp/locomo-qa-prompt` verbatim (та версия что дала +51%)
  - `factualQAPromptZH` — перевод на китайский параллельно (соблюдает текущий EN/ZH pattern)
- Роутинг в `buildSystemPrompt`:
  ```
  if basePrompt != "" → use as-is (существующее override поведение)
  else if answer_style == "factual" → factualQAPrompt<EN|ZH>(lang)
  else → cloudChatPrompt<EN|ZH>(lang)
  ```
- Metric: `memdb.chat.prompt_template_used_total{template="factual|conversational|custom"}` — так ops видят adoption.

### Тесты (обязательны перед PR)

1. **Unit** `chat_prompt_test.go`:
   - default empty → `cloudChatPromptEN`
   - `answer_style="factual"` → `factualQAPromptEN`
   - `answer_style="factual"` + ZH query → `factualQAPromptZH`
   - `answer_style="factual"` + non-empty `system_prompt` → custom wins (backward compat)
   - invalid value → validation error
2. **Integration (livepg)** `chat_prompt_factual_livepg_test.go`:
   - Real Postgres, mock LLM server recording request body, verify system message contains "SHORTEST factual phrase"
3. **Regression:**
   - Existing chat endpoint tests must still pass (zero-change for `answer_style == ""`)

### Definition of Done

- [ ] PR merged в main
- [ ] `memdb-go/internal/handlers/chat_prompt_tpl.go` содержит `factualQAPromptEN` + `factualQAPromptZH`
- [ ] Unit + livepg tests green
- [ ] Metric эмитируется, в Prometheus через `/metrics`
- [ ] `docs/memdb-go/API.md` (если есть) обновлён с новым param; если нет — inline godoc в `types.go`
- [ ] Вся диффа < 200 строк per source file (hard rule)

### Timeline

1-2h work. Blocking для Stream E (harness должна использовать новый param).

### Branch
`feat/answer-style-factual`

---

## 3. Stream B — LoCoMo ingest → `mode=raw` (BE-2 + ML-Eval + Reviewer)

### Цель
Переключить harness-ingest на per-message granularity. Ожидаемо hit@k с 0.000 подскочит до 0.3-0.5 за счёт матчинга question embeddings к atomic facts, а не к 4096-char windows.

### Технические решения

- `evaluation/locomo/ingest.py`:
  - `"mode": "fast"` → `"mode": "raw"` в payload
  - Добавить комментарий-обоснование (почему raw для QA benchmark)
  - Опционально: env `LOCOMO_INGEST_MODE` с default `raw`, позволяет prod-клонам experiment'ить без patch
- Очистка: перед перезаливкой смоутсти старые conv-* cubes (мы их сохранили или tripped trap'ом). Script `cleanup_locomo_cubes.py` идемпотентный, удаляет Memory vertices + edges + tree_consolidation_log + cube row.

### Тесты

1. **Dry-run ingest** на sample (1 conv): проверить что в базе появилось ~50+ memories per cube (раньше было 3), все с `memory_type=LongTermMemory` и `hierarchy_level=raw`.
2. **Retrieval probe** после ingest: curl `/product/search` с пятью вопросами из gold; ожидать ≥1 match в top-20 на каждый.
3. **Dedup safety check:** убедиться что `content_hash` dedup корректно отфильтровывает duplicates (если одна фраза повторяется в разных messages, остается один memory).

### Definition of Done

- [ ] PR `docs/locomo-ingest-mode-raw` merged
- [ ] `evaluation/locomo/scripts/cleanup_locomo_cubes.py` — идемпотентный cleanup
- [ ] Проверено: после заливки conv-26 → Postgres имеет 100+ raw memories per cube
- [ ] Search probe возвращает non-empty text_mem на LoCoMo-style questions
- [ ] Baseline MILESTONES.md entry с новыми числами **ДО** prompt fix — чтобы изолировать эффект ingest-only

### Timeline

30m code + 1-2h measurement. Полностью параллелен Stream A.

### Branch
`chore/locomo-ingest-raw-mode`

---

## 4. Stream C — Sliding window как product decision (BE-3)

### Цель
Решить архитектурный вопрос: должен ли 4096-char window быть hardcoded? Если нет — как сделать configurable так чтобы не сломать существующих клиентов (vaelor, go-nerv, oxpulse-chat).

### Исследование и обоснование (первый час)

BE-3 пишет `docs/design/2026-04-25-sliding-window-decision.md` с анализом:

1. **Какие клиенты сейчас зависят от 4096?**
   - `grep` по всем сервисам krolik-server, найти `MEMDB_URL` и шаблоны вызовов `/product/add`
   - Для каждого: какие размеры chat sessions они присылают? 4096 чуs dominate или прорежает?
2. **Что происходит на edge cases?**
   - Session < windowChars: сейчас 1 memory. С window=512 было бы N memories. Ухудшается ли retrieval для uniform-topic sessions (vaelor Telegram chat)? Мерить.
   - Session > windowChars (LoCoMo 58 msgs = ~5000 chars): сейчас 1-2 windows. С window=512 было бы 10-15. Это лучше для QA, хуже для chatbot summarisation.
3. **Предложение на выбор**:
   - **Option A (conservative)**: сделать `window_chars` param в `/product/add` request, default остаётся 4096. Null effect на existing clients.
   - **Option B (default-shift)**: снизить default до 2048, сделать configurable. Клиенты с явной потребностью в больших окнах (чатботы с long-context summarisation) добавляют `"window_chars": 4096`. Break risk: vaelor возможно полагается на текущее поведение.
   - **Option C (mode-pair)**: новый mode `"fast-qa"` с windowChars=512, `"fast"` остаётся 4096. Добавочная API surface, но zero-risk для existing.

### Решение

TL (controller) читает дизайн-док и выбирает A/B/C. Для первого захода **рекомендую Option A** (минимальный риск, max возможность A/B мерить).

### Реализация (после решения)

- Option A: добавить `WindowChars *int` в `fullAddRequest`, валидация [128, 16384], default 4096 (константа сохраняется). Пробросить в `extractFastMemories`.
- Option C: новый mode literal + switch в `addMode()`.
- Option B: изменить константу + задокументировать в CHANGELOG + alert существующих клиентов.

### Тесты

1. **Unit**: extractFastMemories с разными windowChars → проверить число output memories
2. **Integration livepg**: ingest ту же session при window=512 vs 4096, убедиться что количество memories на порядок отличается и обе работают без ошибок
3. **Regression**: existing chat tests passing (default unchanged)
4. **Client compat probe**: тестовый add от vaelor-style payload с window нераскомментированным — должен работать identically до/после

### Definition of Done

- [ ] `docs/design/2026-04-25-sliding-window-decision.md` с вариантами и выбором
- [ ] PR implementation мерджен
- [ ] Все existing tests green
- [ ] CHANGELOG.md entry если Option B

### Timeline

1-2h дизайн + 1-2h реализация. Последовательно за Stream A (common file `types.go`).

### Branch
`feat/window-chars-configurable`

---

## 5. Stream D — QA regression suite (после A+B+C merge)

### Цель
Доказать что три слитых фикса не ломают существующие benchmarks и клиентов.

### Test matrix

| Test set | Before merge | After merge | Pass criteria |
|----------|--------------|-------------|----------------|
| `go test ./...` in memdb-go | green | green | 100% pass, coverage не падает |
| `evaluation/locomo` baseline (M4 params) | F1 0.053 | F1 >= 0.15 | **+183% aggregate** |
| `evaluation/personamem` (если есть harness) | baseline | no regression | F1 within ±2% |
| `evaluation/PrefEval` | baseline | no regression | same |
| vaelor smoke: `/product/add` + `/product/search` | works | works | same response shape, no 500s |
| go-nerv smoke | works | works | same |
| oxpulse-chat memory write (customer memory path) | works | works | same |
| memos cube retrieval latency p50 / p95 | measured | measured | p95 не больше baseline на 20% |

### Исполнение

QA субагент запускает все тесты параллельно где возможно. Для каждого failing test открывает отдельный issue / blocks merge. Если кто-то из клиентов зависел от undocumented behavior (например 4096 windows) — откат Stream C на Option A (default unchanged).

### Definition of Done

- [ ] Все ячейки "Pass criteria" зелёные
- [ ] `docs/testing/2026-04-25-m7-regression-report.md` с таблицей и ссылками на artifacts

### Timeline

2-3h (параллельная прогонка всех harness-ов).

---

## 6. Stream E — LoCoMo 50 → 250 → 2000 QA (ML-Eval)

### Цель
Построить measurement staircase: быстрая проверка → medium confidence → statistically-sound full run.

### Stages

**Stage 1 (10-15 min)**: 50 QA sample (5 cat × 10). Gate: aggregate F1 >= 0.12. Если нет — регрессия в A/B/C, revert.

**Stage 2 (1h)**: 250 QA (5 cat × 50). Gate: per-category F1 стабилен ± 20% от Stage 1. Если cat-2 multi-hop всё ещё 0 — диагностика D2 / relation edges, открыть issue для следующей сессии.

**Stage 3 (4-8h, в фоне)**: full 10 convs × 200 QA = ~2000 QA. Gate: aggregate F1 >= 0.15. Если pass — сравнение vs published MemOS/Mem0/Zep, запись в публичный benchmark log.

### Instrumentation

- Each stage пишет `evaluation/locomo/results/m7-stage<N>.json`
- Auto-compare через `compare.py` vs baseline
- При каждом gate — decision point: continue / rollback / escalate

### Definition of Done

- [ ] Stage 1+2 green, commit measurement files
- [ ] Stage 3 запущен в фоне, результаты через `ScheduleWakeup` на послезавтра
- [ ] MILESTONES entry обновлён с всеми тремя stages

---

## 7. Stream F — Perf/Latency + Prod Probe (Perf Engineer)

### Цель
Убедиться что новые фиксы не ломают latency SLO и не шумят в production `memos` cube.

### Тесты

1. **pprof CPU profile** на `/product/add` при window=4096 vs window=512:
   - Ожидаемо: window=512 увеличивает insert count в 8× но уменьшает embedding call count (per-embedding CPU)
   - Net CPU может быть нейтральным или лучше
2. **Add latency distribution** (1000 requests, concurrency=10):
   - p50 / p95 / p99 before / after
   - Critical: p95 не деградирует > 20%
3. **Search latency** (1000 запросов на `memos` cube):
   - с новым `answer_style=factual` prompt — LLM call за отдельный счёт, cache behavior
4. **Prod probe**:
   - Replay 100 реальных запросов с `memos` cube через обе версии (текущая + новая)
   - Diff ответов: если F1-like метрика падает > 5% — escalate

### Deployment drill

- Staged rollout: feature-flag `MEMDB_ANSWER_STYLE_DEFAULT`: пустая string = старое поведение, "factual" = новое
- Сначала на dev-сервере, потом canary 10%, потом full

### Definition of Done

- [ ] `docs/perf/2026-04-25-m7-latency-report.md`
- [ ] p95 deltas < 20%
- [ ] No 500s на prod-probe replay

---

## 8. TL (controller) orchestration

### Утренний warmup (5 мин)

```
git pull origin main
git log --oneline -5
docker ps | grep -E 'memdb|embed|postgres'
curl -sf http://127.0.0.1:8080/health
```

### Dispatch

- Оpen в parallel: Agent A, Agent B, Agent C (3 implementer subagents)
- Для каждого — implementer prompt из `superpowers:subagent-driven-development` pattern: task text + context + branch name + DoD
- Respect "не более 1 implementer на disjoint файлы" — A и B disjoint, C ждёт merge A

### Per-stream review cycle

1. Agent завершает → status DONE
2. TL dispatch spec-reviewer (verifies что PR делает ровно что просили)
3. Если spec PASS → dispatch code-quality-reviewer (style, naming, edge cases)
4. Если quality APPROVED → TL squash-merge в main
5. Повторить для следующего stream

### Escalation triggers

- Спец-ревью отклоняет 2 раза подряд → TL читает PR сам, возможно меняет brief
- Quality review находит concurrency issue → stop streamline, focused debug session
- LoCoMo Stage 1 < 0.10 F1 → откат всего дня, возврат к M6 state, analysis next day

### Commit hygiene (от CLAUDE.md)

- `git status --short` перед каждым commit — dirty = STOP
- `git branch --show-current` перед commit — не main
- Feature branch → push → PR → merge (TL only, NEVER subagent)
- Smoke cubes cleanup trap в каждом integration-like скрипте

---

## 9. Risk register

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|-----------|
| Stream B ingest change ломает dedup | Medium | Medium | Pre-checks в Stream D; cleanup cubes first |
| Window configurability ломает vaelor | Low | High | Option A (default unchanged) как safe default |
| cliproxyapi embedding endpoint 404 (наблюдали в M6) | High | Medium | Fix embedding fallback в score.py ИЛИ добавить `/v1/embeddings` в cliproxyapi |
| LoCoMo Stage 3 (full 2000 QA) timeout | Medium | Low | Run в background, поднять timeout до 12h |
| Parallel BE-1 и BE-3 concurrent edits в types.go | High | Medium | Serialize: A первый, C ждёт merge |
| M5 relation edges накопились noisy в prod | Low | Medium | Otel counter `relation_error` мониторить в Grafana |

---

## 10. Success criteria (all must be green)

- [ ] `feat/answer-style-factual` merged
- [ ] `chore/locomo-ingest-raw-mode` merged
- [ ] `feat/window-chars-configurable` merged (или `docs/design` с обоснованным "решили не трогать")
- [ ] LoCoMo 50 QA: aggregate F1 **≥ 0.12**
- [ ] LoCoMo 250 QA: aggregate F1 **≥ 0.15**, hit@k **≥ 0.3**
- [ ] LoCoMo 2000 QA запущен в фоне, результаты ждём wake-up
- [ ] `go test ./...` green в memdb-go
- [ ] vaelor / go-nerv / oxpulse-chat smoke — без регрессий
- [ ] Perf p95 deltas < 20%
- [ ] MILESTONES updated, все design docs commited
- [ ] Grafana prod memos cube — нет новых ошибок

---

## 11. Контроль качества решений (TL self-checks)

- Когда ли при работе меняется scope (кто-то предлагает "ещё один маленький фикс") — TL отклоняет, записывает в `docs/backlog/2026-04-26-followups.md`. Однодневный sprint не растягиваем.
- Когда agent утверждает "DONE" — TL ВСЕГДА запускает `gh pr diff` и `gh pr checks` самостоятельно перед disp review.
- Когда F1 улучшается за счёт одной категории и теряется на другой (как Fix 1.1) — TL требует per-category decision и повторное A/B вместо merge.
- Когда обнаруживается неожиданная регрессия — TL не merge'ит compound сразу, откатывает компонент, диагностирует изолировано.

---

## 12. Если всё пошло плохо

Rollback sequence:

```
# Main → state as of 2026-04-24 end
git log --oneline | grep 'M5 follow-ups' -A 1    # find last known-good SHA
git revert <m7-stream-a-merge-sha>
git revert <m7-stream-b-merge-sha>
git revert <m7-stream-c-merge-sha>
git push origin main
# Dozor auto-redeploys; monitor for 10 min; re-run LoCoMo baseline
```

Если прод сломался:
```
docker compose stop memdb-go
git checkout <previous-prod-sha>
docker compose up -d --force-recreate memdb-go
# Post-mortem next day
```

---

## 13. Что НЕ делаем в M7

- Модель swap (pro/3.x) — отдельная сессия после compound confirmed
- Embedder swap (BGE-M3/Voyage) — нет bottleneck, отдельная сессия
- CE rerank — план 2026-04-20 всё ещё актуален, отдельная сессия после compound measurement
- D3 relation phase tuning — natural accumulation, мерить через неделю
- Full system migration py→go (Phase 5) — продолжается по своему темпу

---

**Финальная нота для TL:** день построен на compound hypothesis. Если prompt + ingest вместе не дают 3× lift — гипотеза неверна, возвращаемся к моделированию bottleneck ranking. Но на основании M6 evidence вероятность compound >= 70%. Сегодня или измерение это подтверждает, или мы узнаём новое важное о системе.
