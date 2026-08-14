# Go の土台と `/healthz` 実装計画

> **For agentic workers:** この計画は `mode:pair-tdd` の issue のものである。
> **`backend/**/*.go`（`*_test.go` を除く）に `Edit` / `Write` を走らせてはならない。**
> Go の実装コードは人間が手で書く（`CONTRIBUTING.md` §1）。エージェントが担当するのは
> テスト（`*_test.go`）・設定ファイル・ドキュメント・コマンド実行のみ。
> したがって subagent-driven-development での全自動実行はできない。人間の手番で必ず止まる。
> ステップは checkbox (`- [ ]`) で追跡する。

**Goal:** `backend/` に Go モジュールを作り、`GET /healthz` が `{"status":"ok"}` を返すところまで通す。

**Architecture:** 依存を内向きに揃えた 2 パッケージ + 起動のみの `main`。`internal/config` が `PORT` を読んで検証し、`internal/http` が `ServeMux` の組み立てまで持って `http.Handler` を返す。`cmd/api/main.go` は両者を引数で繋ぐだけで、DI ライブラリは使わない。

**Tech Stack:** Go 1.26（標準ライブラリのみ）/ nix devShell + direnv / just 1.50 / golangci-lint 2.12

**Spec:** `docs/superpowers/specs/2026-08-14-go-foundation-healthz-design.md`

## Global Constraints

- **`mode:pair-tdd`。実装コード（`backend/**/*.go`、`*_test.go` を除く）は人間が手で書く。** Claude は提示するに留め、書き終えるのを待つ（`CONTRIBUTING.md` §1・§2）
- module path は `github.com/taktiks2/go-todo/backend`
- `go.mod` の go directive は **`go 1.26.0`**。`go mod init` は N-1 の `go 1.25.0` を書くので手で上げる
- devShell の Go は **`pkgs.go_1_26`** を明示（現在 1.26.5）。`pkgs.go` は使わない
- `.golangci.yml` は **`version: "2"` 形式**。v2 バイナリは v1 の設定をパースできない
- `internal/http` の package 宣言は **`http`**。エイリアスするのは `cmd/api/main.go` の import 行だけ（`httpapi`）
- `config.Load()` のシグネチャは **`func Load() (Config, error)`**、`Config.Port` は `int`
- `PORT` は未設定なら `8080`。設定されていて数値でない・空文字・1〜65535 の範囲外なら **error**
- `/healthz` の応答は 200 / `Content-Type: application/json` / `{"status":"ok"}`
- **`t.Setenv` を使うテストに `t.Parallel()` を書かない。** 併用すると Go が panic する
- `just` のレシピは `[working-directory('backend')]` 属性を使う。`cd backend &&` は書かない
- `main` に直接コミットしない。ブランチ `2-go-foundation-healthz` は作成済み・チェックアウト済み
- コミットは Conventional Commits。scope はパッケージ名（`CONTRIBUTING.md` §4）
- スコープ外: グレースフルシャットダウン（#3）/ ミドルウェア / `slog` の Cloud Logging 整形 / Dockerfile（#4）/ `api/openapi.yaml` / TODO ドメイン一切

## File Structure

| ファイル | 責務 | 書く人 |
|---|---|---|
| `flake.nix` | devShell の定義。Go・lint・デバッガのバージョンをリポジトリに固定する | Claude |
| `flake.lock` | 上の固定を厳密にする。コミットする | `nix` が生成 |
| `.envrc` | `use flake` のみ。direnv が cd 時に devShell に入れる | Claude |
| `justfile` | `dev` / `test` / `lint` / `fmt` の入口。リポジトリルートに置く | Claude |
| `.gitignore` | `.direnv/` `.gobin/` `result` を追記 | Claude |
| `backend/go.mod` | モジュール定義。多言語モノレポなので `backend/` に閉じる | `go mod init` + 手直し |
| `backend/.golangci.yml` | linter と formatter の設定。`backend/` 配下に置き適用範囲を明快にする | Claude |
| `backend/internal/config/config.go` | 環境変数を読んで検証し `Config` にする。他パッケージに依存しない | **人間** |
| `backend/internal/config/config_test.go` | `Load()` の table-driven テスト | Claude |
| `backend/internal/http/handler.go` | `Handler` 型と `Healthz` ハンドラ | **人間** |
| `backend/internal/http/router.go` | `ServeMux` の組み立て。ルーティング表を 1 箇所に集める | **人間** |
| `backend/internal/http/handler_test.go` | 応答とルーティングのテスト | Claude |
| `backend/cmd/api/main.go` | 起動と DI 配線のみ | **人間** |
| `docs/DESIGN.md` | 今回確定した判断を反映 | Claude |

