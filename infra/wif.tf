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
