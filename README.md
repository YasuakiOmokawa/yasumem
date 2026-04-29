# yasumem

セッション間の記憶を永続化・検索する Claude Code プラグイン。

過去の会話を SQLite に蓄積し、MCP ツールから検索できる。Claude が必要なときにツールで取り出す。

## ツール

- `memory_save` / `memory_search` — 会話の保存・検索
- `lesson_save` / `lesson_search` / `lesson_list` / `lesson_update` / `lesson_delete` — 開発レッスンの管理
- `persona_save` / `persona_recall` — ペルソナとの思い出（persona未指定時は subaru）

## インストール

```bash
/plugin marketplace add YasuakiOmokawa/yasumem
/plugin install yasumem@yasumem-marketplace
```

初回のみビルドが必要（Node.js 20+）:

```bash
cd ${CLAUDE_PLUGIN_ROOT}/ts
npm install
npm run build
```