`handler.go` と `router.go` を分けるのは、Phase 1 で 4 エンドポイントとミドルウェアが増えたとき、
「ルーティング表」と「ハンドラ本体」が同じファイルで混ざらないようにするため。

---

### Task 1: 開発環境と Go モジュールの土台を作る

**Files:**
- Create: `flake.nix`
- Create: `.envrc`
- Create: `justfile`
- Create: `backend/.golangci.yml`
- Modify: `.gitignore`
- Generate: `backend/go.mod`, `flake.lock`

**Interfaces:**
- Consumes: なし（最初のタスク）
- Produces: module path `github.com/taktiks2/go-todo/backend`。以降のタスクの import はすべてこの下。`just test` / `just lint` / `just dev` / `just fmt` が使えるようになる

**このタスクの目的は「計器を立てること」。** `CONTRIBUTING.md:41` は pair-tdd の手順 1 で
「RED を**実行ログで**確認する」と定めている。`just test` が動かないうちは RED を確認できないので、
テストを書く前にここを通す。

- [ ] **Step 1: `flake.nix` を作る**

```nix
{
  description = "go-todo devShell (Go 1.26)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      forAllSystems = nixpkgs.lib.genAttrs [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
    in
    {
      devShells = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            name = "go-todo";

            # メジャーを明示して固定する。`pkgs.go` にすると `nix flake update` で
            # 勝手に 1.27 に上がり、go.mod の go directive と Dockerfile (#4) の
            # golang:1.26 だけ取り残される。
            packages = with pkgs; [
              go_1_26
              gopls           # 公式 LSP
              gotools         # goimports / godoc / stringer
              delve           # デバッガ (dlv)
              golangci-lint   # 統合 linter (v2 系)
              just
              jq
            ];

            shellHook = ''
              # `go install` の出力をプロジェクトローカルに分離する。
              # GOPATH (= モジュールキャッシュ) はあえて触らず global 共有を維持。
              export GOBIN="$PWD/.gobin"
              export PATH="$GOBIN:$PATH"
              mkdir -p "$GOBIN"
              echo "→ devShell: $(go version | awk '{print $3}') / golangci-lint $(golangci-lint version --short 2>/dev/null)"
            '';
          };
        });
    };
}
```

- [ ] **Step 2: `.envrc` を作って direnv を許可する**

`.envrc`:

```
use flake
```

Run: `direnv allow`
Expected: devShell のビルドが走り、`shellHook` の `→ devShell: go1.26.5 / …` が出る（初回は数分かかる）

- [ ] **Step 3: Go と golangci-lint が入ったことを確認する**

Run: `go version && golangci-lint version && just --version`
Expected: `go version go1.26.5 …` / `golangci-lint has version 2.12.x` / `just 1.50.0`

**ここで `go` が見つからない場合は direnv が効いていない。** 先に進まず `direnv status` を見る。

- [ ] **Step 4: Go モジュールを初期化する**

Run: `cd backend && go mod init github.com/taktiks2/go-todo/backend`
Expected: `go: creating new go.mod: module github.com/taktiks2/go-todo/backend`

- [ ] **Step 5: go directive を 1.26.0 に上げる**

`backend/go.mod` は次の内容になっているはず（`go mod init` は N-1 を書く）:

```
module github.com/taktiks2/go-todo/backend

go 1.25.0
```

`go 1.25.0` を `go 1.26.0` に書き換えて、最終的にこうする:

```
module github.com/taktiks2/go-todo/backend

go 1.26.0
```

- [ ] **Step 6: `backend/.golangci.yml` を作る**

```yaml
version: "2"

linters:
  # standard = errcheck / govet / ineffassign / staticcheck / unused。
  # docs/DESIGN.md §14「既定のまま始める。必要を感じてから絞る」に従う。
  default: standard

formatters:
  # v2 でフォーマッタは linters から分離された。ここで有効にしないと
  # `golangci-lint fmt` は何もしない。
  enable:
    - gofmt
    - goimports
```

