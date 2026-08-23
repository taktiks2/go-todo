# Workload Identity Federation を設定する

- issue: #6（`phase:0` / `area:infra` / `mode:ai-only`）
- 日付: 2026-08-23
- 関連: `docs/DESIGN.md` §9 CI/CD（GitHub Actions + Workload Identity Federation）、§13 却下した選択肢（SA キー JSON）
- 前提: #1（GCP プロジェクトと state バケット、`iam` / `iamcredentials` / `sts` API の有効化）、#5（Artifact Registry / Cloud Run / ランタイム SA）

## 目的

GitHub Actions が **SA キー JSON なしで** GCP に認証できるようにする。

WIF は GitHub Actions が発行する OIDC トークンを GCP が検証し、短命の権限を渡す仕組み。
`docs/DESIGN.md` §9 はこれを 3 ステップで説明している——**プールを作る → GitHub
リポジトリを条件に紐づける → SA への借用を許可する**。この spec はその 3 ステップを
Terraform のリソースに 1 対 1 で写す。

作るのは認証の経路だけで、それを使う workflow は #7 が書く。#7 が動き出した瞬間に
「認証が原因か workflow が原因か」を切り分けられなくなるため、**この issue の中で
一度だけ実際に OIDC 交換を通して確認してから閉じる。**

## 決定事項

| 項目 | 値 | 理由 |
|---|---|---|
| 認証方式 | **SA impersonation** | `docs/DESIGN.md` §9 の 3 ステップと issue の `DEPLOY_SA` 変数がこの前提。OAuth access token が得られるので `gcloud run deploy` と `docker push` が確実に動く。Direct WIF はトークン寿命 10 分・一部サービス非対応 |
| プール ID | `github` | 用途を限定しない名前にする。削除後 30 日 ID を再利用できないため、作り直しは別 ID（`github-2`）に逃がす |
| プロバイダ ID | `github-actions` | |
| デプロイ SA | `go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com` | ランタイム SA（`go-todo-run`）と分ける。CD が持つ権限とアプリが実行時に持つ権限は別物 |
| 入口の条件 | `assertion.repository_owner_id == '37180466' && assertion.repository_id == '1332914759'` | **数値 ID で閉じる。** 名前ベースはリポジトリ／アカウントを消したときに同名を取られる（GCP 公式がスクワッティングとして警告） |
| 借用の許可 | `principalSet://…/attribute.repository/taktiks2/go-todo` | 入口を数値 ID で閉じた後なので、IAM 側は人間が読める名前でよい |
| ブランチ制限 | **入れない** | 入れると検証 workflow がブランチ上で走れない。main 限定にしたくなったら `attribute.ref` で principalSet を分ける |
| ロール（Cloud Run） | `roles/run.developer`（プロジェクト） | `run.admin` は IAM ポリシーの書き換えまで含み、CD が公開設定をいじれてしまう。サービス単位まで絞らないのは、`gcloud` がプロジェクトスコープの読み取りをしたときに #7 で 403 になる余地を残さないため |
| ロール（Artifact Registry） | `roles/artifactregistry.writer`（**リポジトリスコープ**） | プロジェクト単位にする理由が無い |
| ロール（actAs） | `roles/iam.serviceAccountUser`（**`go-todo-run` SA スコープ**） | `gcloud run deploy --service-account go-todo-run@…` に必要。プロジェクト単位にすると全 SA を借用できてしまう |
| `prevent_destroy` | プールとプロバイダに `true` | 誤って消すと 30 日間同じ ID で作れず #7 の CD が止まる。`cloud_run.tf` の `deletion_protection = false`（壊して作り直す前提）が **WIF にだけは成立しない** |
| リポジトリ変数 | `just gh-vars` で `terraform output` から流し込む | 手で写すと事故る。プールを作り直したときも 1 コマンドで同期できる |
| 検証 workflow | 一時ファイル。**同じ PR 内で削除する** | issue の「含まない: workflow 本体（#7）」を守る。証跡は PR 本文の実行ログに残す |
| action のバージョン | `google-github-actions/auth@v3` / `setup-gcloud@v3` | issue 本文は `@v2` だが v3 が最新（2025-09-03）。#7 でも v3 を使う |
| TDD | 適用外 | `mode:ai-only`。Go のコードに触れない。`CONTRIBUTING.md` §2 の pair-tdd は Go 実装に対する規律 |

## 成果物

