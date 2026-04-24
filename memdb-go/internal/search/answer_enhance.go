package search

// answer_enhance.go — D10 post-retrieval answer enhancement.
//
// After the rerank/decay/boost pipeline produces top-K memory candidates,
// a single LLM call distills them into a concise, query-aligned answer.
//
// Targets the F1/EM gap on LoCoMo-style benchmarks: stored memories are
// verbose ("Caroline is advocating against sexual assault and child
// protection through her work as a social worker") while gold answers
// are short ("social worker"). D10 synthesises the short surface form
// directly and prepends it as a synthetic "EnhancedAnswer" item at
// position 0 of text_mem — downstream chat/complete surfaces this as
// the preferred response.
//
// Gated by MEMDB_SEARCH_ENHANCE=true (default off). Sits orthogonal to
// the MemOS-style memory rewriting in enhance.go (EnhanceMemories) —
// that rewrites each memory's text; this synthesises one extra item.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"bytes"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	answerEnhanceTopN           = 5
	answerEnhanceTimeout        = 10 * time.Second
	answerEnhanceMinRelativity  = 0.4
	answerEnhanceMaxTokens      = 120
	answerEnhanceRespBodyLimit  = 16 * 1024
	answerEnhanceSynthIDHexLen  = 12 // sha256 prefix length for synthetic item id
	answerEnhanceUnknownAnswer  = "UNKNOWN"
)

const answerEnhanceSystemPrompt = `You are a precise answer extractor. Given a user's question and a list of retrieved memories, respond with the SHORTEST possible answer that directly answers the question.

Rules:
- Use the exact surface form from the memories where possible (e.g., "social worker", not "working as a social worker").
- Prefer noun phrases or single words over full sentences.
- If the memories do not contain the answer, respond with "UNKNOWN".
- Never hallucinate facts not present in the memories.

Return strict JSON: {"answer": string, "source_ids": [string...], "confidence": float between 0.0 and 1.0}`

// AnswerEnhanceConfig configures the post-retrieval answer-extraction LLM call.
// Mirrors LLMRerankConfig shape so the service can reuse the same proxy credentials.
type AnswerEnhanceConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// answerEnhanceEnabled reports whether MEMDB_SEARCH_ENHANCE is set to "true".
func answerEnhanceEnabled() bool {
	return os.Getenv("MEMDB_SEARCH_ENHANCE") == "true"
}

// AnswerEnhanceResponse is the parsed LLM response.
type AnswerEnhanceResponse struct {
	Answer     string   `json:"answer"`
	SourceIDs  []string `json:"source_ids"`
	Confidence float64  `json:"confidence"`
}

// EnhanceRetrievalAnswer distills the top-K retrieved memories into a concise,
// query-aligned answer. Returns (answer, sourceIDs, confidence, err).
//
// Semantics:
//   - empty items → ("UNKNOWN", nil, 0, nil)
//   - no items with relativity ≥ answerEnhanceMinRelativity → ("UNKNOWN", nil, 0, nil)
//   - LLM error or malformed JSON → ("UNKNOWN", nil, 0, err) (caller should
//     log Debug and continue without enhancement)
//   - LLM returns literal "UNKNOWN" → propagated as-is, err = nil
func EnhanceRetrievalAnswer(
	ctx context.Context,
	query string,
	items []map[string]any,
	cfg AnswerEnhanceConfig,
) (string, []string, float64, error) {
	if len(items) == 0 || cfg.APIURL == "" {
		return answerEnhanceUnknownAnswer, nil, 0, nil
	}

	// Keep only items above the relativity floor, cap at top-N.
	candidates := make([]map[string]any, 0, answerEnhanceTopN)
	for _, it := range items {
		if len(candidates) >= answerEnhanceTopN {
			break
		}
		if getRelativity(it) < answerEnhanceMinRelativity {
			continue
		}
		candidates = append(candidates, it)
	}
	if len(candidates) == 0 {
		return answerEnhanceUnknownAnswer, nil, 0, nil
	}

	var memBlock strings.Builder
	for i, c := range candidates {
		id, _ := c["id"].(string)
		mem, _ := c["memory"].(string)
		fmt.Fprintf(&memBlock, "[%d] id=%s  %s\n", i+1, id, strings.TrimSpace(mem))
	}

	userMsg := fmt.Sprintf(
		"Question: %s\n\nMemories:\n%s\nRespond with JSON only.",
		query, memBlock.String(),
	)

	callCtx, cancel := context.WithTimeout(ctx, answerEnhanceTimeout)
	defer cancel()

	content, err := callAnswerEnhanceLLM(callCtx, userMsg, cfg)
	if err != nil {
		return answerEnhanceUnknownAnswer, nil, 0, fmt.Errorf("enhance llm: %w", err)
	}

	var parsed AnswerEnhanceResponse
	if err := json.Unmarshal(llm.StripJSONFence([]byte(content)), &parsed); err != nil {
		return answerEnhanceUnknownAnswer, nil, 0, fmt.Errorf("enhance parse: %w", err)
	}
	if strings.TrimSpace(parsed.Answer) == "" {
		return answerEnhanceUnknownAnswer, nil, 0, nil
	}
	return parsed.Answer, parsed.SourceIDs, parsed.Confidence, nil
}

