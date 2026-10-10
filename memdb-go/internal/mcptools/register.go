package mcptools

import (
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps carries everything the tool registrations need. Nil PG/QD are safe for
// registration itself — they are only captured in handler closures.
type Deps struct {
	PG            *db.Postgres
	QD            *db.Qdrant
	MemDBGoURL    string
	ServiceSecret string
	Logger        *slog.Logger
}

// RegisterAll registers every memdb-mcp tool on server. It is the single
// ground truth for the registered tool set: cmd/mcp-server and the schema
// gate test both call it, so a tool added here is covered by the gate
// automatically. Registration order is fixed (search, memory, user, cube,
// go-proxy) — keep new groups appended at the end.
func RegisterAll(server *mcp.Server, deps Deps) {
	RegisterSearchTool(server, deps.MemDBGoURL, deps.ServiceSecret, deps.Logger)
	RegisterMemoryTools(server, deps.PG, deps.QD, deps.Logger)
	RegisterUserTools(server, deps.PG, deps.Logger)
	RegisterCubeTools(server, deps.PG, deps.Logger)
	RegisterNativeGoProxyTools(server, deps.MemDBGoURL, deps.ServiceSecret, deps.Logger)
}
