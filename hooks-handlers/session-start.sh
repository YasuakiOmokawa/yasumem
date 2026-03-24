#!/usr/bin/env bash
set -euo pipefail
PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# recall_hook.py → db.py をimport（db.pyはstdlib only）
# uv不使用、PYTHONPATH指定で直接実行し cold-start 遅延を回避
cat | PYTHONPATH="$PLUGIN_ROOT/src" python3 -m yasumem.recall_hook "$PLUGIN_ROOT/data/memory.db" 2>/dev/null || true
exit 0
