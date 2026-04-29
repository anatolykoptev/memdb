package rerank

// applyInstructions returns query and docs with optional prefix strings prepended.
// Empty prefixes are no-ops. If both prefixes are empty the original slices are
// returned unchanged (no allocations).
//
// Instruction prefix use cases:
//   - bge-v1.5 / E5 family: prepend task descriptions to improve recall.
//   - bge-m3 / gte-multi-rerank: leave defaults empty (model cards say no prefix).
//
// Example for bge-v1.5:
//
//	WithInstruction("Represent this question for searching relevant passages:", "")
func applyInstructions(query string, docs []string, queryPrefix, docPrefix string) (string, []string) {
	if queryPrefix == "" && docPrefix == "" {
		return query, docs
	}
	modQuery := query
	if queryPrefix != "" {
		modQuery = queryPrefix + " " + query
	}
	modDocs := docs
	if docPrefix != "" {
		modDocs = make([]string, len(docs))
		for i, d := range docs {
			modDocs[i] = docPrefix + " " + d
		}
	}
	return modQuery, modDocs
}

// WithInstruction sets per-request prefix strings prepended to the query and
// each document before the HTTP call. Empty strings disable that side.
// bge-m3 / gte-multi-rerank callers should leave both at the default ("").
func WithInstruction(queryPrefix, docPrefix string) Opt {
	return func(c *cfgInternal) {
		c.queryInstruction = queryPrefix
		c.docInstruction = docPrefix
	}
}
