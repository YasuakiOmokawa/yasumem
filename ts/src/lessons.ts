import type { DB } from "./db.js";

export interface Lesson {
  id: number;
  category: string;
  title: string;
  content: string;
  project_path: string;
  tags: string;
  source: string;
  source_ref: string;
  recall_count: number;
  created_at: number;
  updated_at: number;
}

export interface LessonInput {
  category: string;
  title: string;
  content: string;
  project_path: string;
  tags: string;
  source: string;
  source_ref: string;
}

export interface LessonUpdate {
  title?: string;
  content?: string;
  category?: string;
  tags?: string;
}

export function normalizeTags(tags: string): string {
  return tags
    .split(",")
    .map((t) => t.trim().toLowerCase())
    .filter((t) => t !== "")
    .join(",");
}

export function saveLesson(db: DB, l: LessonInput): number {
  const now = Math.floor(Date.now() / 1000);
  const tags = normalizeTags(l.tags);
  const result = db
    .prepare(
      `INSERT INTO lessons (category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
    )
    .run(
      l.category,
      l.title,
      l.content,
      l.project_path,
      tags,
      l.source,
      l.source_ref,
      now,
      now,
    );
  return Number(result.lastInsertRowid);
}

export function updateLesson(db: DB, id: number, patch: LessonUpdate): void {
  const now = Math.floor(Date.now() / 1000);
  const setClauses: string[] = ["updated_at = ?"];
  const args: (string | number)[] = [now];

  if (patch.title !== undefined) {
    setClauses.push("title = ?");
    args.push(patch.title);
  }
  if (patch.content !== undefined) {
    setClauses.push("content = ?");
    args.push(patch.content);
  }
  if (patch.category !== undefined) {
    setClauses.push("category = ?");
    args.push(patch.category);
  }
  if (patch.tags !== undefined) {
    setClauses.push("tags = ?");
    args.push(normalizeTags(patch.tags));
  }
  args.push(id);

  const result = db
    .prepare(`UPDATE lessons SET ${setClauses.join(", ")} WHERE id = ?`)
    .run(...args);
  if (result.changes === 0) {
    throw new Error(`lesson not found: ${id}`);
  }
}

export function deleteLesson(db: DB, id: number): void {
  const result = db.prepare("DELETE FROM lessons WHERE id = ?").run(id);
  if (result.changes === 0) {
    throw new Error(`lesson not found: ${id}`);
  }
}

export function listLessons(
  db: DB,
  projectPath: string,
  category: string,
  limit: number,
): Lesson[] {
  let q = `SELECT id, category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at
       FROM lessons WHERE 1=1`;
  const args: (string | number)[] = [];
  if (projectPath !== "") {
    q += " AND (project_path = ? OR project_path = '')";
    args.push(projectPath);
  }
  if (category !== "") {
    q += " AND category = ?";
    args.push(category);
  }
  q += " LIMIT ?";
  args.push(limit);
  return db.prepare(q).all(...args) as Lesson[];
}

function fts5SearchLessons(db: DB, query: string, limit: number): Lesson[] {
  return db
    .prepare(
      `SELECT l.id, l.category, l.title, l.content, l.project_path, l.tags, l.source, l.source_ref, l.recall_count, l.created_at, l.updated_at
       FROM lessons_fts f JOIN lessons l ON l.id = f.rowid
       WHERE lessons_fts MATCH ? ORDER BY rank LIMIT ?`,
    )
    .all(query, limit) as Lesson[];
}

function likeSearchLessons(db: DB, query: string, limit: number): Lesson[] {
  const pattern = `%${query}%`;
  return db
    .prepare(
      `SELECT id, category, title, content, project_path, tags, source, source_ref, recall_count, created_at, updated_at
       FROM lessons WHERE title LIKE ? OR content LIKE ? OR tags LIKE ?
       LIMIT ?`,
    )
    .all(pattern, pattern, pattern, limit) as Lesson[];
}

export function searchLessons(
  db: DB,
  query: string,
  projectPath: string,
  category: string,
  tags: string,
  source: string,
  limit: number,
): Lesson[] {
  let results: Lesson[];
  if (query.length >= 3) {
    try {
      results = fts5SearchLessons(db, query, limit * 2);
      if (results.length === 0) {
        results = likeSearchLessons(db, query, limit * 2);
      }
    } catch {
      results = likeSearchLessons(db, query, limit * 2);
    }
  } else {
    results = likeSearchLessons(db, query, limit * 2);
  }

  const tagFilters = tags
    ? tags
        .split(",")
        .map((t) => t.trim().toLowerCase())
        .filter((t) => t !== "")
    : [];

  const filtered = results.filter((l) => {
    if (projectPath !== "" && l.project_path !== "" && l.project_path !== projectPath)
      return false;
    if (category !== "" && l.category !== category) return false;
    if (source !== "" && l.source !== source) return false;
    if (tagFilters.length > 0) {
      const lower = l.tags.toLowerCase();
      if (!tagFilters.some((tf) => lower.includes(tf))) return false;
    }
    return true;
  });

  return filtered.slice(0, limit);
}

export function incrementRecallCount(db: DB, ids: number[]): void {
  if (ids.length === 0) return;
  const placeholders = ids.map(() => "?").join(",");
  db.prepare(
    `UPDATE lessons SET recall_count = recall_count + 1 WHERE id IN (${placeholders})`,
  ).run(...ids);
}

export function categoryLabel(category: string): string {
  switch (category) {
    case "review_feedback":
      return "レビュー指摘";
    case "design_decision":
      return "設計決定";
    case "lesson_learned":
      return "学び";
    case "pattern":
      return "パターン";
    case "mistake":
      return "失敗";
    default:
      return category;
  }
}

function extractSearchSegments(text: string, segLen: number): string[] {
  const runes = Array.from(text);
  if (runes.length <= segLen) {
    return runes.length >= 3 ? [text] : [];
  }
  const segments: string[] = [];
  for (let i = 0; i + segLen <= runes.length; i += segLen) {
    segments.push(runes.slice(i, i + segLen).join(""));
  }
  return segments;
}

export function findSimilarLessons(
  db: DB,
  title: string,
  content: string,
  projectPath: string,
  excludeID: number,
  limit: number,
): Lesson[] {
  const seen = new Set<number>([excludeID]);
  const results: Lesson[] = [];

  for (const seg of extractSearchSegments(title, 5)) {
    try {
      const candidates = fts5SearchLessons(db, seg, limit * 3);
      for (const l of candidates) {
        if (!seen.has(l.id)) {
          seen.add(l.id);
          results.push(l);
        }
      }
    } catch {
      // ignore segment errors
    }
    if (results.length >= limit) break;
  }

  if (results.length < limit) {
    for (const seg of extractSearchSegments(content, 5)) {
      try {
        const candidates = fts5SearchLessons(db, seg, limit * 3);
        for (const l of candidates) {
          if (!seen.has(l.id)) {
            seen.add(l.id);
            results.push(l);
          }
        }
      } catch {
        // ignore
      }
      if (results.length >= limit) break;
    }
  }

  const filtered = results.filter(
    (l) => projectPath === "" || l.project_path === "" || l.project_path === projectPath,
  );
  return filtered.slice(0, limit);
}
