# 箱だけ作る。値（version）は Phase 2 で gcloud から入れる。
# tfvars にも state にも平文を置かないため（docs/DESIGN.md §9）。
# 今は箱が空なので lifecycle は付けない。Phase 2 で実値の version が入ったら、
# lifecycle { prevent_destroy = true } を検討する価値がある――今のままだと
# terraform destroy がこの secret とその version を道連れにする。
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
