# Workload Identity Federation を設定する 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** GitHub Actions が SA キー JSON なしで GCP に認証できる経路を Terraform で作り、実際に OIDC 交換が通ることを一度だけ確認してから閉じる。

**Architecture:** リソースは 7 個（プール / プロバイダ / デプロイ SA / IAM binding 4 本）。`docs/DESIGN.md` §9 の 3 ステップ——プールを作る → リポジトリを条件に紐づける → SA への借用を許可する——をそのままファイルの並びにする。入口（`attribute_condition`）は GitHub の数値 ID で閉じ、IAM 側の principalSet は読みやすい名前で書く。プールとプロバイダは削除後 30 日 ID を再利用できないため `prevent_destroy` で塞ぐ。

**Tech Stack:** Terraform（core `>= 1.14`、ローカルは 1.14.9）/ `hashicorp/google ~> 7.0`（lock は 7.45.0）/ `gcloud` / `gh` / `just` / `google-github-actions/auth@v3` / `google-github-actions/setup-gcloud@v3`

**Spec:** `docs/superpowers/specs/2026-08-23-workload-identity-federation-design.md`

## Global Constraints

すべてのタスクの要件に、以下が暗黙に含まれる。

- `PROJECT_ID` は `taktiks2-go-todo`。リージョンは `asia-northeast1`（WIF プール自体は `global`）
- Workload Identity プール ID は `github`、プロバイダ ID は `github-actions`
- デプロイ SA の `account_id` は `go-todo-deploy`（ランタイム SA `go-todo-run` とは別物）
- GitHub の識別子は owner id `37180466` / repository id `1332914759` / `taktiks2/go-todo`
- **`google_service_account_key` を絶対に書かない。** キーを作らないことがこの issue の目的
- provider は `hashicorp/google ~> 7.0`、`required_version` は `>= 1.14`。`terraform.tfvars` は作らない
- `google_project_service` を書かない（API 有効化は #1 の `scripts/bootstrap.sh` が唯一の記録）
- **Go のコードには一切触らない**
- コミットは Conventional Commits。Terraform / justfile は scope `infra`、workflow は `ci`
- 各タスクの最後に `just tf-fmt` を通してからコミットする
- GCP と GitHub に実物を作るコマンド（`terraform apply` / `gh variable set` / `git push`）は**人間が実行する**。Claude はファイルを書き、コマンドを提示して結果を待つ
- ブランチは `6-workload-identity-federation`（作成済み。spec のコミットが 1 本入っている）

## 前提条件（Task 1 の前に確認する）

```sh
gcloud auth application-default print-access-token >/dev/null && echo ADC-OK
just tf-init
gh auth status
```

**`gcloud` の各コマンドに `--project=taktiks2-go-todo` を付けてある。** このマシンの
`core/project` は未設定で、省略すると `Failed to find attribute [project]` で落ちる
（実測）。`gcloud config set project taktiks2-go-todo` を一度打って省略する手もあるが、
グローバル設定を書き換えないほうを既定にする。

1 つ目は #1 の `bootstrap.sh` が案内した ADC が生きているかの確認。2 つ目は GCS backend の初期化（済んでいれば数秒で終わる）。3 つ目は Task 3 の `gh variable set` がリポジトリの管理権限を要求するため。

---

### Task 1: WIF プールとプロバイダ

OIDC の受け皿を作り、`taktiks2/go-todo` 以外のトークンを入口で弾く状態にする。まだ誰も何も借用できない。

**Files:**
- Create: `infra/wif.tf`
- Modify: `infra/variables.tf`（末尾に 3 変数を追記）
- Modify: `infra/outputs.tf`（末尾に 1 output を追記）

**Interfaces:**
- Consumes: `var.project_id`（`infra/variables.tf`、#5 で定義済み）
- Produces:
  - `google_iam_workload_identity_pool.github` — `.name` が `projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github`。Task 2 の principalSet が参照する
  - `google_iam_workload_identity_pool_provider.github` — `.name` がフルリソース名
  - `var.github_repository` / `var.github_owner_id` / `var.github_repository_id`
  - `output.workload_identity_provider` — Task 3 の `just gh-vars` が読む

