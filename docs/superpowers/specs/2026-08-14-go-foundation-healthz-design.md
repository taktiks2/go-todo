# Go の土台と `/healthz`

- issue: #2（`phase:0` / `area:http` / `mode:pair-tdd`）
- 日付: 2026-08-14
- 関連: `docs/DESIGN.md` §2 設計原則、§3 アーキテクチャ、§6 ルーティング、§9 設定とシークレット、§10 テスト戦略、§12 Phase 0
- 前提: `CONTRIBUTING.md` §1 役割分担、§2 pair-tdd

## 目的

`backend/` に Go モジュールを作り、`GET /healthz` が JSON を返すところまで通す。
Phase 1 以降の 4 エンドポイント（#8〜#11）がすべてこの上に乗る。

**このリポジトリで Go を書く最初の issue である。** したがって速度ではなく、
パッケージ境界と `main()` での DI 配線を手で覚えることを優先する。
`mode:pair-tdd` なのでテスト（`*_test.go`）は Claude が書き、**Go の実装コードは人間が書く**。

`docs/DESIGN.md` §12 は Phase 0 に「Go の土台」を含めると定めている。ここで土台を作り切ることで、
Phase 1 の 4 issue が「土台 + エンドポイント」に肥大化せず、すべて同じ大きさに揃う。

## 決定事項

| 項目 | 決定 | 理由 |
|---|---|---|
| **ツールチェーンの提供** | `flake.nix` + `.envrc`（nix devShell + direnv） | `go` も `golangci-lint` もこの環境の PATH に無く、受け入れ条件の `just lint` が緑にならない。dotfiles の `templates/go` を土台にする |
| **Go のバージョン** | `pkgs.go_1_26`（現在 1.26.5）、`go.mod` は `go 1.26.0` | `docs/DESIGN.md` §14「着手時の最新安定版」。**Go 1.26 の `go mod init` は N-1 の `go 1.25.0` を書く**ため手で上げる。`flake.nix` / `go.mod` / `Dockerfile`（#4 の `golang:1.26`）の 3 点を揃え、`nix flake update` で勝手にメジャーが動かないようにする |
| `golangci-lint` | 2.12.2、`version: "2"` 形式、`linters.default: standard` + `formatters` に `gofmt` / `goimports` | **v2 バイナリは v1 の設定をパースできない**。新規なので最初から v2 で書く。linter は `docs/DESIGN.md` §14「既定のまま始める」。**v2 はフォーマッタを `formatters` セクションに分離した**ため、`just fmt` を動かすにはここでの有効化が要る |
| module path | `github.com/taktiks2/go-todo/backend` | リモートは `git@github.com:taktiks2/go-todo.git`。`go.mod` は `backend/` に置く（`docs/DESIGN.md` §3） |
| **`internal/http` の package 名** | `package http` のまま。衝突するのは `main.go` の import 行だけで、そこを `httpapi` とエイリアスする | `docs/DESIGN.md` §3 のツリーと §3 の `main()` 例（`httpapi.NewHandler(svc)`）を両方そのまま満たす。**パッケージ自身の名前はファイルスコープに識別子を作らないため、`package http` の中で `net/http` をエイリアス無しで import できる**（go1.25 でコンパイル確認済み）。「名前は利用側が決める」という Go の作法にも沿う |
| **`config.Load()` の型** | `func Load() (Config, error)`、`Config.Port` は `int` | `docs/DESIGN.md` §9「必須変数が無ければ起動時に落とす」。Phase 2 で `DATABASE_URL` を必須にするときシグネチャを変えずに済む。`docs/DESIGN.md` §3 の `cfg := config.Load()` という例からは外れるが、そちらは error を省いた簡略表記として扱う |
| **`PORT` が不正なときの挙動** | `config` が error を返し `main` が `os.Exit(1)` | 黙って既定値にフォールバックすると、Cloud Run が注入したポートと違うポートで待ち受け、「なぜかデプロイが失敗する」に化ける。`docs/DESIGN.md` §9 が `PORT` を「最頻出の詰まりどころ」と名指ししている |
| **ルーティングの置き場所** | `internal/http` が `ServeMux` まで組み、`http.Handler` を返す | `main()` は DI 配線と起動だけになり `docs/DESIGN.md` §3「起動と DI 配線のみ」に合う。**テストがパスとメソッドごと叩けるので、受け入れ条件の `curl` が守られているかをテストで押さえられる**。`main` に組むとパスの書き間違いがテストを素通りする |
| ディレクトリの骨組み | コードが入る 4 つだけ作る（`cmd/api` / `internal/http` / `internal/config` / `go.mod`） | Go では空パッケージは import できず godoc にも出ない。`.gitkeep` で先に切るとドキュメントの重複になる。`todo/` `postgres/` `auth/` `db/` はそれを使う issue が作る |
| `justfile` の置き場所 | リポジトリルート。`[working-directory('backend')]` 属性を使う | `docs/DESIGN.md` §3 のツリーどおり。`go.mod` は `backend/` にあるが、属性（just 1.38.0+、ローカルは 1.50.0）で `cd backend &&` を書かずに済む |
| レスポンスの中身 | `{"status":"ok"}` のみ | version フィールドは `ldflags` が要り、Dockerfile（#4）の領域 |
| `slog` の整形 | 既定のまま | `docs/DESIGN.md` §9 の Cloud Logging 整形（`severity`/`message`/`timestamp`）はミドルウェア込みの話で、#2 のスコープ外 |
| ホットリロード（air） | 入れない | `go run` で足りる。issue のスコープに無い |