- [ ] **Step 7: `justfile` を作る**

```just
# 引数なしで実行したらレシピ一覧を出す
default:
    @just --list

# go.mod は backend/ にあるが justfile はルートに置くので、
# working-directory 属性で寄せる（just 1.38.0 以降）。
# `cd backend &&` を各行に書かないためのもの。

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

- [ ] **Step 8: `.gitignore` に追記する**

既存の `# Editor / OS` セクションの前に、次のブロックを足す:

```
# nix / direnv
.direnv/
.gobin/
result
```

- [ ] **Step 9: 計器が動くことを確認する**

Run: `just test`
Expected: `no test files` が並んで終了コード 0（テストが 0 件でも成功する）

Run: `just lint`
Expected: 何も出力せず終了コード 0

Run: `just`
Expected: `dev` / `fmt` / `lint` / `test` のレシピ一覧

**`just lint` がここで赤い場合、それは lint 設定の問題であってコードの問題ではない。**
`.golangci.yml` の `version: "2"` が抜けていないか確認する。

- [ ] **Step 10: コミットする**

```bash
git add flake.nix flake.lock .envrc justfile .gitignore backend/go.mod backend/.golangci.yml
git commit -m "$(cat <<'EOF'
chore: Go の開発環境と backend モジュールを用意する

go も golangci-lint もこのマシンの PATH に無く、just lint を緑にできない
ので、flake.nix + .envrc で devShell に固定する。go_1_26 を明示するのは、
pkgs.go にすると nix flake update で勝手にメジャーが上がり、go.mod と
Dockerfile (#4) の golang:1.26 だけ取り残されるため。

go mod init は N-1 の go 1.25.0 を書くので、go directive は手で 1.26.0 に
上げた。

golangci-lint は v2 系で、v1 の設定ファイルはパースできない。最初から
version: "2" 形式で書き、フォーマッタは分離された formatters セクションで
有効にした。

テストを書く前にここを通すのは、pair-tdd の手順 1 が RED を実行ログで
確認すると定めているため。just test が動かないうちは RED を確認できない。
EOF
)"
```

---

### Task 2: `internal/config` — `PORT` の読み込みと検証

**Files:**
- Test: `backend/internal/config/config_test.go`（Claude が書く）
- Create: `backend/internal/config/config.go`（**人間が書く**）

**Interfaces:**
- Consumes: Task 1 の module path
- Produces: `config.Config{ Port int }` と `func config.Load() (Config, error)`。Task 4 の `main` が使う

- [ ] **Step 1: 失敗するテストを書く（Claude）**

`backend/internal/config/config_test.go`:

```go
package config_test

import (
	"os"
	"testing"

	"github.com/taktiks2/go-todo/backend/internal/config"
)

func TestLoad(t *testing.T) {
	// t.Setenv を使うので t.Parallel() は書かない。
	// 併用すると Go が「test using t.Setenv or t.Chdir can not use t.Parallel」で panic する。

	tests := []struct {
		name     string
		port     string
		unsetEnv bool
		wantPort int
		wantErr  bool
	}{
		{name: "PORT が未設定なら既定値 8080", unsetEnv: true, wantPort: 8080},
		{name: "PORT が数値ならその値を使う", port: "9090", wantPort: 9090},
		{name: "PORT が下限 1 でも通る", port: "1", wantPort: 1},
		{name: "PORT が上限 65535 でも通る", port: "65535", wantPort: 65535},
		{name: "PORT が数値でなければエラー", port: "abc", wantErr: true},
		{name: "PORT が空文字ならエラー", port: "", wantErr: true},
		{name: "PORT が 0 ならエラー", port: "0", wantErr: true},
		{name: "PORT が 65536 ならエラー", port: "65536", wantErr: true},
		{name: "PORT が負ならエラー", port: "-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv は元の値を記録してテスト終了時に復元する。
			// 未設定ケースでも一度呼んでおくことで、そのあとの Unsetenv も
			// テスト終了時に巻き戻る。
			t.Setenv("PORT", tt.port)
			if tt.unsetEnv {
				os.Unsetenv("PORT")
			}

			got, err := config.Load()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() = %+v, err = nil; エラーを期待した", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() が予期しないエラーを返した: %v", err)
			}
			if got.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", got.Port, tt.wantPort)
			}
		})
	}
}
```

