# Dockerfile（マルチステージ + distroless）

- issue: #4（`phase:0` / `area:infra` / `mode:ai-only`）
- 日付: 2026-08-16
- 関連: `docs/DESIGN.md` §9 コンテナ / グレースフルシャットダウン / Cloud Run 設定、§10 ローカル環境、§12 Phase 0、§14 着手時に決めること
- 前提: `CONTRIBUTING.md` §1 役割分担、§6 品質ゲート、§8 ドキュメントの役割
- 申し送り元: `docs/superpowers/specs/2026-08-15-graceful-shutdown-design.md` の「#4（Dockerfile）への申し送り」
- 環境: go1.26.5（nix devShell 実測）/ docker CLI 29.4.1 / Docker Desktop

> **注記（実装後に追記）:** この spec は着手時点の設計である。実装後の `/code-review` で
> いくつかの判断と数値が覆った。**確定した形は `backend/Dockerfile` / `backend/.dockerignore` /
> `justfile` / `docs/DESIGN.md` §9 を見ること。** 以下は初版のまま残してある。
>
> | 初版の記述 | 実際 |
> |---|---|
> | cache mount の `id` に `TARGETARCH` を混ぜる（「GOCACHE はアーキごとに別物で共有すると毎回捨てられる」） | **誤り。** Go のビルドキャッシュは内容アドレス方式で GOOS/GOARCH がキーに入るため共存できる。`id` は外した |
> | `.dockerignore` 許可リストの狙いに「認証情報の混入防止」 | **誇張。** `!internal` はサブツリーごと戻すし、最終イメージにはバイナリしか入らない。実際の狙いは転送量とレイヤキャッシュの安定 |
> | Artifact Registry は「約 8MB × デプロイ回数で 60 回前後」 | **モデルが誤り。** レジストリは gzip 後のレイヤを digest で重複排除する。実測でアプリ層 2.54MB/デプロイ、base 0.77MB は 1 回だけ。190 回前後 |
> | サイズ表記（8.44MB / 5.81MB / 7.92MB） | すべて MiB だった。`docker images` に合わせて 10 進 MB に統一（8.85MB / 6.10MB / 8.31MB） |
> | `docker run --name {{image}}` | コンテナ名は別変数 `container` にした。#5 で `image` が `/` を含むレジストリパスになると `--name` が受け付けない |
> | `docker-run` に `[working-directory('backend')]` | `docker run` は作業ディレクトリを読まないため外した |
> | `.dockerignore` の許可リスト | `!db` を追加。`docs/DESIGN.md` §3 の構成にあり、Phase 2 で `go:embed` する可能性がある |
>
> `ARG GO_IMAGE` / `ARG RUNTIME_IMAGE` は「未使用の間接参照で `FROM` を読むスキャナから
> pin が見えない」と指摘されたが、**残す判断をした**（このリポジトリは Dependabot / Renovate を
> 使っておらず、参照と更新手順が先頭にまとまる利点を採った）。導入時に再判断する。

## 目的

`CGO_ENABLED=0` の静的リンクバイナリを distroless に載せ、Cloud Run に投げられるイメージを作る。

Cloud Run はリクエスト受信後にコンテナを起動するため、**イメージサイズがそのままコールドスタート時間に効く。**
`docs/DESIGN.md` §12 は Phase 0 の成果物に Dockerfile を挙げており、#5（Artifact Registry）と
#7（Cloud Run デプロイ）はこの issue が決めるイメージ契約の上に乗る。

**この issue の本題は「小さいイメージを作ること」ではない。** 実測では素のビルドでも約 10MB で、
完了条件の 30MB には最初から大きな余裕がある。本題は **Apple Silicon（arm64）で書いて
Cloud Run（amd64 必須）に載せるという非対称をどう扱うか**であり、ここを外すと
「ローカルでは動くのに #7 で `exec format error`」という、Phase 0 の最後まで気づけない事故になる。

## 決定事項

