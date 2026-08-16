# Dockerfile（マルチステージ + distroless）実装計画

> **For agentic workers:** この計画は `mode:ai-only` の issue のものである。
> Go の実装コードは触らないが、`backend/Dockerfile` / `backend/.dockerignore` / `justfile` /
> `docs/DESIGN.md` は Claude が書いてよい（`CONTRIBUTING.md` §1）。
> 実装サブスキルは `superpowers:subagent-driven-development` か `superpowers:executing-plans`。
> ステップは checkbox (`- [ ]`) で追跡する。
>
> **この計画に自動テストは無い。** Dockerfile に単体テストの枠組みは存在しないため、
> 各 Task の「テスト」にあたるのは検証コマンドの実行と、その出力の目視確認である。
> **出力を見ずに次へ進んではならない。** `CONTRIBUTING.md` §6 が「テストが緑でも実際は
> 動かないケース」を最後の砦と呼んでいるのが、まさにこの形の作業。

**Goal:** `CGO_ENABLED=0` の静的リンクバイナリを distroless に載せ、Cloud Run に投げられる linux/amd64 イメージを作る。

**Architecture:** マルチステージ。ビルドステージを `FROM --platform=$BUILDPLATFORM` でホスト（arm64 Mac）に固定し、`GOOS`/`GOARCH` で linux/amd64 バイナリをクロスコンパイルする。エミュレーションを一度も挟まない。ランタイムは `gcr.io/distroless/static-debian13:nonroot`。両ベースイメージは `name:tag@sha256:...` で digest ピンする。

**Tech Stack:** Docker Desktop（CLI 29.4.1）/ BuildKit（cache mount、自動プラットフォーム ARG）/ golang:1.26.6-trixie / gcr.io/distroless/static-debian13:nonroot / just / jq

**Spec:** `docs/superpowers/specs/2026-08-16-dockerfile-distroless-design.md`

## Global Constraints

- **`mode:ai-only`。** Claude が全部書く。pair-tdd ループは回さない（`CONTRIBUTING.md` §1）
- ブランチは **`4-dockerfile-distroless`**（作成済み・チェックアウト済み）。`main` に直接コミットしない
- コミットは Conventional Commits。scope は **`infra`**
- **`backend/**/*.go` を変更しない。** `backend/go.mod` も変更しない。この issue で Go のコードは 1 行も動かない
- ビルドイメージは **`golang:1.26.6-trixie@sha256:ab563819a16cfe5faff0f96a8bb598fbb0e400ab2ac751996e60abcb23b106a3`**
- ランタイムイメージは **`gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6`**
- バイナリの置き場は **`/app`**（`justfile:22` の既存コメントに合わせる）
- `ENTRYPOINT` は **exec 形式 `["/app"]`**。シェル形式にすると #3 のグレースフルシャットダウンが丸ごと無意味になる
- `EXPOSE` は **書かない**（`PORT` は実行時に決まるため嘘になる）
- 成果物は **amd64 単独**。マルチアーキ manifest は作らない
- ローカルのイメージ名は **`go-todo`**
- スコープ外: Artifact Registry への push（#5 / #7）/ Cloud Run へのデプロイ（#7）/ GitHub Actions（#7）/ マルチアーキ manifest / `slog` の Cloud Logging 整形（#13）

## File Structure

| ファイル | 責務 |
|---|---|
| `backend/.dockerignore` | **新規。** 許可リスト方式でビルドコンテキストを絞る |
| `backend/Dockerfile` | **新規。** マルチステージ。ビルドはホスト固定でクロスコンパイル、ランタイムは distroless |
| `justfile` | **修正。** `image` 変数と `docker-build` / `docker-run` の 2 レシピを追加 |
| `docs/DESIGN.md` | **修正。** §9 に「実物との差分」注記、§10 / §14 のバージョン揃えルール・Docker ランタイム・イメージサイズ見積もりを改定 |

## 事前確認

- [ ] **Docker Desktop が起動している**

Run: `docker info > /dev/null && echo ok`
Expected: `ok`（`Cannot connect to the Docker daemon` なら `/Applications/Docker.app` を起動してから再実行）

---

## Task 1: `.dockerignore` と `Dockerfile` を作り、amd64 イメージをビルドする

