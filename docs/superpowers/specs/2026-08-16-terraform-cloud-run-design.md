# Terraform で Artifact Registry / Cloud Run / Secret Manager を作る

- issue: #5（`phase:0` / `area:infra` / `mode:ai-only`）
- 日付: 2026-08-16
- 関連: `docs/DESIGN.md` §9 インフラ（Cloud Run 設定 / 設定とシークレット / IaC: Terraform）、§14 リスク
- 前提: #1（GCP プロジェクトと state バケット）、#4（Dockerfile）

## 目的

`infra/` に Terraform を書き、#1 で作った GCS バケットを backend にして
Artifact Registry・Cloud Run・Secret Manager の箱を作る。

`docs/DESIGN.md` §12 の Walking Skeleton の 3 歩目にあたる。ここで作る識別子
（AR のパス、Cloud Run のサービス名、ランタイム SA）は #6（WIF）と #7（CI/CD）が
そのまま参照する。

**このプロジェクトは無料枠の中で運用する。** リージョンの選択と Artifact Registry の
cleanup policy はこの制約から導かれている。

## 決定事項

| 項目 | 値 | 理由 |
|---|---|---|
| Cloud Run / AR のリージョン | `asia-northeast1` | **Tokyo は Cloud Run の Tier 1 リージョン**で、free tier は Tier 1 価格ベースの spending based discount。無料枠が満額効く（後述） |
| Cloud Run サービス名 | `go-todo-api` | |
| AR リポジトリ ID | `go-todo` | イメージのフルパスは `asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo/api` |
| ランタイム SA | `go-todo-run@taktiks2-go-todo.iam.gserviceaccount.com` | Compute 既定 SA に依存しない（後述） |
| 初回イメージのタグ | `:bootstrap` | `:latest` にしない。#7 の CD は SHA タグを打つ設計で、`latest` を混ぜると「今動いているのはどのコミットか」がレジストリから読めなくなる |
| Secret の箱 | `database-url` 1 個 | 値（version）は Phase 2 で `gcloud` から入れる |
| 未認証呼び出し | Terraform で `allUsers` に `roles/run.invoker` | #7 の「手動でやること」から移設 |
| API 有効化 | **Terraform では管理しない** | #1 の `bootstrap.sh` を唯一の記録として残す。二重管理と、`destroy` で API を無効化する事故を避ける |
| `deletion_protection` | `false` | provider 7.x の既定は `true`。`destroy` だけでなく再作成を伴う apply も落ちる |
| provider | `hashicorp/google ~> 7.0` | 最新 7.44.0（2026-08-11）。8.0 は未リリース |
| `required_version` | `>= 1.14` | 下限だけ守る。terraform 本体は system（dotfiles）供給 |
| `terraform.tfvars` | **作らない** | 環境分離をしない以上、定数 4 つで足りる。ファイルを存在させないことで「Secret を tfvars に書かない」を構造で守る |
| 初回 apply | `-target` による 2 段階 | Cloud Run は実在するイメージを要求するが AR は空（後述） |
| TDD | 適用外 | `mode:ai-only`。テストで守れる振る舞いが無い。`CONTRIBUTING.md` §2 の pair-tdd は Go 実装に対する規律 |

## 成果物

- `infra/versions.tf` / `providers.tf` / `variables.tf` / `artifact_registry.tf` / `cloud_run.tf` / `secrets.tf` / `outputs.tf`
- `infra/.terraform.lock.hcl`（darwin_arm64 + linux_amd64。**コミットする**）
- `justfile` に `tf-init` / `tf-plan` / `tf-apply` / `tf-apply-registry` / `docker-push` を追加し、`image` を AR のパスに変更
- `.gitignore` に Terraform の除外を追加
- `docs/DESIGN.md` と issue #7 本文の更新

## `infra/versions.tf`

