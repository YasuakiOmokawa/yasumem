import type { DB } from "./db.js";
import { normalizeTags } from "./lessons.js";

export interface PersonaMemory {
  id: number;
  persona: string;
  content: string;
  scene_type: string;
  mood: string;
  tags: string;
  recall_count: number;
  created_at: number;
  updated_at: number;
}

export interface PersonaMemoryInput {
  persona: string;
  content: string;
  scene_type: string;
  mood: string;
  tags: string;
}

export function savePersonaMemory(db: DB, m: PersonaMemoryInput): number {
  const now = Math.floor(Date.now() / 1000);
  const tags = normalizeTags(m.tags);
  const persona = m.persona === "" ? "subaru" : m.persona;
  const result = db
    .prepare(
      `INSERT INTO persona_memories (persona, content, scene_type, mood, tags, recall_count, created_at, updated_at)
       VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
    )
    .run(persona, m.content, m.scene_type, m.mood, tags, now, now);
  return Number(result.lastInsertRowid);
}

function recentPersonaMemories(
  db: DB,
  persona: string,
  days: number,
  limit: number,
): PersonaMemory[] {
  let q = `SELECT id, persona, content, scene_type, mood, tags, recall_count, created_at, updated_at
       FROM persona_memories WHERE 1=1`;
  const args: (string | number)[] = [];
  if (persona !== "") {
    q += " AND persona = ?";
    args.push(persona);
  }
  if (days > 0) {
    const cutoff = Math.floor(Date.now() / 1000) - days * 86400;
    q += " AND created_at > ?";
    args.push(cutoff);
  }
  q += " ORDER BY created_at DESC LIMIT ?";
  args.push(limit);
  return db.prepare(q).all(...args) as PersonaMemory[];
}

function fts5SearchPersonaMemories(
  db: DB,
  query: string,
  limit: number,
): PersonaMemory[] {
  return db
    .prepare(
      `SELECT m.id, m.persona, m.content, m.scene_type, m.mood, m.tags, m.recall_count, m.created_at, m.updated_at
       FROM persona_memories_fts f JOIN persona_memories m ON m.id = f.rowid
       WHERE persona_memories_fts MATCH ? ORDER BY rank LIMIT ?`,
    )
    .all(query, limit) as PersonaMemory[];
}

function likeSearchPersonaMemories(
  db: DB,
  query: string,
  limit: number,
): PersonaMemory[] {
  const pattern = `%${query}%`;
  return db
    .prepare(
      `SELECT id, persona, content, scene_type, mood, tags, recall_count, created_at, updated_at
       FROM persona_memories WHERE content LIKE ? OR tags LIKE ?
       ORDER BY created_at DESC LIMIT ?`,
    )
    .all(pattern, pattern, limit) as PersonaMemory[];
}

function incrementPersonaMemoryRecallCount(db: DB, ids: number[]): void {
  if (ids.length === 0) return;
  const placeholders = ids.map(() => "?").join(",");
  db.prepare(
    `UPDATE persona_memories SET recall_count = recall_count + 1 WHERE id IN (${placeholders})`,
  ).run(...ids);
}

export function searchPersonaMemories(
  db: DB,
  query: string,
  persona: string,
  sceneType: string,
  mood: string,
  tags: string,
  days: number,
  limit: number,
): PersonaMemory[] {
  let results: PersonaMemory[];

  if (query === "") {
    results = recentPersonaMemories(db, persona, days, limit);
  } else {
    const fetchLimit = Math.max(limit * 4, 20);
    if (query.length >= 3) {
      try {
        results = fts5SearchPersonaMemories(db, query, fetchLimit);
        if (results.length === 0) {
          results = likeSearchPersonaMemories(db, query, fetchLimit);
        }
      } catch {
        results = likeSearchPersonaMemories(db, query, fetchLimit);
      }
    } else {
      results = likeSearchPersonaMemories(db, query, fetchLimit);
    }
  }

  const tagFilters = tags
    ? tags
        .split(",")
        .map((t) => t.trim().toLowerCase())
        .filter((t) => t !== "")
    : [];

  const cutoff = days > 0 ? Math.floor(Date.now() / 1000) - days * 86400 : 0;

  const filtered = results.filter((m) => {
    if (persona !== "" && m.persona !== persona) return false;
    if (sceneType !== "" && m.scene_type !== sceneType) return false;
    if (mood !== "" && m.mood !== mood) return false;
    if (tagFilters.length > 0) {
      const lower = m.tags.toLowerCase();
      if (!tagFilters.some((tf) => lower.includes(tf))) return false;
    }
    if (cutoff > 0 && m.created_at < cutoff) return false;
    return true;
  });

  const sliced = filtered.slice(0, limit);

  if (sliced.length > 0) {
    incrementPersonaMemoryRecallCount(
      db,
      sliced.map((m) => m.id),
    );
  }
  return sliced;
}
