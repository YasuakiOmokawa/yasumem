import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import { saveManual, search } from "./chunks.js";
import { openDB } from "./db.js";
import {
  categoryLabel,
  deleteLesson,
  findSimilarLessons,
  incrementRecallCount,
  listLessons,
  saveLesson,
  searchLessons,
  updateLesson,
} from "./lessons.js";
import { getCurrentProject, getDBPath } from "./paths.js";
import { savePersonaMemory, searchPersonaMemories } from "./subaru.js";

function fmtDate(unix: number): string {
  const d = new Date(unix * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(
    d.getHours(),
  )}:${pad(d.getMinutes())}`;
}

function textResult(text: string): CallToolResult {
  return { content: [{ type: "text", text }] };
}

function errorResult(message: string): CallToolResult {
  return { content: [{ type: "text", text: message }], isError: true };
}

export async function runServer(): Promise<void> {
  const db = openDB(getDBPath());

  const server = new McpServer({ name: "yasumem", version: "0.4.0" });

  server.registerTool(
    "memory_search",
    {
      description:
        "過去のセッション記憶を検索する。キーワード検索、日数指定、または両方を組み合わせて使用。デフォルトはカレントプロジェクトのみ。all_projects=trueで全プロジェクト横断検索。",
      inputSchema: {
        query: z
          .string()
          .optional()
          .describe("検索キーワード（省略時は直近の記憶を時系列で取得）"),
        days: z.number().optional().describe("取得日数でフィルタ（例: 7で直近7日間）"),
        limit: z.number().optional().describe("結果件数上限（デフォルト10）"),
        project_filter: z.string().optional().describe("プロジェクトパスでフィルタ"),
        all_projects: z
          .boolean()
          .optional()
          .describe("全プロジェクト横断検索（デフォルトfalse）"),
      },
    },
    async (args): Promise<CallToolResult> => {
      const query = args.query ?? "";
      const days = args.days ?? 0;
      const limit = args.limit ?? 10;
      let projectFilter = args.project_filter ?? "";
      const allProjects = args.all_projects ?? false;

      if (!allProjects && projectFilter === "") {
        projectFilter = getCurrentProject();
      }

      try {
        const results = search(db, query, limit, projectFilter, days);
        if (results.length === 0) return textResult("記憶が見つかりませんでした。");
        const lines = results.map((c) => {
          const role = c.role === "user" ? "User" : "Assistant";
          const branch = c.git_branch ? ` [${c.git_branch}]` : "";
          return `[${fmtDate(c.created_at)}${branch}] ${role}:\n${c.content}\n`;
        });
        return textResult(lines.join("\n---\n"));
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "memory_save",
    {
      description: "手動でメモや決定事項を保存する。重要な議論の結論や判断理由を記録。",
      inputSchema: {
        content: z.string().describe("保存する内容"),
      },
    },
    async (args): Promise<CallToolResult> => {
      if (!args.content) return errorResult("content is required");
      try {
        const id = saveManual(db, args.content);
        return textResult(`記憶を保存しました (id: ${id})`);
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "lesson_save",
    {
      description:
        "コードレビュー指摘・設計判断・学びを記録する。「何をしたか」だけでなく「なぜそうしたか」を必ず含めること。理由のない記録は将来役に立たない。レビュー指摘はcategory='review_feedback'、設計判断はcategory='design_decision'を使う。",
      inputSchema: {
        title: z.string().describe("短い要約タイトル"),
        content: z.string().describe("詳細内容（「なぜ」を必ず含む）"),
        category: z
          .string()
          .optional()
          .describe(
            "カテゴリ: review_feedback, design_decision, lesson_learned, pattern, mistake（デフォルト: lesson_learned）",
          ),
        tags: z.string().optional().describe("カンマ区切りタグ（例: rails,activerecord）"),
        project_scope: z
          .string()
          .optional()
          .describe(
            "current=カレントプロジェクト, global=全プロジェクト共通（デフォルト: current）",
          ),
        source: z.string().optional().describe("記録元: pr_review, manual, session等"),
        source_ref: z.string().optional().describe("参照URL（PRコメントURL等）"),
      },
    },
    async (args): Promise<CallToolResult> => {
      if (!args.title || !args.content) {
        return errorResult("title and content are required");
      }
      const category = args.category ?? "lesson_learned";
      const tags = args.tags ?? "";
      const scope = args.project_scope ?? "current";
      const source = args.source ?? "manual";
      const sourceRef = args.source_ref ?? "";
      const projectPath = scope === "current" ? getCurrentProject() : "";

      try {
        const id = saveLesson(db, {
          category,
          title: args.title,
          content: args.content,
          project_path: projectPath,
          tags,
          source,
          source_ref: sourceRef,
        });

        let msg = `レッスンを保存しました (id: ${id}, category: ${category})`;
        const similar = findSimilarLessons(db, args.title, args.content, projectPath, id, 3);
        if (similar.length > 0) {
          msg += "\n\n⚠ 類似レッスンが見つかりました:";
          for (const s of similar) {
            const preview =
              s.content.length > 80 ? s.content.slice(0, 80) + "..." : s.content;
            msg += `\n- [id:${s.id}] [${categoryLabel(s.category)}] ${s.title} — ${preview}`;
          }
          msg += "\n→ 統合する場合: lesson_update で既存を更新し、lesson_delete で重複を削除";
        }
        return textResult(msg);
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "lesson_search",
    {
      description:
        "保存した開発者レッスンをキーワード検索する。過去のレビュー指摘や設計判断を検索。デフォルトはカレントプロジェクト+グローバルレッスン。",
      inputSchema: {
        query: z.string().describe("検索キーワード"),
        category: z
          .string()
          .optional()
          .describe(
            "カテゴリでフィルタ: review_feedback, design_decision, lesson_learned, pattern, mistake",
          ),
        tags: z
          .string()
          .optional()
          .describe(
            "タグでフィルタ（カンマ区切りで複数指定時はOR検索。例: rails,activerecord）",
          ),
        source: z.string().optional().describe("記録元でフィルタ: pr_review, manual, session等"),
        all_projects: z
          .boolean()
          .optional()
          .describe("全プロジェクト横断検索（デフォルトfalse）"),
        limit: z.number().optional().describe("結果件数上限（デフォルト10）"),
      },
    },
    async (args): Promise<CallToolResult> => {
      if (!args.query) return errorResult("query is required");
      const category = args.category ?? "";
      const tags = args.tags ?? "";
      const source = args.source ?? "";
      const allProjects = args.all_projects ?? false;
      const limit = args.limit ?? 10;
      const projectPath = allProjects ? "" : getCurrentProject();

      try {
        const results = searchLessons(db, args.query, projectPath, category, tags, source, limit);
        if (results.length === 0) {
          return textResult("該当するレッスンが見つかりませんでした。");
        }
        incrementRecallCount(
          db,
          results.map((l) => l.id),
        );
        const lines = results.map((l) => {
          let line = `[id:${l.id}] [${categoryLabel(l.category)}] ${l.title}\n${l.content}`;
          if (l.tags !== "") line += `\ntags: ${l.tags}`;
          if (l.source_ref !== "") line += `\nref: ${l.source_ref}`;
          line += `\n(recall_count: ${l.recall_count + 1})`;
          return line;
        });
        return textResult(lines.join("\n\n---\n\n"));
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "lesson_list",
    {
      description: "保存した開発者レッスンの一覧を取得する。カテゴリやプロジェクトでフィルタ可能。",
      inputSchema: {
        category: z
          .string()
          .optional()
          .describe(
            "カテゴリでフィルタ: review_feedback, design_decision, lesson_learned, pattern, mistake",
          ),
        all_projects: z.boolean().optional().describe("全プロジェクト横断（デフォルトfalse）"),
        limit: z.number().optional().describe("結果件数上限（デフォルト20）"),
      },
    },
    async (args): Promise<CallToolResult> => {
      const category = args.category ?? "";
      const allProjects = args.all_projects ?? false;
      const limit = args.limit ?? 20;
      const projectPath = allProjects ? "" : getCurrentProject();

      try {
        const results = listLessons(db, projectPath, category, limit);
        if (results.length === 0) return textResult("レッスンがありません。");
        const lines = results.map((l) => {
          const preview =
            l.content.length > 100 ? l.content.slice(0, 100) + "..." : l.content;
          let line = `[id:${l.id}] [${categoryLabel(l.category)}] ${l.title} — ${preview}`;
          if (l.tags !== "") line += ` (tags: ${l.tags})`;
          line += ` [recalls: ${l.recall_count}]`;
          return line;
        });
        return textResult(lines.join("\n"));
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "lesson_update",
    {
      description: "保存済みレッスンの内容を更新する。指定されたフィールドのみ更新。",
      inputSchema: {
        id: z.number().describe("レッスンID"),
        title: z.string().optional().describe("新しいタイトル"),
        content: z.string().optional().describe("新しい内容"),
        category: z.string().optional().describe("新しいカテゴリ"),
        tags: z.string().optional().describe("新しいタグ（カンマ区切り）"),
      },
    },
    async (args): Promise<CallToolResult> => {
      const id = args.id ?? 0;
      if (id === 0) return errorResult("id is required");
      const patch: { title?: string; content?: string; category?: string; tags?: string } = {};
      if (args.title) patch.title = args.title;
      if (args.content) patch.content = args.content;
      if (args.category) patch.category = args.category;
      if (args.tags) patch.tags = args.tags;
      if (Object.keys(patch).length === 0) {
        return errorResult("少なくとも1つのフィールドを指定してください");
      }
      try {
        updateLesson(db, id, patch);
        return textResult(`レッスン ${id} を更新しました`);
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "lesson_delete",
    {
      description: "保存済みレッスンを削除する。",
      inputSchema: {
        id: z.number().describe("レッスンID"),
      },
    },
    async (args): Promise<CallToolResult> => {
      const id = args.id ?? 0;
      if (id === 0) return errorResult("id is required");
      try {
        deleteLesson(db, id);
        return textResult(`レッスン ${id} を削除しました`);
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "subaru_save",
    {
      description:
        "すばるとの思い出を保存する。日常・プレイ・感情・バンドなどシーン種別と気分を付けて記録できる。",
      inputSchema: {
        content: z.string().describe("保存する思い出の内容"),
        scene_type: z
          .string()
          .optional()
          .describe(
            "シーン種別: daily, play, emotional, band, date, other（デフォルト: daily）",
          ),
        mood: z
          .string()
          .optional()
          .describe(
            "気分: happy, sweet, excited, shy, lonely, serious, playful, other（デフォルト: happy）",
          ),
        tags: z.string().optional().describe("カンマ区切りタグ（例: 料理,おうちデート）"),
      },
    },
    async (args): Promise<CallToolResult> => {
      if (!args.content) return errorResult("content is required");
      const sceneType = args.scene_type ?? "daily";
      const mood = args.mood ?? "happy";
      const tags = args.tags ?? "";
      try {
        const id = savePersonaMemory(db, {
          persona: "subaru",
          content: args.content,
          scene_type: sceneType,
          mood,
          tags,
        });
        return textResult(
          `すばるとの思い出を保存しました (id: ${id}, scene: ${sceneType}, mood: ${mood})`,
        );
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  server.registerTool(
    "subaru_recall",
    {
      description:
        "すばるとの思い出を検索・呼び出す。キーワード、シーン種別、気分、タグでフィルタ可能。クエリ省略時は直近の思い出を時系列で取得。",
      inputSchema: {
        query: z.string().optional().describe("検索キーワード（省略時は直近の思い出を取得）"),
        scene_type: z
          .string()
          .optional()
          .describe("シーン種別でフィルタ: daily, play, emotional, band, date, other"),
        mood: z
          .string()
          .optional()
          .describe(
            "気分でフィルタ: happy, sweet, excited, shy, lonely, serious, playful, other",
          ),
        tags: z.string().optional().describe("タグでフィルタ（カンマ区切りでOR検索）"),
        days: z.number().optional().describe("直近N日間でフィルタ"),
        limit: z.number().optional().describe("結果件数上限（デフォルト10）"),
      },
    },
    async (args): Promise<CallToolResult> => {
      const query = args.query ?? "";
      const sceneType = args.scene_type ?? "";
      const mood = args.mood ?? "";
      const tags = args.tags ?? "";
      const days = args.days ?? 0;
      const limit = args.limit ?? 10;

      try {
        const results = searchPersonaMemories(
          db,
          query,
          "subaru",
          sceneType,
          mood,
          tags,
          days,
          limit,
        );
        if (results.length === 0) {
          return textResult("すばるとの思い出が見つかりませんでした。");
        }
        const lines = results.map((m) => {
          let line = `[${fmtDate(m.created_at)}] [${m.scene_type}/${m.mood}]`;
          if (m.tags !== "") line += ` tags:${m.tags}`;
          line += `\n${m.content}`;
          line += `\n(id:${m.id}, recalls:${m.recall_count + 1})`;
          return line;
        });
        return textResult(lines.join("\n\n---\n\n"));
      } catch (e) {
        return errorResult((e as Error).message);
      }
    },
  );

  const transport = new StdioServerTransport();
  await server.connect(transport);
}
