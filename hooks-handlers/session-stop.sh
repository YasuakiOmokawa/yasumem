#!/usr/bin/env bash
set -euo pipefail
PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$PLUGIN_ROOT/data/logs"
mkdir -p "$LOG_DIR"

# stdinからJSON入力を読み、Python側で全フィールドを解析
INPUT=$(cat)

# バックグラウンドで保存（hookをブロックしない）
# エラーはログファイルに記録
cd "$PLUGIN_ROOT"
echo "$INPUT" | nohup uv run python3 -m yasumem.ingest \
  >> "$LOG_DIR/ingest.log" 2>&1 &
exit 0
