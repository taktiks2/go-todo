# グレースフルシャットダウン

- issue: #3（`phase:0` / `area:http` / `mode:pair-tdd`）
- 日付: 2026-08-15
- 関連: `docs/DESIGN.md` §9 グレースフルシャットダウン（必須）、§9 Cloud Run 設定、§10 テスト戦略、§12 Phase 0
- 前提: `CONTRIBUTING.md` §1 役割分担、§2 pair-tdd、§8 ドキュメントの役割
- 環境: go1.26.5 / golangci-lint 2.12.2（nix devShell 実測）

## 目的

SIGTERM を受けたら、処理中のリクエストを捌き切ってから終了する。

Cloud Run はインスタンス停止時に SIGTERM を送り、**10 秒後に SIGKILL** する。この猶予は固定で設定できない。
無視すれば処理中のリクエストが切断される。同じ形が Kubernetes でも成立するため、
これはコンテナ上で動くサーバの基本作法であって、Cloud Run 固有の話ではない。

`docs/DESIGN.md` §12 は Phase 0 の「Go の土台」に `cmd/api/main.go`・`/healthz`・
グレースフルシャットダウンの 3 つを挙げている。#2 で前 2 つが済んでおり、**これで Phase 0 の Go 側が閉じる。**

**この issue の本題は「シャットダウンの書き方」ではなく「`main()` のどこに継ぎ目を入れればテストできるか」である。**
受け入れ条件が「処理中のリクエストが完了してから終了することをテストで検証している」を要求しているが、
`docs/DESIGN.md` §9 のコード例は全部 `main()` にインラインで書かれており、`main()` はテストできない。

## 決定事項

