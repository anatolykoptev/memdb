# Design: Quality-Aware Gate Evolution

**Date:** 2026-02-21
**Status:** Approved
**Goal:** Evolve 4 Phase-7 gates from binary skip/proceed into quality-aware routers that improve memory quality, not just reduce costs.

## Context

Phase 7 shipped 4 cost-reduction gates:
1. **Classifier gate** — skips trivial messages before LLM extraction
2. **Near-duplicate gate** — skips messages with cosine > 0.97 to existing memories
3. **Rerank gate** — skips LLM reranking when results are few or top cosine is high
4. **Episodic gate** — skips episodic summary when no facts extracted or mostly code

These work but are blunt: skip or proceed, no middle ground. This design evolves each gate to produce quality signals that feed downstream stages.

## Competitive Research

Patterns extracted from mem0, LangMem, Graphiti, and Letta:

| Pattern | Source | Applicable |
|---------|--------|-----------|
| LLM few-shot noise classifier | mem0 | Overkill — our rule-based approach is cheaper |
| Schema-based routing + SNR rules | LangMem | Yes — content type detection for prompt hints |
| 3-level dedup (exact → MinHash → LLM) | Graphiti | Yes — graduated merge instead of hard skip |
| EpisodeType routing for summaries | Graphiti | Yes — session type detection |
| LLM-as-judge for importance | Letta | Not yet — too expensive at current scale |
| Result spread for confidence | multiple | Yes — gap between top and bottom cosine |

## Gate 7.1: Classifier → Content Router

### Current
`classifySkipExtraction(msgs, conv) → (skip bool, reason string)`
Hard skip for trivial messages and code-only content.

### New
`classifyContent(msgs, conv) → ContentSignal`

```go
type ContentSignal struct {
    Skip       bool     // hard skip (trivial only)
    SkipReason string   // "trivial" — only for clear no-value messages
    Hints      []string // quality hints for extraction prompt
    ContentType string  // "factual" | "opinion" | "technical" | "multi-turn" | "mixed"
}
```

**Detection rules** (rule-based, no LLM):
- **Opinion detection**: first-person + preference verbs ("I prefer", "I like", "I think")
- **Technical detection**: code blocks, technical terms density, CLI commands
- **Factual detection**: declarative statements, names, dates, numbers
- **Multi-turn detection**: >2 messages, back-and-forth pattern

**How hints are used**: Injected into `ExtractAndDedup` system prompt as a `<content_hints>` section. Example: `"Content contains opinions and preferences — extract user preferences with high fidelity"`.

**Code-only behavior change**: Instead of hard skip for >90% code, emit hint `"Mostly code — extract only architectural decisions or tool preferences mentioned alongside code"`. Only skip if 100% code with zero natural language.

### Buffer Zone Integration
The buffer zone (`add_buffer.go`) calls `runFinePipeline` with pre-formatted conversation text (no raw `chatMessage` structs). Add a `classifyContentFromText(conversation string) ContentSignal` variant that works on formatted text, using the same rules but parsing the conversation format (`role: [timestamp]: content`). This ensures buffered batches also get quality hints.

## Gate 7.2: Near-Duplicate → Smart Merge Router

### Current
Hard skip at cosine > 0.97.

### New
Graduated response based on similarity tiers:

| Similarity | Action | Hint |
|-----------|--------|------|
| > 0.97 | Skip (keep current behavior) | — |
| 0.92–0.97 | Pass through with merge hint | `"High-similarity existing memory found — prefer UPDATE over ADD if semantically equivalent"` |
| < 0.92 | Normal extraction | — |

**Implementation**: `fetchFineCandidates` already returns `topScore`. Add a second return: `mergeHint string`. The hint text gets appended to the `ContentSignal.Hints` before injection into the extraction prompt.

**Pre-LLM exact string dedup**: Before embedding, hash-compare incoming conversation text against a short Redis cache of recent conversations per cube (last 50 hashes, 5-minute TTL). This catches exact retries/webhook duplicates at near-zero cost.

## Gate 7.3: Rerank → Adaptive Strategy

