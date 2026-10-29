# notion-sync

ローカルの Markdown を Notion のページやデータベースに一方向同期する CLI です。Notion 公式 CLI `ntn` の認証を使用します。

## インストール

Go 1.27 以上（推奨 1.27.3）と [Notion 公式 CLI `ntn`](https://developers.notion.com/cli/get-started/installation) が必要です。`ntn` 0.23.25 で動作確認しています。

### Go module から

リポジトリの公開後に利用できます。

```sh
go install github.com/yuyj109/notion-sync@latest
```

### ローカルでビルド

```sh
# リポジトリ内で実行
go install .
```

Go の bin ディレクトリを `PATH` に追加してください。

## クイックスタート

```sh
# ユーザー自身がログイン
notion-sync auth login
notion-sync auth status

# 親ページに同期（変更予定を確認してから実行）
notion-sync sync ./docs --parent-page-id <page-id> --dry-run
notion-sync sync ./docs --parent-page-id <page-id>

# DB に同期
notion-sync sync ./docs --parent-db-id <database-id> --dry-run
notion-sync sync ./docs --parent-db-id <database-id>
```

## コマンド

### 認証

```sh
notion-sync auth login              # ntn を使ってログイン
notion-sync auth status             # 認証状態を確認
```

### ページ

```sh
notion-sync page list -q "meeting"  # ページを一覧表示
notion-sync page view <page>        # ページを表示
notion-sync page create --parent <page> --title "Title" --content "Markdown"

# 単一ファイルをページまたは DB に同期
notion-sync page sync ./docs/guide.md --parent <page> --dry-run
notion-sync page sync ./docs/guide.md --parent <page>
notion-sync page sync ./docs/guide.md --parent-db <database> --dry-run
notion-sync page sync ./docs/guide.md --parent-db <database>
```

### 検索

```sh
notion-sync search "meeting" --limit 50 --json
```

### データベース

```sh
notion-sync db list
notion-sync db query <database> --json
notion-sync db create <database> --title "Title" --content "Markdown"
```

### 同期・復旧

```sh
notion-sync diff ./docs --parent-page-id <page-id>
notion-sync sync ./docs --parent-db-id <database-id> --dry-run
notion-sync sync ./docs --parent-db-id <database-id>
notion-sync reset ./docs --dry-run   # ローカルの同期情報をクリアする対象を確認
notion-sync reset ./docs             # 同期情報をクリア
notion-sync recover ./docs/guide.md --page-id <verified-page-id-or-url>
notion-sync recover ./docs/guide.md --not-created
```

### その他

```sh
notion-sync init                    # .env を作成
notion-sync doctor                  # 認証と指定先の読み取りアクセスを確認
notion-sync --help
notion-sync <command> --help
```

`<page>` と `<database>` は ID・Notion URL・一意な完全タイトルを使用できます。`--parent-page-id` と `--parent-db-id` は ID のみ受け付けます。`page create` と `db create` で作成したページは同期対象に自動登録されません。

## 設定

`notion-sync init` で `.env` を作成できます。既存の `.env` は上書きしません。設定の優先順位は、コマンドオプション、環境変数、`.env`、既定値の順です。親ページと DB の両方を同じ優先順位で指定するとエラーになります。明示的な親ページまたは DB のオプションは、環境変数と `.env` の同期先より優先されます。

## 環境変数

| 環境変数 | オプション | 既定値 |
| --- | --- | --- |
| `NOTION_SYNC_PARENT_PAGE_ID` | `--parent-page-id` | なし |
| `NOTION_SYNC_PARENT_DB_ID` | `--parent-db-id` | なし |
| `NOTION_SYNC_SOURCE` | `--source` または同期元の引数 | `./docs` |
| `NOTION_SYNC_PRESERVE_HIERARCHY` | `--preserve-hierarchy` | `true` |
| `NOTION_SYNC_INCLUDE` | `--include` | `**/*.md` |
| `NOTION_SYNC_EXCLUDE` | `--exclude` | `**/node_modules/**` |

たとえば DB を環境変数で指定して同期できます。`NOTION_SYNC_PARENT_DB_ID` は `.env` にも設定できます。

```sh
export NOTION_SYNC_PARENT_DB_ID="your-database-id"
notion-sync sync ./docs
```

## 仕組み

`ntn` 経由で Notion API を呼び出します。`diff` と `--dry-run` は Notion とローカルファイルを変更せず、`sync` は Markdown の frontmatter に同期情報を書き戻します。DB 同期では、サブディレクトリのファイルも同じ DB に作成されます。

ファイルごとに既存ページ、または新規ページの配置先を指定できます。ファイルの配置先は既定の同期先より優先されます。

```yaml
---
notion-page-id: "<existing-page-id>"         # 既存ページへの同期
# notion-parent-page-id: "<parent-page-id>"  # 新規ページの親
# notion-database-id: "<database-id>"        # 新規 DB 項目の配置先
---
```

親ページと DB は同時指定できません。新規ページのタイトルは本文の最初の `#` 見出し、なければファイル名です。既存ページのタイトルは変更しません。同期後に追加される `notion-*` は競合検出と重複作成の防止に使います。

同期先を変えて新しいページを作る場合、`reset` で対象の Notion ID と同期情報をクリアし、新しい同期先で dry-run を確認します。既存の Notion ページは移動・削除されません。必要なら元ファイルをバックアップしてください。ディレクトリの `reset` は Markdown を再帰的に処理し、`--include` / `--exclude` は適用しません。

作成結果が不明な場合は、Notion で確認してから `recover --page-id` で作成済みページを紐付けるか、未作成を確認した場合は `recover --not-created` で保留を解除します。画像を含むファイルは、ページ本文と画像がローカルの内容どおりに揃っていることも確認してから復旧してください。画像ページの復旧はその内容を同期済みとして記録し、次回同期で画像を再作成しません。DB に同期したファイルの復旧には、同じ DB を `--parent-db-id`、環境変数または `.env` で指定してください。保留中は `reset` できません。

- 同期済みページの本文や親に変更がある場合、競合として停止します。競合確認と更新の間の変更は防げません。
- 既存ページを初めて指定する場合、本文が空かローカルと一致するときのみ同期します。
- Markdown の単独行 `![説明](images/photo.png)` で指定したローカル画像を本文中の位置にアップロードできます。画像は Markdown からの相対パスで、同期元ディレクトリ内の通常ファイルに限り、PNG、JPEG、GIF、WebP の各20 MB以下です。本文が変わらず画像の hash だけが変わった場合は、画像を再アップロードして既存の画像ブロックを置き換えます。本文が変わった場合は、本文とすべてのローカル画像を元の順に再作成します。ローカル画像の削除は未対応です。アップロード結果が不明な場合は再実行前に Notion を確認してください。
- Notion 側の自動削除・アーカイブ、ページ移動、双方向同期、一般の添付ファイルには対応していません。シンボリックリンクは走査しません。
- 複数データソースを持つ DB と、タイトル以外の必須プロパティがある DB への作成には対応していません。Markdown の変換には [Notion API の制約](https://developers.notion.com/guides/data-apis/working-with-markdown-content) があります。
- ロックファイルは OS の一時ディレクトリに作業ディレクトリごとに作成します。異常終了後にロックが残った場合は、他のプロセスが動いていないことを確認してからエラーに表示されたファイルを削除してください。

## Skills

[notion-sync skill](skills/notion-sync/SKILL.md)

## リンク

- [Notion CLI のインストール](https://developers.notion.com/cli/get-started/installation)
- [Notion API リファレンス](https://developers.notion.com/reference)

## ライセンス

[MIT License](LICENSE)
