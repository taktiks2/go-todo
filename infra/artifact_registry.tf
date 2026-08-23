# Cloud Run と同じリージョンに置く。pull がリージョン内で完結し、
# コールドスタート時間に効く（docs/DESIGN.md §9）。
# AR の 0.5 GB 無料枠にリージョン制限は無い。
resource "google_artifact_registry_repository" "app" {
  location      = var.region
  repository_id = var.repository_id
  format        = "DOCKER"
  description   = "go-todo のコンテナイメージ"

  # dry run にしない。今このリポジトリにイメージは 0 個で、下の keep-recent が
  # 直近 20 世代を無条件に守るため、有効にしても消えるものが存在しない。
  # cleanup policy は非同期（およそ日次）で走るので dry run のログを PR の中で
  # 確認する術も無い。「入れたが効いていない」状態で放置する方が危険。
  cleanup_policy_dry_run = false

  # KEEP は DELETE より優先される。直近 20 世代は日数に関係なく必ず残るので、
  # ロールバック先の image が delete-old に消されている、という事故を防ぐ
  # （#1 の tfstate-lifecycle.json と同じ考え方）。
  #
  # 5 ではなく 20 にしている。5 世代だと「直近 5 つには入らず、30 日以内でも
  # ない」ロールバック先が delete-old の対象に落ちうる。min_instance_count = 0
  # で常にコールドスタートするため、その image が消えた状態でロールバックすると
  # pull に失敗し、コードを一切変えていないのに公開 URL が 5xx になる。
  # コストは #4 の実測（アプリ層 1 枚あたり圧縮後 2.54 MB）から 20 × 2.54 MB
  # ≈ 51 MB、無料枠 0.5 GB の約 10%。直近 20 世代より外側は引き続き
  # delete-old の 30 日ルールで刈られるので、無限に積み上がりはしない。
  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"

    most_recent_versions {
      keep_count = 20
    }
  }

  # delete-old は tagState: ANY で解決される（gcloud artifacts repositories
  # describe で実測済み）ため、タグ付きイメージも消える対象に入る。
  # cloud_run.tf は :bootstrap を作成時点の image として直書きしており、
  # keep-recent の直近 20 世代からこぼれ落ちて 30 日を過ぎると delete-old に
  # 消される。消えた状態でサービスを再作成する apply（name/location の変更、
  # destroy → apply、state 消失後の再構築）を走らせると「Image ... not found」
  # で落ちるため、bootstrap タグを守る。
  #
  # tag_prefixes は前方一致。`:bootstrap` だけでなく bootstrap で始まる
  # タグ全部（docker-push はタグを引数で受け取るので `just docker-push
  # bootstrap-2026-01` のようなタグも作れる）が対象になり、KEEP が DELETE に
  # 優先するのでそれらも永久に残る。
  cleanup_policies {
    id     = "keep-bootstrap"
    action = "KEEP"

    condition {
      tag_state    = "TAGGED"
      tag_prefixes = ["bootstrap"]
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
