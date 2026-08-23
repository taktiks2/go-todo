# Terraform で Artifact Registry / Cloud Run / Secret Manager を作る 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **注記（実装後に追記）:** この計画書は実行時の記録であり、本文は書き換えていない。
> 確定した最終形は `docs/DESIGN.md` §9・§14 と `infra/` を見ること。以下の本文が
> `/healthz` と書いている箇所は、実際に配備されたコードでは `/api/healthz` を指す。

**Goal:** `infra/` に Terraform を書き、GCS backend で state を管理しながら Artifact Registry・Cloud Run・Secret Manager の箱を作り、公開 URL が `/healthz` の JSON を返すところまで通す。

**Architecture:** リソースは 6 個。モジュール化しない。Cloud Run は実在するイメージを要求するが Artifact Registry はこの issue で初めて作るため、`-target` で AR だけ先に apply → イメージを push → 残りを apply という 2 段階の bootstrap を踏む。イメージのバージョン管理は Terraform の責務ではないので、`image` と `client` / `client_version` は `lifecycle.ignore_changes` で CD（#7）に譲る。

**Tech Stack:** Terraform（core `>= 1.14`、ローカルは 1.14.9）/ `hashicorp/google ~> 7.0`（最新 7.44.0）/ `gcloud` / Docker Desktop / `just`

**Spec:** `docs/superpowers/specs/2026-08-16-terraform-cloud-run-design.md`

## Global Constraints

すべてのタスクの要件に、以下が暗黙に含まれる。

- `PROJECT_ID` は `taktiks2-go-todo`。**変更不可**
- state バケットは `gs://taktiks2-go-todo-tfstate`、prefix は `infra`
- Cloud Run / Artifact Registry のリージョンは `asia-northeast1`
- Cloud Run のサービス名は `go-todo-api`
- Artifact Registry のリポジトリ ID は `go-todo`。イメージのフルパスは `asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo/api`
- ランタイム SA の `account_id` は `go-todo-run`
- Secret の `secret_id` は `database-url`。**値（`google_secret_manager_secret_version`）は絶対に書かない**
- provider は `hashicorp/google` の `~> 7.0`、`required_version` は `>= 1.14`
- `terraform.tfvars` を**作らない**
- `google_project_service` を**書かない**（API 有効化は #1 の `scripts/bootstrap.sh` が唯一の記録）
- コミットは Conventional Commits。scope は `infra`
- **Go のコードには一切触らない**
- 各タスクの最後に `terraform fmt -recursive` を通してからコミットする
- GCP に実物を作るコマンド（`gcloud` / `terraform apply` / `docker push`）は**人間が実行する**。Claude はファイルを書き、コマンドを提示して結果を待つ

## 前提条件（Task 1 の前に人間が済ませる）

```sh
gcloud auth application-default set-quota-project taktiks2-go-todo
gcloud auth configure-docker asia-northeast1-docker.pkg.dev
```

1 つ目は #1 の `scripts/bootstrap.sh` 末尾が案内しているもの。2 つ目は Task 3 の `docker push` に要る。

---

### Task 1: `infra/` の骨格と `terraform init`

Terraform が GCS backend に繋がり、provider が固定される状態を作る。リソースはまだ 1 つも書かない。

**Files:**
- Create: `infra/versions.tf`
- Create: `infra/providers.tf`
- Create: `infra/variables.tf`
- Modify: `.gitignore`（末尾に追記）
- Generate: `infra/.terraform.lock.hcl`（`terraform providers lock` が作る。**コミットする**）

**Interfaces:**
- Consumes: #1 が作った `gs://taktiks2-go-todo-tfstate`
- Produces: `var.project_id` / `var.region` / `var.repository_id` / `var.service_name` — 以降の全タスクが参照する

- [ ] **Step 1: `infra/versions.tf` を作る**

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

- [ ] **Step 2: `infra/providers.tf` を作る**

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

