package handlers

// chat_record.go — per-request observability helpers for the chat handlers.
// Keeps chat.go itself focused on request flow (read body → search →
// build prompt → call LLM → write response). Metric/header emission
// is independent of that flow but called from it; split out on the M12.2
// PR to keep both files under the 200-line target.
//
// Instruments are declared in metrics.go (chatPromptMx, chatRefusalMx,
// chatAcceptanceMx). The helpers here are the ONLY callers in the handlers
// package — keep that contract so subsystem-level metric churn stays
// isolated.

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// promptTemplateLabel maps the (basePrompt, answerStyle) pair to the metric label
// emitted by chat handlers. "custom" wins when basePrompt is non-empty (backward-compat).
func promptTemplateLabel(basePrompt, answerStyle string) string {
	if basePrompt != "" {
		return "custom"
	}
	if answerStyle == answerStyleFactual {
		return answerStyleFactual
	}
	return answerStyleConversational
}

// recordChatPromptUsed bumps memdb.chat.prompt_template_used_total{template=...}.
// Called once per chat request right after buildSystemPrompt returns.
func recordChatPromptUsed(ctx context.Context, basePrompt, answerStyle string) {
	chatPromptMx().TemplateUsed.Add(ctx, 1,
		metric.WithAttributes(attribute.String("template", promptTemplateLabel(basePrompt, answerStyle))),
	)
}

// refusalDebugHeader is the HTTP header carrying the prompt-routing reason
// emitted by buildSystemPromptWithDecision. Operators / harnesses can read
// this to debug post-hoc why the model refused on a given query without
// having to scrape Prometheus. Header is set ONLY on the factual path; the
// conversational / custom branches emit no header so legacy clients see no
// behaviour change.
const refusalDebugHeader = "X-Memdb-Refusal-Reason"

// recordFactualPromptDecision wires the M12.2 observability triplet into
// every chat request that took the factual branch:
//  1. emit memdb.chat.refusal_total{reason, variant} (counter +1)
//  2. observe memdb.chat.top_retrieval_score (histogram, raw cosine in [0, 1])
//  3. set X-Memdb-Refusal-Reason on the response writer
//
// Setting the header MUST happen before any body bytes are written —
// callers invoke this between buildSystemPromptWithDecision and the LLM
// call (and before any h.writeJSON or rpc.SSEHeaders).
//
// Skipped (zero-cost) when decision.Variant == factualVariantNone (the
// non-factual branches): no metric, no header. This preserves legacy chat
// payload shape for conversational and custom-system-prompt clients.
func recordFactualPromptDecision(ctx context.Context, w http.ResponseWriter, decision factualPromptDecision) {
	if decision.Variant == factualVariantNone {
		return
	}
	mx := chatRefusalMx()
	mx.RefusalTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("reason", string(decision.Reason)),
		attribute.String("variant", string(decision.Variant)),
	))
	mx.TopRetrievalScore.Record(ctx, decision.TopScore)
	if w != nil && decision.Reason != "" {
		w.Header().Set(refusalDebugHeader, string(decision.Reason))
	}
}
