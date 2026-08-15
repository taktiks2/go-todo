# グレースフルシャットダウン実装計画

> **For agentic workers:** この計画は `mode:pair-tdd` の issue のものである。
> **`backend/**/*.go`（`*_test.go` を除く）に `Edit` / `Write` を走らせてはならない。**
> Go の実装コードは人間が手で書く（`CONTRIBUTING.md` §1）。エージェントが担当するのは
> テスト（`*_test.go`）・ドキュメント・コマンド実行のみ。
> したがって subagent-driven-development での全自動実行はできない。**Task 2 で必ず止まる。**
> ステップは checkbox (`- [ ]`) で追跡する。

**Goal:** SIGTERM を受けたら、処理中のリクエストを捌き切ってから終了する。テストでそれを証明する。

**Architecture:** `cmd/api/main.go` に `run(ctx, ln, srv, shutdownTimeout) error` を切り出し、`main()` は「設定読み込み・DI 配線・`net.Listen`・終了コード」だけにする。`run` が `signal.NotifyContext` を自分で張るので、テストは親 ctx を `cancel()` するだけでシグナルと同じ経路を通せる。`net.Listener` を引数で受けることで、テストが `127.0.0.1:0` の実ポートを掴める。

**Tech Stack:** Go 1.26.5（標準ライブラリのみ。新規依存ゼロ）/ golangci-lint 2.12.2 / just

**Spec:** `docs/superpowers/specs/2026-08-15-graceful-shutdown-design.md`

## Global Constraints

- **`mode:pair-tdd`。実装コード（`backend/**/*.go`、`*_test.go` を除く）は人間が手で書く。** Claude は提示するに留め、書き終えるのを待つ（`CONTRIBUTING.md` §1・§2）
- ブランチは **`3-graceful-shutdown`**（作成済み・チェックアウト済み）。`main` に直接コミットしない
- コミットは Conventional Commits。scope は **`api`**（`cmd/api` 配下のため）
- `run` のシグネチャは **`func run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error`**
- 定数名は **`defaultShutdownTimeout = 8 * time.Second`**。`run` の引数名 `shutdownTimeout` とシャドウさせない
- シャットダウン用 ctx の親は **`context.Background()`**。`run` が受け取った `ctx` から派生させない
- `http.Server` に **`Addr` を設定しない**（`Serve(ln)` は見ないため、持たせると嘘になる）
- `internal/config` / `internal/http` は **変更しない**
- 新規依存を入れない。`backend/go.mod` は変更しない
- **`testing/synctest` は使わない**（実ネットワーク I/O と相性が悪い）
- テストのハンドラ内で **`t.Error` / `t.Fatal` / `t.Log` を呼ばない**（`run` が戻った後もハンドラ goroutine が生き残るケースがあり、テスト終了後の `t.*` は panic になる）
- テストは全ケース `t.Parallel()` を付ける（`t.Setenv` を使わないので併用できる）
- Go コマンドは **`nix develop --command`** 経由か、direnv が効いているシェルで `just` を使う
- スコープ外: `pool.Close()`（Phase 2）/ Cloud Run へのデプロイ（#7）/ `slog` の Cloud Logging 整形（#13）/ ミドルウェア

## File Structure

| ファイル | 責務 | 書く人 |
|---|---|---|
| `backend/cmd/api/main_test.go` | **新規。** `run` の 3 ケース。実 TCP + 遅いハンドラで in-flight 完了を検証する | Claude |
| `backend/cmd/api/main.go` | **修正。** `run` を切り出し、シグナル捕捉とドレインを実装。`main` は配線と終了コードのみ | **人間** |
| `docs/DESIGN.md` §9 | **修正。** コード例を確定形に差し替え、判断の理由と Phase 2 の `pool.Close()` 位置を明示 | Claude |

`internal/server` パッケージは作らない。`main` が薄いうちは `package main` に閉じるほうが構成が小さい。
Phase 2 で `pool.Close()` が増えて `main` が重くなったら、そのとき切り出しを判断する（YAGNI）。