**テスト設計の読みどころ**

- `package config_test`（末尾 `_test`）にしている。**外部テストパッケージ**と呼ばれ、公開 API しか触れない。
  内部実装に依存したテストを書けなくなるので、リファクタで壊れにくい
- 境界値（1 / 65535 / 0 / 65536）を両側入れている。`< 1` を `<= 1` と書き間違えても捕まる
- 「未設定」と「空文字で設定済み」を別ケースにしている。**この 2 つを区別するのが `Load()` の要点**

- [ ] **Step 2: RED を実行ログで確認する**

Run: `just test`
Expected: FAIL。実測した出力は次のとおり（`config.go` がまだ無いのでビルドが通らない）:

```
github.com/taktiks2/go-todo/backend/internal/config: no non-test Go files in /Users/taktiks2/dev/go-todo/backend/internal/config
FAIL	github.com/taktiks2/go-todo/backend/internal/config [build failed]
FAIL
```

**これは「テストが落ちた」ではなく「ビルドが通らない」RED である。** 最初のテストでは必ずこうなる。
実装が入ったあと初めて、アサーションによる RED / GREEN が見えるようになる。

- [ ] **Step 3: RED の状態でコミットする**

```bash
git add backend/internal/config/config_test.go
git commit -m "$(cat <<'EOF'
test(config): Load() の PORT 読み込みと検証の失敗テストを追加

未設定なら 8080、設定されていて数値でない・空文字・1〜65535 の範囲外なら
エラーという契約を先に固定する。境界値は 1 / 65535 / 0 / 65536 の 4 点を
両側から押さえた。

「未設定」と「空文字で設定済み」を別ケースにしているのがこのテストの要点。
os.Getenv では区別できず、os.LookupEnv の第 2 戻り値が要る。

t.Setenv を使うので t.Parallel() は書かない（併用すると Go が panic する）。
EOF
)"
```

- [ ] **Step 4: 実装を書く（人間の手番）**

Claude はここで**止まる**。以下を提示するに留める。

`backend/internal/config/config.go`（新規作成）:

```go
package config

import (
	"fmt"
	"os"
	"strconv"
)

const defaultPort = 8080

// Config はアプリの起動に必要な設定を持つ。
type Config struct {
	Port int
}

// Load は環境変数から設定を読む。値が不正なら error を返し、呼び出し側は起動を諦める。
func Load() (Config, error) {
	port := defaultPort

	if v, ok := os.LookupEnv("PORT"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("PORT %q は数値ではない: %w", v, err)
		}
		if n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("PORT %d は 1〜65535 の範囲外", n)
		}
		port = n
	}

	return Config{Port: port}, nil
}
```

**読みどころ**

- **`os.Getenv` ではなく `os.LookupEnv`。** `os.Getenv("PORT")` は未設定でも空文字設定でも `""` を返し、
  この 2 つを区別できない。第 2 戻り値 `ok` が「設定されているか」を教える。**空文字は `Atoi` が弾く**ので、
  空文字ケースのために別の分岐は要らない
- `fmt.Errorf` の `%w` は原因のエラー（`strconv.ErrSyntax`）を保ったまま文脈を積む動詞。
  `%v` だと文字列になって `errors.Is` で辿れなくなる。`docs/DESIGN.md` §4 の中心的な作法
- エラー時に `Config{}` を返しているのは Go の慣習。**エラーがあるとき戻り値は使われない前提**なので、
  中途半端に埋めた値を返さない
- `defaultPort` を定数に切り出しているのは、テストの期待値 `8080` と実装の `8080` が
  別々に書かれている状態を避けるため（片方だけ直す事故を防ぐ）

- [ ] **Step 5: GREEN を確認する**

Run: `just test`
Expected: PASS。`ok github.com/taktiks2/go-todo/backend/internal/config`

Run: `just lint`
Expected: 終了コード 0

- [ ] **Step 6: コミットする**

