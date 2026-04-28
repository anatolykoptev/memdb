// Package handlers — M11 F12 linked-resolver fire-and-forget trigger.
//
// Lives between the atomic persist step and the legacy background fan-out.
// Runs only when MEMDB_F12_LINKED is unset (default ON) or set to a truthy
// value. Per-fact bounded concurrency via linkedResolverSemaphore so a
// burst of /add traffic doesn't fan out N×M LLM calls in one shot.
//
// For each persisted atomic fact:
//   1. Skip if the fact text or ltmID is empty.
//   2. Pull top-N (linkedResolverTopN, default 20) cosine-similar memories
//      via Postgres VectorSearch — the wider candidate window the
//      extract-time pass never saw.
//   3. Filter out the fact itself (a fact cannot link to its own UUID).
//   4. Call LinkedIDsResolver.Resolve to ask the LLM which candidates are
//      causally / temporally linked.
//   5. Merge with the extract-time linked_memory_ids and persist via
//      Postgres.SetLinkedMemoryIDs.
//
// Errors at any step bump a metric outcome and continue with the next fact;
// the resolver is best-effort signal enrichment, never load-bearing.
package handlers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// linkedResolverSemaphoreSize caps concurrent F12 LLM calls across the
// entire process. Sized to match profileExtractSemaphore (4) — the same
// admission-control envelope keeps the LLM provider's per-key RPM under
// 60 even at peak /add throughput.
const linkedResolverSemaphoreSize = 4

// linkedResolverPerFactBudget caps the wallclock spent per fact (LLM
// candidate fetch + LLM call + persist). Picked above the chat timeout so
// an upstream stall doesn't cascade across facts.
const linkedResolverPerFactBudget = 45 * time.Second

// linkedResolverCandidateLimit — pgvector top-N for the cosine candidate
// pool fed into the resolver. 20 keeps the prompt cheap (~2 KB) while
// covering the long-tail relations the extract-time 10-candidate window
// missed.
const linkedResolverCandidateLimit = linkedResolverTopN

var (
	linkedResolverSemaphore     chan struct{}
	linkedResolverSemaphoreOnce sync.Once
)

func acquireLinkedResolverSlot() chan struct{} {
	linkedResolverSemaphoreOnce.Do(func() {
		linkedResolverSemaphore = make(chan struct{}, linkedResolverSemaphoreSize)
	})
	return linkedResolverSemaphore
}

// linkedResolverWrapperOnce caches the resolver wrapper bound to the
// handler's chat client. Re-allocates if the underlying client pointer
// changes (test re-init), mirroring atomicExtractorCache.
type linkedResolverCacheT struct {
	mu      sync.Mutex
	client  *llm.Client
	wrapper *LinkedIDsResolver
}

var linkedResolverCache linkedResolverCacheT //nolint:gochecknoglobals // cache singleton

func (h *Handler) getLinkedResolver() *LinkedIDsResolver {
	if h.llmExtractor == nil {
		return nil
	}
	c := h.llmExtractor.Client()
	linkedResolverCache.mu.Lock()
	defer linkedResolverCache.mu.Unlock()
	if linkedResolverCache.wrapper == nil || linkedResolverCache.client != c {
		linkedResolverCache.client = c
		linkedResolverCache.wrapper = NewLinkedIDsResolver(c, h.logger)
	}
	return linkedResolverCache.wrapper
}

// triggerLinkedIDsResolver fans out per-fact F12 resolution. Fire-and-forget:
// each fact runs in its own goroutine, gated by the package semaphore.
// Returns immediately to keep the /add request path off the LLM hop.
func (h *Handler) triggerLinkedIDsResolver(
	atomicFacts []llm.AtomicFact,
	embedded []embeddedFact,
	cubeID, personID, agentID string,
) {
	if h == nil || h.postgres == nil {
		return
	}
	if !linkedResolverEnabled() {
		ctx := context.Background()
		for range atomicFacts {
			recordLinkedFactProcessed(ctx, linkedOutcomeDisabled)
		}
		return
	}
	resolver := h.getLinkedResolver()
	if resolver == nil {
		return
	}
	if len(atomicFacts) != len(embedded) {
		// Length mismatch is the same defensive guard applyAtomicInfoToFacts
		// uses — the upstream contract pins them in lock-step.
		return
	}

	sem := acquireLinkedResolverSlot()
	for i := range atomicFacts {
		fact := atomicFacts[i]
		ef := embedded[i]
		if ef.ltmID == "" || ef.fact.Memory == "" {
			continue
		}
		go h.runLinkedResolverForFact(resolver, sem, fact, ef, cubeID, personID, agentID)
	}
}