## この計画のコードは検証済み

計画に載っているテストと実装は、リポジトリ外の捨てモジュールで go1.26.5 / golangci-lint 2.12.2 に
かけて確認してある。**人間が Task 2 を書くときに「計画のコードが間違っていた」で時間を溶かさないため。**

- `go test -race -count=2` — 3 本とも PASS
- `golangci-lint run`（`backend/.golangci.yml` と同じ設定）— 0 issues
- **変異テスト**: 実装を 4 通りに壊すと、意図したテストが落ちることを確認した

| 実装の壊し方 | 落ちるテスト |
|---|---|
| `context.WithTimeout` の親を `Background` ではなく `ctx` にする | `TestRunDrainsInFlightRequests`（リクエストが切断される）と `TestRunReportsDrainTimeout`（`Canceled` が返る） |
| `_ = srv.Shutdown(...)` で戻り値を捨てる | `TestRunReportsDrainTimeout` |
| `Serve` のエラーを `ErrServerClosed` 以外も `nil` に潰す | `TestRunReportsServeError` |
| `<-ctx.Done()` の後にも `errCh` を待つ | `TestRunDrainsInFlightRequests` と `TestRunReportsDrainTimeout`（どちらも「戻らなかった」で 3 秒後に失敗） |

---

### Task 1: `run` の失敗テストを書き、RED を確認する

**Files:**
- Create: `backend/cmd/api/main_test.go`

**Interfaces:**
- Consumes: なし（`run` はまだ存在しない。これが RED の理由）
- Produces: `run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error` の契約。Task 2 の実装はこのシグネチャに従う

**契約（Task 2 が満たすべきもの）:**

| 状況 | `run` の戻り値 |
|---|---|
| `ctx` が Done → `shutdownTimeout` 内に捌き切った | `nil` |
| `ctx` が Done → `shutdownTimeout` を超えた | `context.DeadlineExceeded` を wrap したエラー |
| `ctx` が Done になる前に `Serve` が失敗した | そのエラーを wrap して返す |
| `ctx` が Done になる前に `Serve` が `http.ErrServerClosed` を返した | `nil` |

- [ ] **Step 1: `backend/cmd/api/main_test.go` を作成する**

テスト関数名は既存の `TestHealthz` / `TestRoutes` に合わせて ASCII、説明は日本語のコメントと
エラーメッセージで書く（`backend/internal/http/handler_test.go` と同じ流儀）。
`run` は未公開なので `package http_test` のような外部テストパッケージにはできず、`package main` に置く。