```bash
git add backend/internal/config/config.go
git commit -m "$(cat <<'EOF'
feat(config): PORT を読んで検証する Load() を実装

os.LookupEnv の第 2 戻り値で「未設定」と「空文字で設定済み」を区別する。
前者は既定の 8080、後者は設定ミスとして error にする。Cloud Run が空文字を
注入することはないので、空文字が来ている時点で人間の設定が壊れている。

不正値を黙って既定値に落とすと、Cloud Run が注入したポートと違うポートで
待ち受けて「なぜかデプロイが失敗する」に化ける。起動前に落とす。

Load() が (Config, error) を返すのは、Phase 2 で DATABASE_URL を必須に
するときにシグネチャを変えずに済ませるため。
EOF
)"
```

---

### Task 3: `internal/http` — `/healthz` ハンドラとルーティング

**Files:**
- Test: `backend/internal/http/handler_test.go`（Claude が書く）
- Create: `backend/internal/http/handler.go`（**人間が書く**）
- Create: `backend/internal/http/router.go`（**人間が書く**）

**Interfaces:**
- Consumes: Task 1 の module path
- Produces: `type Handler struct{}` / `func NewHandler() *Handler` / `func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request)` / `func (h *Handler) Routes() http.Handler`。Task 4 の `main` が `NewHandler()` と `Routes()` を使う

- [ ] **Step 1: 失敗するテストを書く（Claude）**

`backend/internal/http/handler_test.go`:

```go
package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/taktiks2/go-todo/backend/internal/http"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	httpapi.NewHandler().Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("レスポンスボディを JSON としてデコードできない: %v (body = %q)", err, rec.Body.String())
	}

	if len(body) != 1 || body["status"] != "ok" {
		t.Errorf("body = %v, want map[status:ok]", body)
	}
}

func TestRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "GET /healthz は 200", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK},
		{name: "POST /healthz は 405", method: http.MethodPost, path: "/healthz", wantStatus: http.StatusMethodNotAllowed},
		{name: "未登録のパスは 404", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)

			httpapi.NewHandler().Routes().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
```

**テスト設計の読みどころ**

- **`h.Healthz` を直接呼ばず `h.Routes()` 経由で叩いている。** 直接呼ぶと、パスを `/health` と
  書き間違えてもテストは緑のまま通り、受け入れ条件の `curl` で初めて落ちる。
  `CONTRIBUTING.md` §6 が言う「テストが緑でも実際は動かない」の典型例をここで潰す
- 405 と 404 は `ServeMux` が自動で返すもので、実装では 1 行も書かない。それでもテストするのは、
  **`"GET /healthz"` という登録記法が効いていることの確認**になるから。記法を間違えると 405 が 404 になる
- ボディは JSON にデコードして比べている。生文字列比較にすると `json.Encoder.Encode` が付ける
  末尾改行で落ちる
- `len(body) != 1` を見ているので、余計なフィールドが増えたら気づく
- `t.Setenv` を使わないのでこちらは `t.Parallel()` を書いてよい

- [ ] **Step 2: RED を実行ログで確認する**

Run: `just test`
Expected: `internal/config` は PASS のまま、`internal/http` が FAIL する:

```
ok  	github.com/taktiks2/go-todo/backend/internal/config
github.com/taktiks2/go-todo/backend/internal/http: no non-test Go files in /Users/taktiks2/dev/go-todo/backend/internal/http
FAIL	github.com/taktiks2/go-todo/backend/internal/http [build failed]
FAIL
```

- [ ] **Step 3: RED の状態でコミットする**

```bash
git add backend/internal/http/handler_test.go
git commit -m "$(cat <<'EOF'
test(http): /healthz の応答とルーティングの失敗テストを追加

ハンドラを直接呼ばず Routes() 経由で叩く。直接呼ぶとパスを書き間違えても
テストが緑のまま通り、受け入れ条件の curl まで検出が遅れる。

405 と 404 は ServeMux が自動で返すので実装では 1 行も書かないが、
"GET /healthz" という登録記法が効いていることの確認になるのでテストする。
記法を間違えると 405 が 404 になる。

ボディは JSON にデコードして比較する。生文字列比較だと Encode が付ける
末尾改行で落ちる。
EOF
)"
```

- [ ] **Step 4: 実装を書く（人間の手番）**

