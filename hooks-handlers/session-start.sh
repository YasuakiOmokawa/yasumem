#!/usr/bin/env bash
set -euo pipefail
PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$PLUGIN_ROOT/data/logs"
mkdir -p "$LOG_DIR"

# Read hook input once
INPUT=$(cat)

# 1. Ingest recent unprocessed sessions (sync, so recall sees new data)
echo "$INPUT" | PYTHONPATH="$PLUGIN_ROOT/src" python3 -m yasumem.ingest_recent "$PLUGIN_ROOT/data/memory.db" >> "$LOG_DIR/ingest.log" 2>&1 || true

# 2. Recall memories (returns context to Claude)
echo "$INPUT" | PYTHONPATH="$PLUGIN_ROOT/src" python3 -m yasumem.recall_hook "$PLUGIN_ROOT/data/memory.db" 2>/dev/null || true

exit 0