```go
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// テスト全体で使う待ち時間の上限。これを超えたら「戻ってこない」と判定する。
const waitLimit = 3 * time.Second

// newListener は 127.0.0.1 の空きポートで待ち受ける listener を返す。
//
// ポート 0 を指定して OS に選ばせるので、固定ポートの取り合いで落ちない。
// listener をテスト側で作れるのは run が net.Listener を引数で受け取るからで、
// ListenAndServe を直に呼ぶ設計だと実ポートを知る手段がない。
func newListener(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	return ln
}

// newClient は keep-alive の接続をテスト間に持ち越さないクライアントを返す。
// http.DefaultClient を使うと接続が他のテストに残る。
func newClient(t *testing.T) *http.Client {
	t.Helper()

	c := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(c.CloseIdleConnections)

	return c
}

// TestRunDrainsInFlightRequests は、シャットダウン要求の時点で処理中だった
// リクエストが切断されず最後まで応答されることを検証する。受け入れ条件の本体。
func TestRunDrainsInFlightRequests(t *testing.T) {
	t.Parallel()

	ln := newListener(t)

	// ハンドラは「突入を知らせてから 200ms かけて応答する」。
	// この 200ms の途中でシャットダウンを要求するのがこのテストの肝。
	// ハンドラの中で t.* を呼んではいけない（テスト終了後に走る可能性がある）。
	//
	// sync.OnceFunc で包むのは、クライアントがリクエストを再送した場合に
	// 閉じた channel を二度 close して panic するのを防ぐため。
	entered := make(chan struct{})
	markEntered := sync.OnceFunc(func() { close(entered) })

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			markEntered()
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "drained")
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, srv, 8*time.Second) }()

	type result struct {
		status int
		body   string
		err    error
	}

	client := newClient(t)
	resCh := make(chan result, 1)

	go func() {
		resp, err := client.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			resCh <- result{err: err}

			return
		}
		defer func() { _ = resp.Body.Close() }()

		b, err := io.ReadAll(resp.Body)
		resCh <- result{status: resp.StatusCode, body: string(b), err: err}
	}()

	// ハンドラに入ったことを確認してから終了を要求する。
	// time.Sleep で待つと遅いマシンで falsely green になる。
	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("ハンドラに到達しなかった")
	}

	cancel() // SIGTERM 相当

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("処理中のリクエストが切断された: %v", res.err)
		}
		if res.status != http.StatusOK {
			t.Errorf("status = %d, want %d", res.status, http.StatusOK)
		}
		if res.body != "drained" {
			t.Errorf("body = %q, want %q", res.body, "drained")
		}
	case <-time.After(waitLimit):
		t.Fatal("レスポンスが返らなかった")
	}

	// run が nil を返すこと = main が slog.Error を呼ばないこと。
	// 受け入れ条件「SIGTERM で終了するとき、エラーログを出さない」はここで担保する。
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("run() = %v, want nil（正常終了ではエラーログを出さない）", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("run() が戻らなかった")
	}
}

// TestRunReportsDrainTimeout は、猶予内に捌き切れなかったことが run の戻り値に
// 出ることを検証する。srv.Shutdown の戻り値を捨てると落ちる。
func TestRunReportsDrainTimeout(t *testing.T) {
	t.Parallel()

	ln := newListener(t)

	entered := make(chan struct{})
	markEntered := sync.OnceFunc(func() { close(entered) })

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			markEntered()
			time.Sleep(500 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// ドレインを 20ms しか許さない。ハンドラは 500ms かかるので必ず超過する。
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, srv, 20*time.Millisecond) }()

	client := newClient(t)

	go func() {
		resp, err := client.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("ハンドラに到達しなかった")
	}

	cancel()

	// Shutdown の戻り値を捨てると、捌き切れなかった事実が消える。
	select {
	case err := <-runErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("run() = %v, want context.DeadlineExceeded を含むエラー", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("run() が戻らなかった")
	}
}

// TestRunReportsServeError は、http.ErrServerClosed 以外の Serve のエラーを
// nil に潰していないことを検証する。
func TestRunReportsServeError(t *testing.T) {
	t.Parallel()

	// 閉じた listener を渡すと Serve は Accept に失敗して即座に戻る。
	ln := newListener(t)
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	srv := &http.Server{
		Handler:           http.NewServeMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	runErr := make(chan error, 1)
	go func() { runErr <- run(t.Context(), ln, srv, 8*time.Second) }()

	// ErrServerClosed 以外を nil に潰していないことを固定する。
	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("run() = nil, want error")
		}
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("run() = %v, want net.ErrClosed を含むエラー", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("run() が戻らなかった")
	}
}
```

- [ ] **Step 2: RED を実行ログで確認する**

Run: `just test`

Expected: **FAIL**。`run` が未定義なのでコンパイルが通らない。

```
# github.com/taktiks2/go-todo/backend/cmd/api [github.com/taktiks2/go-todo/backend/cmd/api.test]
./main_test.go:NN:NN: undefined: run
FAIL	github.com/taktiks2/go-todo/backend/cmd/api [build failed]
```

`internal/config` と `internal/http` は `ok` のまま。**ここで実際の出力を目で見る**（`CONTRIBUTING.md:41`）。

- [ ] **Step 3: コミットする**

