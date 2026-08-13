# go-todo 設計ドキュメント

作成日: 2026-08-13

## 1. 目的と前提

### このプロジェクトは何か

Go をバックエンドに本格導入するための**前段階の学習プロジェクト**。題材は TODO アプリ。

### 前提（すべての設計判断の根拠）

| 項目 | 内容 |
|---|---|
| 導入先の想定 | 新規サービスでの採用 |
| 学習の最優先事項 | **Go のアプリ設計とテスト**（レイヤリング、interface の切り方、テスタビリティ） |
| Go の習熟度 | ほぼ未経験 |
| 最終スコープ | マルチユーザー + 認証。機能は TODO の CRUD のみ |

### スコープの原則

**機能を横に広げず、縦に一本通す。** タグ・期限・検索・共有は作らない。CRUD 一本を、認証からデプロイまで端から端まで通すことに時間を使う。

---

## 2. 貫いている設計原則

### 原則 1: 標準で足りるものは標準で

| 用途 | 採用 | 不採用にしたもの |
|---|---|---|
| HTTP | `net/http` | Gin, echo, chi |
| ログ | `log/slog` | zap, zerolog |
| エラー | `errors` | pkg/errors, cockroachdb/errors |
| 設定 | `os.Getenv` | viper |
| DI | `main()` で手書き | wire, fx, dig |
| モック | 手書き fake | gomock, mockery |
| バリデーション | ドメイン層に手書き | go-playground/validator |

外部ライブラリを入れたのは、標準に存在しない機能だけ: `pgx`, `sqlc`, `golang-migrate`, `firebase-admin-go`, `google/uuid`。

**理由**: Go 未経験の状態でフレームワークから入ると、「Go」ではなく「そのフレームワーク」を学ぶことになる。標準を理解していればフレームワークは後から読めるが、逆は成立しない。加えて、選定理由をすべて自分の言葉で説明できる状態は、チームへの導入時にそのまま必要になる。

### 原則 2: 依存は常に内向き

```
http ──┐
       ├──> todo （何にも依存しない）
postgres ─┘
```

`todo` パッケージは HTTP も DB も知らない。`todo` が「自分に必要な永続化の形」を `interface` として宣言し、`postgres` がそれを満たす。

**Go 特有の作法**: `interface` は実装側ではなく**利用側**が定義する。Go には `implements` 宣言がなく、メソッドセットが一致すれば自動的に満たされる（暗黙的満足）ため、これが自然にできる。

**検証方法**: Phase 1 でメモリ実装、Phase 2 で Postgres 実装に差し替える。このとき **`todo` パッケージのテストが 1 行も変わらなければ**、依存の向きが正しい。

### 原則 3: 契約が先、実装が後

`api/openapi.yaml` を手で書き、それを唯一の契約とする。Go のコードから OpenAPI を生成する（swaggo）方式は取らない。

---

## 3. アーキテクチャ

### ディレクトリ構成

```
go-todo/
├── api/
│   └── openapi.yaml              # 唯一の API 契約。Go / TS 双方が参照
├── backend/
│   ├── go.mod                    # モジュールルートは backend/
│   ├── cmd/api/main.go           # 起動と DI 配線のみ
│   ├── internal/
│   │   ├── todo/                 # ドメイン。他に依存しない
│   │   │   ├── todo.go           #   Todo 型 / New() コンストラクタ / ビジネスルール
│   │   │   ├── service.go        #   ユースケース
│   │   │   ├── repository.go     #   interface 定義（実装は持たない）
│   │   │   └── *_test.go         #   手書き fake を使った table-driven test
│   │   ├── postgres/             # todo.Repository の実装（sqlc 生成物を含む）
│   │   ├── http/                 # ハンドラ / ミドルウェア / DTO
│   │   ├── auth/                 # Firebase ID トークン検証
│   │   └── config/               # 環境変数の読み込み（30 行程度）
│   ├── db/
│   │   ├── migrations/           # golang-migrate の up/down SQL
│   │   └── query/                # sqlc の入力 SQL
│   ├── sqlc.yaml
│   └── Dockerfile
├── web/
│   ├── package.json
│   └── src/                      # Vite + React + TanStack Query
├── infra/                        # Terraform
├── scripts/
│   ├── bootstrap.sh              # Terraform state 用 GCS バケット作成（手動実行）
│   └── setup-firebase.sh         # Firebase 側の手動設定を記録
├── .github/workflows/
└── Makefile                      # make dev / test / migrate / gen の入口
```

