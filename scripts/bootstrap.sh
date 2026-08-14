#!/usr/bin/env bash
#
# Terraform を書き始めるための前提を、一度だけ手で作る。
#
# state 用バケットは Terraform で作れない。backend がバケットの存在を前提にするため
# （鶏と卵）。だからここに gcloud コマンドとして記録し、手で流す。
#
# 前提:
#   - gcloud CLI がインストール済み        : gcloud version
#   - ログイン済み                          : gcloud auth login
#   - 課金アカウントが 1 つ以上ある         : gcloud billing accounts list
#   - その課金アカウントに roles/billing.admin か roles/billing.costsManager がある
#     （手順 7 の予算アラート作成に必要）
#   - ADC でログイン済み                    : gcloud auth application-default login
#     （手順 7 だけが ADC 経由で動く。gcloud auth login とは別物）
#
# 使い方:
#   BILLING_ACCOUNT=XXXXXX-XXXXXX-XXXXXX ./scripts/bootstrap.sh
#
# これは一度きりの手順。2 回目を頭から流すと手順 1 の projects create で止まる。
# ただし全ステップがそうではない。手順 5・6 の buckets update は黙って再適用され、
# 手順 7 の budgets create は存在チェックを持たないので同名の予算をもう 1 つ作る。
# 落ちたステップ以降を手で流し直すときは、手順 7 を二重に走らせないこと。
#
# 落ちたときの対処:
#   projects create       PROJECT_ID がグローバル衝突   → PROJECT_ID=... で別名を指定
#   billing projects link roles/billing.user 不足 / ID 形式違い
#                                                       → gcloud billing accounts list で確認
#   services enable       課金が未リンク                 → 手順 2 に戻る
#   buckets create        バケット名がグローバル衝突     → BUCKET=... で別名を指定
#   （事前チェック）      BUCKET_LOCATION が無料枠外  → us-east1 / us-west1 / us-central1 から選ぶ
#   budgets create        SERVICE_DISABLED / INVALID_ARGUMENT
#                                                       → 手順 7 のコメントに 3 つの罠を書いた
#
# 落ちたステップを直したら、そのステップ以降を手で流し直す。

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-taktiks2-go-todo}"
BUCKET="${BUCKET:-${PROJECT_ID}-tfstate}"

# Cloud Storage の Always Free は US リージョン限定（us-east1 / us-west1 / us-central1）。
# state バケットは Cloud Run から触られないので、東京に置く理由が無い。
# Cloud Run 自身のリージョン（asia-northeast1）は #5 の Terraform 側で指定する。
BUCKET_LOCATION="${BUCKET_LOCATION:-us-central1}"

: "${BILLING_ACCOUNT:?required. Run: gcloud billing accounts list}"

# Cloud Storage の Always Free は US の 3 リージョン限定。上書きするならこの中から選ぶ。
case "${BUCKET_LOCATION}" in
  us-east1 | us-west1 | us-central1) ;;
  *)
    echo "BUCKET_LOCATION=${BUCKET_LOCATION} は Cloud Storage の Always Free 対象外" >&2
    exit 1
    ;;
esac

# ADC が無いと手順 7 で初めて落ちる。そのとき手順 1〜6 は GCP に実物を作り終えており、
# このスクリプトは冪等ではないので手で復旧することになる。だから GCP に触る前に落とす。
if ! gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo "ADC が無い。先に実行: gcloud auth application-default login" >&2
  exit 1
fi

# 1. プロジェクトを作る
#    個人アカウントで組織が無いので --organization は付けない
gcloud projects create "${PROJECT_ID}" --name="go-todo"

# 2. 課金を紐付ける
#    これより先に services enable すると弾かれる。順序は project → billing → services
gcloud billing projects link "${PROJECT_ID}" --billing-account="${BILLING_ACCOUNT}"

