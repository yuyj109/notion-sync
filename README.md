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
notion-sync sync ./docs --parent-id <page-id> --parent-type page --dry-run
notion-sync sync ./docs --parent-id <page-id> --parent-type page

# DB に同期
notion-sync sync ./docs --parent-id <database-id> --parent-type db --dry-run
notion-sync sync ./docs --parent-id <database-id> --parent-type db
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
notion-sync page sync ./docs/guide.md --parent-id <page-id> --parent-type page --dry-run
notion-sync page sync ./docs/guide.md --parent-id <page-id> --parent-type page
notion-sync page sync ./docs/guide.md --parent-id <database-id> --parent-type db --dry-run
notion-sync page sync ./docs/guide.md --parent-id <database-id> --parent-type db
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

DB 同期では、Markdown の frontmatter からプロパティを設定できます。

```yaml
---
notion-properties:
  Status: Done
  Description: Hello World
  Tags: [Go, Notion]
---
```

全対象ファイルに同じ値を設定する場合は、`--property` を繰り返します。指定値は各ファイルの `notion-properties` にも保存され、同名の既存値を上書きします。スペースを含む値はシェルの引用符で囲んでください。

```sh
notion-sync sync ./docs --parent-id <database-id> \
  --property Status=Done --property 'Description=Hello World' --dry-run
notion-sync sync ./docs --parent-id <database-id> \
  --property Status=Done --property 'Description=Hello World'
```

`page sync`、`diff`、`page diff` でも指定できます。DB にない名前や未対応の型はエラーになり、指定のないプロパティは保持します。対応型はテキスト、数値、チェックボックス、日付（開始日のみ）、セレクト、ステータス、マルチセレクト、URL、メール、電話番号です。セレクト系は DB に存在する選択肢を指定してください。マルチセレクトは frontmatter の配列、または `--property 'Tags=Go,Notion'` のようにカンマ区切りで指定し、値全体を置き換えます。読み取り専用型、リレーション、ユーザー、ファイル、日付範囲には対応していません。タイトルは従来どおり本文から決まります。

`--dry-run` で変更予定を確認できます。実行時はローカルの frontmatter を先に更新してから Notion に反映するため、途中で失敗した場合は両方を確認してください。frontmatter の YAML の書式は変更されることがあります。計画後に対象プロパティが Notion 側で変更された場合は上書きせず停止します。

### 同期・復旧