```hcl
terraform {
  # ローカルは nix-darwin 供給の 1.14.9（2026-08-16 時点。最新安定版は 1.15.8）。
  # nixpkgs の terraform は BUSL で unfree なので flake.nix には入れず、
  # system 供給のままにする。ここで守るのは下限だけ。
  required_version = ">= 1.14"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }

  # このバケットは Terraform では作れない。backend がバケットの存在を前提に
  # するため（鶏と卵）。#1 の scripts/bootstrap.sh が作っている。
  #
  # backend ブロックは変数を受け付けないので、バケット名だけは直書きになる。
  backend "gcs" {
    bucket = "taktiks2-go-todo-tfstate"
    prefix = "infra"
  }
}
```

## `infra/providers.tf`

```hcl
# 認証は ADC。事前に以下が済んでいること（#1 の bootstrap.sh 末尾で案内済み）:
#
#   gcloud auth application-default login
#   gcloud auth application-default set-quota-project taktiks2-go-todo
provider "google" {
  project = var.project_id
  region  = var.region
}
```

## `infra/variables.tf`

```hcl
variable "project_id" {
  description = "GCP プロジェクト ID。#1 で確定。GCP の仕様上あとから変更できない"
  type        = string
  default     = "taktiks2-go-todo"
}

# Cloud Run の free tier は「Tier 1 価格ベースの spending based discount」として
# 適用され、asia-northeast1 (Tokyo) は Tier 1 リージョンに含まれる。
# したがって東京に置いても無料枠は満額効く。
#
# 「無料枠は US 3 リージョン限定」は Cloud Storage の規則であって Cloud Run の
# 規則ではない。#1 の spec が未確認として残した論点はここで解決した。
variable "region" {
  description = "Cloud Run と Artifact Registry のリージョン"
  type        = string
  default     = "asia-northeast1"
}

variable "repository_id" {
  description = "Artifact Registry のリポジトリ ID"
  type        = string
  default     = "go-todo"
}

variable "service_name" {
  description = "Cloud Run のサービス名"
  type        = string
  default     = "go-todo-api"
}
```

## `infra/artifact_registry.tf`

```hcl
# Cloud Run と同じリージョンに置く。pull がリージョン内で完結し、
# コールドスタート時間に効く（docs/DESIGN.md §9）。
# AR の 0.5 GB 無料枠にリージョン制限は無い。
resource "google_artifact_registry_repository" "app" {
  location      = var.region
  repository_id = var.repository_id
  format        = "DOCKER"
  description   = "go-todo のコンテナイメージ"

  # 初回は true で流し、Cloud Logging に出る「消える対象」を確認してから false にする。
  # 無料枠 0.5 GB を守るのが目的であって、消しすぎてロールバック先を失うのは本末転倒。
  cleanup_policy_dry_run = true

  # KEEP は DELETE より優先される。直近 5 世代は日数に関係なく必ず残るので、
  # 事故った直後の巻き戻しは常に効く（#1 の tfstate-lifecycle.json と同じ考え方）。
  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"

    most_recent_versions {
      keep_count = 5
    }
  }

  # tag_state を UNTAGGED にしない。#7 の CD は git SHA でタグを打つので
  # 全イメージが TAGGED になり、UNTAGGED 条件では 1 つも消えない。
  #
  # 秒で書くのは、API が duration を秒に正規化して返すため。"30d" と書くと
  # plan が毎回差分を出す可能性がある。
  cleanup_policies {
    id     = "delete-old"
    action = "DELETE"

    condition {
      older_than = "2592000s" # 30d
    }
  }
}
```

## `infra/cloud_run.tf`