**Files:**
- Create: `backend/.dockerignore`
- Create: `backend/Dockerfile`

**Interfaces:**
- Consumes: `backend/cmd/api`（既存。`main` パッケージ）、`backend/go.mod`（module `github.com/taktiks2/go-todo/backend`）
- Produces: ローカルイメージ `go-todo`（linux/amd64、`ENTRYPOINT ["/app"]`、`User=65532:65532`）。Task 2 の `justfile` レシピと Task 4 の受け入れ確認がこれに依存する

- [ ] **Step 1: `backend/.dockerignore` を作る**

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

- [ ] **Step 2: `backend/Dockerfile` を作る**

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

- [ ] **Step 3: ビルドする**

Run: `docker build --platform linux/amd64 -t go-todo backend/`
Expected: 最終行が `naming to docker.io/library/go-todo` で exit 0

失敗したら、下の「想定される失敗とフォールバック」を見て直し、このステップからやり直す。

- [ ] **Step 4: イメージサイズを確認する（完了条件 2）**

Run: `docker image inspect go-todo | jq -r '.[0].Size / 1048576 | floor'`
Expected: **30 未満**（設計時の見積もりは約 8）

- [ ] **Step 5: アーキテクチャを確認する**

Run: `docker image inspect go-todo | jq -r '.[0].Architecture, .[0].Os'`
Expected:
```
amd64
linux
```

**`arm64` が出たら止まる。** `--platform linux/amd64` が効いていないか、`FROM --platform=$BUILDPLATFORM` の位置が間違っている。ローカルでは動いてしまうため、ここで捕まえないと #7 まで気づけない。

- [ ] **Step 6: 実行ユーザーと ENTRYPOINT を確認する（完了条件 4）**

Run: `docker image inspect go-todo | jq -r '.[0].Config.User, (.[0].Config.Entrypoint | tostring), (.[0].Config.ExposedPorts | tostring)'`
Expected:
```
65532:65532
["/app"]
null
```

`Entrypoint` が `["/bin/sh","-c","/app"]` のような形になっていたらシェル形式になっている。exec 形式に直す。

- [ ] **Step 7: 2 回目のビルドでキャッシュが効くことを確認する**

Run: `docker build --platform linux/amd64 -t go-todo backend/ 2>&1 | grep -c CACHED`
Expected: **1 以上**（`go mod download` と `go build` のレイヤがキャッシュから返る）

- [ ] **Step 8: コミット**

```bash
git add backend/Dockerfile backend/.dockerignore
git commit -m "chore(infra): Dockerfile と .dockerignore を追加

マルチステージ + distroless。ビルドステージを BUILDPLATFORM で
ホストに固定し、GOOS/GOARCH でクロスコンパイルする。Cloud Run は
x86_64 のみで、エミュレーションを挟むと Phase 1 以降で効いてくる。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

### 想定される失敗とフォールバック

| 症状 | 原因 | 対処 |
|---|---|---|
| `failed to compute cache key: "/go.sum": not found` | `COPY` の glob がこの BuildKit で任意扱いにならない | `COPY go.mod go.su[m] ./` を `COPY go.mod ./` に変える。**そのうえで「Phase 1 で外部依存を入れたら `COPY go.mod go.sum ./` に直すこと」というコメントを必ず添える**（直し忘れると `go build` が `missing go.sum entry` で落ちる） |
| `dockerfile parse error` で `id=` の行を指す | cache mount の `id` で ARG 展開が使えない | `,id=go-build-${TARGETARCH}` を丸ごと削る。**今は amd64 しかビルドしないので実害は無い**（アーキ分離が将来効かなくなるだけ）。削った旨をコメントに残す |
| `manifest unknown` / `not found` で digest を指す | digest が古い、またはタイポ | `docker buildx imagetools inspect golang:1.26.6-trixie` と `... gcr.io/distroless/static-debian13:nonroot` で現在の digest を取り直して差し替える |
| `go.mod requires go >= X (running go Y; GOTOOLCHAIN=local)` | イメージの Go が `go.mod` の要求を満たさない | **これは `GOTOOLCHAIN=local` が意図どおり働いた状態。** `GO_IMAGE` を `go.mod` の go directive 以上のパッチに上げる |
| `exec format error`（Task 2 の実行時に出る） | arm64 イメージができている | Step 5 に戻る |

---

## Task 2: `justfile` にレシピを足し、実行時の完了条件を検証する

**Files:**
- Modify: `justfile`（`default` レシピの直後に `image` 変数、ファイル末尾に 2 レシピ）

**Interfaces:**
- Consumes: Task 1 が作ったイメージ `go-todo`
- Produces: `just docker-build` / `just docker-run [port]`。Task 4 の受け入れ確認がこれを使う

- [ ] **Step 1: `justfile:3`（`@just --list`）の直後に変数を足す**

```just
# ローカルのイメージ名。Artifact Registry のパスは #5 で決める。
image := "go-todo"
```

- [ ] **Step 2: `justfile` の末尾（`fmt` レシピの後）に 2 レシピを足す**

```just
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
# サイズ表示に docker の --format を使わないのは、Go テンプレートの {{ }} が
# just の補間構文に食われるため（エスケープは {{{{ }}）。jq は flake.nix にある。
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