### 構成の判断理由

**ドメインで切る（`todo/`）／層で切らない（`handler/`, `usecase/`, `repository/`）**

Go はパッケージ名が呼び出し側に現れる。層で切ると `repository.TodoRepository` と同じ語を繰り返すことになるが、ドメインで切れば `todo.Repository` と読める。

層で切る構成は Java / PHP 由来であり、Go に持ち込むと不自然になる。これは実務のコードレビューで実際に議論になる論点。

**`go.mod` を `backend/` に置く**

Go 単体リポジトリならルートが慣習だが、多言語モノレポでは `backend/` に閉じ込めた方が `go test ./...` の対象や `.golangci.yml` の適用範囲が明快になる。

**DI ライブラリを使わない**

```go
func main() {
    cfg  := config.Load()
    pool := pgxpool.New(ctx, cfg.DatabaseURL)
    repo := postgres.NewTodoRepository(pool)
    svc  := todo.NewService(repo)
    h    := httpapi.NewHandler(svc)
    // ...
}
```

これで足りる。「DI とは引数で渡すこと」という本質が見える。

---

## 4. ドメイン設計

### エラー設計: センチネルエラー + `errors.Is`

```go
// internal/todo/todo.go —— ドメインは HTTP を知らない
var (
    ErrNotFound     = errors.New("todo not found")
    ErrForbidden    = errors.New("not the owner")
    ErrInvalidInput = errors.New("invalid input")
)

// internal/postgres/todo.go —— 文脈を足して包む
if errors.Is(err, pgx.ErrNoRows) {
    return nil, fmt.Errorf("find todo %s: %w", id, todo.ErrNotFound)
}

// internal/http/handler.go —— ここで初めて HTTP になる
switch {
case errors.Is(err, todo.ErrNotFound):     respondError(w, 404, "not found")
case errors.Is(err, todo.ErrForbidden):    respondError(w, 403, "forbidden")
case errors.Is(err, todo.ErrInvalidInput): respondError(w, 400, err.Error())
default:
    slog.ErrorContext(ctx, "unexpected error", "err", err)
    respondError(w, 500, "internal server error")   // 内部情報は返さない
}
```

**要点**

- `%w` でラップすると、原因を保ったまま文脈を積める。ログには `find todo abc-123: todo not found` と経路が出るが、判定は `errors.Is` で貫通する。Go はこれで例外のスタックトレースに相当する情報を実現している。
- `err != nil` を毎回書くのが Go の作法。`panic` で省略しない。
- 500 番でクライアントに内部エラー文字列を返さない（SQL やテーブル名が漏れる）。ログには全文、レスポンスには汎用メッセージ + リクエスト ID。
- ドメインのエラーに `StatusCode` フィールドを持たせない。持たせた瞬間に原則 2 が崩れる。

カスタムエラー型 + `errors.As` は、エラーコードや詳細フィールドが必要になってから移行する。

### バリデーション: ドメイン層に手書き

責務を 2 段に分ける。

| 層 | 検証内容 | 例 |
|---|---|---|
| `http` | JSON として読めるか | `title` に数値が入っている → 400 |
| `todo` | ビジネスルールを満たすか | タイトルが空 / 200 文字超 → 400 |

```go
func New(userID, title string) (*Todo, error) {
    if utf8.RuneCountInString(title) == 0 {
        return nil, fmt.Errorf("title is required: %w", ErrInvalidInput)
    }
    if utf8.RuneCountInString(title) > 200 {
        return nil, fmt.Errorf("title must be 200 characters or less: %w", ErrInvalidInput)
    }
    id, err := uuid.NewV7()
    if err != nil {
        return nil, fmt.Errorf("generate id: %w", err)
    }
    return &Todo{ID: id, UserID: userID, Title: title, Done: false, CreatedAt: time.Now()}, nil
}
```

