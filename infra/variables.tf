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
