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