| 項目 | 決定 | 理由 |
|---|---|---|
| **充実度** | 実務水準のフル構成（cache mount / `-trimpath -ldflags` / digest ピン / `ARG` 管理） | 学習プロジェクトとして、実務でそのまま使える形を一度書く。`docs/DESIGN.md` §9 の 10 行は骨子として別途残す |
| **ビルド戦略** | `FROM --platform=$BUILDPLATFORM` でビルドステージをホストに固定し、`GOOS`/`GOARCH` でクロスコンパイル | ビルドステージが `TARGETPLATFORM` に従うと golang イメージごと amd64 が引かれ、**Go コンパイラ自体が QEMU で走る**。依存ゼロの今は数十秒で済むが、pgx / firebase-admin-go が入る Phase 1 以降で数倍悪化する。`CGO_ENABLED=0` なら Go のクロスコンパイルは完全に成立し、エミュレーションが一度も起きない。**これは `docs/DESIGN.md` §9 が「Ruby や Python では真似できない Go の武器」と書いた `CGO_ENABLED=0` の話の、もう半分にあたる** |
| **マルチアーキ manifest** | 作らない（amd64 単独） | Cloud Run は x86_64 のみで、#7 でも arm64 は使わない。Artifact Registry の無料枠 0.5GB（`docs/DESIGN.md` §14 リスク）を 2 倍の速さで食う。`docker images` のサイズ表示が manifest 単位になり完了条件の判定も濁る。YAGNI。ただし `TARGETOS`/`TARGETARCH` を使う形にしてあるので、必要になれば `buildx --platform` を足すだけで広がる |
| **`docker-build` の成果物** | **amd64 固定**（ホストネイティブの arm64 は作らない） | 完了条件の検証を本番と同一のバイナリに対して行う。Docker Desktop は Rosetta 2 で amd64 バイナリを実用速度で走らせるので、ローカル実行に支障がない |
| **ランタイムイメージ** | `gcr.io/distroless/static-debian13:nonroot` | **サフィックス無しの `static` は「現在は debian13 を指すが、将来次の Debian に黙って移る」**（distroless README）。実測では両者は同一 digest だが、Debian 14 が出た日に基盤 OS が勝手に変わる。明示すれば防げる |
| **ビルドイメージ** | `golang:1.26.6-trixie` | 静的リンクなのでディストリは実質無関係だが、ランタイムの `-debian13`（trixie）と揃えて読み手の混乱を減らす |
| **ピン留めの粒度** | `name:tag@sha256:...` の両方書き | digest だけだと何のイメージか読めない。タグだけだと中身が日々入れ替わり「昨日通ったビルドが今日落ちる」。Docker は両方書けて digest が優先される。更新は `docker buildx imagetools inspect <tag>` |
| **`go.sum` 不在の扱い** | `COPY go.mod go.su[m] ./`（glob） | このリポジトリはまだ外部依存ゼロで `go.sum` が存在せず、`docs/DESIGN.md` §9 の例そのまま（`COPY go.mod go.sum ./`）は **not found でビルドが落ちる**。glob なら「あればコピー、無ければ何もしない」になり、Phase 1 で pgx を入れた瞬間に自動で拾われる。`COPY go.mod ./` だけにすると、依存を足した日に静かに壊れて直し忘れる |
| **BuildKit cache mount** | 入れる。`/go/pkg/mod` と `/root/.cache/go-build` | 依存ゼロの現時点では効果が測れないが、**効果が出てから入れるものではない**（そのときには「なぜ遅いか」の調査から始まる）。`id` に `TARGETARCH` を混ぜる（GOCACHE の中身はアーキごとに別物で、共有すると毎回捨てられて意味がなくなる） |
| **ビルドフラグ** | `-trimpath -ldflags="-s -w"` | `-trimpath` はバイナリから `/src` などの絶対パスを消し、ビルド環境が変わっても同じ入力なら同じ出力になる。`-ldflags="-s -w"` はシンボルテーブルと DWARF を落として **8.44MB → 5.81MB**（実測）。**panic のスタックトレースは pclntab 由来なので残る。** 失うのは `dlv` でのアタッチだけで、distroless には `dlv` もシェルも無いので元から使えない |
| **`GOTOOLCHAIN`** | ビルドステージで `local` に固定 | 既定の `auto` は、`go.mod` がイメージより新しい Go を要求すると**ビルド中に黙って別のツールチェーンをダウンロードする**。`local` にすればその場でエラーになる。`docs/DESIGN.md` §10「ローカル環境」の「バージョンは各自の環境任せにしない」を、コメントではなくビルドの失敗として強制できる |
| **バイナリの置き場** | `/app` | `justfile:22` が既に「本番の Cloud Run は `ENTRYPOINT ["/app"]` でバイナリが PID 1」と書いている。既存の記述に合わせる |
| **`USER`** | `65532:65532`（数値） | `:nonroot` タグは既に `User=65532`（実測）なので冗長だが明示する。名前ではなく数値で書くのは、`/etc/passwd` を読めない場面（Kubernetes の `runAsNonRoot` 検査など）でも非 root と判定できるようにするため |
| **`EXPOSE`** | **書かない** | `PORT` は実行時に決まるので `EXPOSE 8080` と書くと嘘になる。Cloud Run は `EXPOSE` を見ない。`cmd/api/main.go:145` が `http.Server.Addr` を持たないのと同じ判断 |
| **`ENTRYPOINT`** | exec 形式 `["/app"]` | シェル形式にするとシェルが PID 1 になり SIGTERM がアプリに届かず、#3 の実装が丸ごと無意味になる（`docs/DESIGN.md` §9）。distroless にはそもそもシェルが無いので動きもしないが、**明示的な判断として残す** |
| **`.dockerignore`** | 許可リスト方式（`*` で全除外してから `!` で戻す） | 除外リスト方式だと `backend/` に新しく置かれたファイルが黙ってビルドコンテキストに入る。許可リストなら意図しないファイル（誤って置かれた認証情報など）が混入しない。Go のディレクトリ構成は `docs/DESIGN.md` §3 で固定されており維持コストがほぼゼロ |
| **`*_test.go` の除外** | 除外する | `go build ./cmd/api` は元から無視するが、`COPY . .` のレイヤキャッシュは壊す。pair-tdd でテストを触る頻度が高いこのリポジトリでは効果が大きい。イメージ内でテストを走らせる予定は無い（CI は `go test` を直接叩く。`CONTRIBUTING.md` §7） |
| **`docker-run` の `PORT`** | 既定 8080 とは**異なる** 9090 を使い、ホスト側を 8080 に固定 | issue の確認手順は `PORT=8080` で、これはアプリの既定値と同じ。**`PORT` を完全に無視する実装でも `curl` が通ってしまい、完了条件 3 の検証になっていない。** 9090 を渡してホスト 8080 にマップすれば、`curl localhost:8080/healthz` が通ること自体が `PORT` が効いている証拠になる |
| **サイズ表示** | `docker image inspect \| jq` | `docker images --format '{{.Size}}'` は Go テンプレートの `{{ }}` が **just の補間構文に食われる**（エスケープは `{{{{`）。`jq` は `flake.nix` に既にあるので、そちらに寄せて罠ごと避ける |
| **コンテナランタイム** | Docker Desktop | issue 本文と `docs/DESIGN.md` §14「着手時に決めること」は colima を前提にしているが、この環境に colima は入っておらず `/Applications/Docker.app` がある。**issue 本文と DESIGN.md の両方を現状に合わせて直す** |
| **自動テスト** | 無し。検証マトリクスの手動実行が品質ゲート | Dockerfile に単体テストの枠組みは無い。`CONTRIBUTING.md` §6 が「テストが緑でも実際は動かないケース」を最後の砦と呼んでいるのが、まさにこの形の作業 |

