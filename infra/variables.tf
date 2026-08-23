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
