package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxChunkLength = 2000
const maxSessionsPerWorktree = 3

var claudeProjectsDir = filepath.Join(os.Getenv("HOME"), ".claude", "projects")

func encodeCwd(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
}

type jsonlEntry struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	GitBranch string          `json:"gitBranch"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type messageContent struct {
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

func extractTextContent(raw json.RawMessage) string {
	// Try as string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try as array of blocks
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "tool_use":
				parts = append(parts, fmt.Sprintf("[Tool: %s]", b.Name))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

var sentenceSplitRe = regexp.MustCompile(`[。.!?！？\n]`)

func splitChunk(text string) []string {
	if len(text) <= maxChunkLength {
		return []string{text}
	}
	// Split after sentence-ending characters by finding their positions
	indices := sentenceSplitRe.FindAllStringIndex(text, -1)
	if len(indices) == 0 {
		// No sentence boundaries; split by length
		var chunks []string
		for len(text) > maxChunkLength {
			chunks = append(chunks, text[:maxChunkLength])
			text = text[maxChunkLength:]
		}
		if text != "" {
			chunks = append(chunks, text)
		}
		return chunks
	}

	var chunks []string
	current := ""
	prev := 0
	for _, idx := range indices {
		// Segment includes the delimiter (split AFTER the punctuation)
		seg := text[prev:idx[1]]
		if len(current)+len(seg) > maxChunkLength && current != "" {
			chunks = append(chunks, current)
			current = seg
		} else {
			current += seg
		}
		prev = idx[1]
	}
	// Remaining text after last delimiter
	if prev < len(text) {
		current += text[prev:]
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}

type sessionMeta struct {
	SessionID   string
	ProjectPath string
	GitBranch   string
	StartedAt   float64
	EndedAt     float64
}

func parseJsonl(path string) (sessionMeta, []Chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return sessionMeta{}, nil, err
	}
	defer f.Close()

	var meta sessionMeta
	var chunks []Chunk
	chunkIndex := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry jsonlEntry
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}

		if meta.SessionID == "" {
			meta.SessionID = entry.SessionID
			meta.ProjectPath = entry.Cwd
			meta.GitBranch = entry.GitBranch
		}

		var ts float64
		if entry.Timestamp != "" {
			t, err := time.Parse(time.RFC3339, strings.Replace(entry.Timestamp, "Z", "+00:00", 1))
			if err == nil {
				ts = float64(t.Unix())
			}
		}
		if ts == 0 {
			ts = float64(time.Now().Unix())
		}
		if meta.StartedAt == 0 || ts < meta.StartedAt {
			meta.StartedAt = ts
		}
		if ts > meta.EndedAt {
			meta.EndedAt = ts
		}

		var msg messageContent
		if json.Unmarshal(entry.Message, &msg) != nil {
			continue
		}
		text := extractTextContent(msg.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}

		for _, part := range splitChunk(text) {
			chunks = append(chunks, Chunk{
				SessionID:   meta.SessionID,
				ProjectPath: meta.ProjectPath,
				GitBranch:   sql.NullString{String: meta.GitBranch, Valid: meta.GitBranch != ""},
				ChunkIndex:  chunkIndex,
				Role:        entry.Type,
				Content:     part,
				CreatedAt:   ts,
			})
			chunkIndex++
		}
	}
	return meta, chunks, scanner.Err()
}

func findJsonl(sessionID, cwd string) string {
	encoded := encodeCwd(cwd)
	projectDir := filepath.Join(claudeProjectsDir, encoded)

	// Try exact match
	p := filepath.Join(projectDir, sessionID+".jsonl")
	if _, err := os.Stat(p); err == nil {
		return p
	}

	// Try subdirectories
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			candidate := filepath.Join(projectDir, e.Name(), sessionID+".jsonl")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}

	// Try session_id as directory name
	sessionDir := filepath.Join(projectDir, sessionID)
	if entries, err := os.ReadDir(sessionDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				return filepath.Join(sessionDir, e.Name())
			}
		}
	}
	return ""
}

// --- Ingest command (single session from stdin) ---

func runIngest() {
	var input struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintln(os.Stderr, "Error: invalid JSON on stdin")
		os.Exit(1)
	}
	if input.SessionID == "" || input.Cwd == "" {
		fmt.Fprintln(os.Stderr, "Error: missing session_id or cwd")
		os.Exit(1)
	}

	dbPath := getDBPath()
	db, err := openDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "DB error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if sessionExists(db, input.SessionID) {
		return
	}

	jsonlPath := findJsonl(input.SessionID, input.Cwd)
	if jsonlPath == "" {
		fmt.Fprintf(os.Stderr, "Warning: JSONL not found for session %s\n", input.SessionID)
		return
	}

	meta, chunks, err := parseJsonl(jsonlPath)
	if err != nil || len(chunks) == 0 {
		return
	}

	if meta.SessionID == "" {
		meta.SessionID = input.SessionID
	}
	if meta.ProjectPath == "" {
		meta.ProjectPath = input.Cwd
	}

	canonical := resolveCanonicalProject(input.Cwd)
	meta.ProjectPath = canonical
	for i := range chunks {
		chunks[i].ProjectPath = canonical
	}

	startedAt := meta.StartedAt
	if startedAt == 0 {
		startedAt = float64(time.Now().Unix())
	}
	var endedAt sql.NullFloat64
	if meta.EndedAt > 0 {
		endedAt = sql.NullFloat64{Float64: meta.EndedAt, Valid: true}
	}

	saveSession(db, meta.SessionID, meta.ProjectPath,
		sql.NullString{String: meta.GitBranch, Valid: meta.GitBranch != ""},
		startedAt, endedAt)
	count, _ := saveChunks(db, chunks)
	fmt.Fprintf(os.Stderr, "Saved %d chunks from session %s\n", count, input.SessionID)

	if pruned := pruneOldChunks(db, 90); pruned > 0 {
		fmt.Fprintf(os.Stderr, "Pruned %d old chunks\n", pruned)
	}
}

// --- Ingest-recent command (SessionStart) ---

func getWorktreePaths(cwd string) []string {
	out, err := exec.Command("git", "-C", cwd, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return []string{cwd}
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimPrefix(line, "worktree "))
		}
	}
	if len(paths) == 0 {
		return []string{cwd}
	}
	return paths
}

type jsonlFile struct {
	path    string
	modTime time.Time
}

func findRecentJsonls(worktreePaths []string) []string {
	var all []jsonlFile
	for _, wt := range worktreePaths {
		encoded := encodeCwd(wt)
		projectDir := filepath.Join(claudeProjectsDir, encoded)
		entries, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				info, err := e.Info()
				if err != nil {
					continue
				}
				all = append(all, jsonlFile{
					path:    filepath.Join(projectDir, e.Name()),
					modTime: info.ModTime(),
				})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].modTime.After(all[j].modTime) })
	var result []string
	for i, f := range all {
		if i >= maxSessionsPerWorktree {
			break
		}
		result = append(result, f.path)
	}
	return result
}

func runIngestRecent() {
	dbPath := getDBPath()

	var input struct {
		Cwd string `json:"cwd"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil || input.Cwd == "" {
		os.Exit(0)
	}

	canonical := resolveCanonicalProject(input.Cwd)
	worktreePaths := getWorktreePaths(input.Cwd)

	db, err := openDB(dbPath)
	if err != nil {
		os.Exit(0)
	}
	defer db.Close()

	ingested := 0
	for _, jsonlPath := range findRecentJsonls(worktreePaths) {
		sessionID := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")
		meta, chunks, err := parseJsonl(jsonlPath)
		if err != nil || len(chunks) == 0 {
			continue
		}
		if meta.SessionID == "" {
			meta.SessionID = sessionID
		}
		for i := range chunks {
			chunks[i].ProjectPath = canonical
		}

		startedAt := meta.StartedAt
		if startedAt == 0 {
			startedAt = float64(time.Now().Unix())
		}
		var endedAt sql.NullFloat64
		if meta.EndedAt > 0 {
			endedAt = sql.NullFloat64{Float64: meta.EndedAt, Valid: true}
		}

		saveSession(db, meta.SessionID, canonical,
			sql.NullString{String: meta.GitBranch, Valid: meta.GitBranch != ""},
			startedAt, endedAt)
		count, _ := saveChunks(db, chunks)
		ingested++
		fmt.Fprintf(os.Stderr, "Ingested %d chunks from %s\n", count, sessionID)
	}

	if ingested > 0 {
		if pruned := pruneOldChunks(db, 90); pruned > 0 {
			fmt.Fprintf(os.Stderr, "Pruned %d old chunks\n", pruned)
		}
	}
}