```bash
git add backend/cmd/api/main_test.go
git commit -m "test(api): グレースフルシャットダウンの失敗テストを追加

処理中のリクエストを捌き切ってから終了すること、ドレイン時間を超えたら
エラーを返すこと、Serve の失敗を握り潰さないことの 3 本。
run() が未定義なのでコンパイルエラーで RED。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: `main.go` に `run` を実装して GREEN にする

**⚠️ このタスクの実装コードは人間が手で書く。** Claude は Step 1 で提示し、Step 2 で待つ。

**Files:**
- Modify: `backend/cmd/api/main.go`（全面的に書き換わる。現状 `backend/cmd/api/main.go:34` の `ListenAndServe` 直呼びが消える）

**Interfaces:**
- Consumes: `config.Load() (config.Config, error)` / `httpapi.NewHandler() *httpapi.Handler` / `(*Handler).Routes() http.Handler`（いずれも #2 で実装済み・変更しない）
- Produces: `run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error`

- [ ] **Step 1: Claude が実装を提示する**

`backend/cmd/api/main.go` を以下の内容にする。

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/taktiks2/go-todo/backend/internal/config"
	httpapi "github.com/taktiks2/go-todo/backend/internal/http"
)

// Cloud Run は SIGTERM の 10 秒後に SIGKILL する。この猶予は固定で設定できない。
// 8 秒にして、残り 2 秒を後片付けとプロセス終了に残す。
const defaultShutdownTimeout = 8 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	h := httpapi.NewHandler()

	srv := &http.Server{
		// Addr は設定しない。Serve(ln) は見ないので、持たせると嘘になる。
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// listen を main が持つと、bind 失敗を起動時に切り分けられる。
	// Cloud Run が注入した PORT に bind できない事故はここで死ぬ。
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}

	if err := run(context.Background(), ln, srv, defaultShutdownTimeout); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

// run は ln で待ち受け、ctx が Done になるまでリクエストを捌く。
// Done 後は新規接続を止め、処理中のリクエストを最大 shutdownTimeout だけ待つ。
func run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	// バッファ 1。Shutdown 後に誰も受け取らなくても goroutine が漏れない。
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	slog.Info("server started", "addr", ln.Addr().String())

	select {
	case err := <-errCh: // Shutdown を呼ぶ前に落ちた = 異常
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	slog.Info("shutting down", "cause", context.Cause(ctx))

	// ここで stop() を呼ぶと、2 回目の SIGTERM / Ctrl-C が既定動作に戻る。
	// 呼ばないとドレインが詰まったとき SIGKILL を待つしかない。
	stop()

	// 親は Background。ctx から派生させると既にキャンセル済みなので、
	// Shutdown が即座に諦めて 1 リクエストも捌かない。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}
```

**読みどころ 4 つ:**

1. **`context.WithTimeout(context.Background(), ...)`** — 親を `ctx` にするとコンパイルは通り一見動くが、`Shutdown` が即座に諦めて 1 リクエストも捌かない。この issue で唯一「動くけど完全に間違っている」書き方
2. **`select` が `errCh` を「Done より前」の経路としてのみ使っている** — `Shutdown` を呼ぶと `Serve` は*即座に* `ErrServerClosed` を返す（`net/http` の doc が「プログラムを終了させず `Shutdown` の戻りを待て」と明記）。`errCh` を Done の後にも待つ形にすると、捌き切る前に抜ける
3. **`stop()` が `defer` と本体の 2 か所にある** — `context.CancelFunc` は冪等。本体側は「2 回目のシグナルを通す」ため、`defer` 側は `Serve` が先に落ちた経路の後片付けのため
4. **`context.Cause(ctx)`** — Go 1.26 の新挙動。`NotifyContext` はシグナル起因のキャンセル時、`Cause` にどのシグナルかを示すエラーを入れる。SIGTERM か Ctrl-C かがログで区別できる。**戻りは wrap されていないので `errors.Is(..., context.Canceled)` は false。ログ専用に使い、判定には使わない**

- [ ] **Step 2: 人間が `backend/cmd/api/main.go` を手で書く**