- [ ] **Step 3: レシピが登録されたことを確認する**

Run: `just --list`
Expected: `docker-build` と `docker-run` が並ぶ（`docker-run` には `port` 引数が付く）

- [ ] **Step 4: `just docker-build` を走らせる（完了条件 1・2）**

Run: `just docker-build`
Expected: 末尾に `image size: 8.x MB` の形の 1 行（数値は環境で多少ぶれる。**30 未満であればよい**）

`jq: error` が出たら式が壊れている。`jq -r '.[0].Size'` でバイト数を出すだけの形に落とし、
`docker-build` のコメントに「MB 換算は諦めてバイト表示にした」と残す。

- [ ] **Step 5: コンテナを起動する**

Run: `just docker-run` を**別のシェルで**実行したまま置く（`&` で背景に回してもよい）
Expected: `INFO starting server addr=[::]:9090` が出る

**`addr` が `:8080` になっていたら止まる。** `PORT` が読まれていない。

- [ ] **Step 6: `PORT` が効いていることを確認する（完了条件 3）**

Run: `curl -s localhost:8080/healthz | jq`
Expected:
```json
{
  "status": "ok"
}
```

ホスト 8080 → コンテナ 9090 のマッピングなので、**これが通ること自体がコンテナ内で 9090 を
listen している証拠**になる。`PORT` を無視していれば接続が拒否される。

- [ ] **Step 7: SIGTERM が PID 1 に届くことを確認する**

Run: `docker stop go-todo`（`just docker-run` を走らせているのとは別のシェルで）
Expected: `just docker-run` 側に `INFO shutting down cause="terminated signal received"` が出て、**10 秒待たずに**終了する

**10 秒フルに待たされて何のログも出ないなら `ENTRYPOINT` がシェル形式になっている。**
Task 1 Step 6 に戻る。この失敗は「なんとなく遅い」としか見えないので、必ずここで見る。

- [ ] **Step 8: コミット**

```bash
git add justfile
git commit -m "chore(infra): justfile に docker-build と docker-run を追加

docker-run の PORT は既定の 8080 ではなく 9090 を使う。8080 のまま
だと PORT を無視する実装でも curl が通ってしまい、完了条件の検証に
ならないため。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 3: `docs/DESIGN.md` を更新する

**Files:**
- Modify: `docs/DESIGN.md`（§9 コンテナ、§10 ローカル環境、§14 リスク / 着手時に決めること）

**Interfaces:**
- Consumes: Task 1・Task 2 の実測値（イメージサイズ、バイナリサイズ）
- Produces: 無し（ドキュメントのみ）

`CONTRIBUTING.md` §8 の「設計判断が変わったら、それを起こした PR の中で `docs/DESIGN.md` を直す」に従う。
**§9 の 10 行の例は骨子として残す。** 実物と完全一致させると二重管理になる。

- [ ] **Step 1: §9 のコード例のランタイムイメージを直す**

`docs/DESIGN.md:525` の

```dockerfile
FROM gcr.io/distroless/static:nonroot
```

を

```dockerfile
FROM gcr.io/distroless/static-debian13:nonroot
```

に変える。

- [ ] **Step 2: §9 の学習ポイントのイメージサイズを実測値に直す**

`docs/DESIGN.md:536` の

> - 結果イメージは約 20MB。**Cloud Run はリクエスト受信後にコンテナを起動するため、イメージサイズがそのままコールドスタート時間に効く**

を

> - 結果イメージは約 8MB（#4 で実測。distroless/static が約 2MB、バイナリが 5.81MB）。**Cloud Run はリクエスト受信後にコンテナを起動するため、イメージサイズがそのままコールドスタート時間に効く**

に変える。

- [ ] **Step 3: §9 の学習ポイントの箇条書きの直後に「実物との差分」を新設する**

```markdown
**実物との差分（#4 で確定）**