- [ ] **Step 1: `infra/variables.tf` の末尾に GitHub の識別子を追記する**

```hcl
variable "github_repository" {
  description = "WIF が借用を許可する GitHub リポジトリ（owner/repo）"
  type        = string
  default     = "taktiks2/go-todo"
}

# 数値 ID を .tf に直書きすると意味が読めなくなるので変数に名前を付ける。
# 取得: gh api users/taktiks2 -q .id
variable "github_owner_id" {
  description = "GitHub オーナーの数値 ID。名前の再利用による成りすましを防ぐために使う"
  type        = string
  default     = "37180466"
}

# 取得: gh api repos/taktiks2/go-todo -q .id
variable "github_repository_id" {
  description = "GitHub リポジトリの数値 ID"
  type        = string
  default     = "1332914759"
}
```

- [ ] **Step 2: `infra/wif.tf` を作る（プールとプロバイダだけ）**

```hcl
# GitHub Actions が SA キー JSON なしで GCP に認証するための一式。
# docs/DESIGN.md §9 の 3 ステップがそのままこのファイルの並びになっている:
#   1. プールを作る
#   2. GitHub リポジトリを条件に紐づける（provider の attribute_condition）
#   3. SA への借用を許可する（workloadIdentityUser）

# 削除すると約 30 日のソフトデリートに入り、その間は同じ ID で作り直せない。
# このリポジトリの他のリソースは「壊して作り直す」前提（cloud_run.tf の
# deletion_protection = false）だが、WIF だけはその前提が成立しない。
# 消えると #7 の CD が 30 日止まるため prevent_destroy で塞ぐ。
#
# それでも作り直したくなったら、この ID を消すのではなく `github-2` のような
# 別 ID を足して output を差し替える。ソフトデリート中の ID は
# `gcloud iam workload-identity-pools undelete` で復活もできる。
resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "github"
  display_name              = "GitHub Actions"
  description               = "GitHub Actions の OIDC トークンを受ける"

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-actions"
  display_name                       = "GitHub Actions OIDC"

  # google.subject は必須。attribute.repository は Task 2 の principalSet が
  # 参照するので、ここに無いと binding 側が INVALID_ARGUMENT で落ちる。
  # **principalSet と CEL で使う属性は必ず mapping に載せる。**
  attribute_mapping = {
    "google.subject"       = "assertion.sub"
    "attribute.repository" = "assertion.repository"
  }

  # ここが唯一の入口。これを書かないと「GitHub の全リポジトリ」がトークンを
  # 交換できてしまう（Google 自身が spoofing 対策として必須と書いている）。
  #
  # 名前（repository / repository_owner）ではなく数値 ID で閉じる。
  # 名前ベースだと、リポジトリやアカウントを消したときに第三者が同名を取得して
  # 同じ条件を満たせる。数値 ID は再利用されない。
  #
  # ref（ブランチ）は条件に入れていない。入れると main 以外で走る検証 workflow が
  # 通らなくなる。main 限定にしたくなったら、この条件ではなく
  # workloadIdentityUser の principalSet を attribute.ref で分ける。
  attribute_condition = "assertion.repository_owner_id == '${var.github_owner_id}' && assertion.repository_id == '${var.github_repository_id}'"

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"

    # allowed_audiences は書かない。既定でこのプロバイダのフルリソース名が
    # audience になり、google-github-actions/auth はそれに合わせて OIDC トークンを
    # 要求する。明示すると両者がずれて落ちる余地が増えるだけ。
  }

  lifecycle {
    prevent_destroy = true
  }
}
```

- [ ] **Step 3: `infra/outputs.tf` の末尾に output を追記する**

```hcl
# projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github/providers/github-actions
# google-github-actions/auth の workload_identity_provider にこの形で渡す。
output "workload_identity_provider" {
  description = "リポジトリ変数 WIF_PROVIDER の値"
  value       = google_iam_workload_identity_pool_provider.github.name
}
```

