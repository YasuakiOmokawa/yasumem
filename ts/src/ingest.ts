import {
  closeSync,
  existsSync,
  fstatSync,
  openSync,
  readdirSync,
  readFileSync,
  readSync,
  statSync,
  writeFileSync,
  type Dirent,
} from "node:fs";
import { homedir } from "node:os";
import { basename, join } from "node:path";
import type { DB } from "./db.js";
import {
  type Chunk,
  getSessionIngestState,
  saveChunks,
  saveSession,
  sessionExists,
  updateSessionIngestState,
} from "./chunks.js";
import { openDB } from "./db.js";
import {
  getDBPath,
  getWorktreePaths,
  lastIngestAtPath,
  resolveCanonicalProject,
} from "./paths.js";

const claudeProjectsDir = join(homedir(), ".claude", "projects");

const MAX_CHUNK_LENGTH = 2000;
const MIN_CHUNK_RUNES = 15;
const MAX_CHUNK_RUNES = 1000;
const TRUNCATE_TO_RUNES = 500;

function encodeCwd(cwd: string): string {
  return cwd.replaceAll("/", "-");
}

interface JsonlEntry {
  type?: string;
  sessionId?: string;
  cwd?: string;
  gitBranch?: string;
  timestamp?: string;
  message?: { content?: unknown };
}

interface ContentBlock {
  type: string;
  text?: string;
  name?: string;
}

function extractTextContent(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    const parts: string[] = [];
    for (const block of content as ContentBlock[]) {
      if (block.type === "text" && typeof block.text === "string") {
        parts.push(block.text);
      } else if (block.type === "tool_use" && typeof block.name === "string") {
        parts.push(`[Tool: ${block.name}]`);
      }
    }
    return parts.join("\n");
  }
  return "";
}

const ingestNoisePrefixes = [
  "<system-reminder>",
  "<available-deferred-tools>",
  "<functions>",
  "<local-command",
  "<command-name>",
  "<local-command-caveat>",
  "<local-command-stdout>",
  "<usage>",
  "<task-notification>",
  "Tool loaded.",
];

const toolOnlyRe = /^(\[Tool:\s*\w+\]\s*)+$/;

function runeLength(text: string): number {
  // Count Unicode code points (matches Go's []rune count)
  let count = 0;
  for (const _ of text) count++;
  return count;
}

function isNoiseContent(text: string): boolean {
  const trimmed = text.trim();
  for (const p of ingestNoisePrefixes) {
    if (trimmed.startsWith(p)) return true;
  }
  if (toolOnlyRe.test(trimmed)) return true;
  if (runeLength(trimmed) < MIN_CHUNK_RUNES) return true;
  return false;
}

function truncateChunk(text: string): string {
  const runes = Array.from(text);
  if (runes.length > MAX_CHUNK_RUNES) {
    return runes.slice(0, TRUNCATE_TO_RUNES).join("") + "...(省略)";
  }
  return text;
}

const sentenceSplitRe = /[。.!?！？\n]/g;

function splitChunk(text: string): string[] {
  if (text.length <= MAX_CHUNK_LENGTH) return [text];

  const indices: { start: number; end: number }[] = [];
  for (const m of text.matchAll(sentenceSplitRe)) {
    indices.push({ start: m.index!, end: m.index! + m[0].length });
  }
  if (indices.length === 0) {
    const chunks: string[] = [];
    let rest = text;
    while (rest.length > MAX_CHUNK_LENGTH) {
      chunks.push(rest.slice(0, MAX_CHUNK_LENGTH));
      rest = rest.slice(MAX_CHUNK_LENGTH);
    }
    if (rest !== "") chunks.push(rest);
    return chunks;
  }

  const chunks: string[] = [];
  let current = "";
  let prev = 0;
  for (const idx of indices) {
    const seg = text.slice(prev, idx.end);
    if (current.length + seg.length > MAX_CHUNK_LENGTH && current !== "") {
      chunks.push(current);
      current = seg;
    } else {
      current += seg;
    }
    prev = idx.end;
  }
  if (prev < text.length) {
    current += text.slice(prev);
  }
  if (current !== "") chunks.push(current);
  return chunks;
}

interface SessionMeta {
  sessionId: string;
  projectPath: string;
  gitBranch: string;
  startedAt: number;
  endedAt: number;
}

interface ParseResult {
  meta: SessionMeta;
  chunks: Omit<Chunk, "id">[];
  bytesRead: number;
  lastChunkIndex: number;
}

