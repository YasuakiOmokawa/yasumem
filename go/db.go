package main

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaSql = `
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
`

const noiseFilter = `AND content NOT LIKE '<local-command%' AND content NOT LIKE '<command-name>%' AND content NOT LIKE '[Tool:%'`

type Chunk struct {
	ID          int64
	SessionID   string
	ProjectPath string
	GitBranch   sql.NullString
	ChunkIndex  int
	Role        string
	Content     string
	CreatedAt   float64
}

func resolveCanonicalProject(cwd string) string {
	out, err := exec.Command("git", "-C", cwd, "worktree", "list", "--porcelain").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "worktree ") {
				return strings.TrimPrefix(line, "worktree ")
			}
		}
	}
	return cwd
}

func migrateFtsTokenizer(db *sql.DB) {
	var ddl sql.NullString
	_ = db.QueryRow("SELECT sql FROM sqlite_master WHERE name='chunks_fts' AND type='table'").Scan(&ddl)
	if ddl.Valid && !strings.Contains(ddl.String, "trigram") {
		db.Exec("DROP TABLE IF EXISTS chunks_fts")
		db.Exec("DROP TRIGGER IF EXISTS chunks_ai")
		db.Exec("DROP TRIGGER IF EXISTS chunks_ad")
		db.Exec("DROP TRIGGER IF EXISTS chunks_au")
	}
}

func migrateSessionsAddIngestState(db *sql.DB) {
	db.Exec("ALTER TABLE sessions ADD COLUMN last_byte_offset INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE sessions ADD COLUMN last_chunk_index INTEGER NOT NULL DEFAULT -1")
}

func openDB(dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	migrateFtsTokenizer(db)
	migrateSessionsAddIngestState(db)
	if _, err := db.Exec(schemaSql); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema init: %w", err)
	}
	db.Exec("INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')")
	db.Exec("INSERT INTO lessons_fts(lessons_fts) VALUES('rebuild')")
	return db, nil
}

// --- CRUD ---

func sessionExists(db *sql.DB, sessionID string) bool {
	var n int
	err := db.QueryRow("SELECT 1 FROM sessions WHERE session_id = ?", sessionID).Scan(&n)
	return err == nil
}

type sessionIngestState struct {
	ByteOffset int64
	ChunkIndex int
}

func getSessionIngestState(db *sql.DB, sessionID string) sessionIngestState {
	var bo sql.NullInt64
	var ci sql.NullInt64
	db.QueryRow("SELECT last_byte_offset, last_chunk_index FROM sessions WHERE session_id = ?", sessionID).Scan(&bo, &ci)
	state := sessionIngestState{ChunkIndex: -1}
	if bo.Valid {
		state.ByteOffset = bo.Int64
	}
	if ci.Valid {
		state.ChunkIndex = int(ci.Int64)
	}
	return state
}

func saveSession(db *sql.DB, sessionID, projectPath string, gitBranch sql.NullString, startedAt float64, endedAt sql.NullFloat64) error {
	_, err := db.Exec(
		`INSERT INTO sessions (session_id, project_path, git_branch, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET ended_at = excluded.ended_at`,
		sessionID, projectPath, gitBranch, startedAt, endedAt,
	)
	return err
}

func updateSessionIngestState(db *sql.DB, sessionID string, byteOffset int64, chunkIndex int) {
	db.Exec(
		`UPDATE sessions SET last_byte_offset = ?, last_chunk_index = ? WHERE session_id = ?`,
		byteOffset, chunkIndex, sessionID,
	)
}