- [ ] **Step 3: `infra/variables.tf` を作る**

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

- [ ] **Step 4: `.gitignore` に Terraform の除外を追記**

`.gitignore` の末尾（`# Editor / OS` ブロックの後）に足す。

```gitignore

# Terraform
# .terraform.lock.hcl は除外しない。コミットして provider のハッシュを固定する。
# *.tfvars は「作らない」方針を破ったときの保険（docs/DESIGN.md §9）。
.terraform/
*.tfstate
*.tfstate.*
*.tfvars
```

- [ ] **Step 5: `terraform init` を実行して backend の接続を確認する（人間）**

```sh
terraform -chdir=infra init
```

期待する結果:
- `Successfully configured the backend "gcs"!` が出る
- `Installing hashicorp/google v7.44.0...`（またはそれ以降の 7.x）が出る
- `Terraform has been successfully initialized!` で終わる

落ちたときの対処:
- `Failed to get existing workspaces: querying Cloud Storage failed` → ADC が無い。`gcloud auth application-default login` からやり直す
- `403 ... does not have storage.objects.list access` → quota project が未設定。前提条件の 1 つ目を実行する

- [ ] **Step 6: lock ファイルに両プラットフォームのハッシュを入れる（人間）**

```sh
terraform -chdir=infra providers lock \
  -platform=darwin_arm64 \
  -platform=linux_amd64
```

期待する結果: `infra/.terraform.lock.hcl` の `hashes` に `h1:` エントリが複数並ぶ。

現時点で #7 の CD は terraform を回さないので厳密には不要だが、lock ファイルが
プラットフォーム別ハッシュを持つ性質を知らないまま後日 Linux から `init` して落ちると
原因調査から始まることになる。コマンド 1 回で済む。

- [ ] **Step 7: 構文と整形を確認する**

```sh
terraform -chdir=infra validate
terraform -chdir=infra fmt -check -recursive
```

期待する結果: `Success! The configuration is valid.` と、`fmt -check` が無出力（差分なし）。
`fmt -check` が差分を出したら `terraform -chdir=infra fmt -recursive` を実行してから進む。

- [ ] **Step 8: コミット**

```bash
git add infra/versions.tf infra/providers.tf infra/variables.tf infra/.terraform.lock.hcl .gitignore
git commit -m "chore(infra): Terraform の骨格と GCS backend を追加"
```

---

### Task 2: Artifact Registry

イメージの置き場を作る。Cloud Run より先に実在させる必要がある唯一のリソース。

**Files:**
- Create: `infra/artifact_registry.tf`

**Interfaces:**
- Consumes: `var.region` / `var.repository_id`（Task 1）
- Produces: `google_artifact_registry_repository.app` — Task 4 の `depends_on` が参照する。レジストリのパスは `asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo`

- [ ] **Step 1: `infra/artifact_registry.tf` を作る**

```hcl
# Cloud Run と同じリージョンに置く。pull がリージョン内で完結し、
# コールドスタート時間に効く（docs/DESIGN.md §9）。
# AR の 0.5 GB 無料枠にリージョン制限は無い。
resource "google_artifact_registry_repository" "app" {
  location      = var.region
  repository_id = var.repository_id
  format        = "DOCKER"
  description   = "go-todo のコンテナイメージ"

  # dry run にしない。今このリポジトリにイメージは 0 個で、下の keep-recent が
  # 直近 5 世代を無条件に守るため、有効にしても消えるものが存在しない。
  # cleanup policy は非同期（およそ日次）で走るので dry run のログを PR の中で
  # 確認する術も無い。「入れたが効いていない」状態で放置する方が危険。
  cleanup_policy_dry_run = false

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

- [ ] **Step 2: plan で 1 リソースだけ増えることを確認する**

```sh
terraform -chdir=infra validate
terraform -chdir=infra plan
```

期待する結果: `Plan: 1 to add, 0 to change, 0 to destroy.`
`google_artifact_registry_repository.app` 以外が出たら Task 1 の内容を見直す。

- [ ] **Step 3: AR だけを apply する（人間）**

```sh
terraform -chdir=infra apply -target=google_artifact_registry_repository.app
```

期待する結果: `Apply complete! Resources: 1 added, 0 changed, 0 destroyed.`

末尾に `Warning: Applied changes may be incomplete` が出るが、これは `-target` を
使った以上正しい警告なので無視してよい。Cloud Run は実在するイメージを要求するのに
AR がまだ空、という鶏と卵を解くための意図的な 2 段階の 1 段目。

- [ ] **Step 4: リポジトリが存在することを確認する（人間）**

```sh
gcloud artifacts repositories describe go-todo \
  --location=asia-northeast1 \
  --format='value(name,format,cleanupPolicyDryRun)'