## 成果物

```
go-todo/
├── flake.nix          # devShell: go_1_26 / gopls / gotools / delve / golangci-lint / just / jq
├── flake.lock         # コミットする
├── .envrc             # use flake
├── justfile           # default / dev / test / lint / fmt
├── .gitignore         # .direnv/ .gobin/ result を追記
└── backend/
    ├── go.mod         # module github.com/taktiks2/go-todo/backend, go 1.26.0
    ├── .golangci.yml  # version: "2" / linters.default: standard / formatters: gofmt, goimports
    ├── cmd/api/main.go
    └── internal/
        ├── config/
        │   ├── config.go
        │   └── config_test.go
        └── http/
            ├── handler.go
            ├── router.go
            └── handler_test.go
```

## 各ユニットの契約

### `internal/config`

依存は `os` / `strconv` / `fmt` のみ。他パッケージに依存しない。

```go
type Config struct {
    Port int
}

func Load() (Config, error)
```

| `PORT` | 結果 |
|---|---|
| 未設定 | `Port: 8080`、`err == nil` |
| `9090` | `Port: 9090`、`err == nil` |
| `abc` | error |
| `0` | error（1〜65535 の範囲外） |
| `70000` | error（同上） |
| 空文字で設定済み | error |

「未設定」と「空文字で設定済み」は `os.LookupEnv` の第 2 戻り値で区別する。
前者は既定値、後者は**設定ミスとして落とす**。Cloud Run が空文字を注入することはないので、
空文字が来ている時点で人間の設定が壊れている。

エラーは `fmt.Errorf` でメッセージを組む。センチネルエラーは定義しない
（呼び出し側は `main` だけで、種類による分岐をしないため）。

### `internal/http`

`package http`。パッケージ内では `net/http` をエイリアス無しで使う。

```go
type Handler struct{}   // 今は依存ゼロ。#8 で todo.Service を受け取る

func NewHandler() *Handler
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request)
func (h *Handler) Routes() http.Handler
```

`Routes()` が `http.ServeMux` を組み立てて返す。今は 1 行だけ。

```go
mux.HandleFunc("GET /healthz", h.Healthz)
```

`GET /healthz` の応答:

| 項目 | 値 |
|---|---|
| ステータス | 200 |
| `Content-Type` | `application/json` |
| ボディ | `{"status":"ok"}` |

ボディは構造体を定義して `json.NewEncoder(w).Encode()` で書く。文字列の手書きにはしない
（`docs/DESIGN.md` §6 の契約駆動と、Phase 1 で DTO が増えることを見越す）。

`POST /healthz` への 405 と `GET /nope` への 404 は Go 1.22+ の `ServeMux` が自動で返す。
自分では書かないが、**テストでは押さえる**（ルーティングの記法ミスを検出するため）。

### `cmd/api/main.go`

```go
import (
    "net/http"
    httpapi "github.com/taktiks2/go-todo/backend/internal/http"
)
```

1. `cfg, err := config.Load()` → err なら `slog.Error` して `os.Exit(1)`
2. `h := httpapi.NewHandler()`
3. `srv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Port), Handler: h.Routes()}`
4. `srv.ListenAndServe()`

グレースフルシャットダウンは #3 で足す。ここでは `ListenAndServe` を直接呼ぶ。