### バージョン揃えルールの改定

`docs/DESIGN.md` は「`flake.nix` の `go_1_26` / `go.mod` / Dockerfile の `golang:1.26` を揃える」と
2 箇所で定めている（§10「ローカル環境」の本文と、§14「着手時に決めること」の表）。
実際に揃えようとすると、供給元が 3 系統に分かれてパッチがズレる。

| 供給元 | 現在 |
|---|---|
| `flake.nix` の `go_1_26`（nixpkgs） | 1.26.5 |
| `backend/go.mod` の go directive | 1.26.5（下限） |
| Dockerfile の `golang:` | 1.26.6（最新安定版） |

`nix flake update` と Docker Hub は独立に動くので、**パッチまで人手で揃え続けるのは必ず腐る。**
しかも `docs/DESIGN.md` §9 は「go1.26.0 と go1.26.5 で `os/signal` の挙動が変わった」実例を
自ら記録しており、パッチ差が無害だと言い切れないことも書いている。

**ルールを「メジャー・マイナーを揃える。パッチの下限は `go.mod` の go directive が保証する」に改める。**
go directive は規律ではなく機械的に検証される下限であり、`GOTOOLCHAIN=local` と組み合わせれば
「イメージの Go が `go.mod` の要求を満たさない」状態がビルドの失敗として現れる。

