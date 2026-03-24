"""yasumem database layer. stdlib only (sqlite3, time, os, json, dataclasses)."""

import os
import sqlite3
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path

DEFAULT_DB_PATH = Path(__file__).resolve().parent.parent.parent / "data" / "memory.db"

NOISE_FILTER = (
    "AND content NOT LIKE '<local-command%' "
    "AND content NOT LIKE '<command-name>%' "
    "AND content NOT LIKE '[Tool:%'"
)

SCHEMA_SQL = """
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;

CREATE TABLE IF NOT EXISTS chunks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    project_path TEXT NOT NULL,
    git_branch TEXT,
    chunk_index INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at REAL NOT NULL,
    UNIQUE(session_id, chunk_index)
);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    content,
    content=chunks,
    content_rowid=id,
    tokenize='trigram'
);

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE OF content ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
    INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    project_path TEXT NOT NULL,
    git_branch TEXT,
    started_at REAL NOT NULL,
    ended_at REAL
);

CREATE INDEX IF NOT EXISTS idx_chunks_session ON chunks(session_id);
CREATE INDEX IF NOT EXISTS idx_chunks_project ON chunks(project_path);
CREATE INDEX IF NOT EXISTS idx_chunks_created ON chunks(created_at);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_path);
"""


@dataclass
class Chunk:
    id: int
    session_id: str
    project_path: str
    git_branch: str | None
    chunk_index: int
    role: str
    content: str
    created_at: float


def resolve_canonical_project(cwd: str) -> str:
    """Resolve git worktree path to main repo path for grouping."""
    try:
        result = subprocess.run(
            ["git", "-C", cwd, "worktree", "list", "--porcelain"],
            capture_output=True, text=True, timeout=5,
        )
        if result.returncode == 0:
            for line in result.stdout.splitlines():
                if line.startswith("worktree "):
                    return line.split(" ", 1)[1]
    except Exception:
        pass
    return cwd


def _migrate_fts_tokenizer(conn: sqlite3.Connection) -> None:
    """Migrate FTS5 tokenizer from unicode61 to trigram if needed."""
    try:
        row = conn.execute(
            "SELECT sql FROM sqlite_master WHERE name='chunks_fts' AND type='table'"
        ).fetchone()
        if row and "trigram" not in row[0]:
            conn.execute("DROP TABLE IF EXISTS chunks_fts")
            conn.execute("DROP TRIGGER IF EXISTS chunks_ai")
            conn.execute("DROP TRIGGER IF EXISTS chunks_ad")
            conn.execute("DROP TRIGGER IF EXISTS chunks_au")
    except Exception:
        pass


def get_connection(db_path: str | Path | None = None) -> sqlite3.Connection:
    path = str(db_path or DEFAULT_DB_PATH)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    conn = sqlite3.connect(path)
    _migrate_fts_tokenizer(conn)
    conn.executescript(SCHEMA_SQL)
    # Rebuild FTS content after potential migration
    try:
        conn.execute("INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')")
    except Exception:
        pass
    conn.row_factory = sqlite3.Row
    return conn


# --- CRUD ---

def session_exists(conn: sqlite3.Connection, session_id: str) -> bool:
    row = conn.execute(
        "SELECT 1 FROM sessions WHERE session_id = ?", (session_id,)
    ).fetchone()
    return row is not None


def save_session(
    conn: sqlite3.Connection,
    session_id: str,
    project_path: str,
    git_branch: str | None,
    started_at: float,
    ended_at: float | None,
) -> None:
    conn.execute(
        """INSERT INTO sessions (session_id, project_path, git_branch, started_at, ended_at)
           VALUES (?, ?, ?, ?, ?)
           ON CONFLICT(session_id) DO UPDATE SET ended_at = excluded.ended_at""",
        (session_id, project_path, git_branch, started_at, ended_at),
    )


def save_chunks(
    conn: sqlite3.Connection,
    chunks: list[dict],
) -> int:
    """Save a batch of chunk dicts. Returns number inserted."""
    count = 0
    for c in chunks:
        try:
            conn.execute(
                """INSERT INTO chunks (session_id, project_path, git_branch, chunk_index, role, content, created_at)
                   VALUES (?, ?, ?, ?, ?, ?, ?)""",
                (c["session_id"], c["project_path"], c["git_branch"],
                 c["chunk_index"], c["role"], c["content"], c["created_at"]),
            )
            count += 1
        except sqlite3.IntegrityError:
            pass  # duplicate (session_id, chunk_index)
    return count


def save_manual(
    conn: sqlite3.Connection,
    content: str,
    project_path: str = "",
) -> int:
    """Save a manually created memory chunk. Returns chunk id."""
    now = time.time()
    session_id = f"manual-{int(now)}"
    cur = conn.execute(
        """INSERT INTO chunks (session_id, project_path, git_branch, chunk_index, role, content, created_at)
           VALUES (?, ?, NULL, 0, 'manual', ?, ?)""",
        (session_id, project_path, content, now),
    )
    conn.commit()
    return cur.lastrowid


# --- Search ---

def _rows_to_chunks(rows) -> list[Chunk]:
    return [
        Chunk(
            id=r["id"], session_id=r["session_id"], project_path=r["project_path"],
            git_branch=r["git_branch"], chunk_index=r["chunk_index"],
            role=r["role"], content=r["content"], created_at=r["created_at"],
        )
        for r in rows
    ]


