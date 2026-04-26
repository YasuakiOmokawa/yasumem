import type { DB } from "./db.js";
import { NOISE_FILTER } from "./db.js";

export interface Chunk {
  id: number;
  session_id: string;
  project_path: string;
  git_branch: string | null;
  chunk_index: number;
  role: string;
  content: string;
  created_at: number;
}

export interface SessionIngestState {
  byteOffset: number;
  chunkIndex: number;
}

export function sessionExists(db: DB, sessionID: string): boolean {
  const row = db
    .prepare("SELECT 1 FROM sessions WHERE session_id = ?")
    .get(sessionID);
  return row !== undefined;
}

export function getSessionIngestState(
  db: DB,
  sessionID: string,
): SessionIngestState {
  const row = db
    .prepare(
      "SELECT last_byte_offset, last_chunk_index FROM sessions WHERE session_id = ?",
    )
    .get(sessionID) as
    | { last_byte_offset: number | null; last_chunk_index: number | null }
    | undefined;
  return {
    byteOffset: row?.last_byte_offset ?? 0,
    chunkIndex: row?.last_chunk_index ?? -1,
  };
}

export function saveSession(
  db: DB,
  sessionID: string,
  projectPath: string,
  gitBranch: string | null,
  startedAt: number,
  endedAt: number | null,
): void {
  db.prepare(
    `INSERT INTO sessions (session_id, project_path, git_branch, started_at, ended_at)
     VALUES (?, ?, ?, ?, ?)
     ON CONFLICT(session_id) DO UPDATE SET ended_at = excluded.ended_at`,
  ).run(sessionID, projectPath, gitBranch, startedAt, endedAt);
}

export function updateSessionIngestState(
  db: DB,
  sessionID: string,
  byteOffset: number,
  chunkIndex: number,
): void {
  db.prepare(
    "UPDATE sessions SET last_byte_offset = ?, last_chunk_index = ? WHERE session_id = ?",
  ).run(byteOffset, chunkIndex, sessionID);
}

export function saveChunks(db: DB, chunks: Omit<Chunk, "id">[]): number {
  const stmt = db.prepare(
    `INSERT INTO chunks (session_id, project_path, git_branch, chunk_index, role, content, created_at)
     VALUES (?, ?, ?, ?, ?, ?, ?)`,
  );
  let count = 0;
  const txn = db.transaction((rows: Omit<Chunk, "id">[]) => {
    for (const c of rows) {
      try {
        stmt.run(
          c.session_id,
          c.project_path,
          c.git_branch,
          c.chunk_index,
          c.role,
          c.content,
          c.created_at,
        );
        count++;
      } catch {
        // duplicate (session_id, chunk_index) → skip
      }
    }
  });
  txn(chunks);
  return count;
}

export function saveManual(db: DB, content: string): number {
  const now = Math.floor(Date.now() / 1000);
  const sessionID = `manual-${now}`;
  const result = db
    .prepare(
      `INSERT INTO chunks (session_id, project_path, git_branch, chunk_index, role, content, created_at)
       VALUES (?, '', NULL, 0, 'manual', ?, ?)`,
    )
    .run(sessionID, content, now);
  return Number(result.lastInsertRowid);
}

function fts5Search(db: DB, query: string, limit: number): Chunk[] {
  return db
    .prepare(
      `SELECT c.id, c.session_id, c.project_path, c.git_branch,
              c.chunk_index, c.role, c.content, c.created_at
       FROM chunks_fts f JOIN chunks c ON c.id = f.rowid
       WHERE chunks_fts MATCH ? ORDER BY rank LIMIT ?`,
    )
    .all(query, limit) as Chunk[];
}

function likeSearch(db: DB, query: string, limit: number): Chunk[] {
  return db
    .prepare(
      `SELECT id, session_id, project_path, git_branch,
              chunk_index, role, content, created_at
       FROM chunks WHERE content LIKE ? ORDER BY created_at DESC LIMIT ?`,
    )
    .all(`%${query}%`, limit) as Chunk[];
}

function recentChunks(
  db: DB,
  projectFilter: string,
  maxAgeDays: number,
  limit: number,
): Chunk[] {
  let q = `SELECT id, session_id, project_path, git_branch,
            chunk_index, role, content, created_at
     FROM chunks WHERE 1=1 ${NOISE_FILTER}`;
  const args: (string | number)[] = [];
  if (maxAgeDays > 0) {
    const cutoff = Math.floor(Date.now() / 1000) - maxAgeDays * 86400;
    q += " AND created_at > ?";
    args.push(cutoff);
  }
  if (projectFilter !== "") {
    q += " AND project_path LIKE ?";
    args.push(`%${projectFilter}%`);
  }
  q += " ORDER BY created_at DESC LIMIT ?";
  args.push(limit);
  return db.prepare(q).all(...args) as Chunk[];
}

export function search(
  db: DB,
  query: string,
  limit: number,
  projectFilter: string,
  maxAgeDays: number,
): Chunk[] {
  if (query === "") {
    return recentChunks(db, projectFilter, maxAgeDays, limit);
  }

  const fetchLimit = Math.max(limit * 4, 20);
  let results: Chunk[];
  if (query.length < 3) {
    results = likeSearch(db, query, fetchLimit);
  } else {
    try {
      results = fts5Search(db, query, fetchLimit);
      if (results.length === 0) {
        results = likeSearch(db, query, fetchLimit);
      }
    } catch {
      results = likeSearch(db, query, fetchLimit);
    }
  }

  if (projectFilter !== "") {
    results = results.filter((c) => c.project_path.includes(projectFilter));
  }
  if (maxAgeDays > 0) {
    const cutoff = Math.floor(Date.now() / 1000) - maxAgeDays * 86400;
    results = results.filter((c) => c.created_at > cutoff);
  }
  if (results.length > limit) {
    results = results.slice(0, limit);
  }
  return results;
}

export function pruneOldChunks(db: DB, maxAgeDays: number): number {
  const cutoff = Math.floor(Date.now() / 1000) - maxAgeDays * 86400;
  const result = db
    .prepare("DELETE FROM chunks WHERE created_at < ?")
    .run(cutoff);
  db.prepare(
    "DELETE FROM sessions WHERE ended_at IS NOT NULL AND ended_at < ?",
  ).run(cutoff);
  try {
    db.exec("PRAGMA incremental_vacuum");
  } catch {
    // ignore
  }
  return result.changes;
}