```hcl
# Cloud Run のランタイム ID。
#
# service_account を書かないと Compute Engine の既定 SA が使われるが、
# compute.googleapis.com は #1 で有効化していないため既定 SA 自体が存在しない
# 可能性が高い。存在したとしても Editor 権限で過剰。
resource "google_service_account" "run_runtime" {
  account_id   = "go-todo-run"
  display_name = "go-todo Cloud Run runtime"
}

resource "google_cloud_run_v2_service" "api" {
  name     = var.service_name
  location = var.region

  # provider 7.x の既定は true。destroy だけでなく、name や location を変えて
  # 再作成が必要になる apply も落ちる。作り直す前提の学習用なので最初から外す。
  deletion_protection = false

  template {
    service_account = google_service_account.run_runtime.email

    # docs/DESIGN.md §9: max × pgxpool.MaxConns <= DB の接続上限。
    # 既定の 100 のままだと DB にコネクションが殺到する。
    scaling {
      min_instance_count = 0
      max_instance_count = 3
    }

    # 未指定でも CPU >= 1 なら 80 が既定。docs/DESIGN.md §9 が値として決めている
    # 以上、「決めたのにコードに無い」状態を作らないため明示する。
    max_instance_request_concurrency = 80

    containers {
      # 下の ignore_changes があるので、この値が使われるのは初回作成時だけ。
      # 実物は just docker-push が :bootstrap として先に置いてある。
      image = "${var.region}-docker.pkg.dev/${var.project_id}/${var.repository_id}/api:bootstrap"
    }

    # resources / cpu_idle は書かない。v2 の既定（1 vCPU / 512 MiB /
    # リクエスト処理中のみ CPU 割り当て）がそのまま docs/DESIGN.md §9 の意図と一致し、
    # min_instance_count = 0 と合わせてアイドル課金がゼロになる。
    #
    # DATABASE_URL も繋がない。Cloud Run はリビジョン起動時に secret を解決するため、
    # version の入っていない secret を参照するとリビジョンが Ready にならない。
    # Phase 2 で値を入れるときに env_from_secret ごと足す。
  }

  # 「インフラの形は Terraform、動くバージョンは CD」（docs/DESIGN.md §9）。
  #
  # image だけでは足りない。#7 の gcloud run deploy はデプロイのたびに
  # client / client_version を書き換えるので、除外しないと次の plan が毎回
  # それを戻そうとして差分を出す。CD が触るフィールドは全部除外して初めて成立する。
  lifecycle {
    ignore_changes = [
      template[0].containers[0].image,
      client,
      client_version,
    ]
  }

  # image は文字列なので、Terraform だけでは AR への依存を推論できない。
  depends_on = [google_artifact_registry_repository.app]
}

# 未認証で叩けるようにする。issue #7 の「手動でやること」から移設した。
#
# AR からの pull は Cloud Run のサービスエージェントが行い、run.googleapis.com の
# 有効化時に artifactregistry.reader を自動で持つ。こちらに追加の IAM は要らない。
resource "google_cloud_run_v2_service_iam_member" "public" {
  project  = google_cloud_run_v2_service.api.project
  location = google_cloud_run_v2_service.api.location
  name     = google_cloud_run_v2_service.api.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
```

## `infra/secrets.tf`

```hcl
# 箱だけ作る。値（version）は Phase 2 で gcloud から入れる。
# tfvars にも state にも平文を置かないため（docs/DESIGN.md §9）。
resource "google_secret_manager_secret" "database_url" {
  secret_id = "database-url"

  replication {
    auto {}
  }
}

# Phase 2 は「version を入れて Cloud Run の env に繋ぐ」だけで済むように、
# 権限は今のうちに張っておく。version が無い間、この binding は何もしない。
resource "google_secret_manager_secret_iam_member" "runtime_accessor" {
  secret_id = google_secret_manager_secret.database_url.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.run_runtime.email}"
}
```

## `infra/outputs.tf`

```hcl
output "service_url" {
  description = "Cloud Run の公開 URL"
  value       = google_cloud_run_v2_service.api.uri
}

# リポジトリまでのパス。イメージのフルパスは "${repository_url}/api:<tag>"。
output "repository_url" {
  description = "docker push と #7 の gcloud run deploy が使う Artifact Registry のリポジトリパス"
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${var.repository_id}"
}

output "runtime_service_account_email" {
  description = "#6 で deploy SA に roles/iam.serviceAccountUser を張る対象"
  value       = google_service_account.run_runtime.email
}
```