| 項目 | 決定 | 理由 |
|---|---|---|
| **切り出し先** | `package main` の `run(ctx, ln, srv, shutdownTimeout) error`。テストは `cmd/api/main_test.go` | 新パッケージを作らずに済み、`docs/DESIGN.md` §3 のディレクトリ構成が不変。`main()` は「配線と終了コード」だけになり §3「起動と DI 配線のみ」に沿う。Go で広く使われている形（`main` は薄く、`run` が error を返す）。`internal/server` パッケージ化は Phase 2 で `pool.Close()` が増えて `main` が重くなってから判断すればよい（YAGNI） |
| **`signal.NotifyContext` の位置** | **`main()` ではなく `run()` の中**。`run` は親 `ctx` を受け取り、自分で派生させる | 「`<-ctx.Done()` の直後に `stop()` を呼ぶ」を採るには、Done を待つ側が `stop` を持っていなければならない。`main()` に置くと `stop` を引数で `run` に渡すことになり、シグネチャが濁る。**テストは親 ctx を `cancel()` するだけでシグナルと同じ経路を通せる**（親の Done が閉じれば派生 ctx も閉じる） |
| **`net.Listener` を `main` が作る** | `main` が `net.Listen` し、`run` は `srv.Serve(ln)` を呼ぶ。`http.Server.Addr` は設定しない | **`ListenAndServe` は `:0` で待ち受けても実ポートを教えてくれない。** 固定ポートを使うテストは flaky になるため、実 TCP を張るテストを書くなら listener の注入が要る。加えて bind 失敗を起動時に切り分けられる（Cloud Run が注入した `PORT` に bind できない事故がここで死ぬ）。`Serve(ln)` は `Addr` を見ないので、持たせたままにすると嘘になる |
| **`<-ctx.Done()` の直後に `stop()`** | 呼ぶ | `signal.NotifyContext` は **`stop()` を呼ぶまで 2 回目の SIGTERM / Ctrl-C を食い止める**（`os/signal` の doc に明記）。呼ばないと、ドレインが詰まったときオペレータが 2 回目を押しても効かず、SIGKILL を待つしかない。`docs/DESIGN.md` §9 の例は `defer stop()` だけなのでこの逃げ道がない |
| **`srv.Shutdown` の戻り値** | 捨てずに `run` の戻り値として返す。`main` が `slog.Error` + `os.Exit(1)` | ctx が期限切れなら `Shutdown` は ctx のエラーを返す。**これは「8 秒で捌き切れずリクエストを切った」という事実そのもの。** 捨てると本番で断続的に接続が切れていても気づけない。`docs/DESIGN.md` §9 の例は `_ = srv.Shutdown(...)` |
| **シャットダウンタイムアウト** | `defaultShutdownTimeout = 8 * time.Second` を `cmd/api/main.go` の定数に置き、**`run` の引数で渡す** | 8 秒は Cloud Run の 10 秒に対するマージン（残り 2 秒で後片付けとプロセス終了）。引数にするのは**タイムアウト超過の経路をテストするため**。定数直参照だとその 1 本が 8 秒待ちになる。定数名を `default` 付きにするのは、`run` の引数名 `shutdownTimeout` と衝突して定数がシャドウされるのを避けるため。環境変数には出さない（猶予 10 秒は Cloud Run 側で固定・設定不可なので可変にする実益がない） |
| **シャットダウン用 ctx の親** | **`context.Background()`**。`run` が受け取った `ctx` から派生させない | `ctx` は既にキャンセル済みなので、そこから `WithTimeout` すると `Shutdown` が即座に諦め、**1 リクエストも捌かずに戻る。** この issue で唯一「コンパイルも通り一見動くが完全に間違っている」書き方 |
| **シグナル名のログ** | `slog.Info("shutting down", "cause", context.Cause(ctx))` | **Go 1.26 の新挙動**: `NotifyContext` はシグナル起因のキャンセル時、`context.Cause` にどのシグナルかを示すエラーを入れる。SIGTERM（Cloud Run のスケールダウン）か `os.Interrupt`（ローカルの Ctrl-C）かがログで区別できる。**ただし `ctx.Err()` は従来どおり `context.Canceled` であり、`context.Cause` の戻りは wrap されていないので `errors.Is(..., context.Canceled)` は false になる。判定には使わずログ専用にする**（golang/go#77639 は「`Cause` の誤用」として 2026-05 にクローズ） |
| **`Serve` の goroutine** | エラーを容量 1 のバッファ付き channel に送る | `Shutdown` 完了後に誰も受け取らなくても goroutine が漏れない。#2 の `main` にあった goroutine 内 `os.Exit(1)` は廃止する（テストできないため） |
| **`http.ErrServerClosed`** | `Shutdown` 前に `Serve` が返したときだけ判定し、`nil` を返す | `Shutdown` を呼ぶと `Serve` は**即座に** `ErrServerClosed` を返す（doc は「プログラムを終了させず `Shutdown` の戻りを待て」と明記）。`select` で `errCh` を先に受ける形にすると、捌き切る前に `main` を抜ける |
| **シグナル配線のテスト** | しない。`run` のテストは親 ctx の `cancel()` で駆動する | シグナル → ctx キャンセルは標準ライブラリの責務。テストプロセス自身に `syscall.Kill` を撃つ形はプロセスグローバルに効き `t.Parallel()` と併用できず、得られるのは標準ライブラリの動作確認でしかない。**実 SIGTERM は issue の `kill -TERM %1` で手動確認する**（`CONTRIBUTING.md` §6 の 3 つ目のゲート） |
| **`testing/synctest`** | 使わない | Go 1.25 で正式化された並行テスト用 bubble（fake clock）だが、**実ネットワーク I/O は禁止**（socket で block した goroutine は "durably blocked" にならず bubble が idle にならない）。今回は実 TCP を張るので相性が悪い。代わりに **channel で「ハンドラに突入した」を通知**して同期し、`time.Sleep` への依存を最小化する |
| **`internal/config` の変更** | なし | タイムアウトを環境変数に出さないため |
| **`internal/http` の変更** | なし | テストは `http.HandlerFunc` を直接 `http.Server` に差すので、本物のルータは要らない |

## 成果物

```
go-todo/
├── backend/cmd/api/
│   ├── main.go        # run() を切り出し、シャットダウンを実装（人間が書く）
│   └── main_test.go   # 新規。run() のテスト 3 本（Claude が書く）
└── docs/DESIGN.md     # §9 のコード例を確定形に差し替え（Claude が書く）
```

## `cmd/api/main.go` の契約

```go
const defaultShutdownTimeout = 8 * time.Second

func run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error
```

`run` は `ln` で待ち受け、`ctx` が Done になるまでリクエストを捌く。Done 後は新規接続を止め、
処理中のリクエストを最大 `shutdownTimeout` だけ待ってから戻る。