上の 10 行は骨格で、`backend/Dockerfile` はここから実務水準に肉付けしてある。増やした分と理由:

| 追加 | 理由 |
|---|---|
| `FROM --platform=$BUILDPLATFORM` + `GOOS`/`GOARCH` でクロスコンパイル | 付けないとビルドステージごと amd64 が引かれ、**Go コンパイラ自体が QEMU で走る**。Apple Silicon で書いて Cloud Run（x86_64 のみ）に載せる以上、避けて通れない。`CGO_ENABLED=0` だからこそ成立する |
| ベースイメージの digest ピン（`name:tag@sha256:...`） | タグは中身が入れ替わる。タグと digest を両方書けば、読めて、かつ再現する |
| `static-debian13` とサフィックスを明示 | サフィックス無しの `static` は現在 debian13 を指すが、**将来次の Debian に黙って移る** |
| BuildKit cache mount（`/go/pkg/mod` と `/root/.cache/go-build`） | 依存が増える Phase 1 以降で効く。効果が出てから入れると「なぜ遅いか」の調査から始まることになる |
| `ENV GOTOOLCHAIN=local` | 既定の `auto` は `go.mod` がイメージより新しい Go を要求すると**黙って別のツールチェーンを落とす**。`local` ならその場で落ちる |
| `-trimpath -ldflags="-s -w"` | ビルドパスを消して再現性を上げ、8.44MB → 5.81MB（実測）。panic のスタックトレースは pclntab 由来なので残り、失うのは `dlv` でのアタッチだけ |
| `COPY go.mod go.su[m] ./`（glob） | 外部依存ゼロの間は `go.sum` が存在せず、上の例の `COPY go.mod go.sum ./` はそのままでは落ちる |
| `backend/.dockerignore`（許可リスト方式） | `*` で全除外してから戻す。`backend/` に置かれた意図しないファイルがビルドコンテキストに入らない |
| `EXPOSE` を**書かない** | `PORT` は実行時に決まるので `EXPOSE 8080` は嘘になる。Cloud Run は `EXPOSE` を見ない |
```

- [ ] **Step 4: §10「ローカル環境」のバージョン揃えルールを改定する**

`docs/DESIGN.md:645` の段落を、次の 2 段落に差し替える。

```markdown
**ただしバージョンは各自の環境任せにしない。** 上の判断はビルドとファイル同期の速度の話であって、ツールをどこから持ってくるかとは別問題。`flake.nix` は `pkgs.go`（常に最新安定版）ではなく `go_1_26` を指しており、`nix flake update` を打ってもマイナーは動かない。`pkgs.go` にすると、ある日 Go だけ 1.27 に上がって Dockerfile 側が取り残される。

