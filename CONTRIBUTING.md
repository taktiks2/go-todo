# CONTRIBUTING

このリポジトリの **「どう作るか」** を定める。**「何を作るか」** は `docs/DESIGN.md`。

前提として、これは Go を実務導入するための**学習プロジェクト**である。
**速く完成させることより、Go のアプリ設計とテストが身につくことを優先する。**
以下のルールはすべてこの前提から導かれている。

---

## 1. 役割分担

| 対象 | 担当 |
|---|---|
| テストコード（`*_test.go`） | **Claude が書く** |
| 実装コード（Go） | **人間が手で書く** |
| Terraform / Dockerfile / GitHub Actions | Claude が書く |
| TypeScript フロントエンド | Claude が書く |

**Claude は Go の実装コードを自分で適用しない。** 実装は提示するに留め、人間が書き終えるのを待つ。
`Edit` / `Write` を `backend/**/*.go`（`*_test.go` を除く）に走らせてよいのは、
`mode:ai-only` ラベルが付いた issue のときだけ。

この分担の理由は `docs/DESIGN.md` の設計原則そのものにある。
`err != nil` の積み方、`interface` の切り方、手書き fake を書く「痛み」——
これらは読んで理解するものではなく、手で書いて体に入れるもの。
Claude が書いてしまうと、残るのは「Claude に go-todo を作らせた経験」であって Go ではない。

一方で Terraform・WIF・フロントエンドは、`docs/DESIGN.md` 自身が
「書き切ったらしばらく触らない」「フロントに時間を取られないための選択」と位置づけている。
学習の重心が Go に寄っているので、ここは Claude が書いて時間を節約する。

---

## 2. 実装の進め方: pair-tdd

**すべての Go 実装は `pair-tdd` スキルのループで進める。** 例外は `mode:ai-only` の issue のみ。

1. Claude が失敗するテストを書く（完成形。`// TODO` を残さない）
2. `just test` で **RED を実行ログで確認する**
3. `test(scope): ...` でコミット
4. Claude が実装を提示する（ファイルパス・配置位置・**読みどころ**付き）
5. **人間が手で書く。** Claude は待つ
6. `just test` で GREEN を確認
7. `feat(scope): ...` でコミット（バグ修正は `fix:`、振る舞いが変わらないなら `refactor:`）

バグを手動確認中に見つけた場合も、このループを回す。1 行修正でも例外にしない。

---

## 3. issue

### 粒度: 垂直スライス

**1 issue = 1 エンドポイント相当**とし、`todo` → `service` → `repository` → `http` を縦に貫く。
層ごと（domain だけ、handler だけ）には切らない。

理由は 2 つ。

- **常に「動くもの」が増える。** レイヤーごとに切ると http の issue が終わるまで何も動かず、
  動作確認が最後にまとめて来る
- **pair-tdd は人間のタイピング速度で律速される。** 1 issue が 1 回座って終わる量でないと、
  PR が何日も開きっぱなしになる

着手後に想定より膨らんだら、issue は open のまま PR を 2 本に割ってよい。
`Closes #N` は最後の PR にだけ書く。
逆方向（複数 issue を 1 PR にまとめる）はやらない。受け入れ条件と差分の対応が崩れる。

### 作成タイミング

**現在の Phase と次の Phase の issue だけを作る。** 先の Phase まで作り込まない。

Go を実際に書いてみないと 1 issue に収まる量が読めず、
古い issue が残ると Claude が陳腐化した前提でテストを書くリスクがある。

### ラベル

Milestone と Projects は使わない。ラベル 3 軸で管理する。

| 軸 | 値 | 用途 |
|---|---|---|
| `phase:N` | `phase:0` 〜 `phase:5` | `docs/DESIGN.md` 12 章の Phase に対応 |
| `area:*` | `domain` `http` `postgres` `auth` `web` `infra` | 領域 |
| `mode:*` | `pair-tdd` `ai-only` | 実装を誰が書くか |

Phase の完了判定:

```sh
gh issue list --label phase:1 --state open   # 0 件なら Phase 1 完了
```

### 受け入れ条件

**必ず `curl` コマンドとその期待結果を書く。** これはマージ前に手で実行する（§6 参照）。

```
## 受け入れ条件

- [ ] `curl -s localhost:8080/api/todos | jq` → `[]` が返る
- [ ] `curl -XPOST localhost:8080/api/todos -d '{"title":"あ"}' -i` → 201 と Location ヘッダ
- [ ] `just test` が緑
```

