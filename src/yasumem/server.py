"""yasumem MCP server. This is the ONLY file that imports mcp."""

import os
from pathlib import Path

from mcp.server import FastMCP

from yasumem.db import get_connection, recall, save_manual, search

DB_PATH = os.environ.get(
    "YASUMEM_DB",
    str(Path(__file__).resolve().parent.parent.parent / "data" / "memory.db"),
)

CURRENT_PROJECT_FILE = Path(__file__).resolve().parent.parent.parent / "data" / "current_project"


def _get_current_project() -> str | None:
    try:
        return CURRENT_PROJECT_FILE.read_text().strip() or None
    except (FileNotFoundError, OSError):
        return None


mcp = FastMCP("yasumem")


@mcp.tool()
def memory_search(query: str, limit: int = 5, project_filter: str | None = None, all_projects: bool = False) -> str:
    """過去のセッション記憶をハイブリッド検索する。キーワードで過去の議論や決定事項を検索。デフォルトはカレントプロジェクトのみ。all_projects=Trueで全プロジェクト横断検索。"""
    if not all_projects and project_filter is None:
        project_filter = _get_current_project()
    conn = get_connection(DB_PATH)
    try:
        results = search(conn, query, limit=limit, project_filter=project_filter)
        if not results:
            return "記憶が見つかりませんでした。"
        lines = []
        for chunk in results:
            import time
            ts = time.strftime("%Y-%m-%d %H:%M", time.localtime(chunk.created_at))
            role = "User" if chunk.role == "user" else "Assistant"
            branch = f" [{chunk.git_branch}]" if chunk.git_branch else ""
            lines.append(f"[{ts}{branch}] {role}:\n{chunk.content}\n")
        return "\n---\n".join(lines)
    finally:
        conn.close()


@mcp.tool()
def memory_save(content: str) -> str:
    """手動でメモや決定事項を保存する。重要な議論の結論や判断理由を記録。"""
    conn = get_connection(DB_PATH)
    try:
        chunk_id = save_manual(conn, content)
        return f"記憶を保存しました (id: {chunk_id})"
    finally:
        conn.close()


@mcp.tool()
def memory_recent(days: int = 7, limit: int = 10, all_projects: bool = False) -> str:
    """直近の記憶一覧を取得する。最近のセッションで何を議論したか確認。デフォルトはカレントプロジェクトのみ。all_projects=Trueで全プロジェクト横断。"""
    project_filter = None if all_projects else _get_current_project()
    conn = get_connection(DB_PATH)
    try:
        results = search(conn, "", limit=limit, max_age_days=days, project_filter=project_filter)
        if not results:
            return "直近の記憶がありません。"
        lines = []
        for chunk in results:
            import time
            ts = time.strftime("%Y-%m-%d %H:%M", time.localtime(chunk.created_at))
            role = "User" if chunk.role == "user" else "Assistant"
            preview = chunk.content[:150] + ("..." if len(chunk.content) > 150 else "")
            lines.append(f"[{ts}] {role}: {preview}")
        return "\n".join(lines)
    finally:
        conn.close()


if __name__ == "__main__":
    mcp.run()