**揃えるのはマイナーまで。パッチは供給元に任せ、下限を `go.mod` の go directive で保証する（#4 で改定）。** 供給元が nixpkgs（`flake.nix`）・Docker Hub（`Dockerfile`）・`go.mod` の 3 系統に分かれており、パッチまで人手で揃え続けると必ず腐る（2026-08 時点で nixpkgs は 1.26.5、Docker Hub の最新は 1.26.6）。go directive は機械的に検証される下限であり、`Dockerfile` の `ENV GOTOOLCHAIN=local` と組み合わせれば、「イメージの Go が `go.mod` の要求を満たさない」状態がビルドの失敗として現れる。
```

- [ ] **Step 5: §10 の Docker ランタイムの記述を直す**

`docs/DESIGN.md:647` の

> Docker ランタイムは colima を想定（testcontainers-go は `DOCKER_HOST` を見るため動作する）。

を

> Docker ランタイムは **Docker Desktop**（#4 で確定。colima を想定していたが未導入だった）。testcontainers-go は `DOCKER_HOST` を見るため、どちらでも動作する。

に変える。

- [ ] **Step 6: §14 リスクの Artifact Registry の見積もりを引き直す**

`docs/DESIGN.md:759` の

> - **Artifact Registry の無料枠は 0.5 GB/月**。distroless イメージ約 20 MB × デプロイ回数で、30〜40 回のデプロイで超える。超過は $0.10/GB/月 なので額は小さいが、#5 でリポジトリを作るときに cleanup policy を入れる

を

> - **Artifact Registry の無料枠は 0.5 GB/月**。distroless イメージ約 8 MB（#4 で実測）× デプロイ回数で、60 回前後のデプロイで超える。超過は $0.10/GB/月 なので額は小さいが、#5 でリポジトリを作るときに cleanup policy を入れる

に変える。

- [ ] **Step 7: §14「着手時に決めること」の 2 行を直す**

`docs/DESIGN.md:770` の

```
| Docker ランタイム | colima |
```

を

```
| Docker ランタイム | **Docker Desktop**（#4 で確定。colima は未導入だった） |
```

に、`docs/DESIGN.md:772` の

```
| Go のバージョン | **1.26**（#2 で確定）。`flake.nix` の `go_1_26` / `go.mod` の `go 1.26.0` / Dockerfile の `golang:1.26` を揃える |
```

を

```
| Go のバージョン | **1.26**（#2 で確定）。`flake.nix` の `go_1_26` / `go.mod` の `go 1.26.5` / Dockerfile の `golang:1.26.6-trixie`。**揃えるのはマイナーまでで、パッチの下限は `go.mod` が保証する**（#4 で改定。§10 参照） |
```

に変える。

- [ ] **Step 8: 古い記述が残っていないことを確認する**

Run: `grep -nE 'colima|1\.26\.0|20 ?MB|static:nonroot' docs/DESIGN.md`
Expected: 残ってよいのは次の 2 種類だけ。

- §10 と §14 の「colima を想定していたが未導入だった」「colima は未導入だった」という**経緯**の記述
- §9 の「実測で go1.26.0 は `false`、go1.26.5 は `true`」（`os/signal` の挙動差の実例。バージョン揃えの話ではない）

これ以外がヒットしたら直し漏れ。

- [ ] **Step 9: コミット**

```bash
git add docs/DESIGN.md
git commit -m "docs: Dockerfile の実装結果を DESIGN.md に反映

§9 に骨子との差分表を新設。あわせて調査で見つかった腐りを直した:
distroless のサフィックス無しタグ、イメージサイズ約 20MB の見積もり
（実測 8MB で Artifact Registry の消費見積もりも変わる）、colima、
go.mod の go 1.26.0 という表記。

バージョン揃えルールは「マイナーまで揃え、パッチの下限は go.mod が
保証する」に改定した。供給元が 3 系統に分かれておりパッチまで人手で
揃え続けると必ず腐るため。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 4: 受け入れ確認、issue 本文の更新、PR

**Files:**
- 変更なし（確認と外部操作のみ）

**Interfaces:**
- Consumes: Task 1〜3 の全成果物

> **このタスクは共有状態を変える操作（issue 本文の更新、`git push`、PR 作成）を含む。**
> 実行前に必ず人間の合図を取る。

- [ ] **Step 1: 検証マトリクスを通しで実行する**

きれいな状態から通す。

```bash
docker rmi -f go-todo
just docker-build
docker image inspect go-todo | jq -r '.[0].Size / 1048576 | floor, .[0].Architecture, .[0].Config.User, (.[0].Config.Entrypoint | tostring)'
```

Expected:
```
8
amd64
65532:65532
["/app"]
```

（サイズは 30 未満なら数値がぶれてよい）

- [ ] **Step 2: 実行時の確認を通しで実行する**

別シェルで `just docker-run` を起動したうえで:

```bash
curl -s localhost:8080/healthz | jq
docker stop go-todo
```

Expected: `{"status": "ok"}` が返り、`docker stop` で `shutting down` のログが出て 10 秒未満で終了する

**この 2 ステップの出力を全部コピーしておく。** PR 本文に貼る（`CONTRIBUTING.md` §5）。

- [ ] **Step 3: `/code-review` を走らせる**

