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

`notion-sync --help` または `notion-sync --version` でビルドのバージョンを確認できます。タグを指定してインストールした場合はそのバージョンを、`@latest` でインストールした場合は Go が解決したバージョンを表示します。ローカルビルドでも Go が Git のタグを取得できる場合はタグ由来のバージョンを表示し、未コミットの変更がある場合は `+dirty` が付きます。バージョンを取得できない場合は `devel` と、利用可能ならコミット ID を表示します。

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

Notion 側に変更がある場合は同期を停止します。ローカルの内容で上書きする場合は、まず `notion-sync sync ./docs --push --dry-run` で対象を確認し、`--dry-run` を外して実行します。`--push` は指定した同期範囲の競合するページすべてに適用されます。単一ファイルは `notion-sync page sync <file> --push` を使えます。従来の `--overwrite` も `--push` の別名として利用できます。

Notion 側の変更をローカルへ取り込む場合は、まず `notion-sync sync ./docs --pull --dry-run` で確認し、`--dry-run` を外して実行します。同期済みで Notion 側に変更があるファイルだけを対象に、変わった行を反映します。ローカルにも異なる未同期変更がある場合は停止します。現時点では画像・ファイル・HTML コメントを含むページや、変更された行にリンクやページメンションがある場合など、安全に変換できない内容は取り込まずに停止します。`--pull` は Notion には書き込みません。`--pull` はディレクトリ同期のみで、`page sync` には対応していません。

macOS、Linux、BSD では、同期が中断されてロックが残った場合、ロックの所有プロセスが終了していれば、次回の同期時に削除して再試行するか確認します。実行中のプロセスが保持しているロックや所有者を確認できないロックは自動削除しません。

競合時は Notion の本文を `-`、ローカルの同期対象本文を `+` とする差分を表示します。見やすくするため、コード以外の空行、Notion の `<empty-block/>`、同じページを指すメンションの表記差は表示用の比較から除きます。端末では各行に、正規化前の Notion 本文と frontmatter を含むローカルファイルの行番号を表示します。`@@` の範囲は正規化後の本文を基準とします。画像 URL の一時的なクエリは表示しません。競合判定自体は省略せず行います。

`sync` と `page sync` のどちらも、端末では差分の削除行に薄い赤、追加行に薄い緑の背景を付けて表示します。出力をリダイレクトした場合は行番号と色を付けず、環境変数 `NO_COLOR` が設定されている場合は色だけを無効にします。

`sync` は新規ページを空の状態で先に作成し、その後に本文を同期します。本文中の `[子ページ](./child/doc.md)` のような Markdown ファイルへの相対リンクは、同期対象のページ ID が分かる場合に Notion のページメンションへ変換します。元の Markdown のリンクは変更しません。同期元内の同期対象外や未作成のリンク先、クエリやフラグメント付きのリンクはそのまま残ります。`page sync` では既に ID が分かっているリンク先を変換します。

同期元ディレクトリの外を指す相対リンクは、GitHub の `origin` があり、リンク先が現在の `HEAD` に含まれていれば、そのコミットの GitHub ファイル URL に変換します。解決できない場合は `[表示テキスト](相対パス)` の形式のまま、リンクではなく文字列として同期します。URL が示すのはコミット済みの内容で、未コミットの変更は含みません。GitHub で閲覧するには、対象コミットがリモートに存在し、閲覧者に権限が必要です。

HTML コメントは Notion への同期本文から除外します。ローカルファイルには残り、コードブロックやインラインコード内のコメント例は保持します。

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
