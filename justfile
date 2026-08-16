# 引数なしで実行したらレシピ一覧を出す
default:
    @just --list

# go.mod は backend/ にあるが justfile はルートに置くので、
# working-directory 属性で寄せる（just 1.38.0 以降）。
# `cd backend &&` を各行に書かないためのもの。

# `go run` を使わずビルドしたバイナリを exec するのは、シグナルを届かせるため。
#
# `go run` はビルドしたバイナリを別プロセスとして起動し、SIGTERM を転送しない
# （このリポジトリで実測）。just -> go run -> バイナリ の 3 段になり、
# fish の `kill -TERM %1` は先頭プロセス（just）の PID にしか送らないので、
# バイナリは何も受け取らない。bash / zsh の `kill %1` はジョブのプロセスグループ
# 全体に送るので届くが、この環境は fish。
#
# さらにどのシェルでも、just / go run が先に死ぬとバイナリは孤児（PPID 1）として
# 生き残ってポートを掴み続け、次の起動が「address already in use」で失敗する。
#
# exec でバイナリに置き換えれば just -> バイナリ の 2 段になり、
# just がシグナルを子に伝えるので `kill -TERM %1` が効く。
# 本番の Cloud Run は ENTRYPOINT ["/app"] でバイナリが PID 1 なので、
# この中継の問題はそもそも存在しない。
#
# グレースフルシャットダウン（docs/DESIGN.md §9）の手動確認:
#
#   just dev &
#   sleep 1
#   curl -s localhost:8080/healthz
#   kill -TERM %1
#
# 期待する出力:
#
#   2026/08/16 14:57:56 INFO starting server addr=[::]:8080
#   {"status":"ok"}
#   2026/08/16 14:57:56 INFO shutting down cause="terminated signal received"
#   error: interrupted by SIGTERM
#
# 最後の 1 行は just 自身が「シグナルで終わった」と報告しているだけで、
# アプリのエラーログではない。受け入れ条件で見るのは INFO / ERROR の行。

# 開発用に起動する
[working-directory('backend')]
dev:
    mkdir -p ../.gobin && go build -o ../.gobin/api ./cmd/api && exec ../.gobin/api

# -race を既定にする。cmd/api の run() は goroutine + channel + signal +
# Shutdown の組み合わせで、テストも実 TCP を張って複数 goroutine で走るため、
# データ競合を検出できないまま緑になる状態を残したくない。
# CI もこのコマンドを使う（CONTRIBUTING.md §7）。

# テストを実行する（-race 付き）
[working-directory('backend')]
test:
    go test -race ./...

# 静的解析をかける
[working-directory('backend')]
lint:
    golangci-lint run

# フォーマットをかける
[working-directory('backend')]
fmt:
    golangci-lint fmt
