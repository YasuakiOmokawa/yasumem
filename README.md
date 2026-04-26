# yasumem

セッション間の記憶を永続化・検索する Claude Code プラグイン。

## 概要

過去の Claude Code セッションのチャット履歴を SQLite に蓄積し、MCP ツール経由で検索できるようにする。

- **SessionStart hook** が起動時に直近の `~/.claude/projects/*.jsonl` を差分取り込みして DB を最新化する
- **MCP ツール** で Claude が能動的に過去会話・レッスン・ペルソナ記憶を検索する
- **コンテキスト自動注入はしない**（会話は Claude が必要な時にツールで取り出す）

## ツール

| 名前 | 用途 |
|---|---|
| `memory_save` | 会話の要点を永続化 |
| `memory_search` | 過去の記憶をキーワード・日数で検索 |
| `lesson_save` | コードレビュー指摘・設計判断・学びを記録 |
| `lesson_search` | レッスンをキーワード・カテゴリ・タグ・ソースで検索 |
| `lesson_list` | レッスン一覧をフィルタ付きで取得 |
| `lesson_update` | レッスンの内容を更新 |
| `lesson_delete` | レッスンを削除 |
| `subaru_save` | ペルソナ（すばる）との思い出をシーン種別・気分・タグ付きで保存 |
| `subaru_recall` | ペルソナとの思い出をキーワード・シーン種別・気分・タグ・日数でフィルタ検索 |

## インストール

### マーケットプレイスから

```bash
/plugin marketplace add YasuakiOmokawa/yasumem
/plugin install yasumem@yasumem-marketplace
```

### 直接インストール

```bash
claude plugin add https://github.com/YasuakiOmokawa/yasumem.git
```

### ビルド（必須）

install 直後は `ts/dist/` が存在しないため、初回起動前にビルドする必要がある。

```bash
cd ${CLAUDE_PLUGIN_ROOT}/ts   # 通常は ~/.claude/plugins/repos/yasumem/ts
npm install
npm run build
```

要件:
- Node.js 20 以上（`for-await stdin` と ESM `import.meta.url` 利用のため）
- `better-sqlite3` のネイティブビルド: 多くの環境で prebuilt binary が落ちる。失敗する場合は `python3` と C++ toolchain (`build-essential` 等) が必要

> zip / tarball ダウンロードで取得した場合は `chmod +x bin/yasumem` も必要。git clone なら mode 100755 で配布済み。

## アーキテクチャ

```
[SessionStart]
   │
   └─→ hooks-handlers/session-start.sh
         │
         ├─ data/current_project に canonical project path を書く
         └─ bin/yasumem ingest-recent
              │
              └─→ ~/.claude/projects/*.jsonl 差分パース
                   └─→ data/memory.db (chunks, sessions テーブル)

[Claude が MCP tool 呼出]
   │
   └─→ bin/yasumem server  (.mcp.json から起動)
         │
         └─→ data/memory.db (chunks / lessons / persona_memories)
              FTS5 trigram tokenizer で日本語含む全文検索
```

## 設定

### 環境変数

| 名前 | 用途 | デフォルト |
|---|---|---|
| `YASUMEM_DB` | SQLite DB ファイルの絶対パス | `${CLAUDE_PLUGIN_ROOT}/data/memory.db` |

### data/ ディレクトリ

| ファイル | 役割 |
|---|---|
| `memory.db` | SQLite 本体（FTS5 仮想テーブル含む） |
| `current_project` | カレントプロジェクトの canonical path（hook が書き、server が project_filter で読む） |
| `last_ingest_at` | 最後に ingest-recent を走らせた時刻（差分判定用） |
| `logs/ingest.log` | session-start hook の stderr |

## コマンド

`bin/yasumem` のサブコマンド:

| コマンド | 用途 |
|---|---|
| `server` | MCP サーバー起動 (stdio) |
| `ingest` | stdin の `{session_id, cwd}` から単一セッションを取り込み |
| `ingest-recent` | stdin の `{cwd}` から最近の jsonl を一括取り込み（SessionStart hook 用） |

## 開発

```bash
cd ts
npm run dev -- server   # tsx で直接実行（ビルド不要）
```

ファイル構成:

```
ts/
  src/
    index.ts     # コマンドディスパッチャ
    paths.ts     # DB / current_project / worktree path 解決
    db.ts        # スキーマ DDL、migration、openDB
    chunks.ts    # memory chunks の CRUD/search
    lessons.ts   # lessons の CRUD/search
    subaru.ts    # persona_memories の CRUD/search
    ingest.ts    # JSONL 差分取り込み
    server.ts    # MCP server + 9 ツール登録
bin/yasumem      # node ts/dist/index.js を呼ぶ bash ラッパ
.mcp.json        # bin/yasumem server を起動
hooks/hooks.json # SessionStart で session-start.sh
hooks-handlers/session-start.sh
data/            # 永続データ（gitignore 対象）
```

DB スキーマと `data/` 配下の互換性は維持しているため、過去バージョン（Go 実装含む）の `memory.db` をそのまま利用できる。