Claude はここで**止まる**。以下を提示するに留める。

`backend/internal/http/handler.go`（新規作成）:

```go
package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Handler は HTTP ハンドラをまとめる。
// 今は依存が無いが、#8 以降で todo.Service を受け取るようになる。
type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

type healthzResponse struct {
	Status string `json:"status"`
}

// Healthz は死活監視に応答する。認証は不要。
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(healthzResponse{Status: "ok"}); err != nil {
		slog.ErrorContext(r.Context(), "encode healthz response", "err", err)
	}
}
```

**読みどころ**

- **`package http` の中で `net/http` をエイリアス無しで import できる。** パッケージ自身の名前は
  ファイルスコープに識別子を作らないので、ここでの `http.ResponseWriter` は `net/http` のもの。
  衝突するのは `cmd/api/main.go`（Task 4）だけ
- **`w.Header().Set` は `w.WriteHeader` より前に呼ぶ。** 後に書いても無視される。ヘッダはもう送信済みだから。
  Go はこれをエラーにしてくれない。**サイレントに効かない**タイプの罠
- `Content-Type` を明示しないと、Go が本文の中身から推測して `text/plain; charset=utf-8` を付ける。
  Task 3 の Content-Type テストはこれを捕まえるためにある
- `Encode` のエラーを `return` できないのは、**もうステータスコードを送ってしまっている**から。
  HTTP の途中で気が変わることはできない。だからログに落とすしかない
- `healthzResponse` を非公開にしているのは、外から組み立てる必要が無いから。
  Go では**公開する理由が無いものは公開しない**

`backend/internal/http/router.go`（新規作成）:

```go
package http

import "net/http"

// Routes はこのパッケージが提供する全ルートを組み立てて返す。
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.Healthz)

	return mux
}
```

**読みどころ**

- `"GET /healthz"` の記法は Go 1.22 以降。**メソッドとパスの間はスペース 1 つ**。
  これがあるのでルータライブラリ（chi / gin）は要らない（`docs/DESIGN.md` §6）
- 返り値が `*http.ServeMux` ではなく **`http.Handler`（interface）**なのは、#3 以降で
  ミドルウェアに包んだとき（`RequestID(Logger(mux))`）に呼び出し側を変えずに済ませるため。
  **返り値は具体型で受けて interface で返す**のが Go の一般的な向き

- [ ] **Step 5: GREEN を確認する**

Run: `just test`
Expected: PASS。`ok .../internal/config` と `ok .../internal/http` の 2 行

Run: `just lint`
Expected: 終了コード 0

- [ ] **Step 6: コミットする**

```bash
git add backend/internal/http/handler.go backend/internal/http/router.go
git commit -m "$(cat <<'EOF'
feat(http): /healthz ハンドラと ServeMux の組み立てを実装

package 宣言は http のまま置いた。パッケージ自身の名前はファイルスコープに
識別子を作らないので、この中では net/http をエイリアス無しで使える。
衝突するのは cmd/api/main.go の import 行だけ。

Routes() が ServeMux まで組んで http.Handler を返す。main に組むとパスの
書き間違いがテストを素通りするため。返り値を interface にしてあるのは、
#3 以降でミドルウェアに包んでも呼び出し側を変えずに済ませるため。

Content-Type は WriteHeader より前に明示する。後だと無視され、Go は
本文から text/plain を推測してしまう。
EOF
)"
```

---

### Task 4: `cmd/api/main.go` — DI 配線と起動

**Files:**
- Create: `backend/cmd/api/main.go`（**人間が書く**）

**Interfaces:**
- Consumes: `config.Load() (config.Config, error)` / `httpapi.NewHandler() *Handler` / `(*Handler).Routes() http.Handler`
- Produces: 実行可能なバイナリ。`just dev` の対象

`main` はテストしない。ここは受け入れ条件の `curl` が担当する（`CONTRIBUTING.md` §6）。
したがってこのタスクに RED / GREEN のサイクルは無い。

- [ ] **Step 1: 実装を書く（人間の手番）**

Claude はここで**止まる**。以下を提示するに留める。

`backend/cmd/api/main.go`（新規作成）:

```go
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/taktiks2/go-todo/backend/internal/config"
	httpapi "github.com/taktiks2/go-todo/backend/internal/http"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	h := httpapi.NewHandler()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: h.Routes(),
	}

	slog.Info("starting server", "addr", srv.Addr)

	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
```

**読みどころ**

- **これが「DI」の全体。** `config.Load()` → `NewHandler()` → `Server` と上から順に組んで
  引数で渡すだけ。wire も fx も要らない（`docs/DESIGN.md` §3）
- `httpapi` というエイリアスが要るのはこの 1 行だけ。`net/http` と `internal/http` の
  両方をこのファイルが使うため。Go の標準ライブラリ内にも `math/rand` と `crypto/rand` の
  同じ問題があり、エイリアスで解くのが定石
- `Addr` が `":8080"` のようにホスト部を空にした形なのは、**全インターフェースで待ち受ける**意味。
  `"localhost:8080"` にするとコンテナの外から繋がらなくなる。#4 で効いてくる
- **`os.Exit(1)` は `defer` を実行しない。** 今は `defer` が無いので問題にならないが、
  #3 でグレースフルシャットダウンを足すときに真っ先に効いてくる制約
- `ListenAndServe` は**正常終了時も必ず non-nil の error を返す**（`http.ErrServerClosed`）。
  今は起動しっぱなしなので素直に落として構わないが、#3 で `errors.Is(err, http.ErrServerClosed)` の
  分岐を足すことになる

- [ ] **Step 2: ビルドと lint を確認する**

Run: `just test && just lint`
Expected: どちらも終了コード 0

- [ ] **Step 3: 受け入れ条件を手で確認する**

`CONTRIBUTING.md` §6 の 3 つ目のゲート。**出力をコピーしておく**（PR 本文に貼る）。

```sh
just dev &
curl -s localhost:8080/healthz | jq
# → {"status": "ok"}

curl -si localhost:8080/healthz | head -3
# → HTTP/1.1 200 OK / Content-Type: application/json

curl -s -o /dev/null -w '%{http_code}\n' -XPOST localhost:8080/healthz
# → 405

kill %1
```

```sh
cd backend
PORT=9090 go run ./cmd/api &
curl -s localhost:9090/healthz
# → {"status":"ok"}
kill %1
```

```sh
cd backend
PORT=abc go run ./cmd/api
# → level=ERROR msg="load config" err="PORT \"abc\" は数値ではない: ..."
# → exit status 1
```

- [ ] **Step 4: コミットする**

```bash
git add backend/cmd/api/main.go
git commit -m "$(cat <<'EOF'
feat(api): config と http ハンドラを main で配線して起動する

DI ライブラリを使わず、config.Load() → NewHandler() → http.Server と
上から順に引数で渡す。これで足りる（docs/DESIGN.md §3）。

net/http と internal/http の両方を使うのはこのファイルだけなので、
エイリアス httpapi もここにしか出てこない。

Addr のホスト部を空にして全インターフェースで待ち受ける。localhost に
縛るとコンテナの外から繋がらなくなり #4 で詰まる。

グレースフルシャットダウンは #3。ここでは ListenAndServe を直接呼ぶ。
EOF
)"
```

---

### Task 5: `docs/DESIGN.md` の反映と PR

**Files:**
- Modify: `docs/DESIGN.md`

**Interfaces:**
- Consumes: Task 1〜4 の確定事項
- Produces: なし（最終タスク）

`CONTRIBUTING.md` §8 —「設計判断が変わったら、それを起こした PR の中で `docs/DESIGN.md` を直す。
別 PR に切り出すと必ず後回しになり、ドキュメントが腐る」。

- [ ] **Step 1: `docs/DESIGN.md` §3 の `main()` 例を直す（Claude）**

`docs/DESIGN.md:116-125` のコード例で `cfg := config.Load()` となっている行を、
`Load()` が `(Config, error)` を返す形に合わせる:

```go
func main() {
    cfg, err := config.Load()      // 不正な設定は起動前に落とす
    if err != nil { /* log して os.Exit(1) */ }
    pool := pgxpool.New(ctx, cfg.DatabaseURL)
    repo := postgres.NewTodoRepository(pool)
    svc  := todo.NewService(repo)
    h    := httpapi.NewHandler(svc)
    // ...
}
```