// runLinkedResolverForFact is the per-fact body. Acquires the semaphore,
// fetches candidates, calls the LLM, merges with extract-time IDs, and
// persists. Each path increments exactly one outcome label so dashboards
// can attribute regressions cleanly.
func (h *Handler) runLinkedResolverForFact(
	resolver *LinkedIDsResolver,
	sem chan struct{},
	fact llm.AtomicFact,
	ef embeddedFact,
	cubeID, personID, agentID string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), linkedResolverPerFactBudget)
	defer cancel()

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		recordLinkedFactProcessed(ctx, linkedOutcomeLLMError)
		return
	}

	// Step 1: fetch top-N cosine candidates. Reuse the existing pgvector
	// path (same one /add candidate dedup uses) for parity. We exclude the
	// just-persisted fact ID downstream — it would otherwise be the top hit
	// (cosine = 1.0 against its own embedding).
	candidates, err := h.fetchLinkedResolverCandidates(ctx, ef, cubeID, personID, agentID)
	if err != nil {
		recordLinkedFactProcessed(ctx, linkedOutcomeLLMError)
		if h.logger != nil {
			h.logger.Debug("linked resolver: candidate fetch failed",
				slog.String("ltm_id", ef.ltmID), slog.Any("error", err))
		}
		return
	}
	if len(candidates) == 0 {
		recordLinkedFactProcessed(ctx, linkedOutcomeNoCandidates)
		return
	}

	// Step 2: LLM call.
	resolved, err := resolver.Resolve(ctx, fact, candidates)
	if err != nil {
		recordLinkedFactProcessed(ctx, linkedOutcomeLLMError)
		if h.logger != nil {
			h.logger.Debug("linked resolver: llm call failed",
				slog.String("ltm_id", ef.ltmID), slog.Any("error", err))
		}
		return
	}

	// Step 3: merge with extract-time IDs and persist.
	merged := mergeLinkedIDs(fact.LinkedMemoryIDs, resolved)
	if len(merged) == 0 {
		recordLinkedFactProcessed(ctx, linkedOutcomeEmpty)
		return
	}
	if err := h.postgres.SetLinkedMemoryIDs(ctx, ef.ltmID, cubeID, merged); err != nil {
		recordLinkedFactProcessed(ctx, linkedOutcomePersistError)
		if h.logger != nil {
			h.logger.Debug("linked resolver: persist failed",
				slog.String("ltm_id", ef.ltmID), slog.Any("error", err))
		}
		return
	}
	recordLinkedFactProcessed(ctx, linkedOutcomeSuccess)
	recordLinkedRelationsFound(ctx, len(merged))
}

// fetchLinkedResolverCandidates pulls top-N pgvector matches for the fact
// and converts them into the llm.Candidate shape the resolver consumes.
// Self-references (the fact's own ltmID) are filtered out.
func (h *Handler) fetchLinkedResolverCandidates(
	ctx context.Context,
	ef embeddedFact,
	cubeID, personID, agentID string,
) ([]llm.Candidate, error) {
	if len(ef.embedding) == 0 || h.postgres == nil {
		return nil, nil
	}
	// LongTermMemory + UserMemory mirrors fetchFineCandidates' set —
	// keeps F12 looking at the same corpus as F8's extract-time dedup.
	results, err := h.postgres.VectorSearch(
		ctx, ef.embedding, cubeID, personID,
		[]string{"LongTermMemory", "UserMemory"},
		agentID, linkedResolverCandidateLimit,
	)
	if err != nil {
		return nil, err
	}
	out := make([]llm.Candidate, 0, len(results))
	for _, r := range results {
		id, mem := extractIDAndMemory(r.Properties)
		if id == "" || mem == "" {
			continue
		}
		if id == ef.ltmID {
			// Drop self-references — a fact never links to itself.
			continue
		}
		out = append(out, llm.Candidate{ID: id, Memory: mem})
	}
	return out, nil
}