## `justfile` の変更

`justfile:67` のコメントが「Artifact Registry のパスは #5 で決める」と宣言しているので、
ここを実値に変える。`container := "go-todo"` は既にパスと分離してあるため
`docker-run` は無変更で動く（`justfile:70-73` のコメントがこの変更を見越している）。

```just
image := "asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo/api"
```

追加するレシピ:

```just
[working-directory('infra')]
tf-init:
    terraform init

[working-directory('infra')]
tf-plan:
    terraform plan

[working-directory('infra')]
tf-apply:
    terraform apply

# 初回だけ使う。Cloud Run は実在するイメージを要求するが、AR はまだ空。
# AR を先に作り、イメージを push してから残りを apply する。
[working-directory('infra')]
tf-apply-registry:
    terraform apply -target=google_artifact_registry_repository.app

# docker-build が linux/amd64 でタグ無し（= :latest）を作るので、
# ここで目的のタグを付け直して push する。
docker-push tag="bootstrap": docker-build
    docker tag {{image}} {{image}}:{{tag}}
    docker push {{image}}:{{tag}}
```

## `.gitignore` の追加

```gitignore
# Terraform
# .terraform.lock.hcl は除外しない。コミットする。
.terraform/
*.tfstate
*.tfstate.*
*.tfvars
```

`*.tfvars` を無視するのは、「tfvars を作らない」方針を破ったときの保険。

## 実行手順（一度きり）

`#1` の `bootstrap.sh` と同じく **一度きりの記録**として読む。冪等化しない。

```sh
# 前提（#1 の bootstrap.sh 末尾で案内済み）
gcloud auth application-default set-quota-project taktiks2-go-todo

# Docker が AR に push できるようにする
gcloud auth configure-docker asia-northeast1-docker.pkg.dev

just tf-init

# lock ファイルに両プラットフォームのハッシュを入れる
terraform -chdir=infra providers lock \
  -platform=darwin_arm64 \
  -platform=linux_amd64

# 1 段階目: AR だけ作る
just tf-apply-registry

# イメージを置く
just docker-push

# 2 段階目: Cloud Run / Secret / IAM
just tf-apply
```

**なぜ `-target` を使うのか。** Cloud Run は実在するイメージを要求するが、
AR はこの issue で初めて作るので空。`-target` は HashiCorp がまさにこの種の
bootstrap のために用意した逃げ道であり、状態を壊さない。

**なぜ lock ファイルに linux_amd64 を入れるのか。** 現時点で #7 の CD は
terraform を回さない（docker build → AR → `gcloud run deploy`）ので、厳密には
不要。それでも入れるのは、lock ファイルがプラットフォーム別のハッシュを持つという
性質を知らないまま後日 Linux から `terraform init` して落ちると、**原因の調査から
始まる**種類の詰まりだから。コマンド 1 回で済む。

## 検証

完了条件の確認。結果は PR 本文に貼る。

```sh
# state が GCS に置かれている
gcloud storage ls "gs://taktiks2-go-todo-tfstate/infra/**"

# 公開 URL が引ける / max-instances が 3 / ランタイム SA が効いている
terraform -chdir=infra output
gcloud run services describe go-todo-api --region asia-northeast1 \
  --format='value(status.url, spec.template.spec.serviceAccountName, spec.template.metadata.annotations["autoscaling.knative.dev/maxScale"])'

# 公開 URL が実際に JSON を返す
curl -s "$(terraform -chdir=infra output -raw service_url)/healthz" | jq
```

期待する結果:

- `default.tfstate` が `gs://taktiks2-go-todo-tfstate/infra/` の下にある
- `terraform output` が `service_url` / `repository_url` / `runtime_service_account_email` の 3 つを返す
- `maxScale` が `3`、`serviceAccountName` が `go-todo-run@...`
- `curl` が 200 で `{"status":"ok"}` 相当を返す

