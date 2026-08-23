# Cloud Run のランタイム ID。
#
# service_account を書かないと Compute Engine の既定 SA が使われるが、
# compute.googleapis.com は #1 で有効化していないため既定 SA 自体が存在しない
# 可能性が高い。存在したとしても Editor 権限で過剰。
resource "google_service_account" "run_runtime" {
  account_id   = "go-todo-run"
  display_name = "go-todo Cloud Run runtime"
}

resource "google_cloud_run_v2_service" "api" {
  name     = var.service_name
  location = var.region

  # provider 7.x の既定は true。destroy だけでなく、name や location を変えて
  # 再作成が必要になる apply も落ちる。作り直す前提の学習用なので最初から外す。
  deletion_protection = false

  template {
    service_account = google_service_account.run_runtime.email

    # docs/DESIGN.md §9: max × pgxpool.MaxConns <= DB の接続上限。
    # 既定の 100 のままだと DB にコネクションが殺到する。
    scaling {
      min_instance_count = 0
      max_instance_count = 3
    }

    # 未指定でも CPU >= 1 なら 80 が既定。docs/DESIGN.md §9 が値として決めている
    # 以上、「決めたのにコードに無い」状態を作らないため明示する。
    max_instance_request_concurrency = 80

    containers {
      # 下の ignore_changes があるので、この値が使われるのは初回作成時だけ。
      # 実物は just docker-push が :bootstrap として先に置いてある。
      image = "${var.region}-docker.pkg.dev/${var.project_id}/${var.repository_id}/api:bootstrap"
    }

    # resources / cpu_idle は書かない。v2 の既定（1 vCPU / 512 MiB /
    # リクエスト処理中のみ CPU 割り当て）がそのまま docs/DESIGN.md §9 の意図と一致し、
    # min_instance_count = 0 と合わせてアイドル課金がゼロになる。
    #
    # DATABASE_URL も繋がない。Cloud Run はリビジョン起動時に secret を解決するため、
    # version の入っていない secret を参照するとリビジョンが Ready にならない。
    # Phase 2 で値を入れるときに参照ごと足す。
  }

  # 「インフラの形は Terraform、動くバージョンは CD」（docs/DESIGN.md §9）。
  #
  # image だけでは足りない。#7 の gcloud run deploy はデプロイのたびに
  # client / client_version を書き換えるので、除外しないと次の plan が毎回
  # それを戻そうとして差分を出す。CD が触るフィールドは全部除外して初めて成立する。
  lifecycle {
    ignore_changes = [
      template[0].containers[0].image,
      client,
      client_version,
    ]
  }

  # image は文字列なので、Terraform だけでは AR への依存を推論できない。
  depends_on = [google_artifact_registry_repository.app]
}

# 未認証で叩けるようにする。issue #7 の「手動でやること」から移設した。
#
# AR からの pull は Cloud Run のサービスエージェントが行い、run.googleapis.com の
# 有効化時に artifactregistry.reader を自動で持つ。こちらに追加の IAM は要らない。
resource "google_cloud_run_v2_service_iam_member" "public" {
  project  = google_cloud_run_v2_service.api.project
  location = google_cloud_run_v2_service.api.location
  name     = google_cloud_run_v2_service.api.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
