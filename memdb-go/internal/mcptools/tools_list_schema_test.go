package mcptools

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpRegisteredToolCount is the number of tools cmd/mcp-server/main.go
// registers: 10 native (search_memories, get_memory, delete_memory,
// delete_all_memories, create_user, get_user_info, create_cube, list_cubes,
// delete_cube, get_user_cubes) + 4 Go-proxy (update_memory, add_memory, chat,
// clear_chat_history). ListTools must return at least this many — an empty or
// truncated list must fail the schema gate, not pass it.
const mcpRegisteredToolCount = 14

// newTestMCPServer builds the MCP server with the same Register* calls as
// cmd/mcp-server/main.go. nil pg/qd are safe: registration only captures them
// in handler closures; no handler is invoked by ListTools.
func newTestMCPServer(t *testing.T) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "memdb-mcp",
		Version: "0.22.0",
	}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	RegisterSearchTool(server, "http://127.0.0.1:1", "", logger)
	RegisterMemoryTools(server, nil, nil, logger)
	RegisterUserTools(server, nil, logger)
	RegisterCubeTools(server, nil, logger)
	RegisterNativeGoProxyTools(server, "http://127.0.0.1:1", "", logger)
	return server
}

// Schema-valued JSON Schema keywords. Values (or array elements) at these keys
// must be schema OBJECTS. A bare boolean there is either an empty-schema
// artifact of type inference (`any`/`map[string]any` marshal as `true`) or a
// hand-written `false` — both are rejected by pydantic-typed MCP clients
// (e.g. the Python SDK used by the Hermes gateway) which model these positions
// as dicts.
var (
	// Keys whose value is a single subschema.
	schemaValueKeys = map[string]bool{
		"items": true, "additionalItems": true, "contains": true,
		"additionalProperties": true, "unevaluatedProperties": true,
		"unevaluatedItems": true, "propertyNames": true,
		"not": true, "if": true, "then": true, "else": true,
		"contentSchema": true,
	}
	// Keys whose value is a map of name -> subschema.
	schemaMapKeys = map[string]bool{
		"properties": true, "patternProperties": true,
		"$defs": true, "definitions": true, "dependentSchemas": true,
	}
	// Keys whose value is a list of subschemas.
	schemaListKeys = map[string]bool{
		"prefixItems": true, "allOf": true, "anyOf": true, "oneOf": true,
	}
)

// checkSubschema inspects the value sitting at a schema position (the value of
// key `key` inside a schema object, a member of a subschema list, or a root
// schema). Booleans fail — with one exemption: `false` under
// additionalProperties / unevaluatedProperties is the closed-object idiom that
// jsonschema-go emits deliberately for every struct-derived schema (it is
// spec-legal, required for the SDK's extra-property rejection, and tolerated by
// clients — only `true`, the empty-schema artifact, is a defect there).
// `true` is never exempt.
func checkSubschema(violations *[]string, path, key string, v any) {
	switch s := v.(type) {
	case bool:
		if !s && (key == "additionalProperties" || key == "unevaluatedProperties") {
			return
		}
		*violations = append(*violations, fmt.Sprintf("%s = %v (boolean in schema position)", path, s))
	case map[string]any:
		walkSchemaObject(violations, path, s)
	}
}

func walkSchemaObject(violations *[]string, path string, schema map[string]any) {
	for k, v := range schema {
		switch {
		case schemaValueKeys[k]:
			checkSubschema(violations, path+"."+k, k, v)
		case schemaMapKeys[k]:
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for name, sub := range m {
				checkSubschema(violations, path+"."+k+"."+name, k, sub)
			}
		case schemaListKeys[k]:
			l, ok := v.([]any)
			if !ok {
				continue
			}
			for i, sub := range l {
				checkSubschema(violations, fmt.Sprintf("%s.%s[%d]", path, k, i), k, sub)
			}
		}
		// Other keywords (type, required, deprecated, default, enum, ...) hold
		// data, not schemas — their boolean values are legitimate and skipped.
	}
}

// TestListToolsSchemas_NoBooleanSubschemas is a regression gate for the live
// defect where tools/list advertised `outputSchema.properties.result = true`
// for all 14 tools (TextResult.Result is `any`; jsonschema-go marshals an
// empty schema as a JSON boolean), breaking every Python MCP client.
// The tool population is derived from ListTools itself, so tools registered
// later are covered automatically.
func TestListToolsSchemas_NoBooleanSubschemas(t *testing.T) {
	ctx := context.Background()
	server := newTestMCPServer(t)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "schema-gate-test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) < mcpRegisteredToolCount {
		t.Fatalf("ListTools returned %d tools, want >= %d registered in cmd/mcp-server/main.go",
			len(res.Tools), mcpRegisteredToolCount)
	}

	var violations []string
	for _, tool := range res.Tools {
		checkSubschema(&violations, tool.Name+".inputSchema", "inputSchema", tool.InputSchema)
		checkSubschema(&violations, tool.Name+".outputSchema", "outputSchema", tool.OutputSchema)
	}
	for _, v := range violations {
		t.Error(v)
	}
}
