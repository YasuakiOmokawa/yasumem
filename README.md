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