## 成果物

```
go-todo/
├── backend/
│   ├── Dockerfile          # 新規
│   └── .dockerignore       # 新規
├── justfile                # docker-build / docker-run と image 変数を追加
└── docs/DESIGN.md          # §9 に差分注記、§10/§14 のバージョン揃えルールと
                            # §14 の Docker ランタイム行を改定
```

## `backend/Dockerfile`

```dockerfile
# syntax=docker/dockerfile:1

# ベースイメージは digest で固定する。タグは人間が読むためのもので、実際に引かれるのは
# @sha256: の方。golang:1.26 のようなタグは日々中身が入れ替わるため、タグだけだと
# 「昨日通ったビルドが今日落ちる」が起きる。
#
# 更新するとき:  docker buildx imagetools inspect golang:1.26.6-trixie
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

# ビルドキャッシュの id に TARGETARCH を混ぜる。GOCACHE の中身はアーキごとに
# 別物で、共有すると毎回捨てられてキャッシュの意味がなくなる。
#
# -trimpath        バイナリから /src などの絶対パスを消す。ビルド環境が変わっても
#                  同じ入力なら同じ出力になる
# -ldflags="-s -w" シンボルテーブルと DWARF を落とす。8.44MB -> 5.81MB（実測）。
#                  panic のスタックトレースは pclntab 由来なので残る。失うのは
#                  dlv でのアタッチだけで、distroless には dlv もシェルも無い
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build,id=go-build-${TARGETARCH} \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/api

FROM ${RUNTIME_IMAGE}

COPY --from=build /out/app /app

# :nonroot タグは既に User=65532 だが明示する。名前ではなく数値で書くのは、
# /etc/passwd を読めない場面（Kubernetes の runAsNonRoot 検査など）でも
# 非 root と判定できるようにするため。
USER 65532:65532

# EXPOSE は書かない。PORT は実行時に決まるので 8080 と書くと嘘になる。
# cmd/api/main.go:145 が http.Server.Addr を持たないのと同じ理由。

# exec 形式。シェル形式にするとシェルが PID 1 になり SIGTERM がアプリに届かず、
# #3 のグレースフルシャットダウンが丸ごと無意味になる（docs/DESIGN.md §9）。
ENTRYPOINT ["/app"]
```

## `backend/.dockerignore`

```
# 既定で全部除外し、必要なものだけ戻す。
#
# 除外リスト方式（.git や *.md を並べる）だと、backend/ に新しく置かれた
# ファイルが黙ってビルドコンテキストに入る。許可リストなら、意図しない
# ファイル（誤って置かれた認証情報など）が混入しない。
# Go のディレクトリ構成は docs/DESIGN.md §3 で決まっていて動かないので、
# 許可リストの維持コストはほぼゼロ。
*

!go.mod
!go.sum
!cmd
!internal

# テストはイメージに要らない。go build ./cmd/api は元から無視するが、
# COPY . . のレイヤキャッシュは壊す。pair-tdd でテストを触る頻度が高い
# このリポジトリでは、除外しておく価値が大きい。
**/*_test.go
```

## `justfile` の追加分