**要点**

- 「タイトルは 1〜200 文字」はビジネスルールであって HTTP の関心事ではない。ドメインに置けば、将来 CLI やバッチから操作しても保証される。
- コンストラクタを通さないと `Todo` を作れない構造にすることで、**不正な `Todo` がプログラム中に存在できない**状態を作る。
- `utf8.RuneCountInString` を使う。**Go の `len(string)` はバイト数**を返し、「あ」は 3 バイト。日本語を扱う以上、必ず踏む落とし穴。
- バリデーションのテストに HTTP もサーバ起動も要らない。`todo.New()` を呼ぶだけの table-driven test で全ケース網羅できる。

---

## 5. データモデル

### `todos`

| カラム | 型 | 備考 |
|---|---|---|
| `id` | `uuid` | PK。**UUIDv7** をアプリ側で採番 |
| `user_id` | `text` | Firebase の uid。`users.id` への FK |
| `title` | `text` | 1〜200 文字（アプリ側で保証） |
| `done` | `boolean` | |
| `created_at` | `timestamptz` | |
| `updated_at` | `timestamptz` | |

インデックス: `(user_id, created_at DESC)` —— 一覧取得のクエリ形に合わせる。

### `users`

| カラム | 型 | 備考 |
|---|---|---|
| `id` | `text` | PK。Firebase の uid をそのまま使う |
| `email` | `text` | |
| `created_at` | `timestamptz` | |

初回ログイン時にアプリ側で upsert する。

### ID に UUIDv7 を選ぶ理由

- **`bigserial`（連番）の問題**: ID が API に露出すると `/todos/1` → `/todos/2` で他人のデータを試せる。所有権チェックで防ぐが、**チェック漏れが即座に事故になる設計**は避ける。加えて総レコード数が外部に漏れる。
- **UUIDv4 の問題**: 完全ランダムなので B-tree インデックスへの挿入位置が毎回バラバラになり、ページ分割が頻発してインデックスが肥大化する。
- **UUIDv7**: 先頭にタイムスタンプを持つため時系列順に並び、連番の挿入効率と UUID の推測困難性を両立する。RFC 9562 で標準化済み。

**採番はアプリ側（Go）で行い、DB の DEFAULT に頼らない。** `todo.New()` の中で採番すれば、DB なしでドメインのテストが完結する（Phase 1 のメモリ実装がそのまま動く）。

---

## 6. API 設計

### 契約

`api/openapi.yaml` を手書きし、これを唯一の情報源とする。

- **TS 側**: `openapi-typescript` で型を生成
- **Go 側**: 当面は手書きハンドラ。`oapi-codegen` の導入は Phase 5 の発展課題

Go 側を生成しない理由: `StrictServerInterface` は「契約とハンドラのズレがコンパイルエラーになる」利点があるが、生成コードの量が多く、Go 未経験の段階では自分でハンドラが書けるようになる前に生成物に埋もれる。**`openapi.yaml` との手動同期が面倒になってから導入する**方が、導入理由を理解できる。

### エンドポイント

| メソッド | パス | 説明 |
|---|---|---|
| `GET` | `/api/todos` | 自分の TODO 一覧 |
| `POST` | `/api/todos` | 作成 |
| `PATCH` | `/api/todos/{id}` | 更新（title / done） |
| `DELETE` | `/api/todos/{id}` | 削除 |
| `GET` | `/healthz` | ヘルスチェック（認証不要） |

すべて `Authorization: Bearer <Firebase ID token>` を要求する（`/healthz` を除く）。

### ルーティング（Go 1.22+ の標準 ServeMux）

```go
mux := http.NewServeMux()
mux.Handle("GET /api/todos",         authMW(http.HandlerFunc(h.ListTodos)))
mux.Handle("POST /api/todos",        authMW(http.HandlerFunc(h.CreateTodo)))
mux.Handle("PATCH /api/todos/{id}",  authMW(http.HandlerFunc(h.UpdateTodo)))
mux.Handle("DELETE /api/todos/{id}", authMW(http.HandlerFunc(h.DeleteTodo)))
mux.HandleFunc("GET /healthz",       h.Healthz)
```

