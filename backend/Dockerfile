# syntax=docker/dockerfile:1

# ベースイメージは digest で固定する。タグは人間が読むためのもので、実際に引かれるのは
# @sha256: の方。golang:1.26 のようなタグは日々中身が入れ替わるため、タグだけだと
# 「昨日通ったビルドが今日落ちる」が起きる。
#
# 更新するとき:  docker buildx imagetools inspect golang:1.26.6-trixie
#
# ARG に置くのは、2 つのイメージ参照と更新手順をここ 1 か所に集めるため。
# ただし FROM 行を読むタイプのツール（Dependabot / docker scout）からは
# pin が見えなくなる。このリポジトリはまだそれらを使っていないので ARG を採る。
# 導入するときに FROM 直書きへ戻すか、ARG を追える設定を入れるかを判断する。
ARG GO_IMAGE=golang:1.26.6-trixie@sha256:ab563819a16cfe5faff0f96a8bb598fbb0e400ab2ac751996e60abcb23b106a3
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

# --platform=$BUILDPLATFORM でビルドステージをホスト側に固定する。
# 付けないと BuildKit は TARGETPLATFORM に合わせて golang イメージごと amd64 を引き、
# Go コンパイラ自体が QEMU エミュレーションで走る。
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build

# BuildKit が自動で入れる予約 ARG。宣言しないと空文字になる。
ARG TARGETOS
ARG TARGETARCH

# 既定の auto は、go.mod がこのイメージより新しい Go を要求すると
# ビルド中に黙って別のツールチェーンを落としてくる。local ならその場で落ちる。
ENV GOTOOLCHAIN=local

WORKDIR /src

# 依存だけ先に入れてレイヤキャッシュを効かせる。
#
# go.su[m] は glob。このリポジトリはまだ外部依存ゼロで go.sum が存在せず、
# `COPY go.mod go.sum ./` と書くと not found でビルドが落ちる。glob なら
# 「あればコピー、無ければ何もしない」になり、Phase 1 で pgx を入れた瞬間に
# 自動で拾われる。直し忘れが起きない。
COPY go.mod go.su[m] ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# GOCACHE はアーキごとに分けない。Go のビルドキャッシュは内容アドレス方式で、
# GOOS/GOARCH は各エントリのキーに入っている。amd64 と arm64 の成果物は
# 1 つのキャッシュに共存でき、破棄は使われなくなってからの経過時間で決まる。
#
# -trimpath        バイナリから /src などの絶対パスを消す。ビルド環境が変わっても
#                  同じ入力なら同じ出力になる
# -ldflags="-s -w" シンボルテーブルと DWARF を落とす。8.85MB -> 6.10MB（実測）。
#                  panic のスタックトレースは pclntab 由来なので残る。失うのは
#                  dlv でのアタッチだけで、distroless には dlv もシェルも無い
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/api

FROM ${RUNTIME_IMAGE}

COPY --from=build /out/app /app

# :nonroot タグは既に User=65532 だが明示する。名前ではなく数値で書くのは、
# /etc/passwd を読めない場面（Kubernetes の runAsNonRoot 検査など）でも
# 非 root と判定できるようにするため。
USER 65532:65532

# EXPOSE は書かない。PORT は実行時に決まるので 8080 と書くと嘘になる。
# cmd/api の newServer が http.Server.Addr を持たないのと同じ理由。

# exec 形式。シェル形式にするとシェルが PID 1 になり SIGTERM がアプリに届かず、
# #3 のグレースフルシャットダウンが丸ごと無意味になる（docs/DESIGN.md §9）。
ENTRYPOINT ["/app"]