## テスト設計

`CONTRIBUTING.md` §1 のとおり `*_test.go` は Claude が書き、実装は人間が書く。

### `internal/config/config_test.go`

上の表をそのまま table-driven にする。

**`t.Setenv` を使うので `t.Parallel()` は書かない。** 併用すると Go が panic する
（`t.Setenv` はプロセス全体の環境変数を触るため、並列テストと両立しない）。

「未設定」のケースも `t.Setenv` では作れない（`t.Setenv` は必ず値を設定する）。
`os.Unsetenv` をテスト内で呼ぶか、サブテストの構成で分ける。

### `internal/http/handler_test.go`

`h.Routes().ServeHTTP(rec, req)` 経由で叩く。ハンドラ関数を直接呼ばない
（パスとメソッドの登録ミスを検出するため）。

| リクエスト | 期待 |
|---|---|
| `GET /healthz` | 200 / `Content-Type: application/json` / ボディが `{"status":"ok"}` |
| `POST /healthz` | 405 |
| `GET /nope` | 404 |

ボディは JSON にデコードして比較する。生文字列の比較にすると、末尾改行や空白の差で落ちる
（`json.Encoder.Encode` は改行を付ける）。

`httptest.NewServer` は使わない。ポートを開かずに `httptest.NewRecorder()` で足りる。

### テストしないもの

`cmd/api/main.go` はテストしない。ここは受け入れ条件の `curl` が担当する。
`CONTRIBUTING.md` §6 が言うとおり、この体制では「テストが緑でも実際は動かない」を捕まえるのは
手で叩く工程だけなので、`main` の配線ミスはそこで落とす。

## `justfile`

```just
default:
    @just --list

[working-directory('backend')]
dev:
    go run ./cmd/api

[working-directory('backend')]
test:
    go test ./...

[working-directory('backend')]
lint:
    golangci-lint run

[working-directory('backend')]
fmt:
    golangci-lint fmt
```

`fmt` は golangci-lint v2 で分離された `formatters` セクションを使う新コマンドで、v1 の `run --fix` とは別物。
`.golangci.yml` の `formatters` で `gofmt` と `goimports` を有効にしていないと**何もしない**ので、
設定側とセットで用意する。

## 進め方

`CONTRIBUTING.md` §2 の pair-tdd ループを 2 回まわす。その前に計器を立てる。

1. **環境整備**（Claude が書く）: `flake.nix` / `.envrc` / `justfile` / `go mod init` / `.golangci.yml` / `.gitignore`
   → この時点で `just test` が「テスト 0 件で成功」、`just lint` が緑になることを確認して `chore:` でコミット
2. **`config`**: Claude が `config_test.go` → RED を実行ログで確認 → `test(config):` → **人間が `config.go`** → GREEN → `feat(config):`
3. **`http`**: Claude が `handler_test.go` → RED → `test(http):` → **人間が `handler.go` / `router.go`** → GREEN → `feat(http):`
4. **配線**: **人間が `cmd/api/main.go`** → `curl` で受け入れ条件を確認 → `feat(api):`

手順 1 を先に置くのは、`CONTRIBUTING.md:41` が「RED を**実行ログで**確認する」と定めているため。
`just test` が動かないうちは RED を確認できない。

## 受け入れ確認

```sh
just test          # 緑
just lint          # 緑

just dev &
curl -s localhost:8080/healthz | jq
# → {"status": "ok"}

PORT=9090 go run ./cmd/api &
curl -s localhost:9090/healthz
# → 同じ結果

PORT=abc go run ./cmd/api
# → 起動せず、exit 1
```

実行結果は PR 本文に貼る（`CONTRIBUTING.md` §5）。

## スコープ外

issue #2 の宣言どおり、以下は含まない。

- グレースフルシャットダウン（#3）
- ミドルウェア（RequestID / Logger / Recoverer）
- `slog` の Cloud Logging 整形
- Dockerfile（#4）
- `api/openapi.yaml`
- TODO ドメインに関するもの一切

## `docs/DESIGN.md` への反映

`CONTRIBUTING.md` §8 に従い、設計判断が変わった箇所は同じ PR で直す。

- §3 の `main()` 例の `cfg := config.Load()` → `config.Load()` が `(Config, error)` を返すことを追記
- §14「着手時に決めること」の「Go のバージョン」を `1.26` に確定
- ローカル開発環境として nix devShell + direnv を使うことを §10「ローカル環境」に追記