```

期待する結果: `projects/taktiks2-go-todo/locations/asia-northeast1/repositories/go-todo`、`DOCKER`、`False`。

- [ ] **Step 5: コミット**

```bash
terraform -chdir=infra fmt -recursive
git add infra/artifact_registry.tf
git commit -m "feat(infra): Artifact Registry と cleanup policy を追加"
```

---

### Task 3: `justfile` の更新とイメージの push

`justfile:67` のコメントが「Artifact Registry のパスは #5 で決める」と宣言しているので、それを実値にする。あわせて Terraform の入口を作り、`:bootstrap` タグのイメージを AR に置く。

**Files:**
- Modify: `justfile:67-68`（`image` 変数とその上のコメント）
- Modify: `justfile`（末尾に `docker-push` と `tf-*` レシピを追加）

**Interfaces:**
- Consumes: `google_artifact_registry_repository.app`（Task 2。apply 済みであること）、既存の `docker-build`（`justfile:96-98`）
- Produces: `asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo/api:bootstrap` — Task 4 の Cloud Run が初回作成時に pull する

- [ ] **Step 1: `justfile:67-68` の `image` を AR のパスに変える**

現在:

```just
# ローカルのイメージ名。Artifact Registry のパスは #5 で決める。
image := "go-todo"
```

変更後:

```just
# Artifact Registry のイメージパス（#5 で確定）。
# docker-build がここにタグを打ち、docker-push がそのまま押し上げる。
image := "asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo/api"
```

`container := "go-todo"`（`justfile:73`）は触らない。`/` を含む名前を `docker run --name` が
受け付けないため別に持つ、と `justfile:70-73` のコメントが既にこの変更を見越している。

- [ ] **Step 2: `docker-push` レシピを `docker-run` の後（ファイル末尾）に追加**

```just

# docker-build はタグ無し（= :latest）で作るので、ここで目的のタグを付け直す。
# 初回は :bootstrap。Cloud Run の初回作成が pull するのはこれ 1 つだけで、
# 以降のイメージ更新は #7 の CD が SHA タグで行う（lifecycle.ignore_changes）。
#
# :latest を本番のタグとして使わない。#7 が SHA タグを打つ設計と混ざると
# 「今動いているのはどのコミットか」がレジストリから読めなくなる。

# ビルドしたイメージを Artifact Registry に push する
docker-push tag="bootstrap": docker-build
    docker tag {{image}} {{image}}:{{tag}}
    docker push {{image}}:{{tag}}
```

- [ ] **Step 3: Terraform のレシピを `docker-push` の後に追加**

```just

# Terraform の入口。infra/ の中で走らせる。
#
# tf-apply-registry だけは初回専用。Cloud Run は実在するイメージを要求するが
# Artifact Registry はこの issue で初めて作るので空、という鶏と卵を解くために
# AR だけ先に apply する。2 回目以降は tf-apply だけでよい。

# Terraform を初期化する
[working-directory('infra')]
tf-init:
    terraform init