パスパラメータは `r.PathValue("id")` で取得する。Go 1.22 でメソッド指定とワイルドカードが標準サポートされたため、ルータライブラリは不要。

### ミドルウェア

すべて `func(http.Handler) http.Handler` の形式で書く。この形式は全フレームワーク共通であり、書いた経験がそのまま転用できる。

| ミドルウェア | 役割 |
|---|---|
| `RequestID` | リクエスト ID を採番し `context` に格納 |
| `Logger` | アクセスログを `slog` で JSON 出力 |
| `Recoverer` | panic を捕捉して 500 を返す |
| `CORS` | Phase 4 で追加。後にリライトへ移行 |
| `Auth` | Firebase ID トークンを検証し、uid を `context` に格納 |

---

## 7. 認証

### 方式: Firebase Authentication

- **フロント**: Firebase SDK でログインし、ID トークンを取得。`Authorization` ヘッダに付与
- **バックエンド**: `firebase-admin-go` の `VerifyIDToken` で検証し、uid を `context` に詰める

### 認証情報の流れ

```
Authorization ヘッダ
  → auth ミドルウェアが検証
  → context.WithValue(ctx, userIDKey, uid)
  → ハンドラが ctx から取得
  → service に引数として渡す
  → repository が WHERE user_id = $1 に使う
```

**要点**

- `context` に詰めるキーは**非公開の独自型**にする（`type ctxKey struct{}`）。文字列キーは他パッケージと衝突する。
- **認可（所有権チェック）は service 層に置く**。「この TODO は本当にこのユーザーのものか」はビジネスルールであり、HTTP の関心事ではない。
- テスト時は `auth` ミドルウェアを差し替えられるよう、`interface` で受ける。実トークン検証は統合テストのみに限定する。

### コスト

SMS 認証を有効にしない限り無料。ソーシャル / メールパスワードは月間アクティブユーザー 5 万人まで無料枠。

---

## 8. データアクセス

### sqlc + pgx

**sqlc は ORM ではない。** SQL ファイルを読んで Go の関数を生成するコード生成ツールであり、実行時には存在しない。生成されるのは `pgx` を直接呼ぶ普通の Go コード。

| | 正体 | 役割 |
|---|---|---|
| `database/sql` | 標準ライブラリ | DB アクセスの抽象インターフェース |
| `pgx` | Postgres ドライバ | 実際の通信 |
| `sqlc` | コード生成ツール | SQL → Go 関数。ビルド時に消える |

**Go に標準の ORM は存在しない。** GORM / ent / sqlc / sqlx / 素の `database/sql` のいずれもデファクトではなく、プロジェクトごとに選ぶのが Go の文化。「暗黙の魔法を嫌い、何が起きているか読めることを優先する」という言語コミュニティの価値観が背景にある。

**sqlc を選ぶ理由**: SQL をそのまま書くので SQL の学びが残り、生成物は読める Go コードなので隠れる部分が最小。ORM の DSL という「その ORM でしか通用しない知識」を覚えずに済む。

### 進め方の条件

**最初の 2〜3 本のクエリは sqlc を使わず手書きする。** `defer rows.Close()`、`Scan` にポインタを渡す、`ErrNoRows` を「エラー」ではなく「見つからない」として扱う——これを一度手で書かないと、生成コードが読めるだけの人になる。

### マイグレーション: golang-migrate

- `backend/db/migrations/` に `000001_create_todos.up.sql` / `.down.sql` を連番で配置
- `schema_migrations` テーブルで適用済みバージョンを管理
- **sqlc はこのディレクトリをスキーマ定義として読む**ため、スキーマの二重管理が発生しない

### 実行タイミング: CI のデプロイ前ステップ

ローカルは `make migrate`。本番は GitHub Actions で Cloud Run デプロイの**前**に実行する。

**アプリ起動時に自動実行しない理由**