`CONTRIBUTING.md` §6 の 3 つのゲートのうち 2 つ目。指摘は盲信も無視もせず、根拠を確認する。

- [ ] **Step 4: issue #4 の本文を現状に合わせて更新する（要・人間の合図）**

2 箇所を直す。

1. 「手動でやること」の `colima の起動（colima start）` → `Docker Desktop の起動`
2. 「確認手順」の

```sh
docker run --rm -p 8080:8080 -e PORT=8080 go-todo &
curl -s localhost:8080/healthz | jq
```

を

```sh
just docker-run &          # コンテナ内は PORT=9090、ホストは 8080 にマップ
curl -s localhost:8080/healthz | jq
docker stop go-todo        # SIGTERM が PID 1 に届くことの確認
```

に変える。**元の手順は `PORT=8080` でアプリの既定値と同じため、`PORT` を完全に無視する
実装でも通ってしまい、完了条件 3 の検証になっていなかった。**

Run: `gh issue edit 4 --body-file <更新後の本文>`

- [ ] **Step 5: push して PR を作る（要・人間の合図）**

```bash
git push -u origin 4-dockerfile-distroless
```

PR タイトル（squash 後の `main` のコミットメッセージになる）:

```
chore: Dockerfile を作る（マルチステージ + distroless）
```

PR 本文には `Closes #4` と、Step 1・2 で取った出力を貼る。

---

## Self-Review

**spec の要求と Task の対応**

| spec の項目 | 実装する Task |
|---|---|
| ビルド戦略（`$BUILDPLATFORM` + クロスコンパイル） | Task 1 Step 2 |
| digest ピン / `static-debian13` / `trixie` | Task 1 Step 2 |
| `go.su[m]` glob | Task 1 Step 2（フォールバックあり） |
| cache mount（`id` に `TARGETARCH`） | Task 1 Step 2・Step 7（フォールバックあり） |
| `-trimpath -ldflags="-s -w"` | Task 1 Step 2 |
| `GOTOOLCHAIN=local` | Task 1 Step 2 |
| `/app` / `USER 65532:65532` / `EXPOSE` 無し / exec `ENTRYPOINT` | Task 1 Step 2・Step 6 |
| `.dockerignore` 許可リスト + `*_test.go` 除外 | Task 1 Step 1 |
| `justfile` の 2 レシピと `image` 変数 | Task 2 Step 1・Step 2 |
| `docker-run` の `PORT` を 9090 にする判断 | Task 2 Step 2・Step 6 |
| サイズ表示を `jq` にする判断 | Task 2 Step 2・Step 4 |
| 完了条件 1（build が通る） | Task 1 Step 3 / Task 2 Step 4 |
| 完了条件 2（30MB 以下） | Task 1 Step 4 / Task 4 Step 1 |
| 完了条件 3（`PORT` が効く） | Task 2 Step 6 / Task 4 Step 2 |
| 完了条件 4（非 root） | Task 1 Step 6 / Task 4 Step 1 |
| 検証 5（amd64 であること） | Task 1 Step 5 / Task 4 Step 1 |
| 検証 6（SIGTERM が PID 1 に届く） | Task 2 Step 7 / Task 4 Step 2 |
| DESIGN.md §9 の差分注記 | Task 3 Step 1〜3 |
| DESIGN.md §10 / §14 のバージョン揃えルール | Task 3 Step 4・Step 7 |
| DESIGN.md の colima 2 箇所 | Task 3 Step 5・Step 7 |
| DESIGN.md §14 の Artifact Registry 見積もり | Task 3 Step 6 |
| issue #4 本文の更新 | Task 4 Step 4 |
| 実測で確かめること 1〜4 | Task 1 Step 3〜7 と「想定される失敗とフォールバック」 |

**未カバー: 無し。**

**名前の一貫性**

- イメージ名は全 Task で `go-todo`（`justfile` の `image` 変数と `docker` 直叩きの両方）
- コンテナ名も `go-todo`（`--name {{image}}`）。`docker stop go-todo` が Task 2・Task 4 で一致
- バイナリのパスは `/out/app`（ビルドステージ）→ `/app`（ランタイム）で全 Task 一致
- `docker-run` の既定 `port` は `9090`。Task 2 Step 5・Step 6 と Task 4 Step 2 で一致