# 差分を確認する
[working-directory('infra')]
tf-plan:
    terraform plan

# 差分を適用する
[working-directory('infra')]
tf-apply:
    terraform apply

# 初回だけ: Artifact Registry を先に作る
[working-directory('infra')]
tf-apply-registry:
    terraform apply -target=google_artifact_registry_repository.app
```

- [ ] **Step 4: レシピが読めることを確認する**

```sh
just --list
```

期待する結果: `docker-build` / `docker-push` / `docker-run` / `tf-init` / `tf-plan` / `tf-apply` / `tf-apply-registry` が並ぶ。
`just: error: Unknown attribute` が出たら `just` のバージョンが古い（`working-directory` 属性は既に `justfile:95` で使われているので、通常は起きない）。

- [ ] **Step 5: イメージをビルドして push する（人間）**

```sh
just docker-push
```

期待する結果:
- `arch: amd64  size: 8.3 MB` 相当が出る（`docker-build` の最終行）
- `docker push` が `bootstrap: digest: sha256:... size: ...` で終わる

落ちたときの対処:
- `denied: Permission "artifactregistry.repositories.uploadArtifacts" denied` → `gcloud auth configure-docker asia-northeast1-docker.pkg.dev` が未実行
- `name unknown: Repository "go-todo" not found` → Task 2 の apply が未実行

- [ ] **Step 6: AR にイメージが載ったことを確認する（人間）**

```sh
gcloud artifacts docker images list \
  asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo/api \
  --include-tags \
  --format='value(package,tags)'
```

`--include-tags` が無いと tags 列が黙って空になる。

期待する結果: `.../go-todo/api` と `bootstrap` の 1 行。

- [ ] **Step 7: コミット**

```bash
git add justfile
git commit -m "chore(infra): justfile に Artifact Registry のパスと tf-* / docker-push を追加"
```

---

### Task 4: Cloud Run（ランタイム SA / サービス / 公開設定）と outputs

このタスクが終わると **公開 URL が `/healthz` の JSON を返す**。Phase 0 の完了条件のうち #7 に残るのは「CI/CD 経由で」の部分だけになる。

**Files:**
- Create: `infra/cloud_run.tf`
- Create: `infra/outputs.tf`

**Interfaces:**
- Consumes: `var.project_id` / `var.region` / `var.service_name` / `var.repository_id`（Task 1）、`google_artifact_registry_repository.app`（Task 2）、`:bootstrap` タグのイメージ（Task 3）
- Produces:
  - `google_service_account.run_runtime` — Task 5 の `secretAccessor` が参照。`.email` は `go-todo-run@taktiks2-go-todo.iam.gserviceaccount.com`
  - `google_cloud_run_v2_service.api` — `.uri` が公開 URL
  - output `service_url` / `repository_url` / `runtime_service_account_email` — #6 と #7 が参照する

- [ ] **Step 1: `infra/cloud_run.tf` を作る**

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
    # Phase 2 で値を入れるときに参照ごと足す。
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

- [ ] **Step 2: `infra/outputs.tf` を作る**

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

- [ ] **Step 3: plan で 3 リソースが増えることを確認する**

```sh
terraform -chdir=infra validate
terraform -chdir=infra plan
```

期待する結果: `Plan: 3 to add, 0 to change, 0 to destroy.`
（`google_service_account.run_runtime` / `google_cloud_run_v2_service.api` / `google_cloud_run_v2_service_iam_member.public`）

`google_artifact_registry_repository.app` が `to change` に出たら、Task 2 で `-target` を
使った apply が取りこぼした差分。内容を読んで意図どおりなら進めてよい。

- [ ] **Step 4: apply する（人間）**

```sh
just tf-apply
```

期待する結果: `Apply complete! Resources: 3 added, 0 changed, 0 destroyed.` の後に
`service_url` / `repository_url` / `runtime_service_account_email` が表示される。

落ちたときの対処:
- `Revision ... is not ready and cannot serve traffic. Image ... not found` → Task 3 の push が未完了
- `The user-provided container failed to start and listen on the port defined by the PORT environment variable` → イメージが arm64。`just docker-build` の `--platform linux/amd64` が効いているか、`docker image inspect` の `arch:` 行で確認する

- [ ] **Step 5: 公開 URL が JSON を返すことを確認する（人間）**

```sh
curl -s "$(terraform -chdir=infra output -raw service_url)/healthz" | jq
```

期待する結果: `{"status":"ok"}` 相当が返る。

これが通ると、#4 の Dockerfile が Cloud Run で動くこと・`PORT` の注入が効くこと・
`allUsers` の IAM が効くことが**この issue の時点で**同時に証明される。

- [ ] **Step 6: サービスの設定値を確認する（人間）**

```sh
gcloud run services describe go-todo-api --region asia-northeast1 \
  --format='value(status.url, spec.template.spec.serviceAccountName, spec.template.metadata.annotations["autoscaling.knative.dev/maxScale"])'
