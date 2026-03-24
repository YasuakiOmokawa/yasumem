"""SessionStart hook entry point.

Reads hook input JSON from stdin, queries recent memories from db.py,
and outputs hookSpecificOutput JSON to stdout.

Usage (from session-start.sh):
    cat | PYTHONPATH="$PLUGIN_ROOT/src" python3 -m yasumem.recall_hook "$DB_PATH"

This module imports db.py which is stdlib-only, so no uv/venv is needed.
"""

import json
import sys

from yasumem.db import get_connection, recall, resolve_canonical_project


def main():
    # DB path from command line argument
    if len(sys.argv) < 2:
        sys.exit(0)
    db_path = sys.argv[1]

    # Read hook input from stdin
    try:
        hook_input = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        sys.exit(0)

    cwd = hook_input.get("cwd", "")
    if not cwd:
        sys.exit(0)

    canonical = resolve_canonical_project(cwd)

    try:
        conn = get_connection(db_path)
        context = recall(conn, project_path=canonical, limit=5)
        conn.close()
    except Exception:
        sys.exit(0)

    if not context:
        sys.exit(0)

    # JSON-escape the context and output hookSpecificOutput
    escaped = json.dumps(context)
    # Build the full hook response
    response = {
        "hookSpecificOutput": {
            "hookEventName": "SessionStart",
            "additionalContext": context,
        }
    }
    print(json.dumps(response))


if __name__ == "__main__":
    main()
