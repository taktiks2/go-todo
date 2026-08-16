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

# ローカルのイメージ名。Artifact Registry のパスは #5 で決める。
image := "go-todo"

# コンテナ名はイメージ名と別に持つ。#5 で image が
# `asia-northeast1-docker.pkg.dev/.../api` のようなパスになると、
# `/` を含む名前は docker run --name が受け付けないため。
container := "go-todo"

# 本番と同じ linux/amd64 のイメージを作る。
#
# Cloud Run は x86_64 しか受け付けない（container runtime contract）。
# --platform を付けないと Apple Silicon では arm64 イメージができ、
# ローカルでは動くのに Cloud Run で exec format error になる。
#
# --platform が指すのは「成果物のアーキ」であって「ビルドの走り方」ではない。
# Dockerfile が FROM --platform=$BUILDPLATFORM でビルドステージをホストに
# 固定しているので、Go のコンパイラは arm64 ネイティブで走る。
#
# 最後の 1 行で Architecture も出すのは、arm64 を作ってしまう事故がローカルでは
# 一切症状を出さず、#7 の Cloud Run で初めて exec format error になるため。
# サイズより先にこちらを見る。
#
# サイズは 10^6 で割る。docker images の表示も 10 進なので数値が一致する
# （2^20 で割ると 7.9 と出て、docker images の 8.31MB と食い違って読み手が混乱する）。
# docker images --format を使わないのは、Go テンプレートの二重波括弧が
# just の補間構文と衝突するため。jq は flake.nix にある。

# コンテナイメージをビルドする（linux/amd64）
[working-directory('backend')]
docker-build:
    docker build --platform linux/amd64 -t {{image}} .
    @docker image inspect {{image}} | jq -r '"arch: \(.[0].Architecture)  size: \((.[0].Size / 100000 | round) / 10) MB"'

# PORT には既定の 8080 ではない値を渡す。8080 のまま検証すると、アプリが PORT を
# 無視して 8080 をハードコードしていても通ってしまい、issue #4 の完了条件
# 「コンテナ内で PORT 環境変数が効く」の検証にならない。
# ホスト側は 8080 に固定するので、確認は素直な curl でよい:
#
#   just docker-run &
#   curl -s localhost:8080/healthz | jq
#
# --platform をここでも書くのは、amd64 イメージを arm64 ホストで動かすのが
# 暗黙のエミュレーション頼みだから。省くと Docker が毎回警告を出すうえ、
# binfmt が入っていない素の arm64 Docker Engine では exec format error になる。
#
# docker-build に依存させて、常に「今ビルドしたもの」を動かす。
#
# 停止は別シェルから `docker stop go-todo`。docker stop の既定猶予は 10 秒で、
# Cloud Run の SIGTERM -> SIGKILL の猶予とちょうど同じなので、
# 本番のシャットダウン契約をそのまま手元で再現できる。
#
# working-directory は付けない。docker run は作業ディレクトリを一切読まず、
# イメージを daemon から解決するため、付けると嘘の依存を示すことになる。

# コンテナを起動する（コンテナ内は PORT、ホストは 8080）
docker-run port="9090": docker-build
    docker run --rm --platform linux/amd64 --name {{container}} -e PORT={{port}} -p 8080:{{port}} {{image}}
