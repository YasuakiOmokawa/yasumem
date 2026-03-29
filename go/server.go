package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func runServer() {
	dbPath := getDBPath()
	db, err := openDB(dbPath)
	if err != nil {
		fmt.Printf("DB error: %v\n", err)
		return
	}
	defer db.Close()

	s := server.NewMCPServer("yasumem", "0.3.0",
		server.WithToolCapabilities(false),
	)

	// memory_search
	s.AddTool(
		mcp.NewTool("memory_search",
			mcp.WithDescription("過去のセッション記憶をハイブリッド検索する。キーワードで過去の議論や決定事項を検索。デフォルトはカレントプロジェクトのみ。all_projects=trueで全プロジェクト横断検索。"),
			mcp.WithString("query", mcp.Required(), mcp.Description("検索キーワード")),
			mcp.WithNumber("limit", mcp.Description("結果件数上限（デフォルト5）")),
			mcp.WithString("project_filter", mcp.Description("プロジェクトパスでフィルタ")),
			mcp.WithBoolean("all_projects", mcp.Description("全プロジェクト横断検索（デフォルトfalse）")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query := req.GetString("query", "")
			limit := int(req.GetFloat("limit", 5))
			projectFilter := req.GetString("project_filter", "")
			allProjects := req.GetBool("all_projects", false)

			if !allProjects && projectFilter == "" {
				projectFilter = getCurrentProject()
			}

			results, err := search(db, query, limit, projectFilter, 0)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(results) == 0 {
				return mcp.NewToolResultText("記憶が見つかりませんでした。"), nil
			}

			var lines []string
			for _, c := range results {
				t := time.Unix(int64(c.CreatedAt), 0)
				ts := t.Format("2006-01-02 15:04")
				role := "Assistant"
				if c.Role == "user" {
					role = "User"
				}
				branch := ""
				if c.GitBranch.Valid && c.GitBranch.String != "" {
					branch = " [" + c.GitBranch.String + "]"
				}
				lines = append(lines, fmt.Sprintf("[%s%s] %s:\n%s\n", ts, branch, role, c.Content))
			}
			return mcp.NewToolResultText(strings.Join(lines, "\n---\n")), nil
		},
	)

	// memory_save
	s.AddTool(
		mcp.NewTool("memory_save",
			mcp.WithDescription("手動でメモや決定事項を保存する。重要な議論の結論や判断理由を記録。"),
			mcp.WithString("content", mcp.Required(), mcp.Description("保存する内容")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content := req.GetString("content", "")
			if content == "" {
				return mcp.NewToolResultError("content is required"), nil
			}
			id, err := saveManual(db, content)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("記憶を保存しました (id: %d)", id)), nil
		},
	)

	// memory_recent
	s.AddTool(
		mcp.NewTool("memory_recent",
			mcp.WithDescription("直近の記憶一覧を取得する。最近のセッションで何を議論したか確認。デフォルトはカレントプロジェクトのみ。all_projects=trueで全プロジェクト横断。"),
			mcp.WithNumber("days", mcp.Description("取得日数（デフォルト7）")),
			mcp.WithNumber("limit", mcp.Description("結果件数上限（デフォルト10）")),
			mcp.WithBoolean("all_projects", mcp.Description("全プロジェクト横断（デフォルトfalse）")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			days := int(req.GetFloat("days", 7))
			limit := int(req.GetFloat("limit", 10))
			allProjects := req.GetBool("all_projects", false)

			projectFilter := ""
			if !allProjects {
				projectFilter = getCurrentProject()
			}

			results, err := search(db, "", limit, projectFilter, days)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(results) == 0 {
				return mcp.NewToolResultText("直近の記憶がありません。"), nil
			}

			var lines []string
			for _, c := range results {
				t := time.Unix(int64(c.CreatedAt), 0)
				ts := t.Format("2006-01-02 15:04")
				role := "Assistant"
				if c.Role == "user" {
					role = "User"
				}
				preview := c.Content
				if len(preview) > 150 {
					preview = preview[:150] + "..."
				}
				lines = append(lines, fmt.Sprintf("[%s] %s: %s", ts, role, preview))
			}
			return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
		},
	)

	// lesson_save
	s.AddTool(
		mcp.NewTool("lesson_save",
			mcp.WithDescription("コードレビュー指摘・設計判断・学びを記録する。「何をしたか」だけでなく「なぜそうしたか」を必ず含めること。理由のない記録は将来役に立たない。レビュー指摘はcategory='review_feedback'、設計判断はcategory='design_decision'を使う。"),
			mcp.WithString("title", mcp.Required(), mcp.Description("短い要約タイトル")),
			mcp.WithString("content", mcp.Required(), mcp.Description("詳細内容（「なぜ」を必ず含む）")),
			mcp.WithString("category", mcp.Description("カテゴリ: review_feedback, design_decision, lesson_learned, pattern, mistake（デフォルト: lesson_learned）")),
			mcp.WithString("tags", mcp.Description("カンマ区切りタグ（例: rails,activerecord）")),
			mcp.WithString("project_scope", mcp.Description("current=カレントプロジェクト, global=全プロジェクト共通（デフォルト: current）")),
			mcp.WithString("source", mcp.Description("記録元: pr_review, manual, session等")),
			mcp.WithString("source_ref", mcp.Description("参照URL（PRコメントURL等）")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			title := req.GetString("title", "")
			content := req.GetString("content", "")
			if title == "" || content == "" {
				return mcp.NewToolResultError("title and content are required"), nil
			}
			category := req.GetString("category", "lesson_learned")
			tags := req.GetString("tags", "")
			scope := req.GetString("project_scope", "current")
			source := req.GetString("source", "manual")
			sourceRef := req.GetString("source_ref", "")

			projectPath := ""
			if scope == "current" {
				projectPath = getCurrentProject()
			}

			lesson := Lesson{
				Category:    category,
				Title:       title,
				Content:     content,
				ProjectPath: projectPath,
				Tags:        tags,
				Source:      source,
				SourceRef:   sourceRef,
			}
			id, err := saveLesson(db, lesson)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("レッスンを保存しました (id: %d, category: %s)", id, category)), nil
		},
	)

	// lesson_search
	s.AddTool(
		mcp.NewTool("lesson_search",
			mcp.WithDescription("保存した開発者レッスンをキーワード検索する。過去のレビュー指摘や設計判断を検索。デフォルトはカレントプロジェクト+グローバルレッスン。"),
			mcp.WithString("query", mcp.Required(), mcp.Description("検索キーワード")),
			mcp.WithString("category", mcp.Description("カテゴリでフィルタ: review_feedback, design_decision, lesson_learned, pattern, mistake")),
			mcp.WithBoolean("all_projects", mcp.Description("全プロジェクト横断検索（デフォルトfalse）")),
			mcp.WithNumber("limit", mcp.Description("結果件数上限（デフォルト10）")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query := req.GetString("query", "")
			if query == "" {
				return mcp.NewToolResultError("query is required"), nil
			}
			category := req.GetString("category", "")
			allProjects := req.GetBool("all_projects", false)
			limit := int(req.GetFloat("limit", 10))

			projectPath := ""
			if !allProjects {
				projectPath = getCurrentProject()
			}

			results, err := searchLessons(db, query, projectPath, category, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(results) == 0 {
				return mcp.NewToolResultText("該当するレッスンが見つかりませんでした。"), nil
			}

			// Increment recall_count for search hits
			ids := make([]int64, len(results))
			for i, l := range results {
				ids[i] = l.ID
			}
			incrementRecallCount(db, ids)

			var lines []string
			for _, l := range results {
				line := fmt.Sprintf("[id:%d] [%s] %s\n%s", l.ID, categoryLabel(l.Category), l.Title, l.Content)
				if l.Tags != "" {
					line += fmt.Sprintf("\ntags: %s", l.Tags)
				}
				if l.SourceRef != "" {
					line += fmt.Sprintf("\nref: %s", l.SourceRef)
				}
				line += fmt.Sprintf("\n(recall_count: %d)", l.RecallCount+1)
				lines = append(lines, line)
			}
			return mcp.NewToolResultText(strings.Join(lines, "\n\n---\n\n")), nil
		},
	)

	// lesson_list
	s.AddTool(
		mcp.NewTool("lesson_list",
			mcp.WithDescription("保存した開発者レッスンの一覧を取得する。カテゴリやプロジェクトでフィルタ可能。"),
			mcp.WithString("category", mcp.Description("カテゴリでフィルタ: review_feedback, design_decision, lesson_learned, pattern, mistake")),
			mcp.WithBoolean("all_projects", mcp.Description("全プロジェクト横断（デフォルトfalse）")),
			mcp.WithNumber("limit", mcp.Description("結果件数上限（デフォルト20）")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			category := req.GetString("category", "")
			allProjects := req.GetBool("all_projects", false)
			limit := int(req.GetFloat("limit", 20))

			projectPath := ""
			if !allProjects {
				projectPath = getCurrentProject()
			}

			results, err := listLessons(db, projectPath, category, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if len(results) == 0 {
				return mcp.NewToolResultText("レッスンがありません。"), nil
			}

			var lines []string
			for _, l := range results {
				preview := l.Content
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				line := fmt.Sprintf("[id:%d] [%s] %s — %s", l.ID, categoryLabel(l.Category), l.Title, preview)
				if l.Tags != "" {
					line += fmt.Sprintf(" (tags: %s)", l.Tags)
				}
				line += fmt.Sprintf(" [recalls: %d]", l.RecallCount)
				lines = append(lines, line)
			}
			return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
		},
	)

	// lesson_update
	s.AddTool(
		mcp.NewTool("lesson_update",
			mcp.WithDescription("保存済みレッスンの内容を更新する。指定されたフィールドのみ更新。"),
			mcp.WithNumber("id", mcp.Required(), mcp.Description("レッスンID")),
			mcp.WithString("title", mcp.Description("新しいタイトル")),
			mcp.WithString("content", mcp.Description("新しい内容")),
			mcp.WithString("category", mcp.Description("新しいカテゴリ")),
			mcp.WithString("tags", mcp.Description("新しいタグ（カンマ区切り）")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := int64(req.GetFloat("id", 0))
			if id == 0 {
				return mcp.NewToolResultError("id is required"), nil
			}

			var title, content, category, tags *string
			if v := req.GetString("title", ""); v != "" {
				title = &v
			}
			if v := req.GetString("content", ""); v != "" {
				content = &v
			}
			if v := req.GetString("category", ""); v != "" {
				category = &v
			}
			if v := req.GetString("tags", ""); v != "" {
				tags = &v
			}

			if title == nil && content == nil && category == nil && tags == nil {
				return mcp.NewToolResultError("少なくとも1つのフィールドを指定してください"), nil
			}

			if err := updateLesson(db, id, title, content, category, tags); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("レッスン %d を更新しました", id)), nil
		},
	)

	// lesson_delete
	s.AddTool(
		mcp.NewTool("lesson_delete",
			mcp.WithDescription("保存済みレッスンを削除する。"),
			mcp.WithNumber("id", mcp.Required(), mcp.Description("レッスンID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := int64(req.GetFloat("id", 0))
			if id == 0 {
				return mcp.NewToolResultError("id is required"), nil
			}
			if err := deleteLesson(db, id); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("レッスン %d を削除しました", id)), nil
		},
	)

	if err := server.ServeStdio(s); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