- [ ] **Step 4: plan で 2 リソースだけ増えることを確認する**

```sh
just tf-validate
just tf-plan
```

期待する結果: `Plan: 2 to add, 0 to change, 0 to destroy.`
増えるのは `google_iam_workload_identity_pool.github` と `google_iam_workload_identity_pool_provider.github` の 2 つだけ。他が出たら既存 `.tf` を壊している。

- [ ] **Step 5: apply する（人間）**

```sh
just tf-apply
```

期待する結果: `Apply complete! Resources: 2 added, 0 changed, 0 destroyed.`

`Error 400: The attribute condition must reference one of the provider's claims` が出たら Step 2 の `attribute_mapping` と `attribute_condition` の綴りを見直す。

- [ ] **Step 6: 実物を確認する（人間）**

```sh
gcloud iam workload-identity-pools describe github \
  --project=taktiks2-go-todo --location=global --format='value(name,state)'

gcloud iam workload-identity-pools providers describe github-actions \
  --project=taktiks2-go-todo --location=global --workload-identity-pool=github \
  --format='value(name,attributeCondition,oidc.issuerUri)'
```

期待する結果:
- 1 つ目: `projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github` と `ACTIVE`
- 2 つ目: プロバイダのフルリソース名、`assertion.repository_owner_id == '37180466' && assertion.repository_id == '1332914759'`、`https://token.actions.githubusercontent.com`

- [ ] **Step 7: コミット**

```bash
just tf-fmt
git add infra/wif.tf infra/variables.tf infra/outputs.tf
git commit -m "feat(infra): Workload Identity プールとプロバイダを追加"
```

---

### Task 2: デプロイ SA と IAM binding

借用される側の ID を作り、「誰が借りられるか」と「借りたら何ができるか」を張る。ここまでで認証経路は完成する。

**Files:**
- Modify: `infra/wif.tf`（末尾に追記）
- Modify: `infra/outputs.tf`（末尾に 1 output を追記）

**Interfaces:**
- Consumes:
  - `google_iam_workload_identity_pool.github`（Task 1）
  - `google_artifact_registry_repository.app`（`infra/artifact_registry.tf`、#5）
  - `google_service_account.run_runtime`（`infra/cloud_run.tf`、#5）
- Produces:
  - `google_service_account.deploy` — `.email` が `go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com`
  - `output.deploy_service_account_email` — Task 3 の `just gh-vars` が読む

- [ ] **Step 1: `infra/wif.tf` の末尾に SA と binding を追記する**

```hcl
# CD が借用する ID。ランタイム SA（go-todo-run）とは役割が違うので分ける。
# キーは作らない——作らないことがこの issue の目的。
resource "google_service_account" "deploy" {
  account_id   = "go-todo-deploy"
  display_name = "go-todo GitHub Actions deployer"
}

# 3 ステップ目: このリポジトリの workflow だけが deploy SA を借用できる。
#
# 入口（attribute_condition）を数値 ID で閉じてあるので、ここは名前で書いてよい。
# 「どのリポジトリに貸しているか」を IAM ポリシー上で人間が読めることを優先する。
resource "google_service_account_iam_member" "deploy_wif_user" {
  service_account_id = google_service_account.deploy.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository/${var.github_repository}"
}

# gcloud run deploy に必要な最小。run.admin にしない——admin は setIamPolicy を
# 含み、CD が「未認証で公開するかどうか」を書き換えられてしまう。
# 公開設定は cloud_run.tf（Terraform）の責務。
resource "google_project_iam_member" "deploy_run_developer" {
  project = var.project_id
  role    = "roles/run.developer"
  member  = "serviceAccount:${google_service_account.deploy.email}"
}

# docker push 用。プロジェクト全体ではなくこのリポジトリだけに張る。
resource "google_artifact_registry_repository_iam_member" "deploy_writer" {
  location   = google_artifact_registry_repository.app.location
  repository = google_artifact_registry_repository.app.name
  role       = "roles/artifactregistry.writer"
  member     = "serviceAccount:${google_service_account.deploy.email}"
}

# gcloud run deploy --service-account go-todo-run@… は「その SA として振る舞う」
# 許可（actAs）を要求する。run.developer だけでは足りない。
#
# ランタイム SA というリソースに対して張る。プロジェクト単位で
# roles/iam.serviceAccountUser を渡すと、CD がプロジェクト内の全 SA を借用できる。
resource "google_service_account_iam_member" "deploy_act_as_runtime" {
  service_account_id = google_service_account.run_runtime.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.deploy.email}"
}
```

