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

// タイムアウトの定数。定数どうしで守れる関係は 2 つだけで、
// どちらも TestTimeoutBudget で縛っている。
//
//	readHeaderTimeout < readTimeout                    ヘッダとボディを分ける
//	defaultShutdownTimeout < cloudRunTerminationGrace  SIGKILL に間に合う
//
// 1 つ目が要るのは、両者が同値だと ReadHeaderTimeout が実質無効になるため。
// net/http は ReadHeaderTimeout が 0 のとき ReadTimeout にフォールバックし、
// さらに両者が等しいとボディ用の読み取り期限を延長する分岐が死ぬ。
//
// **「ドレインが必ず間に合う」はここでは保証できない。**
// WriteTimeout はソケットの書き込み期限であって、ハンドラの実行時間を止めない。
// DB クエリが長引けば接続は active のままなので、Shutdown は猶予を使い切る。
// ハンドラ自体を縛るには http.TimeoutHandler が要るが、それはミドルウェアの
// 領域（Phase 1）。**ドレイン超過は起こりうる前提**で、起きたことが
// run の戻り値とログに出る形にしてある。
//
// 値そのものは #2 で決めたリクエスト側の契約。Cloud Run の猶予から
// 逆算して狭めない（依存の向きが逆になる）。
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
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

	srv := newServer(h.Routes())

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
//	│      │                        │
//	│      │◄─ SIGTERM 到着 ────────┼─ ctx.Done() が閉じる
//	│      │   stop() で 2 回目を通す │
//	│      │                        │
//	│      └◄─ accept が死んだ ──────┼─ errCh に実エラーが来る
//	│      ▼                        │   （こちらでもドレインはする）
//	├─ srv.Shutdown(shutdownCtx)    │
//	│    新規接続を止め、処理中を     │
//	│    捌き切るまで待つ（最大 8 秒） ┤ Serve が ErrServerClosed を返し、
//	│    超過したら srv.Close()      ✗ errCh に置かれて goroutine 終了
//	│                                  （バッファ 1 なので送信は詰まらない）
//	├─ 未受信なら <-errCh で拾う
//	│
//	├─ errors.Join(serve, shutdown) ← 独立した 2 つの事実なので両方返す
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
	// バッファ 1 にするのは、送信側を待たせないため。バッファ 0 だと
	// 受信が来るまで送信側の goroutine が止まり続ける。今の形では必ず
	// 受信するが、経路が増えたときに気づかず goroutine を残さないための保険。
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
	// serveErr = <-errCh は「errCh から取り出して serveErr に入れる」。
	// 矢印の向きが受信を表す。ここで := を使うと case の中に別の変数ができて
	// 外側の serveErr が nil のままになるので、= でなければならない。
	//
	// Shutdown を呼ぶ前に accept ループが死んだ = 異常。ただし受理済みの
	// リクエストは無関係に生きているので、ここでも捌き切ってから返す。
	case serveErr = <-errCh:
		served = true

	// ctx.Done() はキャンセル時に閉じられる channel を返す。閉じた channel からの
	// 受信は即座に成功するので、これは「キャンセルされるまで待つ」という意味になる。
	// 値に意味は無いので受け取り先を書かない。本体が空なのは意図的で、
	// 「シグナルが来た」という事実さえ取れれば十分だから。
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
		// select は ready な case を無作為に選ぶ（Go の仕様）。errCh と ctx.Done()
		// が同時に ready なら ctx.Done() が選ばれることがあり、その裏で Serve が
		// 実エラーで落ちていると値が errCh に残ったままになる。ここで拾い直す。
		//
		// この受信は少し待つことがある。Shutdown は listenerGroup.Wait() で
		// Serve の終了を待つが、net/http は trackListener の解除を l.Close() より
		// 後に defer 登録するため、LIFO で Wait() が先に解けて errCh への送信が
		// まだ済んでいないことがある。待ちは有界なので固まりはしない。
		//
		// なお、この分岐に対する決定的なテストは書けない。「errCh と ctx.Done()
		// が同時に ready」という状態を狙って作れず（select は片方が ready に
		// なった瞬間に起きる）、Shutdown 後に失敗させても net/http が Accept の
		// エラーを一律 ErrServerClosed に置き換えるため実エラーを観測できない。
		serveErr = <-errCh
	}

	// accept ループが死んだこととドレインが超過したことは独立した事実なので、
	// 優先順位を付けて片方を捨てず両方返す。errors.Join は全部 nil なら nil。
	return errors.Join(serveError(serveErr), shutdownErr)
}

// newServer はタイムアウトの定数を配線した http.Server を返す。
// main から切り出してあるのは、配線をテストで確かめられるようにするため
// （TestNewServerUsesTimeoutBudget）。
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
