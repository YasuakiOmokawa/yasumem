"""Ingest recent unprocessed sessions at SessionStart.

Scans all worktree project directories for JSONL files
not yet in the database, and ingests them.

Usage:
    echo '{"cwd":"/path/to/project"}' | python3 -m yasumem.ingest_recent /path/to/memory.db
"""

import json
import sys
import time
from pathlib import Path

from yasumem.db import (
    get_connection,
    prune_old_chunks,
    resolve_canonical_project,
    save_chunks,
    save_session,
    session_exists,
)
from yasumem.ingest import encode_cwd, parse_jsonl

CLAUDE_PROJECTS_DIR = Path.home() / ".claude" / "projects"
MAX_SESSIONS_PER_WORKTREE = 3


def get_worktree_paths(cwd: str) -> list[str]:
    import subprocess
    paths = []
    try:
        result = subprocess.run(
            ["git", "-C", cwd, "worktree", "list", "--porcelain"],
            capture_output=True, text=True, timeout=5,
        )
        if result.returncode == 0:
            for line in result.stdout.splitlines():
                if line.startswith("worktree "):
                    paths.append(line.split(" ", 1)[1])
    except Exception:
        pass
    if not paths:
        paths = [cwd]
    return paths


def find_recent_jsonls(worktree_paths: list[str]) -> list[Path]:
    all_files = []
    for wt_path in worktree_paths:
        encoded = encode_cwd(wt_path)
        project_dir = CLAUDE_PROJECTS_DIR / encoded
        if not project_dir.is_dir():
            continue
        jsonl_files = [f for f in project_dir.iterdir() if f.suffix == ".jsonl"]
        jsonl_files.sort(key=lambda p: p.stat().st_mtime, reverse=True)
        all_files.extend(jsonl_files[:MAX_SESSIONS_PER_WORKTREE])
    return all_files


def main():
    if len(sys.argv) < 2:
        sys.exit(0)
    db_path = sys.argv[1]

    try:
        hook_input = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        sys.exit(0)

    cwd = hook_input.get("cwd", "")
    if not cwd:
        sys.exit(0)

    canonical = resolve_canonical_project(cwd)
    worktree_paths = get_worktree_paths(cwd)

    conn = get_connection(db_path)
    try:
        recent_jsonls = find_recent_jsonls(worktree_paths)
        ingested = 0

        for jsonl_path in recent_jsonls:
            session_id = jsonl_path.stem
            if session_exists(conn, session_id):
                continue

            try:
                result = parse_jsonl(jsonl_path)
                meta = result["meta"]
                chunks = result["chunks"]

                if not chunks:
                    continue

                if not meta["session_id"]:
                    meta["session_id"] = session_id

                meta["project_path"] = canonical
                for c in chunks:
                    c["project_path"] = canonical

                save_session(
                    conn,
                    session_id=meta["session_id"],
                    project_path=canonical,
                    git_branch=meta["git_branch"],
                    started_at=meta["started_at"] or time.time(),
                    ended_at=meta["ended_at"],
                )
                count = save_chunks(conn, chunks)
                conn.commit()
                ingested += 1
                print(f"Ingested {count} chunks from {session_id}", file=sys.stderr)
            except Exception as e:
                print(f"Error ingesting {session_id}: {e}", file=sys.stderr)
                continue

        if ingested > 0:
            pruned = prune_old_chunks(conn)
            if pruned > 0:
                print(f"Pruned {pruned} old chunks", file=sys.stderr)
    finally:
        conn.close()


if __name__ == "__main__":
    main()