| 状況 | `run` の戻り値 |
|---|---|
| `ctx` が Done → `shutdownTimeout` 内に捌き切った | `nil` |
| `ctx` が Done → `shutdownTimeout` を超えた | `context.DeadlineExceeded` を wrap したエラー |
| `ctx` が Done になる前に `Serve` が失敗した | そのエラーを wrap して返す |
| `ctx` が Done になる前に `Serve` が `http.ErrServerClosed` を返した | `nil` |

```go
func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	h := httpapi.NewHandler()

	srv := &http.Server{
		Handler:           h.Routes(), // Addr は持たせない。Serve(ln) は見ない
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

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

	errCh := make(chan error, 1) // バッファ 1。誰も受け取らなくても goroutine が漏れない
	go func() { errCh <- srv.Serve(ln) }()

	slog.Info("server started", "addr", ln.Addr().String())

	select {
	case err := <-errCh: // Shutdown 前に落ちた = 異常
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	slog.Info("shutting down", "cause", context.Cause(ctx))
	stop() // 2 回目の SIGTERM / Ctrl-C で強制終了できるようにする

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel() // 親は Background。ctx から派生させると即座に諦める

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}
```

### 読みどころ

1. **`context.WithTimeout(context.Background(), ...)`** — 親を `ctx` にすると既にキャンセル済みなので `Shutdown` が即座に諦め、1 リクエストも捌かない。手で書くとき最も踏みやすい罠
2. **`select` が `errCh` を「Done より前」の経路としてのみ使っている** — `Shutdown` を呼べば `Serve` は即座に `ErrServerClosed` を返す。この `select` を Done の後にも置くと、捌き切る前に抜ける
3. **`stop()` が `defer` と本体の 2 か所にある** — `CancelFunc` は冪等。本体側は「2 回目のシグナルを通す」ため、`defer` 側は `Serve` が先に落ちた経路での後片付けのため

## テスト設計

`backend/cmd/api/main_test.go`（`package main`）。`t.Setenv` を使わないので全ケース `t.Parallel()` を付ける。

### `TestRun_処理中のリクエストを捌き切ってから終了する`

**受け入れ条件の本体。**

1. `net.Listen("tcp", "127.0.0.1:0")` で listener を作る
2. ハンドラは「突入を channel で知らせてから 200ms スリープし、200 とボディを書く」
3. `ctx, cancel := context.WithCancel(t.Context())` を `run` に渡し、`run` を goroutine で回す
4. クライアントがリクエストを投げる（goroutine）
5. ハンドラ突入の通知を受けてから `cancel()`
6. 検証:
   - クライアントが **200 とボディを完全に受け取れる**（切断されていない）
   - `run` が **`nil` を返す** = `main` が `slog.Error` を呼ばない = 受け入れ条件「SIGTERM で終了するときエラーログを出さない」が構造的に満たされる

`http.DefaultClient` は使わず、テスト内で `&http.Client{Transport: &http.Transport{}}` を作り
`t.Cleanup` で `CloseIdleConnections()` する。keep-alive の接続がテスト間に残らないようにするため。

**ハンドラの中で `t.Error` / `t.Fatal` / `t.Log` を呼ばない。** ドレイン超過を試す次のケースでは、
`run` が戻った後もハンドラの goroutine が走り続けるため、テスト終了後の `t.*` 呼び出しは panic になる。
検証はすべてテスト本体の goroutine で行い、ハンドラは channel で状態を渡すだけにする。

### `TestRun_ドレイン時間を超えたらエラーを返す`

`shutdownTimeout` に 20ms、ハンドラに 200ms のスリープを渡し、
`errors.Is(err, context.DeadlineExceeded)` を検証する。**「`Shutdown` の戻り値を捨てない」を固定するテスト。**

### `TestRun_Serve が失敗したらエラーを返す`

`run` に渡す前に `ln.Close()` しておき、`run` が non-nil を返すことを検証する。
`http.ErrServerClosed` 以外を握り潰していないことを固定する。

### テストしないもの

