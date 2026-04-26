# yasumem

セッション間の記憶を永続化・検索する Claude Code プラグイン。

## 概要

SessionStart フックで直近の会話記憶を自動的にコンテキストに注入し、MCP サーバー経由で記憶の保存・検索を提供する。

- **memory_save**: 会話の要点を永続化
- **memory_search**: 過去の記憶をキーワード・日数で検索
- **lesson_save**: コードレビュー指摘・設計判断・学びを記録
- **lesson_search**: レッスンをキーワード・カテゴリ・タグ・ソースで検索
- **lesson_list**: レッスン一覧をフィルタ付きで取得
- **lesson_update**: レッスンの内容を更新
- **lesson_delete**: レッスンを削除
- **subaru_save**: ペルソナ（すばる）との思い出をシーン種別・気分・タグ付きで保存
- **subaru_recall**: ペルソナとの思い出をキーワード・シーン種別・気分・タグ・日数でフィルタ検索

## インストール

### マーケットプレイスから（推奨）

```bash
# マーケットプレイスを追加
/plugin marketplace add YasuakiOmokawa/yasumem

# プラグインをインストール
/plugin install yasumem@yasumem-marketplace
```

### 直接インストール

```bash
claude plugin add https://github.com/YasuakiOmokawa/yasumem.git
```

## ビルド

TypeScript 実装。Node.js 20 以上が必要。

```bash
cd ts
npm install
npm run build
chmod +x ../bin/yasumem
```

`bin/yasumem` は `ts/dist/index.js` を呼び出すラッパースクリプト。`.mcp.json` から `${CLAUDE_PLUGIN_ROOT}/bin/yasumem` として起動される。

### 開発

```bash
cd ts
npm run dev -- server   # tsx で直接実行
```

### コマンド

- `yasumem server` — MCP サーバー起動 (stdio)
- `yasumem ingest` — stdin の `{session_id, cwd}` から単一セッションを取り込み
- `yasumem ingest-recent` — stdin の `{cwd}` から最近の jsonl を一括取り込み（SessionStart hook 用）

DB スキーマと `data/` ファイル配置は Go 実装 (v0.3.x) と完全互換。
