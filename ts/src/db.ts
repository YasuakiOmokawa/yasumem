import Database from "better-sqlite3";
import { mkdirSync } from "node:fs";
import { dirname } from "node:path";

export type DB = Database.Database;

export const NOISE_FILTER =
  `AND content NOT LIKE '<local-command%' ` +
  `AND content NOT LIKE '<command-name>%' ` +
  `AND content NOT LIKE '[Tool:%' ` +
  `AND content NOT LIKE '<system-reminder>%' ` +
  `AND content NOT LIKE '<available-deferred-tools>%' ` +
  `AND content NOT LIKE 'Tool loaded.%' ` +
  `AND content NOT LIKE '<functions>%'`;

const SCHEMA_SQL = `
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

CREATE TABLE IF NOT EXISTS lessons (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    category TEXT NOT NULL DEFAULT 'lesson',
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    project_path TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    source_ref TEXT NOT NULL DEFAULT '',
    recall_count INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS lessons_fts USING fts5(
    title, content, tags,
    content=lessons,
    content_rowid=id,
    tokenize='trigram'
);

CREATE TRIGGER IF NOT EXISTS lessons_ai AFTER INSERT ON lessons BEGIN
    INSERT INTO lessons_fts(rowid, title, content, tags) VALUES (new.id, new.title, new.content, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS lessons_ad AFTER DELETE ON lessons BEGIN
    INSERT INTO lessons_fts(lessons_fts, rowid, title, content, tags) VALUES ('delete', old.id, old.title, old.content, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS lessons_au AFTER UPDATE ON lessons BEGIN
    INSERT INTO lessons_fts(lessons_fts, rowid, title, content, tags) VALUES ('delete', old.id, old.title, old.content, old.tags);
    INSERT INTO lessons_fts(rowid, title, content, tags) VALUES (new.id, new.title, new.content, new.tags);
END;

CREATE INDEX IF NOT EXISTS idx_lessons_category ON lessons(category);
CREATE INDEX IF NOT EXISTS idx_lessons_project ON lessons(project_path);
CREATE INDEX IF NOT EXISTS idx_lessons_created ON lessons(created_at);
CREATE INDEX IF NOT EXISTS idx_lessons_recall_count ON lessons(recall_count);

CREATE TABLE IF NOT EXISTS persona_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    persona TEXT NOT NULL DEFAULT 'subaru',
    content TEXT NOT NULL,
    scene_type TEXT NOT NULL DEFAULT '',
    mood TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '',
    recall_count INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS persona_memories_fts USING fts5(
    content, tags,
    content=persona_memories,
    content_rowid=id,
    tokenize='trigram'
);

CREATE TRIGGER IF NOT EXISTS persona_memories_ai AFTER INSERT ON persona_memories BEGIN
    INSERT INTO persona_memories_fts(rowid, content, tags) VALUES (new.id, new.content, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS persona_memories_ad AFTER DELETE ON persona_memories BEGIN
    INSERT INTO persona_memories_fts(persona_memories_fts, rowid, content, tags) VALUES ('delete', old.id, old.content, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS persona_memories_au AFTER UPDATE ON persona_memories BEGIN
    INSERT INTO persona_memories_fts(persona_memories_fts, rowid, content, tags) VALUES ('delete', old.id, old.content, old.tags);
    INSERT INTO persona_memories_fts(rowid, content, tags) VALUES (new.id, new.content, new.tags);
END;

CREATE INDEX IF NOT EXISTS idx_persona_memories_persona ON persona_memories(persona);
CREATE INDEX IF NOT EXISTS idx_persona_memories_scene_type ON persona_memories(scene_type);
CREATE INDEX IF NOT EXISTS idx_persona_memories_created ON persona_memories(created_at);
`;

function migrateFtsTokenizer(db: DB): void {
  const row = db
    .prepare(
      "SELECT sql FROM sqlite_master WHERE name='chunks_fts' AND type='table'",
    )
    .get() as { sql?: string } | undefined;
  if (row?.sql && !row.sql.includes("trigram")) {
    db.exec("DROP TABLE IF EXISTS chunks_fts");
    db.exec("DROP TRIGGER IF EXISTS chunks_ai");
    db.exec("DROP TRIGGER IF EXISTS chunks_ad");
    db.exec("DROP TRIGGER IF EXISTS chunks_au");
  }
}

function migrateSessionsAddIngestState(db: DB): void {
  for (const stmt of [
    "ALTER TABLE sessions ADD COLUMN last_byte_offset INTEGER NOT NULL DEFAULT 0",
    "ALTER TABLE sessions ADD COLUMN last_chunk_index INTEGER NOT NULL DEFAULT -1",
  ]) {
    try {
      db.exec(stmt);
    } catch {
      // column may already exist
    }
  }
}

export function openDB(dbPath: string): DB {
  mkdirSync(dirname(dbPath), { recursive: true });
  const db = new Database(dbPath);
  migrateFtsTokenizer(db);
  migrateSessionsAddIngestState(db);
  db.exec(SCHEMA_SQL);
  for (const stmt of [
    "INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')",
    "INSERT INTO lessons_fts(lessons_fts) VALUES('rebuild')",
    "INSERT INTO persona_memories_fts(persona_memories_fts) VALUES('rebuild')",
  ]) {
    try {
      db.exec(stmt);
    } catch {
      // rebuild may fail on first init; safe to ignore
    }
  }
  return db;
}
