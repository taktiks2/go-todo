# 引数なしで実行したらレシピ一覧を出す
default:
    @just --list

# go.mod は backend/ にあるが justfile はルートに置くので、
# working-directory 属性で寄せる（just 1.38.0 以降）。
# `cd backend &&` を各行に書かないためのもの。

# 開発用に起動する。ホットな編集ループ向け
[working-directory('backend')]
dev:
    go run ./cmd/api

# `go run` はビルドしたバイナリを別プロセスとして起動し、SIGTERM を転送しない
# （このリポジトリで実測）。構成は just -> go run -> バイナリ の 3 段になる。
#
# この 3 段が問題になるかはシェルによる。bash / zsh の `kill %1` はジョブの
# プロセスグループ全体に送るのでバイナリにも届くが、fish の `%1` は先頭
# プロセス（just）の PID にしか送らないため、go run が転送しない以上
# バイナリは何も受け取らない。この環境は fish なので後者になる。
#
# どちらのシェルでも、just / go run が先に死ぬとバイナリは孤児（PPID 1）として
# 生き残り、ポートを掴み続ける。次の起動が「address already in use」で失敗する。
#
# そのため、グレースフルシャットダウン（docs/DESIGN.md §9）を手で確かめるときは
# go run を挟まないこのレシピを使う。just -> バイナリ の 2 段になり、just は
# go run と違って子プロセスにシグナルを伝えるので、fish でもバイナリまで届く。
# 本番の Cloud Run は ENTRYPOINT ["/app"] でバイナリが PID 1 なので、
# この中継の問題はそもそも存在しない。
#
#   just serve &
#   sleep 1
#   curl -s localhost:8080/healthz
#   kill -TERM %1
#
# 期待する出力:
#
#   2026/08/16 14:09:08 INFO server started addr=[::]:8080
#   {"status":"ok"}
#   2026/08/16 14:09:08 INFO shutting down cause="terminated signal received"
#   error: interrupted by SIGTERM
#
# 最後の 1 行は just 自身が「シグナルで終わった」と報告しているだけで、
# アプリのエラーログではない。受け入れ条件で見るのは INFO/ERROR の行。

# バイナリをビルドして直接起動する（kill -TERM が届く）
[working-directory('backend')]
serve:
    mkdir -p ../.gobin && go build -o ../.gobin/api ./cmd/api && ../.gobin/api

# -race を既定にする。cmd/api の run() は goroutine + channel + signal +
# Shutdown の組み合わせで、テストも実 TCP を張って複数 goroutine で走るため、
# データ競合を検出できないまま緑になる状態を残したくない。
# CI（CONTRIBUTING.md §7）も同じコマンドを使う。

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
