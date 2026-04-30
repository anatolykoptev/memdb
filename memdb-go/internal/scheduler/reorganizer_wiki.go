package scheduler

// reorganizer_wiki.go — auto-maintain wiki_pages from tree promotions.
//
// Karpathy LLM Wiki "compounding artifact" lands in the reorganizer
// because that's where MemDB already does the LLM-driven synthesis
// (raw memories → cluster summary on promotion). When promoteCluster
// writes a tier parent, the same summary + cluster member list are
// fanned out into a wiki page so the wiki keeps pace with the graph
// without a separate maintenance loop.
//
// Wiki updates are gated by MEMDB_WIKI_AUTO_UPDATE — default OFF so
// existing deployments don't start filling the wiki_pages table
// unannounced. Set to "1"/"true"/"yes"/"on" to enable.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/wiki"
)

// recordTreeReorgWikiOutcome bumps the existing TreeReorg counter
// with a wiki-specific outcome label. Reuses the schedMx instrument so
// dashboards already watching tree reorg pick up wiki events without
// a new metric registration.
func recordTreeReorgWikiOutcome(ctx context.Context, tier, outcome string) {
	schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
		attribute.String("tier", tier),
		attribute.String("outcome", outcome),
	))
}

const wikiAutoUpdateEnv = "MEMDB_WIKI_AUTO_UPDATE"

// wikiAutoUpdateEnabled returns true when env opts in. Mirrors the
// staged-rollout pattern used by atomicFactsEnabled / obsDateEnabled.
func wikiAutoUpdateEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(wikiAutoUpdateEnv)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// recordWikiPage upserts a wiki page from a tree-reorg promotion. Idempotent
// per (cube_id, event_id) — the slug is derived from eventID so a re-run of
// the same reorganization phase replaces the page (version++) instead of
// creating a duplicate.
//
// Best-effort end-to-end: any failure logs at Warn and is counted into the
// existing TreeReorg metric (`outcome=wiki_write_error`) without aborting
// the promotion. The DB write to wiki_pages is the source of truth; the
// FS mirror at $UPLOADS_ROOT/memdb-go/wiki/<cube>/<slug>.md is best-effort.
func (r *Reorganizer) recordWikiPage(
	ctx context.Context,
	cubeID, eventID, summary string,
	embedding []float32,
	childIDs []string,
	tier string,
) {
	if !wikiAutoUpdateEnabled() {
		return
	}
	if cubeID == "" || eventID == "" || strings.TrimSpace(summary) == "" {
		return
	}
	slug := wikiSlugForPromotion(tier, eventID)
	title := wikiTitleFromSummary(summary)
	body := renderWikiPromotionBody(title, summary, childIDs, tier)

	page, err := r.postgres.UpsertWikiPage(ctx, db.UpsertWikiPageParams{
		CubeID:        cubeID,
		Slug:          slug,
		Title:         title,
		Body:          body,
		ParentSources: childIDs,
		Embedding:     embedding,
	})
	if err != nil {
		r.logger.Warn("tree reorg: wiki upsert failed",
			slog.String("cube_id", cubeID),
			slog.String("slug", slug),
			slog.String("tier", tier),
			slog.Any("error", err))
		recordTreeReorgWikiOutcome(ctx, tier, "wiki_write_error")
		return
	}

	if _, fsErr := wiki.ExportToFilesystem(wiki.Page{
		CubeID:    page.CubeID,
		Slug:      page.Slug,
		Title:     page.Title,
		Body:      page.Body,
		Sources:   page.ParentSources,
		Version:   page.Version,
		UpdatedAt: page.UpdatedAt,
	}); fsErr != nil {
		r.logger.Warn("tree reorg: wiki fs export",
			slog.String("cube_id", cubeID),
			slog.String("slug", slug),
			slog.Any("error", fsErr))
		// Don't downgrade outcome — DB write succeeded, that's the contract.
	}
	if err := wiki.AppendLog(cubeID,
		fmt.Sprintf("auto: %s promote %s v%d", tier, slug, page.Version)); err != nil {
		r.logger.Warn("tree reorg: wiki log append",
			slog.String("cube_id", cubeID),
			slog.Any("error", err))
	}
	recordTreeReorgWikiOutcome(ctx, tier, "wiki_written")
}

// wikiSlugForPromotion produces a deterministic slug for a tree-reorg
// event. Format: "auto/<tier>/<eventID-prefix>" so all auto-generated
// pages live under a single namespace separate from human-curated
// pages, and the same eventID always upserts the same row.
func wikiSlugForPromotion(tier, eventID string) string {
	prefix := eventID
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	prefix = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return -1
		}
	}, prefix)
	if prefix == "" {
		prefix = "evt"
	}
	return "auto/" + tier + "/" + prefix
}

// wikiTitleFromSummary returns the first non-empty line of the summary
// truncated to 80 runes. The summary itself comes from the LLM and may
// or may not have a leading "# Title" — we strip a single leading #
// before truncation for cleanliness.
func wikiTitleFromSummary(summary string) string {
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "# ")
		runes := []rune(line)
		if len(runes) > 80 {
			runes = runes[:80]
		}
		return string(runes)
	}
	return "Untitled"
}

// renderWikiPromotionBody assembles the markdown body the wiki page
// stores. Layout: title → summary → ## Sources block listing every
// child memory ID. Sources are kept as bullet UUIDs (not links) so
// the body is portable across surfaces — the API consumer is free
// to render them as anchors.
func renderWikiPromotionBody(title, summary string, childIDs []string, tier string) string {
	var sb strings.Builder
	sb.WriteString("# ")
	sb.WriteString(title)
	sb.WriteString("\n\n")
	sb.WriteString("> Auto-generated from a `")
	sb.WriteString(tier)
	sb.WriteString("` tree promotion (Karpathy LLM Wiki: compounding artifact).\n\n")
	sb.WriteString(strings.TrimSpace(summary))
	sb.WriteString("\n\n## Sources\n")
	if len(childIDs) == 0 {
		sb.WriteString("- (no child IDs recorded)\n")
		return sb.String()
	}
	for _, id := range childIDs {
		sb.WriteString("- ")
		sb.WriteString(id)
		sb.WriteString("\n")
	}
	return sb.String()
}