- [ ] **Step 2: `infra/outputs.tf` の末尾に output を追記する**

```hcl
output "deploy_service_account_email" {
  description = "リポジトリ変数 DEPLOY_SA の値"
  value       = google_service_account.deploy.email
}
```

- [ ] **Step 3: plan で 5 リソースだけ増えることを確認する**

```sh
just tf-validate
just tf-plan
```

期待する結果: `Plan: 5 to add, 0 to change, 0 to destroy.`
増えるのは SA 1 つと IAM binding 4 本。`google_cloud_run_v2_service.api` に差分が出ていたら、#5 の `ignore_changes` が効いていないので先にそちらを調べる。

- [ ] **Step 4: apply する（人間）**

```sh
just tf-apply
```

期待する結果: `Apply complete! Resources: 5 added, 0 changed, 0 destroyed.`

- [ ] **Step 5: IAM が意図どおり張れたか確認する（人間）**

```sh
gcloud iam service-accounts get-iam-policy \
  go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com \
  --format='value(bindings.role,bindings.members)'

gcloud iam service-accounts get-iam-policy \
  go-todo-run@taktiks2-go-todo.iam.gserviceaccount.com \
  --format='value(bindings.role,bindings.members)'

gcloud projects get-iam-policy taktiks2-go-todo \
  --flatten='bindings[].members' \
  --filter='bindings.members:go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com' \
  --format='value(bindings.role)'

gcloud artifacts repositories get-iam-policy go-todo \
  --project=taktiks2-go-todo --location=asia-northeast1 \
  --format='value(bindings.role,bindings.members)'
```

期待する結果:
- 1 つ目: `roles/iam.workloadIdentityUser` と `principalSet://iam.googleapis.com/projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github/attribute.repository/taktiks2/go-todo`
- 2 つ目: `roles/iam.serviceAccountUser` と `serviceAccount:go-todo-deploy@…`
- 3 つ目: `roles/run.developer` **のみ**（`roles/run.admin` や `roles/editor` が出たら張りすぎ）
- 4 つ目: `roles/artifactregistry.writer` と `serviceAccount:go-todo-deploy@…`

- [ ] **Step 6: キーが 1 本も無いことを確認する（人間）**

```sh
gcloud iam service-accounts keys list \
  --iam-account=go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com \
  --managed-by=user
```

期待する結果: `Listed 0 items.`（`--managed-by=user` を付けないと Google 管理の鍵が出るが、それは漏れようのない内部鍵）

- [ ] **Step 7: コミット**

```bash
just tf-fmt
git add infra/wif.tf infra/outputs.tf
git commit -m "feat(infra): デプロイ SA と WIF の借用許可・最小ロールを追加"
```

---

### Task 3: `just gh-vars` でリポジトリ変数を設定する

`terraform output` の値を GitHub に流し込む。手で写すと事故る値（プロバイダ名はプロジェクト番号を含む）なのでコマンドにする。

**Files:**
- Modify: `justfile`（`tf-apply-registry` の下に追記）

**Interfaces:**
- Consumes: `output.workload_identity_provider`（Task 1）/ `output.deploy_service_account_email`（Task 2）
- Produces: リポジトリ変数 `WIF_PROVIDER` / `DEPLOY_SA` — Task 4 の workflow が `${{ vars.… }}` で読む

- [ ] **Step 1: `justfile` の末尾に `gh-vars` を追記する**