**`gcloud run services describe` は v2 で作ったサービスでも Knative v1 の形で返す**ため、
issue 本文の `spec.template.metadata.annotations` はそのまま使える。空が返る場合だけ
`--format=yaml` に切り替えて実際のキーを見る。`gcloud storage buckets describe` の
フィールド名で #1 が踏んだのと同じ種類の罠なので、値が空でも「設定が効いていない」と
即断しない。

`curl` を完了条件に足すのは、`#4` の Dockerfile が Cloud Run で本当に動くこと・
`PORT` の注入が効くこと・`allUsers` の IAM が効くことを **この issue の時点で**
証明するため。#7 に持ち越すと、失敗したときに Terraform と workflow のどちらが
原因かを切り分ける手間が乗る。

## ドキュメントと issue の更新（同じ PR 内）

`CONTRIBUTING.md` §8 に従い、別 PR に切り出さない。

- `docs/DESIGN.md` §9 IaC — #5 の決定値（AR パス / サービス名 / ランタイム SA /
  cleanup policy）、`ignore_changes` に `client` を足した理由、2 段階 apply
- `docs/DESIGN.md` §14 リスク — **「Cloud Run の無料枠にリージョン制限があるか未確認」を
  解決済みに書き換える。** Tokyo は Tier 1 で、free tier は Tier 1 価格ベースの
  spending based discount。`asia-northeast1` 前提は維持
- `docs/DESIGN.md` §14 リスク — Artifact Registry の項に cleanup policy を入れたことを追記
- **issue #7 本文** — 「手動でやること: Cloud Run の未認証呼び出しを許可する」を削除
- `justfile:67` のコメントを実値に更新

## 作業の流れ

```sh
gh issue develop 5 --name 5-terraform-cloud-run --checkout   # 実施済み
# infra/*.tf と justfile / .gitignore を書く
# 人間が gcloud auth ... と just tf-* / just docker-push を実行
# 検証コマンドを実行して結果を控える
# docs/DESIGN.md と issue #7 本文を更新
# /code-review → PR（squash merge）
```

`mode:ai-only` なので Claude がファイルを書く。GCP 側の実行は人間。

## スコープ外

- Workload Identity Federation（#6）。ランタイム SA への `roles/iam.serviceAccountUser`
  付与もそちらで行う
- CI/CD workflow（#7）
- Secret の**値**。`google_secret_manager_secret_version` は書かない
- Cloud Run の env への `DATABASE_URL` の接続（Phase 2）
- Firebase 関連（`docs/DESIGN.md` §9 の方針どおり Terraform 管理外）
- `google_project_service` による API 管理（#1 の `bootstrap.sh` が担当）
- `flake.nix` への terraform / gcloud の追加（後述）

## 調査で確定した前提（2026-08-16 時点）