```just
# ローカルのイメージ名。Artifact Registry のパスは #5 で決める。
image := "go-todo"

# 本番と同じ linux/amd64 のイメージを作る。
#
# Cloud Run は x86_64 しか受け付けない（container runtime contract）。
# --platform を付けないと Apple Silicon では arm64 イメージができ、
# ローカルでは動くのに Cloud Run で exec format error になる。
#
# --platform が指すのは「成果物のアーキ」であって「ビルドの走り方」ではない。
# Dockerfile が FROM --platform=$BUILDPLATFORM でビルドステージをホストに
# 固定しているので、Go のコンパイラは arm64 ネイティブで走る。
[working-directory('backend')]
docker-build:
    docker build --platform linux/amd64 -t {{image}} .
    @docker image inspect {{image}} | jq -r '"image size: \((.[0].Size / 1048576 * 10 | round) / 10) MB"'

# コンテナで起動する。
#
# PORT には既定の 8080 ではない値を渡す。8080 のまま検証すると、アプリが PORT を
# 無視して 8080 をハードコードしていても通ってしまい、完了条件
# 「コンテナ内で PORT 環境変数が効く」の検証にならない。
# ホスト側は 8080 に固定するので、確認コマンドは issue のまま使える:
#
#   just docker-run &
#   curl -s localhost:8080/healthz | jq
#
# 停止は別シェルから `docker stop go-todo`。docker stop の既定猶予は 10 秒で、
# Cloud Run の SIGTERM -> SIGKILL の猶予とちょうど同じなので、
# 本番のシャットダウン契約をそのまま手元で再現できる。
[working-directory('backend')]
docker-run port="9090":
    docker run --rm --name {{image}} -e PORT={{port}} -p 8080:{{port}} {{image}}
```

## 検証マトリクス

完了条件（issue 記載の 4 つ）:

| # | 完了条件 | コマンド | 期待 |
|---|---|---|---|
| 1 | `docker build` が通る | `just docker-build` | exit 0 |
| 2 | イメージサイズが 30MB 以下 | 同上（末尾に自動表示） | 約 8MB |
| 3 | コンテナ内で `PORT` が効く | `just docker-run` → 別シェルで `curl -s localhost:8080/healthz \| jq` | `{"status":"ok"}`（コンテナ内は 9090 で listen） |
| 4 | 非 root ユーザーで実行されている | `docker image inspect go-todo \| jq -r '.[0].Config.User'` | `65532:65532` |

完了条件には無いが**必ず確認する** 2 つ:

| # | 何を防ぐか | コマンド | 期待 |
|---|---|---|---|
| 5 | **arm64 イメージを作る事故。** ローカルでは動くので #7 まで気づけない | `docker image inspect go-todo \| jq -r '.[0].Architecture'` | `amd64` |
| 6 | **exec 形式 `ENTRYPOINT` の実証。** #3 の成果がコンテナ内で生きているか | 別シェルで `docker stop go-todo` | `shutting down cause=...` が出て、10 秒待たずに終了する |

6 が失敗するときは、10 秒フルに待たされて何のログも出ずに落ちる。**「なんとなく遅い」としか見えない**のが厄介なので、明示的に見る。

## 実装中に実測で確かめること

設計時点では「動くはず」だが、daemon を起動して叩くまで確定にしない。

1. `COPY go.mod go.su[m] ./` が `go.sum` 不在でエラーにならないか
2. cache mount の `id=go-build-${TARGETARCH}` で ARG 展開が効くか
3. `docker-build` 末尾の `jq` フィルタの出力形
4. digest ピンした 2 イメージが実際に pull できるか

いずれも外れた場合はフォールバックがある（1 は `COPY go.mod ./` + コメント、2 は `id` を落として固定文字列、
3 は `jq` の式を単純化、4 はタグのみに戻す）。**設計の骨格は変わらない。**

## 進め方

`mode:ai-only` なので pair-tdd ループは回さず、Claude が全部書く（`CONTRIBUTING.md` §1）。

1. `gh issue develop 4 --name 4-dockerfile-distroless --checkout`
2. Docker Desktop を起動する（手動）
3. `backend/.dockerignore` → `backend/Dockerfile` → `justfile` の順に書く
4. 検証マトリクス 1〜6 を実行し、実測で確かめることの 1〜4 を潰す
5. 外れたものがあれば Dockerfile を直し、4 に戻る
6. `chore(infra): Dockerfile と .dockerignore を追加` / `chore(infra): justfile に docker-build と docker-run を追加`
7. `docs/DESIGN.md` を更新し `docs:` でコミット
8. issue #4 本文を現状に合わせて更新
9. `/code-review` を走らせる（`CONTRIBUTING.md` §6）
10. 検証マトリクスの実行結果を PR 本文に貼る

