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

# projects/<PROJECT_NUMBER>/locations/global/workloadIdentityPools/github/providers/github-actions
# google-github-actions/auth の workload_identity_provider にこの形で渡す。
output "workload_identity_provider" {
  description = "リポジトリ変数 WIF_PROVIDER の値"
  value       = google_iam_workload_identity_pool_provider.github.name
}