# 3. API を有効化する
gcloud services enable \
  cloudresourcemanager.googleapis.com \
  serviceusage.googleapis.com \
  storage.googleapis.com \
  iam.googleapis.com \
  iamcredentials.googleapis.com \
  sts.googleapis.com \
  run.googleapis.com \
  artifactregistry.googleapis.com \
  secretmanager.googleapis.com \
  cloudbilling.googleapis.com \
  billingbudgets.googleapis.com \
  --project="${PROJECT_ID}"

# 有効化直後の API は伝播に時間がかかる。このスクリプトが直後に使うのは
# storage（手順 4）と billingbudgets（手順 7）。待たないと SERVICE_DISABLED で
# 断続的に落ちる。iamcredentials を使うのは #6 であってここではない。
sleep 30

# 4. Terraform state 用バケットを作る
#    -b (uniform-bucket-level-access): 既定 OFF。ACL と IAM の混在を避ける
#    --public-access-prevention:       state には機微情報が入りうる
gcloud storage buckets create "gs://${BUCKET}" \
  --project="${PROJECT_ID}" \
  --location="${BUCKET_LOCATION}" \
  --default-storage-class=STANDARD \
  --uniform-bucket-level-access \
  --public-access-prevention

# 5. バージョニングを有効化する
#    buckets create に versioning フラグが無いので、作成とは別コマンドになる
#
#    soft delete は既定の 7 日のまま残す。versioning とは守る範囲が違う。
#    versioning が守るのは「上書き」と「現行世代の削除」だけで、
#    rm --all-versions やバケットごと消す操作からは守れない。そこを埋めるのが soft delete。
#    state は数十 KB で Always Free の 5 GB 枠に収まるので、費用はゼロに丸まる。
gcloud storage buckets update "gs://${BUCKET}" --versioning

# 6. 旧世代の state を刈る
#    これが無いと apply のたびに世代が積もり、消えない
gcloud storage buckets update "gs://${BUCKET}" \
  --lifecycle-file="$(dirname "$0")/tfstate-lifecycle.json"

# 7. 予算アラートを作る
#    このプロジェクトの支出だけを対象にする。通知先は課金アカウントの管理者と利用者
#    （= 自分）に既定で入るので、通知チャネルの設定は要らない。
#
#    ここには罠が 3 つある。3 つとも実際に踏んだ。
#
#    1) --billing-project が要る。budgets create は ADC 経由で動き quota project を
#       要求する。指定しないと gcloud 共有クライアントプロジェクトにフォールバックし、
#       そこでは billingbudgets が無効なので SERVICE_DISABLED になる
#    2) --filter-projects はプロジェクト ID ではなく「番号」。ID を渡すと
#       INVALID_ARGUMENT。番号は作成後にしか分からないのでここで引く
#    3) --budget-amount に通貨サフィックスを付けない。課金アカウントの通貨と一致
#       しないと INVALID_ARGUMENT。省略すればアカウントの通貨に従う
#
#    金額を 1 にしているのは「無料枠を外れたら気づく」のが目的だから。
#    閾値 10% で最初の通知が飛ぶ。アラートは通知するだけで、支出は止めない。
PROJECT_NUMBER="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"
: "${PROJECT_NUMBER:?projects describe が空を返した。プロジェクトの伝播待ちかもしれない}"

gcloud billing budgets create \
  --billing-account="${BILLING_ACCOUNT}" \
  --display-name="go-todo" \
  --budget-amount=1 \
  --filter-projects="projects/${PROJECT_NUMBER}" \
  --threshold-rule=percent=0.1 \
  --threshold-rule=percent=0.5 \
  --threshold-rule=percent=1.0 \
  --billing-project="${PROJECT_ID}"

# 8. 次にやること
cat <<EOF

bootstrap 完了。

  PROJECT_ID      : ${PROJECT_ID}
  BUCKET          : gs://${BUCKET}
  BUCKET_LOCATION : ${BUCKET_LOCATION}

Terraform (#5) を回す前に、ADC に quota project を紐付けておくこと。
ADC のログイン自体は前提条件で済んでいる。

  gcloud auth application-default set-quota-project ${PROJECT_ID}

EOF
