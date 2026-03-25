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
	if _, err := db.Exec(schemaSql); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema init: %w", err)
	}
	db.Exec("INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')")
	return db, nil
}

// --- CRUD ---

func sessionExists(db *sql.DB, sessionID string) bool {
	var n int
	err := db.QueryRow("SELECT 1 FROM sessions WHERE session_id = ?", sessionID).Scan(&n)
	return err == nil
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

func search(db *sql.DB, query string, limit int, projectFilter string, maxAgeDays int) ([]Chunk, error) {
	timeFilter := ""
	var timeArgs []any
	if maxAgeDays > 0 {
		cutoff := float64(time.Now().Unix()) - float64(maxAgeDays)*86400
		timeFilter = "AND created_at > ?"
		timeArgs = append(timeArgs, cutoff)
	}

	var results []Chunk
	var err error

	if query == "" {
		if projectFilter != "" {
			results, err = getRecentChunks(db, projectFilter, 20)
			if err != nil {
				return nil, err
			}
		}
		if len(results) == 0 {
			q := fmt.Sprintf(
				"SELECT id, session_id, project_path, git_branch, chunk_index, role, content, created_at FROM chunks WHERE 1=1 %s %s ORDER BY created_at DESC LIMIT ?",
				noiseFilter, timeFilter)
			args := append(timeArgs, 20)
			rows, err := db.Query(q, args...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			results, err = scanChunks(rows)
			if err != nil {
				return nil, err
			}
		}
	} else if len(query) < 3 {
		results, err = likeSearch(db, query, 20)
	} else {
		results, err = fts5Search(db, query, 20)
		if err != nil || len(results) == 0 {
			results, err = likeSearch(db, query, 20)
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

	results = applyTimeDecay(results, 30.0)
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// --- Recall ---

func getRecentChunks(db *sql.DB, projectPath string, limit int) ([]Chunk, error) {
	rows, err := db.Query(
		fmt.Sprintf(`SELECT id, session_id, project_path, git_branch,
		        chunk_index, role, content, created_at
		 FROM chunks WHERE project_path = ? %s ORDER BY created_at DESC LIMIT ?`, noiseFilter),
		projectPath, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

func recall(db *sql.DB, projectPath string, limit int) string {
	chunks, err := getRecentChunks(db, projectPath, limit)
	if err != nil || len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("=== yasumem: 過去のセッション記憶 ===\n\n")
	for _, c := range chunks {
		t := time.Unix(int64(c.CreatedAt), 0)
		ts := t.Format("01/02 15:04")
		role := "Assistant"
		if c.Role == "user" {
			role = "User"
		}
		branch := ""
		if c.GitBranch.Valid && c.GitBranch.String != "" {
			branch = " [" + c.GitBranch.String + "]"
		}
		preview := c.Content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Fprintf(&b, "- [%s%s] %s: %s\n", ts, branch, role, preview)
	}
	return b.String()
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