## `docs/DESIGN.md` への反映

`CONTRIBUTING.md` §8 に従い、同じ PR で直す。**§9 の 10 行の例は骨子として残し、実物との差分を注記する**
（例を実物と完全一致させると二重管理になる。DESIGN.md は「何を作るか」、実物は `backend/Dockerfile`）。

| 箇所 | 変更 |
|---|---|
| §9 コンテナのコード例（`:525`） | `gcr.io/distroless/static:nonroot` → `static-debian13:nonroot`。サフィックス無しは将来 Debian 14 に黙って移る旨を注記 |
| §9「結果イメージは約 20MB」（`:536`） | 実測値（約 8MB）に直す |
| §9 コード例の直後（`:529` の後） | **「実物との差分」注記を新設。** クロスコンパイル、BuildKit cache mount、digest ピン、`-trimpath -ldflags`、`GOTOOLCHAIN=local`、`.dockerignore` 許可リスト、`EXPOSE` を書かない理由 |
| §10「ローカル環境」の本文（`:645`）/ §14「着手時に決めること」の Go のバージョン行（`:772`） | 上記「バージョン揃えルールの改定」に沿って書き換える。`go.mod` の `go 1.26.0` という記述は実際には `1.26.5` で、#3 の時点で腐っていた |
| §10「ローカル環境」の colima 行（`:647`）/ §14「着手時に決めること」の「Docker ランタイム \| colima」行（`:770`） | Docker Desktop に直す。この環境に colima は入っていない。`:647` は testcontainers-go が `DOCKER_HOST` を見る話も含むので、その部分は残す |
| §14 リスクの Artifact Registry 行（`:759`） | 「distroless イメージ約 20 MB × デプロイ回数で 30〜40 回」の見積もりを実測（約 8MB）で引き直す。**無料枠を使い切るまでのデプロイ回数が倍以上に伸びるので、リスクの評価自体が変わる** |

## issue #4 本文の更新

- 「手動でやること: colima の起動（`colima start`）」→ Docker Desktop の起動
- 確認手順の `-e PORT=8080` → 既定と異なる値を使う形（完了条件 3 が実際に検証される形）

## スコープ外

issue #4 の宣言どおり、以下は含まない。

- Artifact Registry への push（#5 / #7）
- Cloud Run へのデプロイ（#7）
- GitHub Actions（`.github/workflows/` はまだ存在しない。#7 の範囲）
- マルチアーキ manifest（arm64 は誰も要求していない）
- `slog` の Cloud Logging 整形（#13）

## 参照

- `docs/DESIGN.md` §9 コンテナ / グレースフルシャットダウン / Cloud Run 設定、§10 ローカル環境、§12 Phase 0、§14 リスクと未確定事項
- `CONTRIBUTING.md` §1 §4 §5 §6 §8
- `docs/superpowers/specs/2026-08-15-graceful-shutdown-design.md` — #4 への申し送り（exec 形式 `ENTRYPOINT`）
- [Cloud Run container runtime contract](https://docs.cloud.google.com/run/docs/container-contract) — x86_64 のみ、`PORT` 注入、`0.0.0.0` で listen、SIGTERM から 10 秒
- [GoogleContainerTools/distroless](https://github.com/GoogleContainerTools/distroless) — タグ体系とサフィックス無しタグの扱い
- [Dockerfile reference: automatic platform ARGs](https://docs.docker.com/reference/dockerfile/#automatic-platform-args-in-the-global-scope) — `BUILDPLATFORM` / `TARGETOS` / `TARGETARCH`
- [Dockerfile reference: RUN --mount=type=cache](https://docs.docker.com/reference/dockerfile/#run---mounttypecache)
- [Go: GOTOOLCHAIN](https://go.dev/doc/toolchain) — `auto` と `local` の違い