```

期待する結果: 公開 URL / `go-todo-run@taktiks2-go-todo.iam.gserviceaccount.com` / `3`。

`gcloud run services describe` は v2 で作ったサービスでも Knative v1 の形で返すため、
このキーで引ける。空が返る場合だけ `--format=yaml` に切り替えて実際のキーを見る。
`gcloud storage buckets describe` のフィールド名で #1 が踏んだのと同じ種類の罠なので、
**値が空でも「設定が効いていない」と即断しない。**

- [ ] **Step 7: コミット**

```bash
terraform -chdir=infra fmt -recursive
git add infra/cloud_run.tf infra/outputs.tf
git commit -m "feat(infra): Cloud Run サービスとランタイム SA を追加"
```

---

### Task 5: Secret Manager の箱

Phase 2 で `DATABASE_URL` を入れる先を用意する。**値は入れない。**

**Files:**
- Create: `infra/secrets.tf`

**Interfaces:**
- Consumes: `google_service_account.run_runtime`（Task 4）
- Produces: `google_secret_manager_secret.database_url` — Phase 2 が `google_secret_manager_secret_version` ではなく `gcloud` で値を入れる先

- [ ] **Step 1: `infra/secrets.tf` を作る**

```hcl
# 箱だけ作る。値（version）は Phase 2 で gcloud から入れる。
# tfvars にも state にも平文を置かないため（docs/DESIGN.md §9）。
resource "google_secret_manager_secret" "database_url" {
  secret_id = "database-url"

  replication {
    auto {}
  }
}

# Phase 2 は「version を入れて Cloud Run から参照する」だけで済むように、
# 権限は今のうちに張っておく。version が無い間、この binding は何もしない。
resource "google_secret_manager_secret_iam_member" "runtime_accessor" {
  secret_id = google_secret_manager_secret.database_url.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.run_runtime.email}"
}
```

- [ ] **Step 2: plan で 2 リソースが増えることを確認する**

```sh
terraform -chdir=infra validate
terraform -chdir=infra plan
```

期待する結果: `Plan: 2 to add, 0 to change, 0 to destroy.`

**`google_secret_manager_secret_version` が plan に出てはならない。** 出ていたら
書きすぎている。値を Terraform で入れると state に平文で残る。

- [ ] **Step 3: apply する（人間）**

```sh
just tf-apply
```

期待する結果: `Apply complete! Resources: 2 added, 0 changed, 0 destroyed.`

- [ ] **Step 4: 箱があって中身が空であることを確認する（人間）**

```sh
gcloud secrets describe database-url --format='value(name,replication.automatic)'
gcloud secrets versions list database-url
```

期待する結果:
- 1 つ目が `projects/<番号>/secrets/database-url` を返す
- 2 つ目が `Listed 0 items.` を返す ← **version が 0 件であることが要件**

- [ ] **Step 5: コミット**

```bash
terraform -chdir=infra fmt -recursive
git add infra/secrets.tf
git commit -m "feat(infra): Secret Manager に database-url の箱を追加"
```

---

### Task 6: ドキュメントと issue の更新

`CONTRIBUTING.md` §8「設計判断が変わったら、それを起こした PR の中で `docs/DESIGN.md` を直す」に従い、別 PR に切り出さない。

**Files:**
- Modify: `docs/DESIGN.md`（§9 IaC: Terraform、§14 リスク）
- Modify: `docs/superpowers/specs/2026-08-16-terraform-cloud-run-design.md`（cleanup policy の判断）
- Update: issue #7 の本文（`gh` 経由）

**Interfaces:**
- Consumes: Task 1〜5 で確定した値すべて

- [ ] **Step 1: `docs/DESIGN.md` §9 IaC に `#5 で確定` の節を追記する**

