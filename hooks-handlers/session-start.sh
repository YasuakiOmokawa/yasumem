#!/usr/bin/env bash
set -euo pipefail
PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$PLUGIN_ROOT/bin/yasumem"
LOG_DIR="$PLUGIN_ROOT/data/logs"
mkdir -p "$LOG_DIR"

# Read hook input once
INPUT=$(cat)

# 0. Write canonical project path for MCP server default filter
CWD=$(echo "$INPUT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('cwd',''))" 2>/dev/null || true)
if [ -n "$CWD" ]; then
    CANONICAL=$(git -C "$CWD" worktree list --porcelain 2>/dev/null | head -1 | sed 's/^worktree //' || echo "$CWD")
    echo "$CANONICAL" > "$PLUGIN_ROOT/data/current_project"
fi

# 1. Ingest recent unprocessed sessions (sync, so recall sees new data)
echo "$INPUT" | YASUMEM_DB="$PLUGIN_ROOT/data/memory.db" "$BIN" ingest-recent >> "$LOG_DIR/ingest.log" 2>&1 || true

# 2. Recall memories (returns context to Claude)
echo "$INPUT" | YASUMEM_DB="$PLUGIN_ROOT/data/memory.db" "$BIN" recall 2>/dev/null || true

exit 0