---

## 4. ブランチとコミット

### ブランチ

issue から作る。issue と自動で紐づく。

```sh
gh issue develop 5 --name 5-get-api-todos --checkout
```

**`--name` を必ず付ける。** 省略すると issue のタイトルからブランチ名が生成される。
このリポジトリは issue タイトルが日本語なので、
`1-chore-gcp-プロジェクトと-terraform-state-バケットを用意する` のような名前ができる。
git は通すが、CI のジョブ名やコンテナタグに載ると URL エンコードが絡んで読めなくなる。
`<issue 番号>-<英小文字とハイフン>` に揃える。

`main` への直接コミットはしない。
GitHub Free + private では branch protection が使えないため、**これは自己規律で守る。**

### コミット

Conventional Commits。scope はパッケージ名（`todo` / `http` / `postgres` / `auth`）。

```
test(todo): New() のタイトル長バリデーションの失敗テストを追加
feat(todo): New() でタイトルを 1〜200 文字に制限
fix(http): PATCH で存在しない ID に 500 を返していた
refactor(postgres): Scan の重複を helper に抽出
```

---

## 5. Pull Request

**1 issue = 1 PR。マージは squash merge。**

squash を選んだ理由: **main の全コミットが常に緑になる。**
pair-tdd の `test:` コミットは定義上 RED なので、rebase merge だと main の履歴の半分が
テスト失敗状態になり、`git bisect` が使えなくなる。

`test:` → `feat:` の履歴は main からは消えるが、**PR ページには残る。**
GitHub は squash merge 後もブランチ削除後も元のコミット列を保持するため、
TDD を回した記録は `#5` を開けば読める。

### PR タイトル

**squash 後の main のコミットメッセージになる。** Conventional Commits 形式で書く。

```
feat: GET /api/todos           →  main に  feat: GET /api/todos (#5)
```

### PR 本文

```markdown
Closes #5

## 動作確認

$ curl -s localhost:8080/api/todos | jq
[]

$ curl -XPOST localhost:8080/api/todos -d '{"title":"あ"}' -i
HTTP/1.1 201 Created
Location: /api/todos/019...
```

実行した `curl` の**結果を貼る**。後から「何を確認したか」が残る。

---

## 6. マージ前の品質ゲート

**CI は自動で止めてくれない。** branch protection が使えないため、CI が赤くてもマージボタンは押せる。
CI は「止める仕組み」ではなく「自分で見る計器」として扱う。

マージ前に 3 つとも確認する。

| 確認 | 何を防ぐか |
|---|---|
| CI が緑（`go test` / `golangci-lint`） | 実装の壊れ |
| `/code-review` を走らせる | Go の書き方、**およびテスト自体の妥当性** |
| issue の受け入れ条件の `curl` を実行 | **テストが緑でも実際は動かない**ケース |

3 つ目が特に重要。**この体制では「テストが間違っている」を捕まえる仕組みが他にない。**
Claude がテストを書き人間が実装を通す形では、実装のバグは必ず捕まるが、
テストの誤りは緑のまま素通りする。実際に叩いて確認することだけが最後の砦になる。

`/code-review` の指摘は盲信も無視もしない。根拠を確認し、納得できなければ議論する。

---

## 7. CI/CD

| トリガー | 実行内容 |
|---|---|
| `pull_request`（`backend/**`） | `go test ./...` / `golangci-lint run` |
| `pull_request`（`web/**`） | `tsc --noEmit` / `vitest` |
| `push` to `main` | docker build → Artifact Registry → `golang-migrate` → `gcloud run deploy` → Firebase Hosting |

検査は PR、デプロイは main。認証は Workload Identity Federation（SA キー JSON は使わない）。

---

## 8. ドキュメントの役割

| ファイル | 内容 | 読まれ方 |
|---|---|---|
| `CLAUDE.md` | この文書と `docs/DESIGN.md` への入口 | Claude が毎セッション自動で読む |
| `CONTRIBUTING.md` | **どう作るか**（この文書） | `CLAUDE.md` から誘導される |
| `docs/DESIGN.md` | **何を作るか**（アーキテクチャ、ドメイン、Phase 計画、却下した選択肢） | 必要時に参照 |

**設計判断が変わったら、それを起こした PR の中で `docs/DESIGN.md` を直す。**
別 PR に切り出すと必ず後回しになり、ドキュメントが腐る。