`#### 決定値（#1 で確定）` の表の後、`**state バケットだけ ...**` の段落の前に、
以下をそのまま挿入する。

```markdown
#### 決定値（#5 で確定）

| 項目 | 値 |
|---|---|
| Cloud Run サービス名 | `go-todo-api` |
| Cloud Run / AR のリージョン | `asia-northeast1` |
| イメージのパス | `asia-northeast1-docker.pkg.dev/taktiks2-go-todo/go-todo/api` |
| ランタイム SA | `go-todo-run@taktiks2-go-todo.iam.gserviceaccount.com` |
| Secret の箱 | `database-url`（値は Phase 2 で `gcloud` から） |
| state の prefix | `infra` |

**`ignore_changes` は `image` だけでは足りない。** #7 の `gcloud run deploy` は
デプロイのたびに `client` / `client_version` を書き換える。除外しないと次の
`terraform plan` が毎回それを戻そうとして差分を出す。「インフラの形は Terraform、
動くバージョンは CD」という責務分割は、**CD が触るフィールドを全部除外して初めて成立する。**

**初回だけ apply が 2 段階になる。** Cloud Run は実在するイメージを要求するが、
Artifact Registry はこの issue で初めて作るので空。`-target` で AR だけ先に apply し、
`just docker-push` でイメージを置いてから残りを apply する。`scripts/bootstrap.sh` と
同じく一度きりの手順で、冪等化しない。

**ランタイム SA を自前で作る。** `template.service_account` を書かないと Compute Engine
の既定 SA が使われるが、`compute.googleapis.com` は #1 で有効化していないため既定 SA
自体が存在しない可能性が高い。存在したとしても Editor 権限で過剰。

**Phase 0 では `DATABASE_URL` を Cloud Run に繋がない。** Cloud Run は**リビジョン起動時に
secret を解決する**ので、version の入っていない secret を参照した瞬間にリビジョンが
Ready にならず、公開 URL が JSON を返さなくなる。箱と `secretAccessor` だけ先に用意し、
参照は Phase 2 で値を入れるときに足す。
```

- [ ] **Step 2: `docs/DESIGN.md` §14 リスクの Cloud Run 無料枠の項を書き換える**

現在の記述（`docs/DESIGN.md:780`）:

```markdown
- **Cloud Run の無料枠にリージョン制限があるか未確認**。公式の Free Tier ページは Cloud Storage にだけ「US リージョンのみ」と明記し、Cloud Run には書いていないが、二次情報は US 3 リージョン限定と主張している。**#5 でリージョンを確定する前に公式ページで確認する。** US 限定なら `asia-northeast1` 前提そのものを見直すことになる。
```

これを次の内容に置き換える。