**Claude はここで止まる。** 書き終わったと言われるまで次のステップに進まない。

- [ ] **Step 3: GREEN を確認する**

Run: `just test`

Expected: **PASS**。

```
ok  	github.com/taktiks2/go-todo/backend/cmd/api	0.4xxs
ok  	github.com/taktiks2/go-todo/backend/internal/config	0.0xxs
ok  	github.com/taktiks2/go-todo/backend/internal/http	0.0xxs
```

失敗した場合は**テストを変えず実装を直す**（`CONTRIBUTING.md:46`）。よくある外し方:

| 症状 | 原因 |
|---|---|
| 1 本目が「処理中のリクエストが切断された」で落ちる | `WithTimeout` の親を `ctx` にしている |
| 1 本目が「run() が戻らなかった」で落ちる | `<-ctx.Done()` の後にも `errCh` を待っている、または `Serve` を goroutine に入れていない |
| 2 本目が `nil` で落ちる | `_ = srv.Shutdown(...)` で戻り値を捨てている |
| 3 本目が `nil` で落ちる | `Serve` のエラーを `ErrServerClosed` 以外も `nil` に潰している |

- [ ] **Step 4: lint を通す**

Run: `just lint`

Expected: **0 issues**。指摘が出たら `just fmt` を先に走らせる。

- [ ] **Step 5: コミットする**

```bash
git add backend/cmd/api/main.go
git commit -m "feat(api): SIGTERM でグレースフルシャットダウンする

run(ctx, ln, srv, shutdownTimeout) を切り出し、signal.NotifyContext で
SIGTERM / os.Interrupt を捕捉して srv.Shutdown を 8 秒で呼ぶ。
ErrServerClosed は正常終了として扱う。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: `docs/DESIGN.md` §9 を確定形に差し替える

**Files:**
- Modify: `docs/DESIGN.md:392-414`（`### グレースフルシャットダウン（必須）` の節）

**Interfaces:**
- Consumes: Task 2 で確定した `main` / `run` の形
- Produces: なし（ドキュメント）

`CONTRIBUTING.md` §8 —「設計判断が変わったら、それを起こした PR の中で `docs/DESIGN.md` を直す」。

- [ ] **Step 1: `### グレースフルシャットダウン（必須）` の節を丸ごと置き換える**

置換対象は「`### グレースフルシャットダウン（必須）`」から「`この形は Kubernetes でも同じ。コンテナ上で動くサーバの基本作法。`」まで。新しい内容:

````markdown
### グレースフルシャットダウン（必須）

Cloud Run はインスタンス停止時に **SIGTERM を送り、10 秒後に SIGKILL** する。この猶予は固定で、設定できない。無視すると処理中のリクエストが切断される。

**ただし SIGTERM は保証されない。** インフラ都合で送られないことがあるため、グレースフルシャットダウンは best-effort として扱い、これに依存した整合性設計はしない。

`main()` は薄く保ち、本体をテストできる `run()` に寄せる（#3 で確定）。

```go
// Cloud Run の 10 秒に対するマージン。残り 2 秒を後片付けとプロセス終了に残す。
const defaultShutdownTimeout = 8 * time.Second

func main() {
    // ... config.Load() / httpapi.NewHandler() / http.Server の組み立て

    // listen を main が持つと bind 失敗を起動時に切り分けられる。
    // Cloud Run が注入した PORT に bind できない事故はここで死ぬ。
    ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
    if err != nil {
        slog.Error("listen", "err", err)
        os.Exit(1)
    }

    if err := run(context.Background(), ln, srv, defaultShutdownTimeout); err != nil {
        slog.Error("server", "err", err)
        os.Exit(1)
    }
}

func run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error {
    ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
    defer stop()

    errCh := make(chan error, 1)
    go func() { errCh <- srv.Serve(ln) }()

    slog.Info("server started", "addr", ln.Addr().String())

    select {
    case err := <-errCh: // Shutdown を呼ぶ前に落ちた = 異常
        if errors.Is(err, http.ErrServerClosed) {
            return nil
        }

        return fmt.Errorf("serve: %w", err)
    case <-ctx.Done():
    }

    slog.Info("shutting down", "cause", context.Cause(ctx))
    stop() // 2 回目の SIGTERM / Ctrl-C を既定動作に戻す

    shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
    defer cancel()

    if err := srv.Shutdown(shutdownCtx); err != nil {
        return fmt.Errorf("shutdown: %w", err)
    }

    // Phase 2 では pool.Close() をここに置く。Shutdown が戻った時点で
    // 処理中のリクエストは DB を使い終わっている、という順序が要る。
    return nil
}
```

