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
//
// この関数の形は「srv.Serve(ln) は戻ってこない」という 1 点から決まっている。
// Serve を直に呼ぶとそこで流れが止まり、シグナルを待つ余地が無くなる。
// 「リクエストを捌き続ける」と「シグナルを待つ」を同時にやるために流れを 2 本にする。
//
//	main の流れ                       goroutine の流れ
//	│
//	├─ NotifyContext ─► SIGTERM の横取りを開始（stop で解除できる）
//	│
//	├─ go func() ──────────────────┐  ← ここで流れが 2 本になる
//	│                              │
//	├─ slog.Info("server started") │  srv.Serve(ln) の中で
//	│                              │  リクエストを捌き続ける
//	├─ select { どちらが先に来る？ } │
//	│      │                       │
//	│      │◄─ SIGTERM 到着 ───────┼─ ctx.Done() が閉じる
//	│      ▼                       │
//	├─ stop()  2 回目は即死させる    │
//	│                              │
//	├─ srv.Shutdown(shutdownCtx)   │
//	│    新規接続を止め、処理中を    │
//	│    待つ（最大 8 秒）──────────┤ Serve が即 ErrServerClosed を返し、
//	│                              │ errCh に置かれる（バッファ 1 なので
//	│                              │ 誰も受け取らなくても goroutine は終わる）
//	├─ return nil                  ✗ goroutine 終了
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
	// バッファ 1 が効くのは正常終了のとき。Shutdown を呼ぶと Serve は即座に
	// ErrServerClosed を返すが、そのとき run はもう select を抜けていて誰も
	// 受け取らない。バッファ 0 だとこの goroutine は送信待ちのまま永久に残る
	// （= goroutine リーク）。
	errCh := make(chan error, 1)

	// go を付けて呼ぶと別の goroutine で走り始め、呼んだ側は待たずに次の行へ進む。
	// go は関数呼び出ししか受け取れないので、無名関数を定義してその場で呼ぶ形になる。
	// 中では srv.Serve(ln) がサーバの寿命ぶん止まり、戻ってきて初めて errCh <- が動く。
	go func() { errCh <- srv.Serve(ln) }()

	slog.Info("server started", "addr", ln.Addr().String())

	// select は複数の channel 操作を並べ、最初に準備できた 1 つだけを実行する。
	// switch が「値を比べる」のに対し、select は「channel が動くのを待つ」。
	// ここで待っている 2 つは、どちらが先に来るか事前には分からない。
	select {
	// err := <-errCh は「errCh から取り出して err に入れる」。矢印の向きが受信を表す。
	// Shutdown を呼ぶ前に Serve が戻った = 異常（bind 済みの listener が閉じたなど）。
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serve: %w", err)

	// ctx.Done() はキャンセル時に閉じられる channel を返す。閉じた channel からの
	// 受信は即座に成功するので、これは「キャンセルされるまで待つ」という意味になる。
	// 値に意味は無いので受け取り先を書かない。本体が空なのは意図的で、
	// 「シグナルが来た」という事実さえ取れれば十分だから。
	case <-ctx.Done():
	}

	slog.Info("shutting down", "cause", context.Cause(ctx))

	// ここで stop() を呼ぶと既定動作に戻り、2 回目の SIGTERM / Ctrl-C で即死する。
	// 呼ばないと 2 回目以降も横取りされ続け、ドレインが詰まったとき運用者が
	// もう一度押しても何も起きない。CancelFunc は冪等なので defer と二重でよい。
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
