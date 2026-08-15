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