| 事実 | 影響 |
|---|---|
| Cloud Run の free tier は「Tier 1 価格ベースの spending based discount」として適用され、**`asia-northeast1 (Tokyo)` は Tier 1 リージョン**に含まれる | **#1 が残した「無料枠のリージョン制限が未確認」が解決。`asia-northeast1` のままでよい。** 「US 3 リージョン限定」は Cloud Storage の規則で、二次情報の誤り |
| `hashicorp/google` の最新は 7.44.0（2026-08-11）。8.0 は未リリース | #1 の申し送り `~> 7.0` をそのまま採用 |
| Terraform core の最新安定版は 1.15.8、ローカルは 1.14.9 | `required_version` は下限だけ（`>= 1.14`） |
| nixpkgs の `terraform` は BUSL で unfree（`meta.license.free = false`） | `flake.nix` に入れない。terraform / gcloud は system（dotfiles）供給のまま |
| `.terraform.lock.hcl` はプラットフォーム別のハッシュを持つ | darwin_arm64 + linux_amd64 の両方を入れてコミットする |
| `google_cloud_run_v2_service.deletion_protection` の既定は **true** | 明示的に `false` にする。destroy だけでなく再作成を伴う apply も落ちる |
| `max_instance_request_concurrency` は未指定でも CPU >= 1 なら 80 | 既定と同値だが明示する |
| `gcloud run deploy` は `client` / `client_version` を書き換える | `ignore_changes` に足す |
| Cloud Run はリビジョン起動時に secret を解決する | version の無い secret を env に繋ぐとリビジョンが Ready にならない。Phase 2 まで繋がない |
| AR からの pull は Cloud Run のサービスエージェントが行い、API 有効化時に `artifactregistry.reader` を自動で持つ | 追加の IAM は要らない |
| AR の cleanup policy は KEEP が DELETE より優先される | `keep_count = 5` が最後の砦になる |
| AR の cleanup 条件 `tag_state = "UNTAGGED"` は #7 の CD（SHA タグ）では発火しない | `older_than` で切る |
| AR の cleanup の duration は API が秒に正規化する | `"2592000s"` と書く。`"30d"` は plan の差分要因になりうる |
| Cloud Run v2 の既定は 1 vCPU / 512 MiB / リクエスト処理中のみ CPU 割り当て | `resources` を書かない |
| `google_cloud_run_v2_service` には `invoker_iam_disabled` もある | 組織ポリシーで `allUsers` が禁じられる環境向けの回避策。個人プロジェクトには不要 |
| `backend` ブロックは変数を受け付けない | バケット名だけ `versions.tf` に直書き |
| `compute.googleapis.com` は #1 で有効化していない | Compute 既定 SA が存在しない可能性が高い。ランタイム SA を自前で作る根拠 |
| `just docker-build` は既に `--platform linux/amd64` を付けている（`justfile:97`） | push 側で追加対応は不要 |

## 却下した選択肢

| 却下したもの | 理由 |
|---|---|
| プレースホルダイメージ（`us-docker.pkg.dev/cloudrun/container/hello`）で 1 回 apply | apply は 1 回で済むが、#4 の Dockerfile が Cloud Run で動く検証が #7 まで持ち越しになる。失敗したとき Terraform と workflow のどちらが原因かの切り分けが乗る |
| AR を `gcloud` で作って `import` する | IaC の外側に手作業が増え、`import` の工程も乗る。得るものは apply 1 回分 |
| `google_project_service` で API を管理する | #1 で `gcloud` 済み。二重管理になり、`destroy` で API を無効化する事故がある |
| `terraform.tfvars` を作る | 「Secret を tfvars に書かない」を規律ではなく構造で守る。環境分離をしない以上、定数 4 つで足りる |
| `invoker_iam_disabled = true` で公開する | 組織ポリシーで `allUsers` が禁じられる環境向けの回避策。個人プロジェクトには不要で、`allUsers` + `run.invoker` の方が他クラウドにも転用が利く知識 |
| Secret の値を Terraform で入れる | state に平文で残る（`docs/DESIGN.md` §9） |
| `DATABASE_URL` を今から Cloud Run の env に繋ぐ | version が無いのでリビジョンが Ready にならず、Phase 0 の完了条件に届かない |
| `flake.nix` に terraform / gcloud を足す | nixpkgs の terraform は BUSL で unfree。`allowUnfree` の設定が要るうえ、devShell の役割（Go のツールチェーン供給）から外れる。バージョンの下限は `required_version` が守る |
| Terraform をモジュール化する | リソースは 6 個。モジュールにする理由が無く、間接参照が増えるだけ |
| `deletion_protection` を既定の true のまま残す | 作り直しのたびに「外すためだけの apply」が 1 回挟まる。Phase 0 に失うデータは無い |
| Cloud Run のリージョンを `us-central1` に移す | Tokyo は Tier 1 で無料枠が満額効く。レイテンシを捨てる理由が無くなった |
| `:latest` タグを使う | #7 の CD が SHA タグを打つ設計と混ざり、「今動いているのはどのコミットか」がレジストリから読めなくなる |