- Cloud Run はトラフィックに応じてインスタンスを増やすため、起動のたびにマイグレーションが走る（advisory lock で破壊はされないが、ロック待ちで起動が遅くなる）
- コールドスタートが悪化する。Cloud Run の起動時間はそのままユーザーの待ち時間
- マイグレーション失敗 = アプリが起動不能。DB は無事なのに全断する
- ロールバックが破綻する。前のリビジョンに戻してもスキーマは新しいまま。**「デプロイの巻き戻し」と「スキーマの巻き戻し」は別物**

Cloud Run Jobs による専用実行は実務の正解に近いが、Job 定義 / IAM / イメージの設定が増えるため Phase 5 の発展課題とする。

---

## 9. インフラ

### 構成

```
[ブラウザ]
    │
    ├──> Firebase Hosting        （Vite ビルド成果物）
    │
    └──> Cloud Run               （Go / distroless / min=0 max=3）
              │
              ├──> Neon          （サーバーレス Postgres / pooled endpoint）
              ├──> Firebase Auth （ID トークン検証）
              └──> Secret Manager（DATABASE_URL）
```

### Cloud Run 設定

| 項目 | 値 | 理由 |
|---|---|---|
| `min-instances` | `0` | 触らない月の請求をゼロにする |
| `max-instances` | `3` | **コネクション枯渇と課金暴走の両方を防ぐ** |
| `concurrency` | `80`（既定） | Go は goroutine で捌けるので下げる必要がない |
| `pgxpool.MaxConns` | `5` | `max-instances × MaxConns ≤ DB の接続上限` |

**`max-instances` を必ず絞る理由**

Cloud Run は既定で最大 100 インスタンスまで増える。各インスタンスが独立した `pgxpool` を持つため、`MaxConns=10` なら最大 1000 コネクションが DB に殺到し、上限超過で全リクエストが落ちる。

**「アプリはスケールするが DB はスケールしない」**——これがサーバーレス × RDB の本質的な非対称性。`max-instances × MaxConns ≤ DB の接続上限` という掛け算を常に意識する。

対策として **Neon の pooled endpoint（PgBouncer 経由）** を使う。Cloud SQL でも同じ問題が起き、そちらでは Cloud SQL Auth Proxy や PgBouncer を挟むことになる。構造が同じなので知識が転用できる。

### グレースフルシャットダウン（必須）

Cloud Run はインスタンス停止時に **SIGTERM を送り、10 秒後に SIGKILL** する。無視すると処理中のリクエストが切断される。

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
defer stop()

go func() {
    if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        slog.Error("server error", "err", err)
        os.Exit(1)
    }
}()

<-ctx.Done()
shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
defer cancel()
_ = srv.Shutdown(shutdownCtx)
pool.Close()
```

この形は Kubernetes でも同じ。コンテナ上で動くサーバの基本作法。

### コンテナ: マルチステージ + distroless

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download                    # 依存だけ先にコピーしてレイヤキャッシュを効かせる
COPY . .
RUN CGO_ENABLED=0 go build -o /app ./cmd/api

FROM gcr.io/distroless/static:nonroot
COPY --from=build /app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
```

※ ベースイメージのバージョンは着手時の最新安定版に合わせる。

**この 10 行の学習ポイント**

- `CGO_ENABLED=0` で静的リンクバイナリになる。だから実行イメージに libc すら要らない。Ruby や Python では真似できない Go の武器
- 結果イメージは約 20MB。**Cloud Run はリクエスト受信後にコンテナを起動するため、イメージサイズがそのままコールドスタート時間に効く**
- `distroless/static` にはシェルもパッケージマネージャもない。攻撃面が最小になる代わりに「コンテナに入って調べる」ができない。**だからログ設計が重要になる**

### 設定とシークレット

| 種別 | 保管場所 | 例 |
|---|---|---|
| 機密 | Secret Manager → Cloud Run が環境変数として注入 | `DATABASE_URL` |
| 非機密 | Cloud Run の環境変数 | `ENV`, `LOG_LEVEL` |
| ローカル | `.env` + direnv（`.gitignore` 対象、`.env.example` を配布） | 全部 |