この形が抱えている判断は 5 つ。

- **`net.Listener` を `main` が作る。** `ListenAndServe` は `:0` で待ち受けても実ポートを教えてくれず、実 TCP を張るテストが固定ポート頼みになって flaky になる。listener を注入すれば `127.0.0.1:0` でテストでき、同時に bind 失敗を起動時に切り分けられる
- **`signal.NotifyContext` を `run` の中に置く。** 呼び出し側は親 `ctx` を渡すだけでよく、テストは `cancel()` でシグナルと同じ経路を通せる
- **`<-ctx.Done()` の直後に `stop()` を呼ぶ。** `NotifyContext` は `stop()` を呼ぶまで 2 回目の SIGTERM / Ctrl-C を食い止める。呼ばないと、ドレインが詰まったときオペレータが 2 回目を押しても効かず SIGKILL を待つしかない
- **`srv.Shutdown` の戻り値を捨てない。** ctx が期限切れなら `Shutdown` は ctx のエラーを返す。それは「猶予内に捌き切れずリクエストを切った」という事実そのもので、捨てると本番で断続的に接続が切れていても気づけない
- **シャットダウン用 ctx の親は `context.Background()`。** `ctx` から派生させると既にキャンセル済みなので `Shutdown` が即座に諦め、1 リクエストも捌かない

`context.Cause(ctx)` は **Go 1.26 の新挙動**を使っている。`NotifyContext` はシグナル起因のキャンセル時、`Cause` にどのシグナルかを示すエラーを入れる。SIGTERM（Cloud Run のスケールダウン）か `os.Interrupt`（ローカルの Ctrl-C）かがログで区別できる。**ただし戻りは wrap されていないため `errors.Is(..., context.Canceled)` は false になる。ログ専用に使い、判定には使わない。**

**アプリが PID 1 で SIGTERM を受け取る必要がある。** Dockerfile の `ENTRYPOINT` をシェル形式で書くとシグナルが届かず、この節の実装が丸ごと無意味になる。下の Dockerfile 例が exec 形式なのはそのため。

この形は Kubernetes でも同じ。コンテナ上で動くサーバの基本作法。
````

- [ ] **Step 2: 差分を確認する**

Run: `git diff docs/DESIGN.md`

Expected: §9 の該当節だけが変わっている。他の節（Cloud Run 設定、コンテナ、設定とシークレット）に差分が出ていないこと。

- [ ] **Step 3: コミットする**