```just
# terraform output から GitHub のリポジトリ変数を設定する。
#
# 手で写すと事故る値（プロバイダのフルリソース名はプロジェクト番号を含む）なので
# コマンドにする。プールを作り直したときもこれ 1 本で同期できる。
# 前提: just tf-apply が済んでいること。
[working-directory('infra')]
gh-vars:
    gh variable set WIF_PROVIDER --body "$(terraform output -raw workload_identity_provider)"
    gh variable set DEPLOY_SA --body "$(terraform output -raw deploy_service_account_email)"
```

- [ ] **Step 2: レシピが一覧に出ることを確認する**

```sh
just --list | grep gh-vars
```

期待する結果: `gh-vars` の行が出る。出なければ `[working-directory('infra')]` の位置（レシピの直前）を確認する。

- [ ] **Step 3: 実行する（人間）**

```sh
just gh-vars
gh variable list
```

期待する結果: `WIF_PROVIDER` と `DEPLOY_SA` の 2 行。値はそれぞれ `projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github/providers/github-actions` と `go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com`。

`HTTP 403` で落ちたら `gh auth refresh -h github.com -s repo` を実行してから再試行する。

- [ ] **Step 4: コミット**

```bash
git add justfile
git commit -m "chore(infra): terraform output からリポジトリ変数を設定する just gh-vars を追加"
```

---

### Task 4: 検証 workflow で OIDC 交換を通す

**この issue の完了条件そのもの。** 実際に GitHub Actions から認証し、張ったロール 2 本が効いていることを確かめる。

**Files:**
- Create: `.github/workflows/wif-verify.yml`（**一時。Task 5 で削除する**）

**Interfaces:**
- Consumes: リポジトリ変数 `WIF_PROVIDER` / `DEPLOY_SA`（Task 3）
- Produces: PR 本文に貼る実行ログ（受け入れ条件の証跡）

- [ ] **Step 1: `.github/workflows/wif-verify.yml` を作る**

```yaml
# issue #6 の検証専用。WIF が通ることを一度だけ確認するために置く。
# 恒久的な workflow は #7 が書くので、確認が済んだらこのファイルは削除する。
name: wif-verify

on:
  push:
    branches:
      - 6-workload-identity-federation

# id-token: write が無いと OIDC トークンを発行できない。
permissions:
  id-token: write
  contents: read

jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: google-github-actions/auth@v3
        with:
          workload_identity_provider: ${{ vars.WIF_PROVIDER }}
          service_account: ${{ vars.DEPLOY_SA }}

      # auth が書いた認証情報を gcloud に食わせる。これが無いと
      # gcloud auth list に active account が出ない。
      - uses: google-github-actions/setup-gcloud@v3

      # 1. 借用そのもの: go-todo-deploy@… が出れば成功
      - run: gcloud auth list

      # 2. roles/run.developer の確認
      - run: gcloud run services describe go-todo-api --region asia-northeast1 --format 'value(status.url)'

      # 3. roles/artifactregistry.writer の確認
      - run: gcloud artifacts repositories describe go-todo --location asia-northeast1
```

- [ ] **Step 2: コミットして push する（人間）**

```bash
git add .github/workflows/wif-verify.yml
git commit -m "ci: WIF の検証用 workflow を一時的に追加"
git push -u origin 6-workload-identity-federation
```

- [ ] **Step 3: 実行を見る（人間）**

```sh
gh run watch
```

期待する結果: `verify` ジョブが緑。各ステップの出力は
- `gcloud auth list` → `ACTIVE  ACCOUNT` の下に `*  go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com`
- `gcloud run services describe` → `https://go-todo-api-<hash>-an.a.run.app`
- `gcloud artifacts repositories describe` → `format: DOCKER` を含む YAML

**失敗したときの読み分け:**

| エラー | 原因 |
|---|---|
| `Unable to get OIDC token` / audience 不一致 | `permissions: id-token: write` の欠落 |
| `The given credential is rejected by the attribute condition` | Task 1 の条件と実際のリポジトリがずれている |
| `Permission 'iam.serviceAccounts.getAccessToken' denied` | Task 2 の `deploy_wif_user`（principalSet）を疑う。**apply から 5 分以内なら伝播待ちの可能性があるので、まず時間を置いて再実行する** |
| `PERMISSION_DENIED` on `run.services.get` | Task 2 の `deploy_run_developer` |

