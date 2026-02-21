// Package queries contains SQL query constants for PolarDB (PostgreSQL + Apache AGE).
//
// PolarDB uses Apache AGE for graph operations on top of PostgreSQL.
// The main table is "{graph_name}"."Memory" where graph_name defaults to "memos_graph".
// Node properties are stored in a JSONB `properties` column.
// Vector embeddings are stored in `embedding` (vector(1024) for voyage-4-lite).
// Full-text search uses `properties_tsvector_zh` tsvector column with a GIN index.
//
// File layout:
//   queries.go         — DefaultGraphName constant
//   queries_memory.go  — memory CRUD: user/instance, get-all, delete, update, insert, cleanup
//   queries_graph.go   — memory_edges table, graph recall, importance decay
//   queries_entity.go  — entity_nodes table
//   queries_config.go  — user_configs table
//   search_queries.go  — vector + fulltext search queries (existing)
package queries

// DefaultGraphName is the default Apache AGE graph and schema name.
const DefaultGraphName = "memos_graph"