// callAnswerEnhanceLLM performs the single chat-completion round trip for D10.
// Kept separate (not via *llm.Client) so we can enforce a tight 10s timeout
// without interfering with the shared 90s-timeout LLM client used elsewhere.
func callAnswerEnhanceLLM(ctx context.Context, userMsg string, cfg AnswerEnhanceConfig) (string, error) {
	payload := map[string]any{
		"model":       cfg.Model,
		"temperature": 0.0,
		"max_tokens":  answerEnhanceMaxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": answerEnhanceSystemPrompt},
			{"role": "user", "content": userMsg},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cfg.APIURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: answerEnhanceTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, answerEnhanceRespBodyLimit))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enhance llm: status %d", resp.StatusCode)
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", errors.New("enhance llm: bad response")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// prependEnhancedAnswer inserts a synthetic EnhancedAnswer item at position 0
// of items. Downstream formatting treats this as the top result, but the
// ordering of all other items is preserved.
//
// The synthetic id is "enhanced-" + first 12 hex chars of sha256(query), so
// identical queries yield stable ids across requests.
func prependEnhancedAnswer(items []map[string]any, answer string, sourceIDs []string, conf float64, query string) []map[string]any {
	sum := sha256.Sum256([]byte(query))
	id := "enhanced-" + hex.EncodeToString(sum[:])[:answerEnhanceSynthIDHexLen]

	synth := map[string]any{
		"id":     id,
		"memory": answer,
		"metadata": map[string]any{
			"memory_type": "EnhancedAnswer",
			"id":          id,
			"user_name":   "",
			"confidence":  conf,
			"source_ids":  sourceIDs,
			"relativity":  1.0,
			"enhanced":    true,
		},
		"ref_id": "[enhanced]",
	}

	// Also mark downstream items with the enhanced_answer / enhanced_confidence hints.
	for _, it := range items {
		meta, ok := it["metadata"].(map[string]any)
		if !ok {
			continue
		}
		meta["enhanced_answer"] = answer
		meta["enhanced_confidence"] = conf
	}

	out := make([]map[string]any, 0, len(items)+1)
	out = append(out, synth)
	out = append(out, items...)
	return out
}

// applyAnswerEnhancement is the pipeline hook. Called from postProcessResults
// (step 6.8) iff MEMDB_SEARCH_ENHANCE=true and a reranker LLM config is
// available. On LLM failure it logs at Debug and returns items unchanged
// (graceful degrade — never fails the whole search).
func applyAnswerEnhancement(
	ctx context.Context,
	logger *slog.Logger,
	query string,
	items []map[string]any,
	cfg AnswerEnhanceConfig,
) []map[string]any {
	if !answerEnhanceEnabled() || len(items) == 0 || cfg.APIURL == "" {
		searchMx().D10Enhance.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skipped")))
		return items
	}
	// Pre-check relativity floor to distinguish "threshold below" (skipped)
	// from a genuine LLM UNKNOWN response.
	anyRelevant := false
	for _, it := range items {
		if getRelativity(it) >= answerEnhanceMinRelativity {
			anyRelevant = true
			break
		}
	}
	if !anyRelevant {
		searchMx().D10Enhance.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "skipped")))
		return items
	}
	answer, sources, conf, err := EnhanceRetrievalAnswer(ctx, query, items, cfg)
	if err != nil {
		searchMx().D10Enhance.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "error")))
		if logger != nil {
			logger.Debug("enhance failed, continuing without", slog.Any("error", err))
		}
		return items
	}
	if answer == "" || answer == answerEnhanceUnknownAnswer {
		searchMx().D10Enhance.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "unknown")))
		return items
	}
	searchMx().D10Enhance.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "answered")))
	searchMx().D10Conf.Record(ctx, conf)
	return prependEnhancedAnswer(items, answer, sources, conf, query)
}