function readFromOffset(
  path: string,
  offset: number,
): { text: string; bytesRead: number } {
  const fd = openSync(path, "r");
  try {
    const stat = fstatSync(fd);
    const remaining = stat.size - offset;
    if (remaining <= 0) return { text: "", bytesRead: 0 };
    const buf = Buffer.alloc(remaining);
    const n = readSync(fd, buf, 0, remaining, offset);
    return { text: buf.subarray(0, n).toString("utf8"), bytesRead: n };
  } finally {
    closeSync(fd);
  }
}

function parseTimestamp(ts: string): number {
  if (!ts) return 0;
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return 0;
  return Math.floor(t / 1000);
}

function parseJsonlIncremental(
  path: string,
  byteOffset: number,
  skipChunkIndex: number,
): ParseResult {
  let fileText: string;
  let readBytes: number;
  try {
    const r = readFromOffset(path, byteOffset);
    fileText = r.text;
    readBytes = r.bytesRead;
  } catch {
    return {
      meta: { sessionId: "", projectPath: "", gitBranch: "", startedAt: 0, endedAt: 0 },
      chunks: [],
      bytesRead: byteOffset,
      lastChunkIndex: skipChunkIndex,
    };
  }

  const meta: SessionMeta = {
    sessionId: "",
    projectPath: "",
    gitBranch: "",
    startedAt: 0,
    endedAt: 0,
  };
  const chunks: Omit<Chunk, "id">[] = [];
  let chunkIndex = skipChunkIndex + 1;

  const lines = fileText.split("\n");
  for (const rawLine of lines) {
    const line = rawLine.trim();
    if (line === "") continue;
    let entry: JsonlEntry;
    try {
      entry = JSON.parse(line);
    } catch {
      continue;
    }
    if (entry.type !== "user" && entry.type !== "assistant") continue;

    if (meta.sessionId === "") {
      meta.sessionId = entry.sessionId ?? "";
      meta.projectPath = entry.cwd ?? "";
      meta.gitBranch = entry.gitBranch ?? "";
    }

    let ts = parseTimestamp(entry.timestamp ?? "");
    if (ts === 0) ts = Math.floor(Date.now() / 1000);
    if (meta.startedAt === 0 || ts < meta.startedAt) meta.startedAt = ts;
    if (ts > meta.endedAt) meta.endedAt = ts;

    const text = extractTextContent(entry.message?.content);
    if (text.trim() === "") continue;
    if (isNoiseContent(text)) continue;

    for (const part of splitChunk(text)) {
      const truncated = truncateChunk(part);
      if (isNoiseContent(truncated)) continue;
      chunks.push({
        session_id: meta.sessionId,
        project_path: meta.projectPath,
        git_branch: meta.gitBranch !== "" ? meta.gitBranch : null,
        chunk_index: chunkIndex,
        role: entry.type,
        content: truncated,
        created_at: ts,
      });
      chunkIndex++;
    }
  }

  const lastIdx = chunks.length > 0 ? chunks[chunks.length - 1].chunk_index : skipChunkIndex;

  return {
    meta,
    chunks,
    bytesRead: byteOffset + readBytes,
    lastChunkIndex: lastIdx,
  };
}

function findJsonl(sessionID: string, cwd: string): string {
  const encoded = encodeCwd(cwd);
  const projectDir = join(claudeProjectsDir, encoded);

  const direct = join(projectDir, `${sessionID}.jsonl`);
  if (existsSync(direct)) return direct;

  let entries: Dirent[];
  try {
    entries = readdirSync(projectDir, { withFileTypes: true });
  } catch {
    return "";
  }
  for (const e of entries) {
    if (e.isDirectory()) {
      const candidate = join(projectDir, e.name, `${sessionID}.jsonl`);
      if (existsSync(candidate)) return candidate;
    }
  }

  const sessionDir = join(projectDir, sessionID);
  try {
    const subentries = readdirSync(sessionDir, { withFileTypes: true });
    for (const e of subentries) {
      if (!e.isDirectory() && e.name.endsWith(".jsonl")) {
        return join(sessionDir, e.name);
      }
    }
  } catch {
    // ignore
  }
  return "";
}

interface JsonlFile {
  path: string;
  modTimeMs: number;
}