- `infra/wif.tf`（新規）
- `infra/variables.tf` に GitHub の識別子を 3 つ追加
- `infra/outputs.tf` に `workload_identity_provider` / `deploy_service_account_email` を追加
- `justfile` に `gh-vars` を追加
- `.github/workflows/wif-verify.yml`（**一時。同じ PR 内で削除する**）
- `docs/DESIGN.md` §9 に「決定値（#6 で確定）」を追記

## `infra/wif.tf`

```hcl
# GitHub Actions が SA キー JSON なしで GCP に認証するための一式。
# docs/DESIGN.md §9 の 3 ステップがそのままこのファイルの並びになっている:
#   1. プールを作る
#   2. GitHub リポジトリを条件に紐づける（provider の attribute_condition）
#   3. SA への借用を許可する（workloadIdentityUser）

# 削除すると 30 日間のソフトデリートに入り、その間は同じ ID で作り直せない。
# このリポジトリの他のリソースは「壊して作り直す」前提（cloud_run.tf の
# deletion_protection = false）だが、WIF だけはその前提が成立しない。
# 消えると #7 の CD が 30 日止まるため prevent_destroy で塞ぐ。
#
# それでも作り直したくなったら、この ID を消すのではなく `github-2` のような
# 別 ID を足して output を差し替える。ソフトデリート中の ID は復活もできる
# （gcloud iam workload-identity-pools undelete）。
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

  # google.subject は必須。attribute.repository は下の principalSet が参照するので
  # ここに無いと `Attribute condition must reference one of the provider's claims`
  # 系のエラーになる。**principalSet と CEL で使う属性は必ず mapping に載せる。**
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
    # 要求する。明示すると両者がずれて invalid_target で落ちる余地が増えるだけ。
  }

  lifecycle {
    prevent_destroy = true
  }
}

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

# gcloud run deploy に必要な最小。run.admin にしない——admin は
# setIamPolicy を含み、CD が「未認証で公開するかどうか」を書き換えられてしまう。
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

## `infra/variables.tf` の追加

