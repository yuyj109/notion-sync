# notion-sync

Markdown を Notion のページやデータベース（DB）に同期する CLI です。認証には Notion 公式 CLI `ntn` を使います。Notion からの限定的な取り込みにも対応しています。

## インストール

Go 1.27 以上（推奨 1.27.3）と [Notion 公式 CLI `ntn`](https://developers.notion.com/cli/get-started/installation) が必要です。`ntn` 0.23.25 で動作確認しています。

```sh
# このリポジトリ内で実行
go install .
```

Go の bin ディレクトリを `PATH` に追加してください。公開後は `go install github.com/yuyj109/notion-sync@latest` でもインストールできます。バージョンの確認には `notion-sync --version` を使います。

## はじめに

```sh
# ログインと認証状態の確認
notion-sync auth login
notion-sync auth status

# 同期先を探す
notion-sync search "meeting" --limit 50
notion-sync db list

# ディレクトリを同期する前に変更予定を確認
notion-sync sync ./docs --parent-id <page-or-database-id> --dry-run

# 内容を確認してから実行
notion-sync sync ./docs --parent-id <page-or-database-id>
```

`--parent-id` にはページまたは DB の ID を指定します。種類は自動判定され、`--parent-type page` / `db` でも指定できます。DB への同期ではサブディレクトリのファイルも同じ DB に作成されます。

`--dry-run` と `diff` は Notion とローカルファイルを変更しません。実行前に対象と変更内容を確認してください。同期時には Markdown に同期情報が書き込まれます。

## よく使う操作

```sh
# 単一ファイルの同期
notion-sync page sync ./docs/guide.md --parent-id <parent-id> --dry-run
notion-sync page sync ./docs/guide.md --parent-id <parent-id>

# 同期予定の確認
notion-sync diff ./docs --parent-id <parent-id>
notion-sync page diff ./docs/guide.md --parent-id <parent-id>

# ページ・DB の検索と閲覧
notion-sync page list -q "meeting"
notion-sync page view <page>
notion-sync db query <database> --json

# 同期対象に登録せずページを作成
notion-sync page create --parent <page> --title "Title" --content "Markdown"
notion-sync db create <database> --title "Title" --content "Markdown"

# 設定ファイル作成、アクセス確認、ヘルプ
notion-sync init
notion-sync doctor
notion-sync <command> --help
```

`<page>` と `<database>` には ID、Notion URL、一意な完全タイトルを指定できます。`--parent-id` は ID のみです。`doctor` は読み取りアクセスを確認します。

## 同期の設定

`notion-sync init` で `.env` を作成できます。既存ファイルは上書きしません。設定の優先順位は、コマンドオプション、環境変数、`.env`、既定値の順です。

| 環境変数 | オプション | 既定値 |
| --- | --- | --- |
| `NOTION_SYNC_PARENT_ID` | `--parent-id` | なし |
| `NOTION_SYNC_PARENT_TYPE` | `--parent-type` | `auto` |
| `NOTION_SYNC_SOURCE` | `--source` または同期元の引数 | `./docs` |
| `NOTION_SYNC_PRESERVE_HIERARCHY` | `--preserve-hierarchy` | `true` |
| `NOTION_SYNC_DRAWIO_EMBED` | `--drawio-embed` | `false` |
| `NOTION_SYNC_METADATA_FORMAT` | `--metadata-format` | `comment` |
| `NOTION_SYNC_INCLUDE` | `--include` | `**/*.md` |
| `NOTION_SYNC_EXCLUDE` | `--exclude` | `**/node_modules/**` |

単一ファイルの同期・差分確認・復旧では、ファイルを設定された同期元内に置いてください。同期元を設定していない場合はファイルの親ディレクトリが使われます。

ファイルごとに同期先を指定する場合は、frontmatter に次のいずれかを記載します。ファイルごとの指定は既定の同期先より優先されます。

```yaml
---
notion-page-id: "<existing-page-id>"       # 既存ページ
# notion-parent-page-id: "<parent-id>"     # 新規ページの親
# notion-database-id: "<database-id>"      # 新規 DB 項目の配置先
---
```

同期情報と `notion-properties` は既定ではファイル先頭の HTML コメントに保存されます。`--metadata-format frontmatter` または `NOTION_SYNC_METADATA_FORMAT=frontmatter` で YAML frontmatter に変更できます。次の同期で既存の情報も変換されるため、dry-run で確認してください。既存の `.env` の設定は既定値より優先されます。

同期時は Notion の属性を `notion-local-hash`、`notion-remote-hash`、`notion-root-id`、`notion-page-id`、`notion-parent`、`notion-target`、`notion-images`、`notion-properties` の順に保存します。存在する属性だけを書き出し、その他の属性は保持します。既存ファイルの順序変更も dry-run に表示します。

同期情報と共有状態は、競合や重複作成の防止に使います。共有状態は同期元ごとに保存され、`diff` や `--dry-run` を含む同期時に保存先が表示されます。

- macOS：`~/.notion-sync/`。`XDG_CONFIG_HOME` は使いません。
- その他：`$XDG_CONFIG_HOME/notion-sync/`。未設定または相対パスの場合は `~/.config/notion-sync/`。

新しい保存先に状態がない場合、同期元内の旧形式 `.notion-sync/state-<親ID>.json` を実行時に引き継ぎます。旧ファイルは削除されないため、旧バージョンとの併用は避けてください。dry-run では引き継ぎません。同期・復旧・reset 中は対象ファイルを編集しないでください。

### 同期元を移動する場合

保存先は同期元の絶対パスで決まります。移動や別環境へのコピー後、共有状態を引き継ぐ前に同期すると、古い状態が読み込まれ、作成保留やディレクトリの対応先を見落とす可能性があります。

1. 同期・復旧・reset の実行中でないことを確認し、移動前の `sync data directory` にある共有状態をバックアップします。
2. 移動後に `diff` または `--dry-run` で新しい `sync data directory` を確認します。この時点の計画は実行しないでください。
3. 最新の `state-<親ID>.json` と必要な draw.io URL ファイルを新しい保存先に引き継ぎます。同名ファイルがある場合は上書きせず確認してください。
4. 改めて dry-run で対象と変更内容を確認します。最新の状態や作成結果が不明な場合は Notion を確認してから復旧してください。

## DB プロパティ

各ファイルの frontmatter で値を指定できます。

```yaml
---
notion-properties:
  Status: Done
  Description: Hello World
  Tags: [Go, Notion]
---
```

ファイル先頭の `<!-- notion-sync` コメント内にも記載できます。同期時に指定したメタデータ形式へ移動し、両方に定義した場合はエラーになります。YAML のアンカーとエイリアスが形式変更や並べ替えで無効になる場合は、書き換えずに停止します。

```html
<!-- notion-sync
notion-properties:
  Status: Done
-->
```

全対象ファイルに同じ値を設定するには `--property` を繰り返します。`page sync`、`diff`、`page diff` でも使用できます。

```sh
notion-sync sync ./docs --parent-id <database-id> \
  --property Status=Done --property 'Description=Hello World' --dry-run
```

指定した値はマルチセレクトを含め既存値全体を上書きし、未指定のプロパティは保持します。CLI の値はファイル内の同名の値より優先され、同期時に各ファイルにも保存されます。失敗時はローカルと Notion の両方を確認してください。

対応する型はテキスト、数値、チェックボックス、日付（開始日のみ）、セレクト、ステータス、マルチセレクト、URL、メール、電話番号です。セレクト系は DB に存在する選択肢を使います。マルチセレクトは配列または `--property 'Tags=Go,Notion'` で指定します。

## 競合と復旧

Notion 側の本文や親に変更がある場合、通常の同期は競合として停止します。差分を確認し、ローカルの内容で上書きする場合は `--push` を使います。指定した範囲の競合ページすべてに適用されるため、先に dry-run を確認してください。`--overwrite` は `--push` の別名です。

```sh
notion-sync sync ./docs --push --dry-run
notion-sync sync ./docs --push
```

Notion 側の変更を取り込む場合は `--pull` を使います。同期済みで Notion 側に変更があるファイルが対象で、Notion には書き込みません。ローカルにも異なる変更がある場合や安全に変換できない場合は停止します。

```sh
notion-sync page sync ./docs/guide.md --pull --dry-run
notion-sync page sync ./docs/guide.md --pull
```

ディレクトリの pull は画像・添付ファイル・HTML コメントを含むページに対応しません。画像や管理対象 draw.io 埋め込みを含む単一ファイルは、検証可能なテキストのみの末尾追加に限り取り込めます。一般的な双方向同期には対応していません。

作成やアップロードの結果が不明な場合は、再実行する前に Notion を確認してください。作成保留の復旧には、作成済みページを確認した場合は `recover --page-id`、未作成を確認した場合は `recover --not-created` を使います。DB の場合は同じ DB を同期先に指定してください。復旧後の次回同期は画像を含む本文をローカルの内容で置き換えます。

```sh
notion-sync recover ./docs/guide.md --page-id <verified-page-id-or-url>
# または、作成されていないことを確認した場合
notion-sync recover ./docs/guide.md --not-created
```

同期先を変えて新しいページを作る場合は、必要に応じてバックアップし、`reset` でローカルの同期情報を消去してから新しい同期先を dry-run で確認します。既存の Notion ページは移動・削除されません。ディレクトリ指定時は include/exclude 設定に関係なく再帰的に処理し、`--include` / `--exclude` は指定できません。作成保留は先に復旧してください。

```sh
notion-sync reset ./docs --dry-run
notion-sync reset ./docs
notion-sync sync ./docs --parent-id <new-parent-id> --dry-run
```

## 同期される内容と制約

- 新規ページのタイトルは最初の `#` 見出し、なければファイル名です。既存ページのタイトルは変更しません。既存ページへの初回同期は、本文が空かローカルと一致する場合に限ります。
- 新規ページと同期対象の既存ページは Notion 画面でロックされます。API 経由の更新は可能です。`--icon <name>` は組み込みアイコンを新規・既存ページに設定し、既存アイコンを上書きします。`--icon random` も使えます。ロックとアイコンの変更は dry-run で確認できます。
- HTML コメントはローカルに残り、Notion の本文から除外されます。コード内のコメント例は保持します。Markdown の相対ページリンクは解決できればページメンションになり、見出しへのリンクもページ単位になります。古い見出しリンクはリンク先の再作成で切れる場合があるため、関連ページをまとめて同期してください。
- 単独行の `![説明](images/photo.png)` で画像を、`[動画](videos/demo.mp4)` で MP4 を配置できます。画像は同期元内の PNG、JPEG、GIF、WebP、SVG 各 20 MB 以下に対応します。本文の変更時は画像を含む本文が再作成されます。ローカル画像の削除には対応しません。
- draw.io SVG の通常アップロードでは編集データを除去します。`--drawio-embed` は同期元別の保存先にビューアー URL のファイルを作り、URL と保存先を表示します。Notion に作成された空の埋め込みには URL を手動で貼り付けてください。URL とコマンド出力には図のデータが含まれます。既存の埋め込みは保持され、図の変更は自動反映されません。図が未変更で URL ファイルがない場合は再生成し、編集データがなく生成できなければ警告します。既存の URL ファイルと旧保存先のファイルは変更しません。`--push --replace-drawio-embeds` は対象ページの本文と埋め込みを再作成するため、dry-run で確認し、URL を貼り直してください。
- 一般的な添付ファイル、ページ移動、自動削除・アーカイブには対応しません。複数データソースを持つ DB や、タイトル以外に必須プロパティがある DB への作成にも対応しません。シンボリックリンクは走査しません。
- 安全に同期できないページをスキップすると、残りの処理を続けた後に非 0 を返します。競合確認と更新の間の変更や、ローカルファイルの確認と書き換えの間の同時編集は完全には防げません。

詳しい挙動と制約は [仕様書](docs/specification.md)、各コマンドのオプションは `--help` を参照してください。エージェント向けの手順は [notion-sync skill](skills/notion-sync/SKILL.md) にあります。

## ライセンス

[MIT License](LICENSE)
