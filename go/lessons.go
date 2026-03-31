package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type Lesson struct {
	ID          int64
	Category    string
	Title       string
	Content     string
	ProjectPath string
	Tags        string
	Source      string
	SourceRef   string
	RecallCount int
	CreatedAt   float64
	UpdatedAt   float64
}

func scanLessons(rows *sql.Rows) ([]Lesson, error) {
	var lessons []Lesson
	for rows.Next() {
		var l Lesson
		if err := rows.Scan(&l.ID, &l.Category, &l.Title, &l.Content,
			&l.ProjectPath, &l.Tags, &l.Source, &l.SourceRef,
			&l.RecallCount, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		lessons = append(lessons, l)
	}
	return lessons, rows.Err()
}

func normalizeTags(tags string) string {
	parts := strings.Split(tags, ",")
	var normalized []string
	for _, p := range parts {
		t := strings.TrimSpace(strings.ToLower(p))
		if t != "" {
			normalized = append(normalized, t)
		}
	}
	return strings.Join(normalized, ",")
}

func saveLesson(db *sql.DB, l Lesson) (int64, error) {
	now := float64(time.Now().Unix())
	l.Tags = normalizeTags(l.Tags)
	res, err := db.Exec(
		`INSERT INTO lessons (category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		l.Category, l.Title, l.Content, l.ProjectPath, l.Tags, l.Source, l.SourceRef, now, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func updateLesson(db *sql.DB, id int64, title, content, category, tags *string) error {
	now := float64(time.Now().Unix())

	setClauses := []string{"updated_at = ?"}
	args := []any{now}

	if title != nil {
		setClauses = append(setClauses, "title = ?")
		args = append(args, *title)
	}
	if content != nil {
		setClauses = append(setClauses, "content = ?")
		args = append(args, *content)
	}
	if category != nil {
		setClauses = append(setClauses, "category = ?")
		args = append(args, *category)
	}
	if tags != nil {
		normalized := normalizeTags(*tags)
		setClauses = append(setClauses, "tags = ?")
		args = append(args, normalized)
	}

	args = append(args, id)
	query := fmt.Sprintf("UPDATE lessons SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	res, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("lesson not found: %d", id)
	}
	return nil
}

func getLessonByID(db *sql.DB, id int64) (*Lesson, error) {
	row := db.QueryRow(
		`SELECT id, category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at
		 FROM lessons WHERE id = ?`, id)
	var l Lesson
	err := row.Scan(&l.ID, &l.Category, &l.Title, &l.Content,
		&l.ProjectPath, &l.Tags, &l.Source, &l.SourceRef,
		&l.RecallCount, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func deleteLesson(db *sql.DB, id int64) error {
	res, err := db.Exec("DELETE FROM lessons WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("lesson not found: %d", id)
	}
	return nil
}

func listLessons(db *sql.DB, projectPath, category string, limit int) ([]Lesson, error) {
	query := `SELECT id, category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at
		 FROM lessons WHERE 1=1`
	var args []any

	if projectPath != "" {
		query += " AND (project_path = ? OR project_path = '')"
		args = append(args, projectPath)
	}
	if category != "" {
		query += " AND category = ?"
		args = append(args, category)
	}

	query += " ORDER BY recall_count DESC, created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLessons(rows)
}

func searchLessons(db *sql.DB, query, projectPath, category string, limit int) ([]Lesson, error) {
	var results []Lesson
	var err error

	if len(query) >= 3 {
		results, err = fts5SearchLessons(db, query, limit*2)
		if err != nil || len(results) == 0 {
			results, err = likeSearchLessons(db, query, limit*2)
		}
	} else {
		results, err = likeSearchLessons(db, query, limit*2)
	}
	if err != nil {
		return nil, err
	}

	// Apply filters
	filtered := results[:0]
	for _, l := range results {
		if projectPath != "" && l.ProjectPath != "" && l.ProjectPath != projectPath {
			continue
		}
		if category != "" && l.Category != category {
			continue
		}
		filtered = append(filtered, l)
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func fts5SearchLessons(db *sql.DB, query string, limit int) ([]Lesson, error) {
	rows, err := db.Query(
		`SELECT l.id, l.category, l.title, l.content, l.project_path, l.tags, l.source, l.source_ref, l.recall_count, l.created_at, l.updated_at
		 FROM lessons_fts f JOIN lessons l ON l.id = f.rowid
		 WHERE lessons_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLessons(rows)
}

func likeSearchLessons(db *sql.DB, query string, limit int) ([]Lesson, error) {
	pattern := "%" + query + "%"
	rows, err := db.Query(
		`SELECT id, category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at
		 FROM lessons WHERE title LIKE ? OR content LIKE ? OR tags LIKE ?
		 ORDER BY recall_count DESC, created_at DESC LIMIT ?`,
		pattern, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLessons(rows)
}

func incrementRecallCount(db *sql.DB, ids []int64) {
	if len(ids) == 0 {
		return
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("UPDATE lessons SET recall_count = recall_count + 1 WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	db.Exec(query, args...)
}

func categoryLabel(category string) string {
	switch category {
	case "review_feedback":
		return "レビュー指摘"
	case "design_decision":
		return "設計決定"
	case "lesson_learned":
		return "学び"
	case "pattern":
		return "パターン"
	case "mistake":
		return "失敗"
	default:
		return category
	}
}

func searchLessonsByTag(db *sql.DB, tag, projectPath string, limit int) ([]Lesson, error) {
	rows, err := db.Query(
		`SELECT id, category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at
		 FROM lessons
		 WHERE tags LIKE ?
		 AND (project_path = ? OR project_path = '')
		 ORDER BY recall_count DESC, created_at DESC LIMIT ?`,
		"%"+tag+"%", projectPath, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLessons(rows)
}

// extractSearchSegments splits text into overlapping rune-based segments
// for trigram-friendly FTS5 search. Full strings don't match paraphrased text,
// but short segments (e.g. "設計に固") can match across similar titles.
func extractSearchSegments(text string, segLen int) []string {
	runes := []rune(text)
	if len(runes) <= segLen {
		if len(runes) >= 3 {
			return []string{text}
		}
		return nil
	}
	var segments []string
	step := segLen
	for i := 0; i+segLen <= len(runes); i += step {
		segments = append(segments, string(runes[i:i+segLen]))
	}
	return segments
}

func findSimilarLessons(db *sql.DB, title, content, projectPath string, excludeID int64, limit int) ([]Lesson, error) {
	var results []Lesson
	seen := map[int64]bool{excludeID: true}

	// Search with short segments of the title (5 runes each)
	segments := extractSearchSegments(title, 5)
	for _, seg := range segments {
		candidates, err := fts5SearchLessons(db, seg, limit*3)
		if err == nil {
			for _, l := range candidates {
				if !seen[l.ID] {
					seen[l.ID] = true
					results = append(results, l)
				}
			}
		}
		if len(results) >= limit {
			break
		}
	}

	// Fallback: search with content segments if not enough results
	if len(results) < limit {
		contentSegments := extractSearchSegments(content, 5)
		for _, seg := range contentSegments {
			candidates, err := fts5SearchLessons(db, seg, limit*3)
			if err == nil {
				for _, l := range candidates {
					if !seen[l.ID] {
						seen[l.ID] = true
						results = append(results, l)
					}
				}
			}
			if len(results) >= limit {
				break
			}
		}
	}

	filtered := results[:0]
	for _, l := range results {
		if projectPath == "" || l.ProjectPath == "" || l.ProjectPath == projectPath {
			filtered = append(filtered, l)
		}
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}
