package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type PersonaMemory struct {
	ID          int64
	Persona     string
	Content     string
	SceneType   string
	Mood        string
	Tags        string
	RecallCount int
	CreatedAt   float64
	UpdatedAt   float64
}

func scanPersonaMemories(rows *sql.Rows) ([]PersonaMemory, error) {
	var memories []PersonaMemory
	for rows.Next() {
		var m PersonaMemory
		if err := rows.Scan(&m.ID, &m.Persona, &m.Content, &m.SceneType,
			&m.Mood, &m.Tags, &m.RecallCount, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

func savePersonaMemory(db *sql.DB, m PersonaMemory) (int64, error) {
	now := float64(time.Now().Unix())
	m.Tags = normalizeTags(m.Tags)
	if m.Persona == "" {
		m.Persona = "subaru"
	}
	res, err := db.Exec(
		`INSERT INTO persona_memories (persona, content, scene_type, mood, tags, recall_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		m.Persona, m.Content, m.SceneType, m.Mood, m.Tags, now, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func searchPersonaMemories(db *sql.DB, query, persona, sceneType, mood, tags string, days, limit int) ([]PersonaMemory, error) {
	var results []PersonaMemory
	var err error

	if query == "" {
		results, err = recentPersonaMemories(db, persona, days, limit)
	} else {
		fetchLimit := limit * 4
		if fetchLimit < 20 {
			fetchLimit = 20
		}
		if len(query) >= 3 {
			results, err = fts5SearchPersonaMemories(db, query, fetchLimit)
			if err != nil || len(results) == 0 {
				results, err = likeSearchPersonaMemories(db, query, fetchLimit)
			}
		} else {
			results, err = likeSearchPersonaMemories(db, query, fetchLimit)
		}
	}
	if err != nil {
		return nil, err
	}

	// Parse tags filter
	var tagFilters []string
	if tags != "" {
		for _, t := range strings.Split(tags, ",") {
			t = strings.TrimSpace(strings.ToLower(t))
			if t != "" {
				tagFilters = append(tagFilters, t)
			}
		}
	}

	// Apply filters
	filtered := results[:0]
	for _, m := range results {
		if persona != "" && m.Persona != persona {
			continue
		}
		if sceneType != "" && m.SceneType != sceneType {
			continue
		}
		if mood != "" && m.Mood != mood {
			continue
		}
		if len(tagFilters) > 0 {
			matched := false
			for _, tf := range tagFilters {
				if strings.Contains(strings.ToLower(m.Tags), tf) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if days > 0 {
			cutoff := float64(time.Now().Unix()) - float64(days)*86400
			if m.CreatedAt < cutoff {
				continue
			}
		}
		filtered = append(filtered, m)
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	// Increment recall_count
	if len(filtered) > 0 {
		ids := make([]int64, len(filtered))
		for i, m := range filtered {
			ids[i] = m.ID
		}
		incrementPersonaMemoryRecallCount(db, ids)
	}

	return filtered, nil
}

func recentPersonaMemories(db *sql.DB, persona string, days, limit int) ([]PersonaMemory, error) {
	q := `SELECT id, persona, content, scene_type, mood, tags, recall_count, created_at, updated_at
		 FROM persona_memories WHERE 1=1`
	var args []interface{}

	if persona != "" {
		q += " AND persona = ?"
		args = append(args, persona)
	}
	if days > 0 {
		cutoff := float64(time.Now().Unix()) - float64(days)*86400
		q += " AND created_at > ?"
		args = append(args, cutoff)
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPersonaMemories(rows)
}

func fts5SearchPersonaMemories(db *sql.DB, query string, limit int) ([]PersonaMemory, error) {
	rows, err := db.Query(
		`SELECT m.id, m.persona, m.content, m.scene_type, m.mood, m.tags, m.recall_count, m.created_at, m.updated_at
		 FROM persona_memories_fts f JOIN persona_memories m ON m.id = f.rowid
		 WHERE persona_memories_fts MATCH ? ORDER BY rank LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPersonaMemories(rows)
}

func likeSearchPersonaMemories(db *sql.DB, query string, limit int) ([]PersonaMemory, error) {
	pattern := "%" + query + "%"
	rows, err := db.Query(
		`SELECT id, persona, content, scene_type, mood, tags, recall_count, created_at, updated_at
		 FROM persona_memories WHERE content LIKE ? OR tags LIKE ?
		 ORDER BY created_at DESC LIMIT ?`,
		pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPersonaMemories(rows)
}

func incrementPersonaMemoryRecallCount(db *sql.DB, ids []int64) {
	if len(ids) == 0 {
		return
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("UPDATE persona_memories SET recall_count = recall_count + 1 WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	db.Exec(query, args...)
}