- **`PORT` は Cloud Run が自動で注入する。** `os.Getenv("PORT")` を必ず読むこと。ハードコードすると起動に失敗する（最頻出の詰まりどころ）
- **Secret の値を tfvars に書かない。** Terraform では `google_secret_manager_secret` の箱を作るところまでとし、値は `gcloud` で手入力する。値を tfvars に書くと state に平文で残る
- 設定ライブラリは入れない。`internal/config` に `Load()` を書き、必須変数が無ければ起動時に落とす

### ログ: `log/slog` + Cloud Logging 整形

- `slog.JSONHandler` で JSON 出力。Cloud Run は標準出力をそのまま Cloud Logging に送るため、JSON で吐けば自動でパースされフィールド単位で検索できる
- **キー名を Cloud Logging の規約に合わせる**。slog の既定は `level` / `msg` / `time` だが、Cloud Logging が解釈するのは **`severity` / `message` / `timestamp`**。`ReplaceAttr` で変換する。**これをやらないとエラーだけ絞り込むことができない**
- リクエスト ID を `context` に入れ全ログに付与する。Cloud Run は複数インスタンスで並行処理するため、これがないとログが混線して追えない
- `slog.Logger` をグローバル変数にしない。`context` から取り出す形にすればテストで差し替えられる

OpenTelemetry / Cloud Trace は Phase 5 の発展課題。単一サービスなので構造化ログとリクエスト ID で十分追える。

### IaC: Terraform

- **state は GCS バックエンドに置く**（ローカル state だと CI から触れない）
- **state 用バケットは Terraform で作れない**（鶏と卵）ため、`scripts/bootstrap.sh` に `gcloud` コマンドとして記録し手動実行する
- **Firebase 関連（Auth 有効化、Hosting）は Terraform の管理外**とする。`google-beta` に一部リソースはあるが、Hosting のデプロイ自体は CLI。**全部 Terraform でやろうとしない**
- **Cloud Run の `image` は `lifecycle { ignore_changes }` で除外する**。イメージ更新は CD の責務。「**インフラの形は Terraform、動くバージョンは CD**」という責務分割。実務で必ず踏む論点

### CI/CD: GitHub Actions + Workload Identity Federation

```
push to main
  ├─ backend/** が変更 → go test ./... / golangci-lint
  ├─ web/**     が変更 → tsc --noEmit / vitest
  ├─ docker build → Artifact Registry へ push
  ├─ golang-migrate で Neon にマイグレーション適用
  ├─ gcloud run deploy（新リビジョン）
  └─ web/ をビルドして Firebase Hosting へデプロイ
```

**サービスアカウントキー（JSON）を使わない理由**

- SA キーは**無期限の認証情報**。漏れたら失効させるまで無限に使われ、GitHub Secrets に貼った時点で流出経路が増える。Google 自身がキー発行を非推奨としている
- WIF は GitHub Actions が発行する OIDC トークンを GCP が検証して短命の権限を渡す仕組み。**秘密情報がリポジトリに存在しない**
- 設定は「Workload Identity プールを作る → GitHub リポジトリを条件に紐づける → SA への借用を許可する」の 3 ステップ。**理解すれば AWS の OIDC 連携も同じ絵で読める**

---

## 10. テスト戦略

TDD で進める。テストを先に書き、失敗を確認してから実装する。

| 対象 | 手法 | 検証内容 |
|---|---|---|
| `todo` | 手書き fake + table-driven | ビジネスルール、認可判定 |
| `postgres` | **testcontainers-go**（本物の Postgres） | SQL が正しいか、制約が効くか |
| `http` | `httptest` + service の fake | ステータスコード、JSON 形状、認証の通過/拒否 |

### 手書き fake を使う理由（gomock / mockery を使わない理由）

Go の `interface` は小さく切るので、`map` で持つだけの 30 行程度の struct で足りる。生成モックを使うと「モックライブラリの DSL」を学ぶことになり本題からズレる。

**手で fake を書くと、interface が大きすぎるときに手が痛くなる。** これが interface を小さく保つ動機を体で覚える最短路。

