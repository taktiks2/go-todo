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

# `go run` は SIGTERM を子プロセスに転送しない（このリポジトリで実測）。
# 構成は just -> go run -> バイナリ の 3 段で、シェルの `kill -TERM %1` は
# 先頭の just にしか届かず、go run も転送しないため、バイナリは何も受け取らない。
# さらに just / go run が死んでもバイナリは孤児（PPID 1）として生き残り、
# ポートを掴み続けるので、次の起動が「address already in use」で失敗する。
#
# そのため、グレースフルシャットダウン（docs/DESIGN.md §9）を手で確かめるときは
# go run を挟まないこのレシピを使う。just -> バイナリ の 2 段になり、just は
# go run と違って子プロセスにシグナルを伝えるので、バイナリまで届く。
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

# テストを実行する
[working-directory('backend')]
test:
    go test ./...

# 静的解析をかける
[working-directory('backend')]
lint:
    golangci-lint run

# フォーマットをかける
[working-directory('backend')]
fmt:
    golangci-lint fmt
