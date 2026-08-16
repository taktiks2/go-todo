package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"
)

// テスト全体で使う待ち時間の上限。これを超えたら「戻ってこない」と判定する。
const waitLimit = 3 * time.Second

// newListener はポート 0（= OS が空きを選ぶ）で待ち受ける listener を返す。
// 固定ポートの取り合いで落ちない。run が net.Listener を受け取る設計なので
// これができる。ListenAndServe を直に呼ぶ形だと実ポートを知る手段がない。
func newListener(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = ln.Close() }) // 二重 Close は無害

	return ln
}

// newClient は keep-alive の接続を他のテストに持ち越さないクライアントを返す。
func newClient(t *testing.T) *http.Client {
	t.Helper()

	c := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(c.CloseIdleConnections)

	return c
}

// blockingHandler は突入を entered で知らせ、release が閉じられるまで応答しない。
//
// time.Sleep で処理時間を決めないのが要点。sleep だとテスト側の goroutine が
// 遅れたときにリクエストが先に完走し、ドレインを一度も試さないまま全部通る
// 偽の緑になる。止めておけば「シャットダウン時に処理中」が確定する。
//
// ハンドラの中で t.* を呼ばない（テスト終了後に走ることがある）。
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

// waitUntilRefused は addr が新規接続を拒否するまで待つ。
// Shutdown が listener を閉じた証拠として使う。
//
// 受理するのは ECONNREFUSED だけ。dial のタイムアウトを拒否と見なすと、
// まだ listen しているのに閉じたと判定してしまう。
func waitUntilRefused(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)

		switch {
		case err == nil:
			_ = conn.Close()
		case errors.Is(err, syscall.ECONNREFUSED):
			return
		default:
			t.Logf("dial: %v（拒否ではないので再試行）", err)
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("シャットダウンを要求したのに新規接続を拒否しない")
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

// TestTimeoutBudget はタイムアウト定数どうしの関係を固定する。
// ここで縛れるのは必要条件だけ（理由は main.go の定数ブロック）。
func TestTimeoutBudget(t *testing.T) {
	t.Parallel()

	// 同値だと ReadHeaderTimeout が実質無効になる。net/http は 0 のとき
	// ReadTimeout にフォールバックし、両者が等しいとボディ用の期限を
	// 延長する分岐も死ぬので、ヘッダだけを短く縛れなくなる。
	if readHeaderTimeout >= readTimeout {
		t.Errorf(
			"readHeaderTimeout %v >= readTimeout %v（ヘッダとボディの予算が分離されていない）",
			readHeaderTimeout, readTimeout,
		)
	}

	// 後ろだと Shutdown の戻り値を見る前に殺され、ログに何も残らない。
	if defaultShutdownTimeout >= cloudRunTerminationGrace {
		t.Errorf(
			"defaultShutdownTimeout %v >= cloudRunTerminationGrace %v（SIGKILL に間に合わない）",
			defaultShutdownTimeout, cloudRunTerminationGrace,
		)
	}
}

// TestNewServerUsesTimeoutBudget は定数が http.Server に配線されていることを
// 検証する。TestTimeoutBudget は定数どうしの関係しか見ないので、配線を消しても
// 取り違えても、そちらだけでは緑のまま通る。
func TestNewServerUsesTimeoutBudget(t *testing.T) {
	t.Parallel()

	srv := newServer(http.NewServeMux())

	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, readHeaderTimeout},
		{"ReadTimeout", srv.ReadTimeout, readTimeout},
		{"WriteTimeout", srv.WriteTimeout, writeTimeout},
		{"IdleTimeout", srv.IdleTimeout, idleTimeout},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}

	// Serve(ln) は Addr を見ないので、持たせると嘘になる。
	if srv.Addr != "" {
		t.Errorf("Addr = %q, want \"\"（Serve(ln) は Addr を見ない）", srv.Addr)
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

	// ③ 新規接続が拒否される = Shutdown が listener を閉じた。
	//    ハンドラはまだ止めたままなので「シャットダウン中に処理中」が確定する。
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

	// ⑥ nil = main が slog.Error を呼ばない。
	//    受け入れ条件「SIGTERM で終了するときエラーログを出さない」の担保。
	if err := waitForRun(t, runErr); err != nil {
		t.Errorf("run() = %v, want nil（正常終了ではエラーログを出さない）", err)
	}
}

// TestRunClosesConnectionsWhenDrainTimesOut は、猶予内に捌き切れなかったとき
// (1) それが戻り値に出ること (2) 残った接続が強制切断されることを検証する。
// Shutdown は ctx が切れても ctx.Err() を返すだけで接続を閉じない。
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

	// (2) 残った接続が強制切断される。Close が無ければここがタイムアウトする。
	select {
	case res := <-resCh:
		if res.err == nil {
			t.Errorf("ドレイン超過なのに接続が閉じられていない: status = %d", res.status)
		}
	case <-time.After(waitLimit):
		t.Fatal("ドレイン超過後も接続が開いたまま（srv.Close() が呼ばれていない）")
	}
}

// errAcceptBoom は failAfterListener が意図的に返す恒久エラー。
// net/http は一時エラーだと Accept をリトライするので Temporary() を持たせない。
var errAcceptBoom = errors.New("accept boom")

// failAfterListener は fail が閉じられたあとの Accept を恒久エラーにする。
// Accept は本物の listener で待っているので、止めるには fail を閉じたうえで
// 下位の listener も閉じて叩き起こす必要がある（failNow がまとめる）。
type failAfterListener struct {
	net.Listener

	fail chan struct{}
}

func newFailAfterListener(t *testing.T) *failAfterListener {
	t.Helper()

	return &failAfterListener{Listener: newListener(t), fail: make(chan struct{})}
}

func (l *failAfterListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		select {
		case <-l.fail:
			return nil, errAcceptBoom // 意図的に失敗させた
		default:
			return nil, err
		}
	}

	return conn, nil
}

func (l *failAfterListener) failNow() {
	close(l.fail)
	_ = l.Close()
}

// TestRunDrainsWhenServeFails は、accept ループが死んでも受理済みのリクエストを
// 捌き切ってから戻ることを検証する。accept の失敗は受理済み接続の健全性と
// 無関係なので、ここで即 Close するとドレイン猶予を使わずに全部切ってしまう。
func TestRunDrainsWhenServeFails(t *testing.T) {
	t.Parallel()

	ln := newFailAfterListener(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)

	srv := &http.Server{
		Handler:           blockingHandler(entered, release),
		ReadHeaderTimeout: 5 * time.Second,
	}

	runErr := make(chan error, 1)
	go func() { runErr <- run(t.Context(), ln, srv, 8*time.Second) }()

	resCh := getAsync(t, ln.Addr().String())

	// リクエストが処理中になるまで待つ
	select {
	case <-entered:
	case <-time.After(waitLimit):
		t.Fatal("ハンドラに到達しなかった")
	}

	// accept ループを殺す
	ln.failNow()

	// 処理中が残っている間は戻ってはいけない
	select {
	case err := <-runErr:
		t.Fatalf("処理中のリクエストを捌かずに戻った: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	releaseOnce()

	// 処理中だったリクエストは切断されず完走する
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

	// 捌き切ったうえで、accept が死んだ事実は戻り値に出る
	if err := waitForRun(t, runErr); !errors.Is(err, errAcceptBoom) {
		t.Errorf("run() = %v, want %v を含むエラー", err, errAcceptBoom)
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
