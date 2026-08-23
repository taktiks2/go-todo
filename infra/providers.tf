# 認証は ADC。事前に以下が済んでいること（#1 の bootstrap.sh 末尾で案内済み）:
#
#   gcloud auth application-default login
#   gcloud auth application-default set-quota-project taktiks2-go-todo
provider "google" {
  project = var.project_id
  region  = var.region
}