| 対象 | 理由 |
|---|---|
| `signal.NotifyContext` が実 SIGTERM を拾うこと | 標準ライブラリの責務。手動確認（`kill -TERM`）で担保 |
| `stop()` 後に 2 回目のシグナルで落ちること | 同上。プロセスグローバルな副作用があり自動テストに向かない |
| `main()` 本体 | 配線と `os.Exit` だけ。`os.Exit` を含む関数はテストできない |

## 進め方

`CONTRIBUTING.md` §2 の pair-tdd ループを 1 周まわす。

1. Claude が `cmd/api/main_test.go` を書く（完成形。`// TODO` を残さない）
2. `just test` で **RED を実行ログで確認する**（`run` が未定義でコンパイルエラー = RED）
3. `test(api): グレースフルシャットダウンの失敗テストを追加` でコミット
4. Claude が `main.go` の実装を提示する（本ドキュメントの「契約」節がそれにあたる）
5. **人間が `backend/cmd/api/main.go` を手で書く。** Claude は待つ
6. `just test` / `just lint` で GREEN を確認
7. `feat(api): SIGTERM でグレースフルシャットダウンする` でコミット
8. Claude が `docs/DESIGN.md` §9 を差し替え、`docs:` でコミット
9. 受け入れ確認（下記）を手で実行し、結果を PR 本文に貼る

## 受け入れ確認

```sh
just test          # 緑
just lint          # 緑

just dev &
curl -s localhost:8080/healthz
kill -TERM %1
# → "shutting down" が出て、ERROR を出さずに終了する
```

`CONTRIBUTING.md` §6 の 3 つのゲート（CI 緑 / `/code-review` / 手動確認）をすべて通してからマージする。

## スコープ外

issue #3 の宣言どおり、以下は含まない。

- `pool.Close()`（DB は Phase 2）
- Cloud Run へのデプロイ（#7）
- `slog` の Cloud Logging 整形（#13）
- ミドルウェア（RequestID / Logger / Recoverer）
- リクエスト単位のログ

## #4（Dockerfile）への申し送り

**`ENTRYPOINT` は必ず exec 形式 `["/app"]` で書くこと。**

シェル形式（`ENTRYPOINT /app`）にするとアプリが PID 1 にならず、
**Cloud Run が送る SIGTERM が届かないため、この issue の実装が丸ごと無意味になる。**
`docs/DESIGN.md` §9 の Dockerfile 例は exec 形式になっているので、そこを崩さなければよい。

あわせて、Cloud Run の SIGTERM は**保証されない**（インフラ都合で送られないことがある）。
グレースフルシャットダウンは best-effort であり、これに依存した整合性設計はしない。

## `docs/DESIGN.md` への反映

`CONTRIBUTING.md` §8 に従い、同じ PR で直す。対象は §9「グレースフルシャットダウン（必須）」。

- コード例を上記の確定形に差し替える（`main` / `run` の 2 関数）
- 差し替えに伴い追記する理由:
  - `net.Listen` を `main` が持つ理由（bind 失敗の切り分け、テストで実ポートを掴む）
  - `<-ctx.Done()` 直後の `stop()`（2 回目のシグナルを通す）
  - `Shutdown` の戻り値を捨てない（ドレイン失敗を検知する）
  - シャットダウン用 ctx の親が `context.Background()` である理由
- **Phase 2 で `pool.Close()` を入れる位置**を例の中に明示する（`srv.Shutdown` が戻った後）
- Cloud Run の SIGTERM が保証されない点を 1 行足す

## 参照

- `docs/DESIGN.md` §9 グレースフルシャットダウン（必須） / Cloud Run 設定 / コンテナ
- `CONTRIBUTING.md` §1 §2 §4 §5 §6 §8
- [os/signal](https://pkg.go.dev/os/signal) — `NotifyContext` の `stop()` と `Cause`
- [net/http Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)
- [testing/synctest](https://pkg.go.dev/testing/synctest) — bubble と実ネットワーク I/O の制約
- [Go 1.26 Release Notes](https://go.dev/doc/go1.26) — `NotifyContext` の `CancelCauseFunc` 化
- [golang/go#77639](https://github.com/golang/go/issues/77639) — `Cause` が `context.Canceled` と一致しない件
- [Graceful shutdowns on Cloud Run: deep dive](https://cloud.google.com/blog/topics/developers-practitioners/graceful-shutdowns-cloud-run-deep-dive)