### sqlmock を使わない理由

sqlmock は「発行された SQL 文字列が期待と一致するか」を見るだけで、その SQL が Postgres で通るかは検証できない。テストが実装の写経になる。sqlc を使う以上、検証すべきは**書いた SQL が正しいか**そのもの。

### ローカル環境

| 用途 | 手段 |
|---|---|
| 開発用 DB | `compose.yaml` で Postgres 1 サービスのみ |
| テスト用 DB | testcontainers（毎回使い捨て） |
| Go | ホストで直接実行（`go run` / `air`） |
| フロント | `vite dev`（`/api` を `localhost:8080` にプロキシ） |

**Go をコンテナに入れない。** `go run` はホストなら 1 秒台だが、コンテナ経由だとファイル同期とビルドで体感が数倍遅くなり、デバッガや LSP の設定も面倒になる。

Docker ランタイムは colima を想定（testcontainers-go は `DOCKER_HOST` を見るため動作する）。

### SQLite を使わない理由

- **Cloud Run で成立しない**。ファイルシステムはインスタンスごとに独立した揮発領域で、インスタンスが増えれば別々のファイルを見る
- **「ローカルだけ SQLite」も不可**。sqlc はエンジンを 1 つ指定して生成するため、Postgres と SQLite で生成コードが別物になる（プレースホルダ `$1` vs `?`、ドライバ、日時型、採番、配列/JSONB の有無）。マイグレーション SQL・クエリ SQL・生成コード・接続コードを 2 系統持つことになり、「ローカルでは通るのに本番で落ちる」バグの温床になる

---

## 11. フロントエンド

### 構成: Vite + React (SPA) + TanStack Query → Firebase Hosting

**Next.js を使わない理由**: 学習の重心は Go。Next.js を入れると「App Router でトークンをどこに持つか」「Server Component から叩くなら BFF が要るか」「Cloud Run にもう 1 サービス立てるか」と、Go と無関係な論点が増える。今回それらはノイズ。

SPA なら Firebase Auth のクライアント SDK がそのまま使え、「ログイン → ID トークン取得 → `Authorization` を付けて fetch」という認証の本筋だけに集中できる。

### データ取得: TanStack Query

素の `fetch` + `useState` だと、ローディング / エラー / 更新後の再取得 / 二重送信防止を全部手で書くことになる。`useQuery` と `useMutation` の 2 つを覚えるだけで片付く。**フロントに時間を取られないための選択。**

`openapi-typescript` で生成した型を乗せることで、契約駆動がエディタ補完として体感できる。

### 状態管理ライブラリは入れない

SPA の状態のほとんどは「サーバにあるデータのキャッシュ」であり、その大半を TanStack Query が吸収する。残るのは「ログイン中のユーザー」程度で、React Context で十分。

### CORS の扱い

Phase 4 では**あえて別オリジンのまま CORS を自分で設定する**。Preflight とヘッダを自分で通す経験は、バックエンドエンジニアが必ず一度は踏む地雷。

その後、Firebase Hosting の rewrite（`/api/**` → Cloud Run）に切り替えて同一オリジンにする。**踏んでから「そもそも同一オリジンにすれば消える問題」だと知る**順序が、理解として残る。

---

## 12. 開発フェーズ

### 進め方: Walking Skeleton

**Phase 0 で、JSON を 1 つ返すだけのアプリを Cloud Run に本番デプロイする。** TODO の機能はゼロ、DB も認証もなし。

**なぜ最初にデプロイするのか**

GCP プロジェクト作成、課金紐付け、API 有効化、Artifact Registry、IAM、Terraform state、WIF——この経路には未知の詰まりどころが大量にあり、しかも Go の実力とは無関係な種類の詰まり。**最後に回すと、アプリ完成後に権限エラーで何日も溶かし、心が折れる。**

最初に通しておけば、以降は「動いているものに機能を足す」だけになり、壊れたら直前の差分が原因だと即座に分かる。

### 計画

