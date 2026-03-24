"""Ingest session transcripts into yasumem database.

Reads hook input JSON from stdin, locates the session JSONL file,
parses it into chunks, and saves via db.py.

Usage (from Stop hook):
    echo '{"session_id":"...","cwd":"..."}' | python3 -m yasumem.ingest

Can also be run via uv:
    echo '...' | uv run python3 -m yasumem.ingest
"""

import json
import os
import re
import sys
import time
from datetime import datetime
from pathlib import Path

from yasumem.db import (
    get_connection,
    prune_old_chunks,
    resolve_canonical_project,
    save_chunks,
    save_session,
    session_exists,
)

CLAUDE_PROJECTS_DIR = Path.home() / ".claude" / "projects"
MAX_CHUNK_LENGTH = 2000


def encode_cwd(cwd: str) -> str:
    return cwd.replace("/", "-")


def find_jsonl(session_id: str, cwd: str) -> Path | None:
    encoded = encode_cwd(cwd)
    # Try exact match first
    jsonl_path = CLAUDE_PROJECTS_DIR / encoded / f"{session_id}.jsonl"
    if jsonl_path.exists():
        return jsonl_path

    # Try subdirectories (some sessions are in UUID subdirs)
    project_dir = CLAUDE_PROJECTS_DIR / encoded
    if project_dir.is_dir():
        for sub in project_dir.iterdir():
            if sub.is_dir():
                candidate = sub / f"{session_id}.jsonl"
                if candidate.exists():
                    return candidate

    # Try broader search: session_id might be a directory name
    if project_dir.is_dir():
        session_dir = project_dir / session_id
        if session_dir.is_dir():
            for f in session_dir.iterdir():
                if f.suffix == ".jsonl":
                    return f

    return None


def extract_text_content(message: dict) -> str:
    content = message.get("content", "")
    if isinstance(content, str):
        return content

    parts = []
    if isinstance(content, list):
        for block in content:
            if isinstance(block, dict):
                if block.get("type") == "text":
                    parts.append(block.get("text", ""))
                elif block.get("type") == "tool_use":
                    parts.append(f"[Tool: {block.get('name', '?')}]")
            elif isinstance(block, str):
                parts.append(block)
    return "\n".join(parts)


def split_chunk(text: str, max_length: int = MAX_CHUNK_LENGTH) -> list[str]:
    if len(text) <= max_length:
        return [text]

    chunks = []
    # Split at sentence boundaries
    sentences = re.split(r'(?<=[。.!?！？\n])', text)
    current = ""
    for sentence in sentences:
        if len(current) + len(sentence) > max_length and current:
            chunks.append(current)
            current = sentence
        else:
            current += sentence
    if current:
        chunks.append(current)
    return chunks


def parse_jsonl(jsonl_path: Path) -> dict:
    """Parse a JSONL session file. Returns session metadata and chunks."""
    chunks = []
    session_meta = {
        "session_id": "",
        "project_path": "",
        "git_branch": None,
        "started_at": None,
        "ended_at": None,
    }
    chunk_index = 0

    with open(jsonl_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue

            entry_type = entry.get("type", "")
            if entry_type not in ("user", "assistant"):
                continue

            # Extract session metadata from first entry
            if not session_meta["session_id"]:
                session_meta["session_id"] = entry.get("sessionId", "")
                session_meta["project_path"] = entry.get("cwd", "")
                session_meta["git_branch"] = entry.get("gitBranch")

            # Track timestamps
            timestamp_str = entry.get("timestamp", "")
            if timestamp_str:
                try:
                    ts = datetime.fromisoformat(timestamp_str.replace("Z", "+00:00")).timestamp()
                    if session_meta["started_at"] is None or ts < session_meta["started_at"]:
                        session_meta["started_at"] = ts
                    if session_meta["ended_at"] is None or ts > session_meta["ended_at"]:
                        session_meta["ended_at"] = ts
                except (ValueError, OSError):
                    ts = time.time()
            else:
                ts = time.time()

            message = entry.get("message", {})
            text = extract_text_content(message)
            if not text.strip():
                continue

            role = entry_type  # 'user' or 'assistant'

            # Split long content
            text_parts = split_chunk(text)
            for part in text_parts:
                chunks.append({
                    "session_id": session_meta["session_id"],
                    "project_path": session_meta["project_path"],
                    "git_branch": session_meta["git_branch"],
                    "chunk_index": chunk_index,
                    "role": role,
                    "content": part,
                    "created_at": ts,
                })
                chunk_index += 1

    return {"meta": session_meta, "chunks": chunks}


def main():
    # Read hook input from stdin
    try:
        hook_input = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print("Error: invalid JSON on stdin", file=sys.stderr)
        sys.exit(1)

    session_id = hook_input.get("session_id", "")
    cwd = hook_input.get("cwd", "")

    if not session_id or not cwd:
        print(f"Error: missing session_id or cwd: {hook_input}", file=sys.stderr)
        sys.exit(1)

    # Determine DB path
    db_path = os.environ.get(
        "YASUMEM_DB",
        str(Path(__file__).resolve().parent.parent.parent / "data" / "memory.db"),
    )

    conn = get_connection(db_path)
    try:
        # Skip if already processed
        if session_exists(conn, session_id):
            return

        # Find JSONL file
        jsonl_path = find_jsonl(session_id, cwd)
        if not jsonl_path:
            print(f"Warning: JSONL not found for session {session_id} cwd {cwd}", file=sys.stderr)
            return

        # Parse and save
        result = parse_jsonl(jsonl_path)
        meta = result["meta"]
        chunks = result["chunks"]

        if not chunks:
            return

        # Override session_id/project_path from hook input if JSONL didn't have them
        if not meta["session_id"]:
            meta["session_id"] = session_id
        if not meta["project_path"]:
            meta["project_path"] = cwd

        # Normalize worktree paths to main repo path for cross-worktree sharing
        canonical = resolve_canonical_project(cwd)
        meta["project_path"] = canonical
        for c in chunks:
            c["project_path"] = canonical

        save_session(
            conn,
            session_id=meta["session_id"],
            project_path=meta["project_path"],
            git_branch=meta["git_branch"],
            started_at=meta["started_at"] or time.time(),
            ended_at=meta["ended_at"],
        )
        count = save_chunks(conn, chunks)
        conn.commit()

        print(f"Saved {count} chunks from session {session_id}", file=sys.stderr)

        # Prune old data
        pruned = prune_old_chunks(conn)
        if pruned > 0:
            print(f"Pruned {pruned} old chunks", file=sys.stderr)

    finally:
        conn.close()


if __name__ == "__main__":
    main()
