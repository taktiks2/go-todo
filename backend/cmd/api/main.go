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

// タイムアウトの予算。短い順に
//
//	ハンドラに許す時間 (read/writeTimeout)
//	  <= ドレイン猶予 (defaultShutdownTimeout)
//	  <  Cloud Run が SIGKILL するまで (cloudRunTerminationGrace)
//
// でなければならない。逆転すると、WriteTimeout の契約では正当なリクエストを
// ドレインが先に諦めて切ることになり、通常のスケールダウンのたびに
// エラーログと非ゼロ終了が出る。この関係は TestTimeoutBudget で縛っている。
const (
	readHeaderTimeout = 2 * time.Second
	readTimeout       = 3 * time.Second
	writeTimeout      = 5 * time.Second
	idleTimeout       = 60 * time.Second

	// Cloud Run が SIGTERM のあと SIGKILL するまでの猶予。固定で設定できない。
	cloudRunTerminationGrace = 10 * time.Second

	// 上の猶予に対するマージン。残り 2 秒を後片付けとプロセス終了に残す。
	defaultShutdownTimeout = 8 * time.Second
)

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
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
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
//
// この関数の形は「srv.Serve(ln) は戻ってこない」という 1 点から決まっている。
// Serve を直に呼ぶとそこで流れが止まり、シグナルを待つ余地が無くなる。
// 「リクエストを捌き続ける」と「シグナルを待つ」を同時にやるために流れを 2 本にする。
//
//	main の流れ                        goroutine の流れ
//	│
//	├─ NotifyContext ─► SIGTERM の横取りを開始（stop で解除できる）
//	│
//	├─ slog.Info("starting server")
//	│
//	├─ go func() ───────────────────┐  ← ここで流れが 2 本になる
//	│                               │
//	├─ select { どちらが先に来る？ }  │  srv.Serve(ln) の中で
//	│      │                        │  リクエストを捌き続ける
//	│      │◄─ SIGTERM 到着 ────────┼─ ctx.Done() が閉じる
//	│      ▼                        │
//	├─ stop()  2 回目は即死させる     │
//	│                               │
//	├─ srv.Shutdown(shutdownCtx)    │
//	│    新規接続を止め、処理中を     │
//	│    捌き切るまで待つ（最大 8 秒） ┤ Serve が即 ErrServerClosed を返し、
//	│    Serve の終了も待つ          ✗ errCh に置かれて goroutine 終了
//	│                                  （バッファ 1 なので送信は詰まらない）
//	├─ <-errCh で Serve の結果を拾う
//	│
//	└─ defer cancel() / defer stop() が実行される
func run(ctx context.Context, ln net.Listener, srv *http.Server, shutdownTimeout time.Duration) error {
	// signal.NotifyContext は「OS のシグナル」を「context のキャンセル」に翻訳する。
	// 翻訳された後は <-ctx.Done() を待つだけでよくなり、テストは cancel() を呼ぶだけで
	// シグナルと同じ経路を通せる。
	//
	// stop はその横取りをやめる関数。呼ぶと既定動作（プロセス即死）に戻る。
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)

	// defer は「この関数を抜けるとき必ずやること」の予約。その場では実行されず、
	// return が何本あっても panic しても実行される。だから後片付けを
	// 「取得した行の真下」に書けて、経路が増えても書き忘れが起きない。
	// なお defer は関数の終わりであって、for やブロックの終わりではない。
	defer stop()

	// goroutine の戻り値はどこにも行かない（x := go f() とは書けない）ので、
	// Serve のエラーを持ち帰るには channel が要る。channel は受け取る側が
	// 値の到着まで止まる性質を持ち、これが Go における「待つ」の実装方法になる。
	//
	// バッファ 1 にするのは、誰も受け取らない経路があるため。ドレインが超過して
	// Shutdown がエラーを返すと、run は errCh を読まずに return する。
	// バッファ 0 だと送信側の goroutine が永久に残る（= goroutine リーク）。
	errCh := make(chan error, 1)

	slog.Info("starting server", "addr", ln.Addr().String())

	// go を付けて呼ぶと別の goroutine で走り始め、呼んだ側は待たずに次の行へ進む。
	// go は関数呼び出ししか受け取れないので、無名関数を定義してその場で呼ぶ形になる。
	// 中では srv.Serve(ln) がサーバの寿命ぶん止まり、戻ってきて初めて errCh <- が動く。
	go func() { errCh <- srv.Serve(ln) }()

	var (
		serveErr error
		served   bool
	)

	// select は複数の channel 操作を並べ、最初に準備できた 1 つだけを実行する。
	// switch が「値を比べる」のに対し、select は「channel が動くのを待つ」。
	// ここで待っている 2 つは、どちらが先に来るか事前には分からない。
	select {
	// err := <-errCh は「errCh から取り出して err に入れる」。矢印の向きが受信を表す。
	// Shutdown を呼ぶ前に Serve が戻った = 異常（bind 済みの listener が閉じたなど）。
	case serveErr = <-errCh:
		served = true

	// ctx.Done() はキャンセル時に閉じられる channel を返す。閉じた channel からの
	// 受信は即座に成功するので、これは「キャンセルされるまで待つ」という意味になる。
	// 値に意味は無いので受け取り先を書かない。本体が空なのは意図的で、
	// 「シグナルが来た」という事実さえ取れれば十分だから。
	case <-ctx.Done():
		slog.Info("shutting down", "cause", context.Cause(ctx))

		// ここで stop() を呼ぶと既定動作に戻り、2 回目の SIGTERM / Ctrl-C で即死する。
		// 呼ばないと 2 回目以降も横取りされ続け、ドレインが詰まったとき運用者が
		// もう一度押しても何も起きない。CancelFunc は冪等なので defer と二重でよい。
		stop()
	}

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
		// select は ready な case を無作為に選ぶ（Go の仕様）。ctx.Done() が
		// 選ばれた裏で Serve が実エラーで落ちていることがあるので拾い直す。
		serveErr = <-errCh
	}

	return errors.Join(serveError(serveErr), shutdownErr)
}

// serveError は Serve の戻り値を run の戻り値に変換する。
// ErrServerClosed は正常終了なので nil、それ以外は接続を閉じてから返す。
func serveError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return fmt.Errorf("serve: %w", err)
}
