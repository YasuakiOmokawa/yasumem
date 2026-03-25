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

	s := server.NewMCPServer("yasumem", "0.2.0",
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

	if err := server.ServeStdio(s); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