### Current
`shouldLLMRerank(results) → bool` — skip if <4 results or top cosine > 0.93.

### New
`rerankStrategy(results) → RerankDecision`

```go
type RerankDecision struct {
    ShouldRerank bool
    Reason       string
    TopK         int // how many to send to LLM reranker (cap for cost control)
}
```

**Result spread metric**: `spread = results[0].relativity - results[len-1].relativity`

| Condition | Decision | Rationale |
|-----------|----------|-----------|
| <4 results | Skip | Too few to reorder |
| Top cosine > 0.93 | Skip | High confidence in top result |
| Spread < 0.05 | Rerank (all) | Clustered scores — LLM judgment needed |
| Spread > 0.25 | Skip | Clear separation — cosine ordering is reliable |
| Otherwise | Rerank (top 8) | Medium ambiguity — rerank but cap cost |

**TopK capping**: When reranking, only send top N results to LLM instead of all. Reduces tokens while keeping quality.

## Gate 7.4: Episodic → Session-Aware Summary

### Current
Skip if factCount == 0 or codeBlockRatio > 0.8.

### New
Detect session type and customize the summary prompt.

**Session types** (rule-based detection):

| Type | Detection | Summary Focus |
|------|-----------|--------------|
| `decision` | "decided", "chose", "going with", "picked" | Capture what was decided and why |
| `learning` | Questions + answers, "TIL", "learned", "turns out" | Key takeaways and new understanding |
| `debug` | "error", "fix", "bug", stack traces, "solved" | Problem → root cause → solution |
| `code-review` | "review", "PR", "looks good", diffs | Feedback themes and action items |
| `planning` | "plan", "roadmap", "next steps", "TODO" | Goals, timeline, dependencies |
| `general` | default | Standard episodic summary |

**Implementation**: `detectSessionType(conversation string) string` returns the type. The type is appended to the episodic summary prompt as a focus instruction.

**Buffer zone integration**: When `runFinePipeline` generates episodic summaries for batched conversations, detect session type on the concatenated batch. Multi-conversation batches default to "general" unless a dominant type is detected (>60% of text matches one type).

## Files

| File | Action | Changes |
|------|--------|---------|
| `internal/handlers/add_classifier.go` | MODIFY | Evolve to ContentSignal struct, add content type detection, add text-only variant |
| `internal/handlers/add_classifier_test.go` | MODIFY | Tests for ContentSignal, content types, hints |
| `internal/handlers/add_fine.go` | MODIFY | Wire ContentSignal + merge hints into extraction prompt |
| `internal/handlers/add_buffer.go` | MODIFY | Wire classifyContentFromText into runFinePipeline |
| `internal/handlers/add_episodic.go` | MODIFY | Session type detection + prompt customization |
| `internal/search/rerank_gate.go` | MODIFY | Evolve to RerankDecision with spread metric |
| `internal/search/rerank_gate_test.go` | MODIFY | Tests for spread-based decisions |
| `internal/search/service.go` | MODIFY | Wire RerankDecision + TopK cap |
| `internal/llm/extractor.go` | MODIFY | Accept content hints in extraction prompt |

## Verification

```bash
# Unit tests
cd ~/MemDB/memdb-go && go test ./internal/handlers/... ./internal/search/...

# Build + deploy
cd ~/krolik-server && docker compose build memdb-go && docker compose up -d memdb-go

# Quality tests:
# a) Opinion → hint "preferences":
curl -s localhost:8080/product/add -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"I prefer Rust over Go for CLI tools"}],...}'
# Check logs for content_type=opinion

# b) Near-dup at 0.94 → merge hint (not skip):
# Send similar but slightly different message → should extract with UPDATE hint

# c) Spread-based rerank:
# Search with clustered results → should rerank
# Search with spread results → should skip rerank
```

## Non-Goals

- No LLM-based classification (too expensive at current scale)
- No changes to the extraction model or prompt structure beyond hint injection
- No changes to storage layer or embedding pipeline
- No changes to auth, rate limiting, or API contract