```bash
git add docs/DESIGN.md
git commit -m "docs: #3 で確定した判断を DESIGN.md §9 に反映

net.Listener の注入、stop() の位置、Shutdown の戻り値、シャットダウン用
ctx の親を Background にする理由を追記し、Phase 2 で pool.Close() を
置く位置を明示する。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: 受け入れ確認して PR を出す

**Files:**
- 変更なし（確認と PR 作成のみ）

**Interfaces:**
- Consumes: Task 1〜3 の成果すべて
- Produces: なし

`CONTRIBUTING.md` §6 —「CI は自動で止めてくれない」。3 つのゲートを人間が通す。

- [ ] **Step 1: `just test` と `just lint` が緑であることを確認する**

```bash
just test
just lint
```

- [ ] **Step 2: issue #3 の動作確認コマンドを実行する**

**これが最重要。** テストが緑でも実際は動かないケース、およびテスト自体の誤りを捕まえる唯一の手段（`CONTRIBUTING.md:190`）。

```bash
just dev &
curl -s localhost:8080/healthz
kill -TERM %1
```

Expected:

```
{"status":"ok"}
```

そして `kill -TERM` の後に以下が出て、**`level=ERROR` を一切出さずに終了する**。

```
time=... level=INFO msg="shutting down" cause="terminated signal received"
```

`cause` にシグナル名が出ていることも見る（Go 1.26 の `context.Cause`）。
`terminated` は `syscall.SIGTERM.String()`。Ctrl-C で止めた場合は `interrupt signal received` になる。

**ログが出ずに即死したら、間に挟まっているプロセスを疑う。** `just dev` は `just` → `go run` → バイナリ
の 3 段になっており、シグナルがバイナリまで届いているかがこの構成に依存する。切り分けは、
中間層を外して直接バイナリを叩く:

```bash
nix develop --command sh -c 'cd backend && go build -o /tmp/go-todo-api ./cmd/api'
/tmp/go-todo-api &
curl -s localhost:8080/healthz
kill -TERM %1
```

これで正常終了するなら実装は正しく、`just dev` 側の中継の話（本番の Cloud Run は
`ENTRYPOINT ["/app"]` でバイナリが PID 1 なので、この中継は存在しない）。

なお `/healthz` は即座に返るのでドレインすべきものが無い。**「捌き切ってから終了する」を実際に
確かめているのは Task 1 の自動テストであり、この手動確認が見ているのは「シグナルが届き、
エラーを出さずに終了する」ところまで。** 遅いエンドポイントは Phase 1 まで存在しない。

- [ ] **Step 3: `/code-review` を走らせる**

`CONTRIBUTING.md` §6 の 2 つ目のゲート。**テスト自体の妥当性**もレビュー対象に含める。
指摘は盲信も無視もせず、根拠を確認する（`CONTRIBUTING.md:194`）。

- [ ] **Step 4: PR を作る**

```bash
git push -u origin 3-graceful-shutdown
```

PR タイトル（squash 後の `main` のコミットメッセージになる。Conventional Commits）:

```
feat: グレースフルシャットダウンを実装する
```

PR 本文（`curl` の**結果を貼る**。`CONTRIBUTING.md` §5）:

````markdown
Closes #3

SIGTERM を受けたら処理中のリクエストを捌き切ってから終了する。
`cmd/api/main.go` に `run(ctx, ln, srv, shutdownTimeout) error` を切り出し、
in-flight が完了することをテストで検証している。

`docs/DESIGN.md` §9 のコード例からの差分は 3 点（同じ PR で §9 を更新済み）:

- `net.Listener` を `main` が作り `run` に渡す（テストが実ポートを掴めるように）
- `<-ctx.Done()` の直後に `stop()` を呼ぶ（2 回目の SIGTERM を通す）
- `srv.Shutdown` の戻り値を捨てず `run` の戻り値として返す

## 動作確認

```
$ just dev &
$ curl -s localhost:8080/healthz
{"status":"ok"}

$ kill -TERM %1
（ここに実際の出力を貼る）
```

## 受け入れ条件

- [x] `just test` が緑（処理中のリクエストが完了してから終了することを検証している）
- [x] `just lint` が緑
- [x] SIGTERM で終了するとき、エラーログを出さない
````

- [ ] **Step 5: CI が緑になったことを確認してから squash merge する**

`CONTRIBUTING.md` §5 —「1 issue = 1 PR。マージは squash merge」。

---

## 完了条件

- [ ] `just test` が緑。`cmd/api` に 3 本のテストが増えている
- [ ] `just lint` が緑
- [ ] `kill -TERM` で `level=ERROR` を出さずに終了する
- [ ] `docs/DESIGN.md` §9 が実装と一致している
- [ ] PR がマージされ、issue #3 が閉じている
- [ ] `gh issue list --label phase:0 --state open` に残るのが #4 #5 #6 #7 #13 だけになっている（Phase 0 の Go 側が閉じた）