def fts5_search(conn: sqlite3.Connection, query: str, limit: int = 20) -> list[Chunk]:
    rows = conn.execute(
        """SELECT c.id, c.session_id, c.project_path, c.git_branch,
                  c.chunk_index, c.role, c.content, c.created_at
           FROM chunks_fts f
           JOIN chunks c ON c.id = f.rowid
           WHERE chunks_fts MATCH ?
           ORDER BY rank
           LIMIT ?""",
        (query, limit),
    ).fetchall()
    return _rows_to_chunks(rows)


def like_search(conn: sqlite3.Connection, query: str, limit: int = 20) -> list[Chunk]:
    rows = conn.execute(
        """SELECT id, session_id, project_path, git_branch,
                  chunk_index, role, content, created_at
           FROM chunks
           WHERE content LIKE ?
           ORDER BY created_at DESC
           LIMIT ?""",
        (f"%{query}%", limit),
    ).fetchall()
    return _rows_to_chunks(rows)


def apply_time_decay(
    chunks: list[Chunk], half_life_days: float = 30.0
) -> list[Chunk]:
    now = time.time()
    scored = []
    for chunk in chunks:
        age_days = (now - chunk.created_at) / 86400
        decay = 0.5 ** (age_days / half_life_days)
        scored.append((chunk, decay))
    scored.sort(key=lambda x: -x[1])
    return [c for c, _ in scored]


def search(
    conn: sqlite3.Connection,
    query: str,
    limit: int = 5,
    project_filter: str | None = None,
    max_age_days: int | None = None,
) -> list[Chunk]:
    time_filter = ""
    time_params: tuple = ()
    if max_age_days is not None:
        cutoff = time.time() - (max_age_days * 86400)
        time_filter = "AND created_at > ?"
        time_params = (cutoff,)

    if not query:
        # Empty query: return recent chunks
        results = get_recent_chunks(conn, project_filter or "", limit=20) if project_filter else []
        if not results:
            rows = conn.execute(
                f"SELECT id, session_id, project_path, git_branch, chunk_index, role, content, created_at "
                f"FROM chunks WHERE 1=1 {NOISE_FILTER} {time_filter} ORDER BY created_at DESC LIMIT ?",
                (*time_params, 20),
            ).fetchall()
            results = _rows_to_chunks(rows)
    elif len(query) < 3:
        results = like_search(conn, query, limit=20)
    else:
        try:
            results = fts5_search(conn, query, limit=20)
        except sqlite3.OperationalError:
            results = []
        # FTS5 unicode61 doesn't segment Japanese well; fall back to LIKE if no results
        if not results:
            results = like_search(conn, query, limit=20)

    if project_filter:
        results = [c for c in results if project_filter in c.project_path]

    results = apply_time_decay(results)
    return results[:limit]


# --- Recall (for SessionStart hook) ---

def get_recent_sessions(
    conn: sqlite3.Connection,
    project_path: str,
    limit: int = 3,
) -> list[dict]:
    rows = conn.execute(
        """SELECT session_id, project_path, git_branch, started_at, ended_at
           FROM sessions
           WHERE project_path = ?
           ORDER BY started_at DESC
           LIMIT ?""",
        (project_path, limit),
    ).fetchall()
    return [dict(r) for r in rows]


def get_recent_chunks(
    conn: sqlite3.Connection,
    project_path: str,
    limit: int = 10,
) -> list[Chunk]:
    rows = conn.execute(
        f"""SELECT id, session_id, project_path, git_branch,
                  chunk_index, role, content, created_at
           FROM chunks
           WHERE project_path = ?
           {NOISE_FILTER}
           ORDER BY created_at DESC
           LIMIT ?""",
        (project_path, limit),
    ).fetchall()
    return _rows_to_chunks(rows)


def recall(
    conn: sqlite3.Connection,
    project_path: str,
    limit: int = 5,
) -> str:
    """Build context string for SessionStart injection."""
    chunks = get_recent_chunks(conn, project_path, limit=limit)
    if not chunks:
        return ""

    lines = ["=== yasumem: 過去のセッション記憶 ===", ""]
    for chunk in chunks:
        ts = time.strftime("%m/%d %H:%M", time.localtime(chunk.created_at))
        role_label = "User" if chunk.role == "user" else "Assistant"
        branch = f" [{chunk.git_branch}]" if chunk.git_branch else ""
        preview = chunk.content[:200] + ("..." if len(chunk.content) > 200 else "")
        lines.append(f"- [{ts}{branch}] {role_label}: {preview}")

    return "\n".join(lines)


# --- Maintenance ---

def prune_old_chunks(conn: sqlite3.Connection, max_age_days: int = 90) -> int:
    cutoff = time.time() - (max_age_days * 86400)
    cur = conn.execute("DELETE FROM chunks WHERE created_at < ?", (cutoff,))
    conn.execute("DELETE FROM sessions WHERE ended_at IS NOT NULL AND ended_at < ?", (cutoff,))
    conn.commit()
    conn.execute("PRAGMA incremental_vacuum")
    return cur.rowcount
