## graphify (token savings)

Knowledge graph: `graphify-out/` — query it before grepping the whole repo.

Rules:
- Codebase questions: first `graphify query "<question>"` when `graphify-out/graph.json` exists. Relationships: `graphify path "<A>" "<B>"`. Node: `graphify explain "<concept>"`.
- Prefer scoped query/path/explain over reading full `GRAPH_REPORT.md` or raw grep.
- If `graphify-out/wiki/index.md` exists, navigate via wiki.
- After code changes: `graphify update .` (AST-only, no API cost).
- Strict PreToolUse: first raw Read/Glob of sources per session is blocked until one `graphify query` runs (`GRAPHIFY_HOOK_STRICT=0` to soft-nudge).
- MCP: `.mcp.json` + `.grok/config.toml` expose `query_graph`, `shortest_path`, `god_nodes`, etc.