function findRecentJsonls(worktreePaths: string[], sinceMs: number): string[] {
  const all: JsonlFile[] = [];
  for (const wt of worktreePaths) {
    const encoded = encodeCwd(wt);
    const projectDir = join(claudeProjectsDir, encoded);
    let entries: Dirent[];
    try {
      entries = readdirSync(projectDir, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const e of entries) {
      if (e.isDirectory() || !e.name.endsWith(".jsonl")) continue;
      const full = join(projectDir, e.name);
      let mtime: number;
      try {
        mtime = statSync(full).mtimeMs;
      } catch {
        continue;
      }
      if (sinceMs > 0 && mtime <= sinceMs) continue;
      all.push({ path: full, modTimeMs: mtime });
    }
  }
  all.sort((a, b) => b.modTimeMs - a.modTimeMs);
  return all.map((f) => f.path);
}

function readLastIngestAtMs(): number {
  try {
    const text = readFileSync(lastIngestAtPath(), "utf8").trim();
    const t = Date.parse(text);
    return Number.isNaN(t) ? 0 : t;
  } catch {
    return 0;
  }
}

function writeLastIngestAt(date: Date): void {
  writeFileSync(lastIngestAtPath(), date.toISOString() + "\n");
}

interface IngestStdinPayload {
  session_id?: string;
  cwd?: string;
}

async function readStdin(): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) {
    chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : (chunk as Buffer));
  }
  return Buffer.concat(chunks).toString("utf8");
}

export async function runIngest(): Promise<void> {
  let raw: string;
  try {
    raw = await readStdin();
  } catch {
    process.stderr.write("Error: failed to read stdin\n");
    process.exit(1);
  }
  let input: IngestStdinPayload;
  try {
    input = JSON.parse(raw);
  } catch {
    process.stderr.write("Error: invalid JSON on stdin\n");
    process.exit(1);
  }
  if (!input.session_id || !input.cwd) {
    process.stderr.write("Error: missing session_id or cwd\n");
    process.exit(1);
  }

  const db = openDB(getDBPath());
  try {
    if (sessionExists(db, input.session_id)) return;

    const jsonlPath = findJsonl(input.session_id, input.cwd);
    if (jsonlPath === "") {
      process.stderr.write(
        `Warning: JSONL not found for session ${input.session_id}\n`,
      );
      return;
    }

    const r = parseJsonlIncremental(jsonlPath, 0, -1);
    if (r.chunks.length === 0) return;

    const canonical = resolveCanonicalProject(input.cwd);
    const meta = r.meta;
    if (meta.sessionId === "") meta.sessionId = input.session_id;
    meta.projectPath = canonical;
    for (const c of r.chunks) c.project_path = canonical;

    const startedAt = meta.startedAt > 0 ? meta.startedAt : Math.floor(Date.now() / 1000);
    const endedAt = meta.endedAt > 0 ? meta.endedAt : null;

    saveSession(
      db,
      meta.sessionId,
      meta.projectPath,
      meta.gitBranch !== "" ? meta.gitBranch : null,
      startedAt,
      endedAt,
    );
    const count = saveChunks(db, r.chunks);
    updateSessionIngestState(db, meta.sessionId, r.bytesRead, r.lastChunkIndex);
    process.stderr.write(
      `Saved ${count} chunks from session ${input.session_id}\n`,
    );
  } finally {
    db.close();
  }
}

interface IngestRecentPayload {
  cwd?: string;
}

export async function runIngestRecent(): Promise<void> {
  let raw: string;
  try {
    raw = await readStdin();
  } catch {
    return;
  }
  let input: IngestRecentPayload;
  try {
    input = JSON.parse(raw);
  } catch {
    return;
  }
  if (!input.cwd) return;

  const canonical = resolveCanonicalProject(input.cwd);
  const worktreePaths = getWorktreePaths(input.cwd);

  let db: DB;
  try {
    db = openDB(getDBPath());
  } catch {
    return;
  }

  try {
    const sinceMs = readLastIngestAtMs();
    const now = new Date();

    for (const jsonlPath of findRecentJsonls(worktreePaths, sinceMs)) {
      const sessionID = basename(jsonlPath, ".jsonl");

      const state = getSessionIngestState(db, sessionID);
      const r = parseJsonlIncremental(jsonlPath, state.byteOffset, state.chunkIndex);
      if (r.chunks.length === 0) continue;

      const meta = r.meta;
      if (meta.sessionId === "") meta.sessionId = sessionID;
      for (const c of r.chunks) c.project_path = canonical;

      const startedAt =
        meta.startedAt > 0 ? meta.startedAt : Math.floor(Date.now() / 1000);
      const endedAt = meta.endedAt > 0 ? meta.endedAt : null;

      saveSession(
        db,
        meta.sessionId,
        canonical,
        meta.gitBranch !== "" ? meta.gitBranch : null,
        startedAt,
        endedAt,
      );
      const count = saveChunks(db, r.chunks);
      updateSessionIngestState(db, meta.sessionId, r.bytesRead, r.lastChunkIndex);
      process.stderr.write(`Ingested ${count} chunks from ${sessionID}\n`);
    }

    writeLastIngestAt(now);
  } finally {
    db.close();
  }
}