- [ ] **Step 4: PR 本文に貼る 3 行を抜き出す（人間）**

```sh
gh run view --log | grep -E "go-todo-deploy@|run\.app|format: DOCKER"
```

期待する結果: 借用できた SA、Cloud Run の URL、AR のフォーマットの 3 種類が出る。
これを PR 本文の「動作確認」に貼る。ログファイルはコミットしない。

---

### Task 5: 検証 workflow を削除する

issue の「含まない: workflow 本体（#7）」を守る。証跡は Task 4 のログで残っているので、ファイルは要らない。

**Files:**
- Delete: `.github/workflows/wif-verify.yml`

**Interfaces:**
- Consumes: Task 4 の成功ログ（削除の前提）
- Produces: なし

- [ ] **Step 1: Task 4 が緑だったことを確認する**

```sh
gh run list --branch 6-workload-identity-federation --limit 3
```

期待する結果: 直近の `wif-verify` が `completed  success`。緑でないなら削除してはいけない——Task 4 に戻る。

- [ ] **Step 2: 削除してコミットする**

```bash
git rm .github/workflows/wif-verify.yml
git commit -m "ci: WIF の検証用 workflow を削除（#7 が本体を書く）"
```

---

### Task 6: ドキュメントと issue の更新

`docs/DESIGN.md` に決定値と、調べて分かったことを残す。**設計判断はそれを起こした PR の中で直す**（`CONTRIBUTING.md` §8）。

**Files:**
- Modify: `docs/DESIGN.md`（§9 の「CI/CD: GitHub Actions + Workload Identity Federation」節、`docs/DESIGN.md:636` 付近。「サービスアカウントキー（JSON）を使わない理由」の箇条書きの後、`---` の前）
- Modify: issue #7 の本文（`gh issue edit`）

**Interfaces:**
- Consumes: Task 1〜5 で確定した値
- Produces: #7 が読む前提条件

- [ ] **Step 1: `docs/DESIGN.md` §9 に決定値と学びを追記する**

```markdown
#### 決定値（#6 で確定）

| 項目 | 値 |
|---|---|
| Workload Identity プール | `github` |
| プロバイダ | `github-actions` |
| デプロイ SA | `go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com` |
| 入口の条件 | `assertion.repository_owner_id == '37180466' && assertion.repository_id == '1332914759'` |
| 借用の許可 | `principalSet://…/attribute.repository/taktiks2/go-todo` に `roles/iam.workloadIdentityUser` |
| デプロイ SA のロール | `roles/run.developer`（プロジェクト）/ `roles/artifactregistry.writer`（AR リポジトリ）/ `roles/iam.serviceAccountUser`（`go-todo-run` SA） |
| リポジトリ変数 | `WIF_PROVIDER` / `DEPLOY_SA`（`just gh-vars` が `terraform output` から設定） |
| action | `google-github-actions/auth@v3` / `google-github-actions/setup-gcloud@v3` |

**プールとプロバイダだけは「壊して作り直す」が効かない。** 削除すると約 30 日の
ソフトデリートに入り、その間は同じ ID で作り直せない（`undelete` で復活はできる）。
他のリソースは `deletion_protection = false` で作り直す前提にしているが、ここだけは
`prevent_destroy = true` を付けた。代償として `terraform destroy` 全体が止まるので、
本当に消すときはコードを一時的に編集する。作り直したくなったら ID を `github-2` に変える。

**`attribute_condition` は書かないと危険で、書き方も縛られる。** これが無いと
GitHub の**全リポジトリ**がトークンを交換できてしまう。さらに、CEL と principalSet が
参照する属性は `attribute_mapping` に載っていないと `INVALID_ARGUMENT` で弾かれる。

