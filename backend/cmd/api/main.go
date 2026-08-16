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

// タイムアウトの定数。定数どうしで守れる関係は 2 つで、TestTimeoutBudget が縛る。
//
//	readHeaderTimeout < readTimeout                    ヘッダとボディを分ける
//	defaultShutdownTimeout < cloudRunTerminationGrace  SIGKILL に間に合う
//
// ドレインが猶予内に終わることは保証できない。WriteTimeout はソケットの書き込み
// 期限であってハンドラの実行時間を止めないため、ハンドラが長引けば接続は active
// のまま残る。ハンドラ自体を縛るには http.TimeoutHandler が要る（Phase 1）。
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second

	cloudRunTerminationGrace = 10 * time.Second // 固定。設定できない
	defaultShutdownTimeout   = 8 * time.Second  // 残り 2 秒を後片付けに残す
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	h := httpapi.NewHandler()

	srv := newServer(h.Routes())

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

// run は ln で待ち受け、ctx が Done になるまでリクエストを捌く。
// Done 後は新規接続を止め、処理中のリクエストを最大 shutdownTimeout だけ待つ。
//
// srv.Serve(ln) は戻ってこないので、「捌き続ける」と「シグナルを待つ」を
// 同時にやるために流れを 2 本にする。
//
//	main の流れ                        goroutine の流れ
//	│
//	├─ NotifyContext ─► SIGTERM の横取りを開始
//	│
//	├─ go func() ───────────────────┐  ← ここで流れが 2 本になる
//	│                               │
//	├─ select { どちらが先に来る？ }  │  srv.Serve(ln) の中で
//	│      │◄─ SIGTERM 到着 ────────┼─ ctx.Done() が閉じる
//	│      └◄─ accept が死んだ ──────┼─ errCh に実エラーが来る
//	│      ▼                        │
//	├─ stop()  2 回目のシグナルを通す │
//	│                               │
//	├─ srv.Shutdown(shutdownCtx)    │
//	│    処理中を捌き切るまで待つ     ┤ Serve が ErrServerClosed を返し、
//	│    超過したら srv.Close()      ✗ errCh に置かれて goroutine 終了
//	│
//	├─ 未受信なら <-errCh で拾う
//	│
//	└─ errors.Join(serve, shutdown)
func run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error {
	// NotifyContext を run の中に置くと、テストは親 ctx を cancel するだけで
	// シグナルと同じ経路を通せる。stop は横取りをやめる関数。
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	errCh := make(chan error, 1) // 受け取り漏れで goroutine を残さないための保険

	slog.Info("starting server", "addr", ln.Addr().String())

	go func() { errCh <- srv.Serve(ln) }()

	var (
		serveErr error
		served   bool
	)

	select {
	// Shutdown を呼ぶ前に accept ループが死んだ = 異常。ただし受理済みの
	// リクエストは無関係に生きているので、ここでも捌き切ってから返す。
	case serveErr = <-errCh:
		served = true
	case <-ctx.Done():
		slog.Info("shutting down", "cause", context.Cause(ctx))
	}

	// どちらの経路でも横取りをやめる。ドレイン中に 2 回目の SIGTERM / Ctrl-C を
	// 押したら既定動作で即死できるようにするため、select の外に置く。
	stop()

	// 親は Background。ctx から派生させると既にキャンセル済みなので、
	// Shutdown が即座に諦めて 1 リクエストも捌かない。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		// Shutdown は ctx が切れても処理中の接続を閉じない。強制切断する。
		_ = srv.Close()

		shutdownErr = fmt.Errorf("shutdown: %w", shutdownErr)
	}

	if !served {
		// select は ready な case を無作為に選ぶので、ctx.Done() が選ばれた裏で
		// Serve が実エラーで落ちていることがある。ここで拾い直す。
		// この分岐だけは決定的なテストが書けない（同時 ready を狙って作れない）。
		serveErr = <-errCh
	}

	// accept が死んだこととドレインが超過したことは独立した事実なので両方返す。
	return errors.Join(serveError(serveErr), shutdownErr)
}

// newServer はタイムアウトの定数を配線した http.Server を返す。
// main から切り出してあるのは、配線をテストで確かめられるようにするため。
func newServer(h http.Handler) *http.Server {
	return &http.Server{
		// Addr は設定しない。Serve(ln) は見ないので、持たせると嘘になる。
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// serveError は Serve の戻り値を run の戻り値に変換する。
// ErrServerClosed は Shutdown / Close による正常終了なので nil。
func serveError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve: %w", err)
}