| Phase | 内容 | 主な学び | 完了条件 |
|---|---|---|---|
| **0** | Hello World を Cloud Run へ | GCP 一式、Terraform、Dockerfile、WIF | 公開 URL が JSON を返す |
| **1** | メモリ実装の TODO CRUD | Go の書き方、パッケージ分割、TDD、手書き fake | 全 CRUD が動き、テストが緑 |
| **2** | Postgres 永続化 | pgx 手書き → sqlc、migrate、testcontainers | 再起動してもデータが残る |
| **3** | Firebase 認証 | ミドルウェア、`context` 伝搬、所有権チェック | 他人の TODO が見えない |
| **4** | TS フロント | OpenAPI、型生成、CORS、Firebase Hosting | ブラウザから一通り操作できる |
| **5** | 発展 | Cloud Run Jobs、oapi-codegen、OTel、Cloud SQL 体験 | — |

**各 Phase の終わりで必ずデプロイして動作確認する。**

### Phase 1 で DB を使わない理由

`todo.Repository` の実装をメモリ上の `map` で書くことで、**「DB がなくてもアプリは動く」= 依存が内向きに揃っている**ことを最初に確認できる。

Phase 2 で `postgres` 実装に差し替えたとき、**`todo` パッケージのテストが 1 行も変わらない**——これが原則 2 の答え合わせになる。

---

## 13. 却下した選択肢の記録

後から「なぜこれを使わなかったのか」を思い出せるように残す。

| 却下したもの | 理由 |
|---|---|
| Gin / echo / chi | `http.Handler` を先に理解すべき。Go 1.22 でルーティングの不足も解消済み。自作ミドルウェアがフレームワーク独自形式になると転用できない |
| GORM | Go を学ぶのではなく GORM を学ぶことになる。発行 SQL が見えにくい |
| クリーンアーキテクチャ（4 層 + DTO 変換） | Go 未経験の段階ではボイラープレートが本質を覆い隠す |
| wire / fx（DI ライブラリ） | `main()` の手書きで足りる |
| gomock / mockery | 手書き fake の「痛み」が interface 設計の指針になる |
| viper | 環境変数を読むだけの用途には過剰 |
| zap / zerolog | 標準 `slog` で足りる。性能差が効くのは秒間数万ログの世界 |
| go-playground/validator | ルールがドメインの外に出る |
| Firestore | SQL / トランザクション / マイグレーションの学びが丸ごと消える |
| SQLite | Cloud Run で成立せず、ローカル限定利用も sqlc の制約で破綻する |
| Cloud SQL（当面） | 月 2,000〜4,000 円が常時発生。Phase 5 で単発の課題として体験する |
| Connect-RPC / Protobuf | buf と Protobuf の学習コストが乗り、REST の作法が学べない |
| Next.js | Go と無関係な論点（SSR、BFF、トークン配置）が増える |
| Redux / Zustand | サーバ状態は TanStack Query が吸収する |
| Buildpacks / ko | Dockerfile が読めない・書けないままになる |
| SA キー JSON | 無期限の認証情報をリポジトリに置くことになる |
| swaggo | 契約が実装の後追いになり、フロントとの並行開発に向かない |

---

## 14. リスクと未確定事項

### リスク

- **Neon はクロスクラウド**。Cloud Run（GCP asia-northeast1）から Neon（AWS）へは数〜数十 ms のレイテンシが乗る。学習用途では無視できるが、**「本番なら Cloud SQL を選ぶ理由」がここにある**と理解しておく。Phase 5 で Cloud SQL を一度試す
- **Terraform 先行の学習負荷**。Go ほぼ未経験との組み合わせで負荷が高い。Phase 0 のインフラを書き切ったら**しばらく触らない**と決めて Go に戻る
- **Phase 0 が最難関**。Go が 1 行も出てこない。ここを「アプリ開発の前の関門」と割り切れるかが完走の分かれ目

### 着手時に決めること

- Go のバージョン（最新安定版に合わせる）
- Neon のリージョン（東京に近いものを選ぶ）
- GCP プロジェクトを新規作成するか既存を使うか
- `golangci-lint` の設定内容
- Docker ランタイム（colima を想定）
