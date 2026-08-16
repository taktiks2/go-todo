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

	// Serve が内部で閉じるので普段は不要だが、run に渡る前に抜けるテストでも
	// 確実に閉じるよう、ヘルパの契約として後始末を持つ。二重 Close は無害。
	t.Cleanup(func() { _ = ln.Close() })

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

// blockingHandler は「突入を entered で知らせ、release が閉じられるまで応答しない」
// ハンドラを返す。
//
// time.Sleep で処理時間を決めないのが要点。sleep で書くと、テスト側の
// goroutine のスケジューリングが遅れたときにリクエストが先に完走してしまい、
// 「ドレインを一度も試さないまま全アサーションが通る」偽の緑になる。
// ハンドラを止めておけば「シャットダウン開始時に処理中である」ことが確定する。
//
// ハンドラの中で t.* を呼んではいけない（テスト終了後に走る可能性がある）。
func blockingHandler(entered, release chan struct{}) http.Handler {
	mark := sync.OnceFunc(func() { close(entered) })

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mark()
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "drained")
	})
}

// response はクライアント goroutine から本体へ結果を運ぶ。
type response struct {
	status int
	body   string
	err    error
}

// getAsync は addr に GET を投げ、結果を channel で返す。
func getAsync(t *testing.T, addr string) <-chan response {
	t.Helper()

	client := newClient(t)
	ch := make(chan response, 1)

	go func() {
		resp, err := client.Get("http://" + addr + "/")
		if err != nil {
			ch <- response{err: err}

			return
		}
		defer func() { _ = resp.Body.Close() }()

		b, err := io.ReadAll(resp.Body)
		ch <- response{status: resp.StatusCode, body: string(b), err: err}
	}()

	return ch
}

// waitUntilRefused は addr が新規接続を受け付けなくなるまで待つ。
// Shutdown が listener を閉じたことの確認に使う。
func waitUntilRefused(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("シャットダウンを要求したのに新規接続を受け付け続けている")
}

// waitForRun は run の戻り値を待つ。
func waitForRun(t *testing.T, runErr <-chan error) error {
	t.Helper()

	select {
	case err := <-runErr:
		return err
	case <-time.After(waitLimit):
		t.Fatal("run() が戻らなかった")

		return nil
	}
}

// TestTimeoutBudget は、ハンドラに許す時間・ドレイン猶予・Cloud Run の SIGKILL
// までの猶予が、この順に短くなっていることを固定する。
//
// 逆転すると「WriteTimeout の契約では正当なリクエストを、ドレインが先に
// 諦めて切る」ことになり、通常のスケールダウンのたびに ERROR ログと
// 非ゼロ終了が出る。受け入れ条件「SIGTERM で終了するときエラーログを
// 出さない」を構造的に破るので、定数の関係としてテストで縛る。
func TestTimeoutBudget(t *testing.T) {
	t.Parallel()

	if readTimeout > defaultShutdownTimeout {
		t.Errorf("readTimeout %v > defaultShutdownTimeout %v", readTimeout, defaultShutdownTimeout)
	}

	if writeTimeout > defaultShutdownTimeout {
		t.Errorf("writeTimeout %v > defaultShutdownTimeout %v", writeTimeout, defaultShutdownTimeout)
	}

	if defaultShutdownTimeout >= cloudRunTerminationGrace {
		t.Errorf(
			"defaultShutdownTimeout %v >= cloudRunTerminationGrace %v（SIGKILL に間に合わない）",
			defaultShutdownTimeout, cloudRunTerminationGrace,
		)
	}
}

// TestRunDrainsInFlightRequests は、シャットダウン要求の時点で処理中だった
// リクエストが切断されず最後まで応答されることを検証する。受け入れ条件の本体。
func TestRunDrainsInFlightRequests(t *testing.T) {
	t.Parallel()

	ln := newListener(t)
	addr := ln.Addr().String()

	entered := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)

	srv := &http.Server{
		Handler:           blockingHandler(entered, release),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, srv, 8*time.Second) }()

	resCh := getAsync(t, addr)

	// ① ハンドラに入った = リクエストが処理中
	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("ハンドラに到達しなかった")
	}

	// ② SIGTERM 相当
	cancel()

	// ③ 新規接続が拒否されるまで待つ = Shutdown が listener を閉じた証拠。
	//    ここまで来てもハンドラはまだ止まっているので、
	//    「シャットダウン中に処理中のリクエストが存在する」状態が確定する。
	waitUntilRefused(t, addr)

	// ④ ここで初めてハンドラを解放する
	releaseOnce()

	// ⑤ 処理中だったリクエストは切断されず完走する
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

	// ⑥ run が nil を返すこと = main が slog.Error を呼ばないこと。
	//    受け入れ条件「SIGTERM で終了するときエラーログを出さない」はここで担保する。
	if err := waitForRun(t, runErr); err != nil {
		t.Errorf("run() = %v, want nil（正常終了ではエラーログを出さない）", err)
	}
}

// TestRunClosesConnectionsWhenDrainTimesOut は、猶予内に捌き切れなかったとき
// (1) それが戻り値に出ること (2) 残った接続が強制切断されることを検証する。
//
// http.Server.Shutdown は ctx が期限切れになっても ctx.Err() を返すだけで、
// 処理中の接続は閉じない。Close を呼ばないと接続と goroutine が残り続ける。
func TestRunClosesConnectionsWhenDrainTimesOut(t *testing.T) {
	t.Parallel()

	ln := newListener(t)
	addr := ln.Addr().String()

	entered := make(chan struct{})
	release := make(chan struct{})
	// 解放しないまま進めるので、ドレインは必ず超過する。
	t.Cleanup(sync.OnceFunc(func() { close(release) }))

	srv := &http.Server{
		Handler:           blockingHandler(entered, release),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, ln, srv, 50*time.Millisecond) }()

	resCh := getAsync(t, addr)

	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("ハンドラに到達しなかった")
	}

	cancel()

	// (1) 捌き切れなかった事実が戻り値に出る
	if err := waitForRun(t, runErr); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("run() = %v, want context.DeadlineExceeded を含むエラー", err)
	}

	// (2) 残った接続が強制切断される。Close を呼ばないとクライアントは
	//     ハンドラが解放されるまで待ち続け、ここがタイムアウトする。
	select {
	case res := <-resCh:
		if res.err == nil {
			t.Errorf("ドレイン超過なのに接続が閉じられていない: status = %d", res.status)
		}
	case <-time.After(waitLimit):
		t.Fatal("ドレイン超過後も接続が開いたまま（srv.Close() が呼ばれていない）")
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

	err := waitForRun(t, runErr)
	if err == nil {
		t.Fatal("run() = nil, want error")
	}

	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("run() = %v, want net.ErrClosed を含むエラー", err)
	}
}