- [ ] **Step 2: `docs/DESIGN.md` §14 の「Go のバージョン」を確定させる（Claude）**

`docs/DESIGN.md:667` を置き換える:

```markdown
| Go のバージョン | **1.26**（#2 で確定）。`flake.nix` の `go_1_26` / `go.mod` の `go 1.26.0` / Dockerfile の `golang:1.26` を揃える |
```

- [ ] **Step 3: `docs/DESIGN.md` §10「ローカル環境」に devShell を追記する（Claude）**

`docs/DESIGN.md:537` の次に 1 行足す:

```markdown
| Go / golangci-lint / just | nix devShell + direnv（`flake.nix` / `.envrc`。#2 で導入） |
```

そして `docs/DESIGN.md:542` の後ろに 1 段落足す:

```markdown
**Go はホストで直接動かすが、バージョンはリポジトリに固定する。** 上の「コンテナに入れない」
判断はビルドとファイル同期の速度の話であって、バージョンを各自の環境任せにしてよいという意味では
ない。`flake.nix` の `go_1_26` が `go.mod` の `go 1.26.0` と Dockerfile の `golang:1.26` に対応し、
`nix flake update` を打ってもメジャーは動かない。
```

- [ ] **Step 4: コミットする**

```bash
git add docs/DESIGN.md
git commit -m "$(cat <<'EOF'
docs: #2 で確定した判断を DESIGN.md に反映

config.Load() が (Config, error) を返すことに合わせて §3 の main 例を直し、
§14 の Go バージョンを 1.26 に確定させ、§10 のローカル環境に nix devShell +
direnv を追記した。

CONTRIBUTING.md §8 に従い、判断を起こした PR の中で直す。
EOF
)"
```

- [ ] **Step 5: `/code-review` を走らせる**

`CONTRIBUTING.md` §6 の 2 つ目のゲート。**テスト自体の妥当性**も見てもらう。
この体制ではテストの誤りが緑のまま素通りするため、ここが数少ない検出機会になる。

指摘は盲信も無視もしない。根拠を確認し、納得できなければ議論する。

- [ ] **Step 6: push して PR を作る**

```bash
git push -u origin 2-go-foundation-healthz
```

PR タイトル（squash 後に main のコミットメッセージになる。Conventional Commits で書く）:

```
feat: Go の土台と /healthz を作る
```

PR 本文（`Closes #2` + Task 4 Step 3 で実際に取った出力を貼る）:

```markdown
Closes #2

## 動作確認

$ curl -s localhost:8080/healthz | jq
（ここに実際の出力）

$ curl -si localhost:8080/healthz | head -3
（ここに実際の出力）

$ PORT=9090 go run ./cmd/api & curl -s localhost:9090/healthz
（ここに実際の出力）

$ PORT=abc go run ./cmd/api
（ここに実際の出力）
```

- [ ] **Step 7: マージ前の 3 ゲートを確認して squash merge**

`CONTRIBUTING.md` §6。**CI は自動で止めてくれない**（branch protection が使えない）ので、
3 つとも自分で見る。

| 確認 | 状態 |
|---|---|
| CI が緑（`go test` / `golangci-lint`） | ※ CI ワークフローは #7 で作る。この PR の時点では**ローカルの `just test` / `just lint`** が代わり |
| `/code-review` を走らせた | Step 5 |
| issue の受け入れ条件の `curl` を実行した | Task 4 Step 3 |

マージは **squash merge**（`CONTRIBUTING.md` §5）。`test:` コミットは定義上 RED なので、
rebase merge にすると main の履歴の半分がテスト失敗状態になり `git bisect` が使えなくなる。

---

## この計画の実行に関する注意

**Task 2 Step 4 / Task 3 Step 4 / Task 4 Step 1 は人間の手番。**
Claude はコードを提示して待つ。`backend/**/*.go`（`*_test.go` を除く）に
`Edit` / `Write` を走らせない（`CONTRIBUTING.md` §1）。

この分担の理由は `CONTRIBUTING.md:24-27` にある —
`err != nil` の積み方、`interface` の切り方、手書き fake を書く「痛み」は
読んで理解するものではなく手で書いて体に入れるもので、Claude が書いてしまうと
残るのは「Claude に go-todo を作らせた経験」であって Go ではない。