```hcl
variable "github_repository" {
  description = "WIF が借用を許可する GitHub リポジトリ（owner/repo）"
  type        = string
  default     = "taktiks2/go-todo"
}

# 数値 ID を直書きすると .tf から意味が読めなくなるので変数に名前を付ける。
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

## `infra/outputs.tf` の追加

```hcl
# projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github/providers/github-actions
# google-github-actions/auth の workload_identity_provider にこの形で渡す。
output "workload_identity_provider" {
  description = "リポジトリ変数 WIF_PROVIDER の値"
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "deploy_service_account_email" {
  description = "リポジトリ変数 DEPLOY_SA の値"
  value       = google_service_account.deploy.email
}
```

## `justfile` の変更

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

## 検証用 workflow（一時）

`.github/workflows/wif-verify.yml`。**このブランチでしか走らず、PR の最後に削除する。**

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

      # 3. roles/artifactregistry.writer の確認（describe は reader 相当だが、
      #    リポジトリスコープの binding が効いていることは分かる）
      - run: gcloud artifacts repositories describe go-todo --location asia-northeast1
```

## 実行手順

```sh
gh issue develop 6 --name 6-workload-identity-federation --checkout

just tf-fmt
just tf-validate
just tf-plan     # 追加 7 リソース（pool / provider / SA / IAM 4 本）
just tf-apply

just gh-vars
gh variable list  # WIF_PROVIDER と DEPLOY_SA が出る

git push          # wif-verify.yml が走る
gh run watch
```

**apply 直後に workflow が失敗しても即座に設定を疑わない。** WIF の設定は反映まで
最大 5 分かかる。1 度目が落ちたら数分空けて再実行する。

## 検証

| 確認 | 期待 |
|---|---|
| `just tf-plan` が apply 後に `No changes.` | Terraform と実物が一致 |
| `gcloud auth list`（workflow 内） | `go-todo-deploy@taktiks2-go-todo.iam.gserviceaccount.com` が ACTIVE |
| `gcloud run services describe`（workflow 内） | Cloud Run の URL が返る（`run.developer` が効いている） |
| `gcloud artifacts repositories describe`（workflow 内） | リポジトリの情報が返る |
| `git grep -i "BEGIN PRIVATE KEY"` | ヒット 0。SA キー JSON がリポジトリに存在しない |
| `gh variable list` | `WIF_PROVIDER` / `DEPLOY_SA` の 2 つ |

**`roles/iam.serviceAccountUser`（actAs）だけはこの issue では検証できない。**
実際に `gcloud run deploy --service-account` を叩く #7 で初めて効く。ここで落ちる
可能性を #7 に申し送る。

## ドキュメントと issue の更新（同じ PR 内）

- `docs/DESIGN.md` §9 CI/CD に「決定値（#6 で確定）」テーブルを追加（プール ID、
  プロバイダ ID、デプロイ SA、条件、ロール 3 本）
- 同節に散文で残す学び: **プールの 30 日ソフトデリート**、**attribute condition は
  実質必須**、**数値 ID を使う理由**、**run.admin ではなく run.developer を選んだ理由**
- issue #7 の本文に「`auth@v3` + `vars.WIF_PROVIDER` / `vars.DEPLOY_SA` を使う。
  actAs が効くかは #7 で初めて分かる」を追記

## 作業の流れ

実装計画は `docs/superpowers/plans/2026-08-23-workload-identity-federation.md`。
6 タスク・各 1 コミットで、apply を挟むたびに実物を `gcloud` で確認する。

1. プールとプロバイダ（`wif.tf` / `variables.tf` / `outputs.tf`）→ apply
2. デプロイ SA と IAM binding 4 本 → apply
3. `just gh-vars` を追加してリポジトリ変数を設定
4. `wif-verify.yml` を足して push、成功ログを取る
5. `wif-verify.yml` を削除
6. `docs/DESIGN.md` と issue #7 の本文を更新 → PR に実行ログを貼って `Closes #6`

## スコープ外

- `.github/workflows/ci.yml` / `deploy.yml`（#7）
- `golang-migrate`（Phase 2）
- Firebase Hosting 用の権限（Phase 4）
- Terraform の GitHub provider でリポジトリ変数を管理すること。`docs/DESIGN.md` §9 の
  「全部 Terraform でやろうとしない」に反し、GitHub の PAT を新たに要求することになる

## 調査で確定した前提（2026-08-23 時点）

- `hashicorp/google` は `7.45.0` を lock 済み（`~> 7.0`）。WIF 関連の破壊的変更なし
- `google-github-actions/auth` の最新は **v3**、`setup-gcloud` は **v3.0.1**
- `attribute_condition` は Terraform のスキーマ上 optional だが、GitHub の issuer では
  Google 側が実質必須にしている。CEL と principalSet が参照する属性は
  `attribute_mapping` に載っていないと `INVALID_ARGUMENT` で落ちる
- プール／プロバイダの削除は約 30 日のソフトデリート。その間 ID を再利用できず、
  `undelete` で復活はできる
- GCP 公式は `repository` / `repository_owner` の名前ベース条件について
  スクワッティングのリスクを警告し、`*_id` の数値フィールドを推奨している
- `taktiks2` は Organization ではなく User アカウント（owner id `37180466`、
  repo id `1332914759`）

## 却下した選択肢

| 却下したもの | 理由 |
|---|---|
| SA キー JSON | 無期限の認証情報。`docs/DESIGN.md` §13 の決定 |
| Direct WIF（SA なし） | トークン寿命 10 分、一部サービス非対応。issue の `DEPLOY_SA` と `docs/DESIGN.md` §9 の 3 ステップにも合わない |
| `terraform-google-modules/.../gh-oidc` モジュール | 中身が読めないまま動く。Buildpacks / ko を却下したのと同じ理由（`docs/DESIGN.md` §13） |
| `gcloud` で手作業 + `bootstrap.sh` に記録 | state の外に出て、#7 が `terraform output` から値を取れない |
| `roles/run.admin` | `setIamPolicy` を含み、CD が公開設定を書き換えられる |
| プロジェクト単位の `roles/iam.serviceAccountUser` | プロジェクト内の全 SA を借用できてしまう |
| `wif-verify.yml` を恒久的に残す | issue の「含まない: workflow 本体（#7）」に反する。切り分け用に欲しくなったら #7 の中で `workflow_dispatch` として作る |
| 条件に `assertion.ref == 'refs/heads/main'` | 検証 workflow がブランチで走れない。必要になったら principalSet 側で分ける |

## リスク

- **`prevent_destroy` は `terraform destroy` 全体を止める。** 学習中に全部消してやり直す
  ときは、このブロックを一時的に外す判断が要る。外して消したら 30 日 ID が戻らない
- **actAs はこの issue で検証できない。** #7 の最初のデプロイで `PERMISSION_DENIED:
  … actAs …` が出たら、ここの binding を疑う
- **WIF 設定の伝播に最大 5 分。** 1 回目の失敗で設定をいじると、直っていたものを
  壊す方向に動きうる
- **`gh variable set` はリポジトリの admin 権限を要求する。** `gh auth status` の
  スコープが足りないと落ちる