```markdown
- ~~**Cloud Run の無料枠にリージョン制限があるか未確認**~~ → **#5 で解決。`asia-northeast1` のままでよい。** 公式の Cloud Run 料金ページは無料枠を「**Tier 1 価格ベースの spending based discount**」として適用すると明記しており、`asia-northeast1 (Tokyo)` は Tier 1 リージョンの一覧に含まれる。したがってリージョンによる無料枠の喪失は起きない。「US 3 リージョン限定」は **Cloud Storage の規則**であって Cloud Run のものではなく、それを Cloud Run に当てはめた二次情報が誤っていた。
```

- [ ] **Step 3: `docs/DESIGN.md` §14 リスクの Artifact Registry の項に cleanup policy を追記**

`docs/DESIGN.md:779` の末尾（`...古いイメージが無限に積むこと自体を止めたい）。`）に続けて、
#5 で入れた実際のポリシーを 1 文で足す。

```markdown
  #5 で入れたのは「直近 5 世代を無条件に KEEP、30 日より古いものを DELETE」の 2 本（`infra/artifact_registry.tf`）。KEEP が DELETE より優先されるため、巻き戻し先は常に 5 つ残る。
```

- [ ] **Step 4: spec の cleanup policy の記述を実装に合わせる**

`docs/superpowers/specs/2026-08-16-terraform-cloud-run-design.md` は
`cleanup_policy_dry_run = true` で始めて後から `false` にする、と書いてある。
実装では最初から `false` にしたので、spec 側の 2 箇所（`artifact_registry.tf` の
コード例のコメントと、決定事項の表の周辺）を実装に合わせ、理由を残す。

> dry run で始めない。リポジトリのイメージは 0 個で `keep-recent` が直近 5 世代を
> 無条件に守るため、有効にしても消えるものが存在しない。cleanup policy は非同期
> （およそ日次）で走るので dry run のログを PR の中で確認する術も無い。
> 「入れたが効いていない」状態で放置する方が危険。

- [ ] **Step 5: issue #7 の本文から「未認証呼び出しの許可」を削除する（人間または `gh`）**

`allUsers` への `roles/run.invoker` は #5 の Terraform に入ったので、#7 の
`## 手動でやること` から次の 1 行を消す。

```markdown
- Cloud Run サービスの未認証呼び出しを許可する（`roles/run.invoker` を `allUsers` に）
```

行が消えて `## 手動でやること` が空になるなら、セクションごと削除する。

```sh
gh issue view 7 --json body -q .body > /tmp/issue7.md
# 該当行を消す
gh issue edit 7 --body-file /tmp/issue7.md
```

- [ ] **Step 6: 変更が期待どおりか確認する**

```sh
git diff --stat
gh issue view 7 --json body -q .body | grep -c 'allUsers' || echo 0
```

期待する結果: `docs/DESIGN.md` と spec が変更されており、2 つ目のコマンドが `0`（該当なし）を返す。

- [ ] **Step 7: コミット**

```bash
git add docs/DESIGN.md docs/superpowers/specs/2026-08-16-terraform-cloud-run-design.md
git commit -m "docs: Terraform の実装結果を DESIGN.md に反映"
```

---

## 完了後の確認（PR の前に）

`CONTRIBUTING.md` §6 の品質ゲート。3 つとも実行する。

```sh
# 1. issue #5 の完了条件
terraform -chdir=infra plan                       # → No changes. Your infrastructure matches the configuration.
gcloud storage ls "gs://taktiks2-go-todo-tfstate/infra/**"
terraform -chdir=infra output
curl -s "$(terraform -chdir=infra output -raw service_url)/healthz" | jq

# 2. Go 側を壊していないこと
just test

# 3. /code-review を走らせる
```

PR 本文には `terraform output` と `curl` の実際の出力を貼る（`CONTRIBUTING.md` §5）。

**`terraform plan` が `No changes.` にならない場合は原因を潰してからマージする。** 特に
`client` / `client_version` の差分が出るなら `ignore_changes` の指定漏れで、#7 の CD が
動き始めた後に毎回 plan が汚れることになる。