func saveChunks(db *sql.DB, chunks []Chunk) (int, error) {
	count := 0
	for _, c := range chunks {
		_, err := db.Exec(
			`INSERT INTO chunks (session_id, project_path, git_branch, chunk_index, role, content, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.SessionID, c.ProjectPath, c.GitBranch, c.ChunkIndex, c.Role, c.Content, c.CreatedAt,
		)
		if err == nil {
			count++
		}
		// duplicate (session_id, chunk_index) → skip
	}
	return count, nil
}

func saveManual(db *sql.DB, content string) (int64, error) {
	now := float64(time.Now().Unix())
	sessionID := fmt.Sprintf("manual-%d", int64(now))
	res, err := db.Exec(
		`INSERT INTO chunks (session_id, project_path, git_branch, chunk_index, role, content, created_at)
		 VALUES (?, '', NULL, 0, 'manual', ?, ?)`,
		sessionID, content, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// --- Search ---

func scanChunks(rows *sql.Rows) ([]Chunk, error) {
	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.SessionID, &c.ProjectPath, &c.GitBranch,
			&c.ChunkIndex, &c.Role, &c.Content, &c.CreatedAt); err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func fts5Search(db *sql.DB, query string, limit int) ([]Chunk, error) {
	rows, err := db.Query(
		`SELECT c.id, c.session_id, c.project_path, c.git_branch,
		        c.chunk_index, c.role, c.content, c.created_at
		 FROM chunks_fts f JOIN chunks c ON c.id = f.rowid
		 WHERE chunks_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

func likeSearch(db *sql.DB, query string, limit int) ([]Chunk, error) {
	rows, err := db.Query(
		`SELECT id, session_id, project_path, git_branch,
		        chunk_index, role, content, created_at
		 FROM chunks WHERE content LIKE ? ORDER BY created_at DESC LIMIT ?`,
		"%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

func applyTimeDecay(chunks []Chunk, halfLifeDays float64) []Chunk {
	now := float64(time.Now().Unix())
	type scored struct {
		chunk Chunk
		score float64
	}
	ss := make([]scored, len(chunks))
	for i, c := range chunks {
		ageDays := (now - c.CreatedAt) / 86400
		ss[i] = scored{c, math.Pow(0.5, ageDays/halfLifeDays)}
	}
	// sort descending by score
	for i := 0; i < len(ss); i++ {
		for j := i + 1; j < len(ss); j++ {
			if ss[j].score > ss[i].score {
				ss[i], ss[j] = ss[j], ss[i]
			}
		}
	}
	result := make([]Chunk, len(ss))
	for i, s := range ss {
		result[i] = s.chunk
	}
	return result
}

func recentChunks(db *sql.DB, projectFilter string, maxAgeDays int, limit int) ([]Chunk, error) {
	q := fmt.Sprintf(`SELECT id, session_id, project_path, git_branch,
	        chunk_index, role, content, created_at
	 FROM chunks WHERE 1=1 %s`, noiseFilter)
	var args []interface{}

	if maxAgeDays > 0 {
		cutoff := float64(time.Now().Unix()) - float64(maxAgeDays)*86400
		q += " AND created_at > ?"
		args = append(args, cutoff)
	}
	if projectFilter != "" {
		q += " AND project_path LIKE ?"
		args = append(args, "%"+projectFilter+"%")
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

func search(db *sql.DB, query string, limit int, projectFilter string, maxAgeDays int) ([]Chunk, error) {
	var results []Chunk
	var err error

	if query == "" {
		results, err = recentChunks(db, projectFilter, maxAgeDays, limit)
		if err != nil {
			return nil, err
		}
		return results, nil
	}

	fetchLimit := limit * 4
	if fetchLimit < 20 {
		fetchLimit = 20
	}
	if len(query) < 3 {
		results, err = likeSearch(db, query, fetchLimit)
	} else {
		results, err = fts5Search(db, query, fetchLimit)
		if err != nil || len(results) == 0 {
			results, err = likeSearch(db, query, fetchLimit)
		}
	}
	if err != nil {
		return nil, err
	}

	if projectFilter != "" {
		filtered := results[:0]
		for _, c := range results {
			if strings.Contains(c.ProjectPath, projectFilter) {
				filtered = append(filtered, c)
			}
		}
		results = filtered
	}

	if maxAgeDays > 0 {
		cutoff := float64(time.Now().Unix()) - float64(maxAgeDays)*86400
		filtered := results[:0]
		for _, c := range results {
			if c.CreatedAt > cutoff {
				filtered = append(filtered, c)
			}
		}
		results = filtered
	}

	results = applyTimeDecay(results, 30.0)
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// --- Maintenance ---

func pruneOldChunks(db *sql.DB, maxAgeDays int) int {
	cutoff := float64(time.Now().Unix()) - float64(maxAgeDays)*86400
	res, _ := db.Exec("DELETE FROM chunks WHERE created_at < ?", cutoff)
	db.Exec("DELETE FROM sessions WHERE ended_at IS NOT NULL AND ended_at < ?", cutoff)
	db.Exec("PRAGMA incremental_vacuum")
	n, _ := res.RowsAffected()
	return int(n)
}