**条件は名前ではなく数値 ID で書く。** `assertion.repository == 'taktiks2/go-todo'` は、
リポジトリやアカウントを消したときに第三者が同名を取得して同じ条件を満たせる
（GCP 公式がスクワッティングとして警告している）。`repository_owner_id` /
`repository_id` は再利用されない。一方 IAM の principalSet は入口で既に閉じた後なので、
`attribute.repository/taktiks2/go-todo` と人間が読める形にしてある。

**`run.admin` ではなく `run.developer`。** admin は `setIamPolicy` を含み、CD が
「未認証で公開するかどうか」を書き換えられてしまう。公開設定は Terraform の責務
（`infra/cloud_run.tf` の `allUsers` への `roles/run.invoker`）。同じ理由で
`roles/iam.serviceAccountUser` はプロジェクトではなく `go-todo-run` SA に対して張る。

**設定の反映には最大 5 分かかる。** apply 直後の 1 回目が権限エラーで落ちても、
すぐに設定を疑わない。
```

- [ ] **Step 2: 追記が正しい節に入ったか確認する**

```sh
grep -n "決定値（#6 で確定）" docs/DESIGN.md
sed -n '/### CI\/CD: GitHub Actions/,/^## 10\./p' docs/DESIGN.md | head -80
```

期待する結果: §9 の CI/CD 節の中（`## 10. テスト戦略` より前）に入っている。

- [ ] **Step 3: issue #7 の本文に前提を追記する（人間）**

```sh
body=$(mktemp)
gh issue view 7 --json body -q .body > "$body"
cat >> "$body" <<'EOF'

## 前提（#6 で用意済み）

- 認証は `google-github-actions/auth@v3` + `google-github-actions/setup-gcloud@v3`
- `workload_identity_provider: ${{ vars.WIF_PROVIDER }}` / `service_account: ${{ vars.DEPLOY_SA }}`
- job に `permissions: id-token: write` が必要
- デプロイ SA は `roles/run.developer` / `roles/artifactregistry.writer` / `go-todo-run` への `roles/iam.serviceAccountUser` を持つ
- **`actAs`（`roles/iam.serviceAccountUser`）は #6 では検証できていない。** `gcloud run deploy --service-account go-todo-run@taktiks2-go-todo.iam.gserviceaccount.com` が `PERMISSION_DENIED … actAs …` で落ちたら `infra/wif.tf` の `deploy_act_as_runtime` を疑う
EOF
gh issue edit 7 --body-file "$body"
```

期待する結果: `https://github.com/taktiks2/go-todo/issues/7` が表示される。

- [ ] **Step 4: コミット**

```bash
git add docs/DESIGN.md
git commit -m "docs: WIF の決定値と 30 日ソフトデリート・条件の書き方を DESIGN.md に残す"
```

---

## 完了後の確認（PR の前に）

- [ ] `just tf-plan` が `No changes. Your infrastructure matches the configuration.`
- [ ] `git grep -i "BEGIN PRIVATE KEY"` がヒット 0
- [ ] `git status` がクリーン（`wif-verify.yml` が消えている）
- [ ] `gh variable list` が `WIF_PROVIDER` / `DEPLOY_SA` の 2 行
- [ ] issue #6 の完了条件 3 つにチェックが付く状態
- [ ] `/code-review` を走らせる（`CONTRIBUTING.md` §6）

PR は squash merge 前提で、タイトルは Conventional Commits。

```sh
gh pr create --title "ci: Workload Identity Federation を設定する" --body "$(cat <<'EOF'
Closes #6

## 動作確認

（Task 4 の `gh run view --log` から、以下 3 つの出力を貼る）

- `gcloud auth list` が `go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com` を ACTIVE で表示
- `gcloud run services describe go-todo-api` が公開 URL を返す
- `gcloud artifacts repositories describe go-todo` が DOCKER リポジトリを返す

検証用 workflow は同じ PR 内で削除済み。SA キー JSON はリポジトリに存在しない。

## 未検証

`roles/iam.serviceAccountUser`（actAs）は実際に `gcloud run deploy` する #7 で初めて効く。
EOF
)"
```