```sh
notion-sync diff ./docs --parent-id <page-id>
notion-sync sync ./docs --parent-id <database-id> --dry-run
notion-sync sync ./docs --parent-id <database-id>
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

`<page>` と `<database>` は ID・Notion URL・一意な完全タイトルを使用できます。`--parent-id` は ID のみ受け付けます。`page create` と `db create` で作成したページは同期対象に自動登録されません。

## 設定

`notion-sync init` で `.env` を作成できます。既存の `.env` は上書きしません。設定の優先順位は、コマンドオプション、環境変数、`.env`、既定値の順です。

`page sync` / `page diff` / `recover` のファイルは設定された同期元ディレクトリ内に置いてください。`NOTION_SYNC_SOURCE` が環境変数や `.env` にない場合は、ファイルの親ディレクトリを同期元にします。別の同期元を使う場合は `--source` で指定できます。`page sync` / `page diff` は他の対象ファイルも共有の同期情報のために読みますが、画像は指定したファイルだけを検証します。

`--parent-id` または `NOTION_SYNC_PARENT_ID` にはページ ID と DB ID のどちらも指定できます。`--parent-type page` または `--parent-type db` で種類を明示できます。省略時は `auto` として Notion API で種類を確認し、DB はフラットに同期します。以前の親指定オプションや環境変数は使用できません。既存の `.env` も `NOTION_SYNC_PARENT_ID` に変更してください。

## 環境変数

| 環境変数 | オプション | 既定値 |
| --- | --- | --- |
| `NOTION_SYNC_PARENT_ID` | `--parent-id` | なし |
| `NOTION_SYNC_PARENT_TYPE` | `--parent-type` | `auto` |
| `NOTION_SYNC_SOURCE` | `--source` または同期元の引数 | `./docs` |
| `NOTION_SYNC_PRESERVE_HIERARCHY` | `--preserve-hierarchy` | `true` |
| `NOTION_SYNC_DRAWIO_EMBED` | `--drawio-embed` | `false` |
| `NOTION_SYNC_METADATA_FORMAT` | `--metadata-format` | `frontmatter` |
| `NOTION_SYNC_INCLUDE` | `--include` | `**/*.md` |
| `NOTION_SYNC_EXCLUDE` | `--exclude` | `**/node_modules/**` |

たとえば DB を環境変数で指定して同期できます。`NOTION_SYNC_PARENT_ID` は `.env` にも設定できます。

```sh
export NOTION_SYNC_PARENT_ID="your-database-id"
export NOTION_SYNC_PARENT_TYPE="db"
notion-sync sync ./docs
```

同期情報を HTML コメントに保存するには、`NOTION_SYNC_METADATA_FORMAT=comment` を環境変数または `.env` に設定するか、`--metadata-format comment` を指定します。`frontmatter` が従来の既定値です。形式を変更すると次の同期で既存の同期情報も変換されます。継続して同じ形式を使うには環境変数または `.env` に設定してください。

同期元の `.notion-sync/state-<親ID>.json` に、共有ディレクトリの対応先と作成保留状態を保存します。ファイルを除外・削除しても重複作成を防ぐため、このファイルを保持してください。既存の Markdown 内の同期情報も読み取ります。作成保留中のファイルを削除した場合は、復元してから `recover` を実行してください。複数環境で同じ同期元を使う場合は、この共有状態も引き継いでください。

共有状態の移行が未完了の場合、従来の同期情報を探すため、最初の同期では除外ファイルも走査します。移行が成功すると完了状態を保存し、以降は移行目的で除外ファイルを読みません。dry-run は完了状態を保存しません。

draw.io の埋め込み方式を常に使うには、環境変数または `.env` に `NOTION_SYNC_DRAWIO_EMBED=true` を設定します。コマンドのオプションは環境変数より優先されるため、一時的に無効にするには `--drawio-embed=false` を指定します。管理対象の埋め込みがあるページを同期する場合は、埋め込みを保護するため有効にしてください。ディレクトリの `sync --pull` とは併用できません。

## 仕組み

`ntn` 経由で Notion API を呼び出します。`diff` と `--dry-run` は Notion とローカルファイルを変更せず、`sync` は Markdown の frontmatter または HTML コメントに同期情報を書き戻します。DB 同期では、サブディレクトリのファイルも同じ DB に作成されます。

同期や差分確認の開始後は、同期先の確認、ファイルごとの Notion ページやロックの確認、画像・埋め込みの検証などを進捗として表示します。処理件数が多い場合や Notion の応答に時間がかかる場合は、各表示の後に待ち時間が発生します。

新規作成するページは編集ロックをオンにします。同期で作成するディレクトリ用ページと `page create`・`db create` にも適用されます。同期対象の既存ページも、本文に変更がなくてもロックをオンにします。`--dry-run` はロック対象を表示します。作成後にロック設定を確認できなかった場合は、Notion で該当ページを確認し、必要なら手動でロックしてください。同期の場合は作成保留を `recover` で復旧します。

アイコンは指定した場合に設定します。例えば `notion-sync sync ./docs --parent-id <ID> --icon document` は、同期対象の新規・既存ページとディレクトリ用ページに Notion の組み込み `document` アイコンを既定色で設定します。`page sync` や `page create`、`db create` でも `--icon <名前>` を使えます。省略時は新規ページにアイコンを指定せず、既存ページのアイコンを保持します。既存アイコンは上書きされるため、`--dry-run` で対象を確認してください。指定できる名前は Notion の組み込みアイコン名で、絵文字・画像はこのオプションでは指定できません。

`--icon random` は、確認済みの Notion 組み込みアイコンの候補からページごとに選びます。同期では親 ID とファイル・ディレクトリのパスをもとに選ぶため、同じ対象を再同期してもアイコンは変わりません。既存ページのアイコンも選択結果で上書きします。`--dry-run` は実際に設定されるアイコン名を表示します。`page create`・`db create` では作成のたびにランダムに選びます。

Notion 側に変更がある場合は同期を停止します。ローカルの内容で上書きする場合は、まず `notion-sync sync ./docs --push --dry-run` で対象を確認し、`--dry-run` を外して実行します。`--push` は指定した同期範囲の競合するページすべてに適用されます。単一ファイルは `notion-sync page sync <file> --push` を使えます。従来の `--overwrite` も `--push` の別名として利用できます。

Notion のページロックは Notion 画面での編集を制限する機能で、API 経由の更新は制限しません。ロック中のページも同期できますが、Notion 本文が前回の同期時から変わっていれば、通常どおり競合として停止します。

同期できないページをスキップした場合は、他の対象の処理を続けたうえで非 0 の終了コードを返します。dry-run も安全に更新できないページを警告し、非 0 を返します。必須の引数や設定が不足した場合も非 0 を返します。

Notion 側の変更をローカルへ取り込む場合は、`notion-sync sync ./docs --pull --dry-run` または単一ファイルの `notion-sync page sync ./docs/guide.md --pull --dry-run` で確認し、`--dry-run` を外して実行します。同期済みで Notion 側に変更があるファイルだけを対象とし、Notion には書き込みません。ローカルにも異なる未同期変更がある場合や、変更行のリンク・ページメンションなどを安全に変換できない場合は停止します。画像・draw.io 埋め込みを含む単一ファイルは、元の Notion 本文と画像参照が保存済みの状態と一致し、末尾にテキストだけが追加された場合に限り取り込めます。この場合は `--drawio-embed` と併用できます。画像や埋め込み構成の変更、途中の編集、追加されたリンク・HTML、256 行以上の追加など、確認できない変更は停止します。管理対象 draw.io の URL は既存の同期と同様に保持します。ディレクトリの pull は従来どおり画像・ファイル・HTML コメントを含むページには対応していません。

macOS、Linux、BSD では、同期が中断されてロックが残った場合、ロックの所有プロセスが終了していれば、次回の同期時に削除して再試行するか確認します。実行中のプロセスが保持しているロックや所有者を確認できないロックは自動削除しません。

競合時は Notion の本文を `-`、ローカルの同期対象本文を `+` とする差分を表示します。見やすくするため、コード以外の空行、Notion の `<empty-block/>`、同じページを指すメンションの表記差は表示用の比較から除きます。端末では各行に、正規化前の Notion 本文と frontmatter を含むローカルファイルの行番号を表示します。`@@` の範囲は正規化後の本文を基準とします。画像の参照形式や HTML を含む URL は、表示時にユーザー情報や署名・トークンなどの認証パラメータを除去し、その他のクエリを表示します。リストは Markdown 上の階層が同じ場合、インデント幅の差を表示用の比較から除きます。同期情報のブロック ID と変更のないローカル画像が一意に対応する場合は画像の URL とパスを同じ行として扱い、Notion 側の画像内容は別途確認が必要である旨を表示します。競合判定自体は省略せず行います。

画像 URL の競合判定では、Notion の署名付き画像 URL の署名などを除き、クエリの変更も検出します。以前のバージョンで同期したページにクエリ付き画像やそのコード例がある場合、判定方式の変更により、Notion 側に実際の変更がなくても競合として停止することがあります。差分と Notion の内容を確認してから対処してください。

`sync` と `page sync` のどちらも、端末では差分の削除行に薄い赤、追加行に薄い緑の背景を付けて表示します。出力をリダイレクトした場合は行番号と色を付けず、環境変数 `NO_COLOR` が設定されている場合は色だけを無効にします。

`sync` は新規ページを空の状態で先に作成し、その後に本文を同期します。本文中の `[子ページ](./child/doc.md)` のような Markdown ファイルへの相対リンクは、対象のページ ID が分かる場合に Notion のページメンションへ変換します。`[設定](./child/doc.md#設定)` や `[設定](#設定)` は `設定 - ページメンション` と表示し、特定の見出しには移動しません。同期対象外・未同期のページへの見出しリンクは文字列として残し、クエリ付きの内部リンクは変換しません。元の Markdown は変更しません。

旧バージョンで作成した見出しリンクは、各ページを次に同期するとページメンションに置き換わります。移行中は、未同期のページに古い見出しリンクが残ります。リンク先の本文を再作成すると古いリンクが切れる場合があるため、関連ページを含むディレクトリの同期を推奨します。

同期元ディレクトリの外を指す相対リンクは、GitHub の `origin` があり、リンク先が現在の `HEAD` に含まれていれば、そのコミットの GitHub ファイル URL に変換します。`../guide.md#section` のようなフラグメントも URL に引き継ぎます。解決できない場合は `[表示テキスト](相対パス)` の形式のまま、リンクではなく文字列として同期します。URL が示すのはコミット済みの内容で、未コミットの変更は含みません。GitHub で閲覧するには、対象コミットがリモートに存在し、閲覧者に権限が必要です。

HTML コメントは Notion への同期本文から除外します。ローカルファイルには残り、コードブロックやインラインコード内のコメント例は保持します。

ファイルごとに既存ページ、または新規ページの配置先を指定できます。ファイルの配置先は既定の同期先より優先されます。

```yaml
---
notion-page-id: "<existing-page-id>"         # 既存ページへの同期
# notion-parent-page-id: "<parent-page-id>"  # 新規ページの親
# notion-database-id: "<database-id>"        # 新規 DB 項目の配置先
---
```

コメント形式では、ファイル先頭（frontmatter がある場合はその直後）に次のように記述します。既存の frontmatter の Notion 以外の項目は保持されます。

```html
<!-- notion-sync
notion-page-id: "<existing-page-id>"
-->
```

親ページと DB は同時指定できません。新規ページのタイトルは本文の最初の `#` 見出し、なければファイル名です。既存ページのタイトルは変更しません。同期後に追加される `notion-*` は競合検出と重複作成の防止に使います。

同期先を変えて新しいページを作る場合、`reset` で対象の Notion ID と同期情報をクリアし、新しい同期先で dry-run を確認します。既存の Notion ページは移動・削除されません。必要なら元ファイルをバックアップしてください。ディレクトリの `reset` は Markdown を再帰的に処理します。`--include` / `--exclude` は指定できないため、対象を絞る場合は個別のファイルやディレクトリを指定してください。

作成結果が不明な場合は、Notion で確認してから `recover --page-id` で作成済みページを紐付けるか、未作成を確認した場合は `recover --not-created` で保留を解除します。復旧後の次回同期では、画像を含め本文をローカルの内容で置き換えます。Notion 側に残すべき内容がないことを確認してください。DB に同期したファイルの復旧には、同じ DB を `--parent-id`、環境変数または `.env` で指定してください。保留中のファイルは `reset` できません。`reset` は対象ファイルの親ディレクトリにある共有状態も確認します。

- 同期済みページの本文や親に変更がある場合、競合として停止します。競合確認と更新の間の変更は防げません。
- ローカルファイルは書き換え直前にも変更を確認しますが、確認と置換の間の同時編集は防げません。同期・復旧・reset 中は対象ファイルを編集しないでください。
- 既存ページを初めて指定する場合、本文が空かローカルと一致するときのみ同期します。
- Markdown の単独行 `![説明](images/photo.png)` で指定したローカル画像を本文中の位置にアップロードできます。画像は Markdown からの相対パスで、同期元ディレクトリ内の通常ファイルに限り、PNG、JPEG、GIF、WebP、SVG の各20 MB以下です。本文が変わらず画像の hash だけが変わった場合は、画像を再アップロードして既存の画像ブロックを置き換えます。更新前に Notion の画像ブロックと Markdown の参照を一意に対応付け、ファイル URL の一致も確認します。元のファイル URL を確認できない固定プロキシなどは、アップロードせず停止します。更新後はブロックの応答と再取得結果を照合します。本文が変わった場合は、本文とすべてのローカル画像を元の順に再作成します。ローカル画像の削除は未対応です。アップロード結果が不明な場合は再実行前に Notion を確認してください。
- MP4 動画も、単独行の `[動画](videos/demo.mp4)` または `![動画](videos/demo.mp4)` で指定できます。Notion には動画ブロックとして配置し、ファイルが変わった場合は既存の動画ブロックを更新します。20 MiB を超える動画は分割してアップロードします。Notion ワークスペース側のファイルサイズ上限も適用されます。
- draw.io 形式の SVG は、アップロード時に埋め込み編集データ（ルート要素の `content` 属性）を除去します。元の SVG は変更せず、変更検知には元ファイルの hash を使います。Notion から取得した SVG には draw.io の編集データは含まれません。既に同期済みで変更のない画像は自動的には再アップロードしません。画像の表示を保証するものではありません。
- `sync` または `page sync` に `--drawio-embed` を指定すると、新しい `.drawio.svg` 参照のビューアー URL を `.notion-sync/<元のファイル名>.txt` に出力し、Notion には空の埋め込みブロックを配置します。URL は手動で貼り付けてください。既存の管理対象埋め込みは URL とブロックを保持し、図の変更は同期しません。通常画像が混在する場合も、画像と埋め込みの間の本文を、安全に識別できれば部分更新します。通常画像の変更や画像の構成などを安全に扱えない場合はそのページの同期を見送ります。URL には図のデータ自体が含まれるため、共有先には内容が見えます。URL が 2,000 文字を超える場合は警告を表示します。Notion の画面で長い URL を受け付けない場合は、アクセス可能な場所に図を保存し、短いビューアーリンクを利用してください。新しい図の出力先に異なる内容のファイルがある場合や、同名の異なる図がある場合は上書きせず停止します。
- Notion 側の自動削除・アーカイブ、ページ移動、双方向同期、一般の添付ファイルには対応していません。シンボリックリンクは走査しません。
- `--drawio-embed --push --replace-drawio-embeds` では既存の管理対象 draw.io 埋め込みを含む更新対象ページの本文全体を置き換え、埋め込みも空のブロックとして作り直します。図のファイルだけが変更された場合も更新対象です。出力された URL を再度貼り付けてください。既存の URL ファイルと出力内容が異なる場合は、移動してから再実行してください。新規ページへのリンクが解決されることで埋め込みの再作成が必要になる場合も dry-run に表示します。変更のないページや管理対象の埋め込みがないページは、このオプションによる本文全体の置換対象にはなりません。ローカル画像の削除はこのオプションを指定しても未対応です。`--replace-drawio-embeds` を指定しない場合は既存の埋め込みを保持し、安全に更新できないページはスキップします。
- 複数データソースを持つ DB と、タイトル以外の必須プロパティがある DB への作成には対応していません。Markdown の変換には [Notion API の制約](https://developers.notion.com/guides/data-apis/working-with-markdown-content) があります。
- ロックファイルは OS の一時ディレクトリにユーザーごとに作成します。同じユーザーの同期・復旧・reset は、同じ一時ディレクトリを使う場合、同期元や作業ディレクトリが異なっても同時には実行できません。`TMPDIR` などで一時ディレクトリを変えたプロセス同士は排他されないため、同じ対象を同時に操作しないでください。異常終了後にロックが残った場合は、他のプロセスが動いていないことを確認してからエラーに表示されたファイルを削除してください。

## Skills

[notion-sync skill](skills/notion-sync/SKILL.md)

## リンク

- [Notion CLI のインストール](https://developers.notion.com/cli/get-started/installation)
- [Notion API リファレンス](https://developers.notion.com/reference)

## ライセンス

[MIT License](LICENSE)
