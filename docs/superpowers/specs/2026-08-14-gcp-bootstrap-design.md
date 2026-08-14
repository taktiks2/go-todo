# GCP プロジェクトと Terraform state バケットの bootstrap

- issue: #1（`phase:0` / `area:infra` / `mode:ai-only`）
- 日付: 2026-08-14
- 関連: `docs/DESIGN.md` §9 インフラ / IaC、§12 開発フェーズ、§14 着手時に決めること

## 目的

Terraform を書き始めるための前提を手で作る。

Terraform の `backend "gcs"` は**バケットが既に存在していること**を要求する。だから state を
置くバケット自身は Terraform で作れない（鶏と卵）。GCP プロジェクトと課金の紐付けも同様に
Terraform より手前にある。これらを `scripts/bootstrap.sh` に記録し、一度だけ手で流す。

`docs/DESIGN.md` §12 の Walking Skeleton の第一歩にあたる。ここで作る識別子は
#5（Terraform）・#6（WIF）・#7（CI/CD）がそのまま参照する。

これは**学習用のデモプロジェクトであり、無料枠の中で運用する**ことを制約とする。
リージョンの選択と予算アラートはこの制約から導かれている。

## 決定事項

| 項目 | 値 | 理由 |
|---|---|---|
| `PROJECT_ID` | `taktiks2-go-todo` | 個人名前空間で衝突しにくく、手で打てる。**後から変更不可** |
| `BUCKET` | `taktiks2-go-todo-tfstate` | `<PROJECT_ID>-tfstate` の定番 |
| **バケットの location** | **`us-central1`** | **Cloud Storage の Always Free は US リージョン限定**。state バケットは Cloud Run から触られず、触るのはローカルの `terraform` と GitHub Actions（US ランナー）だけなので、東京に置く理由が無い |
| Cloud Run のリージョン | `asia-northeast1`（#5 で使う。このスクリプトには出てこない） | `docs/DESIGN.md` §9 の既定 |
| 環境分離 | しない（単一プロジェクト） | `docs/DESIGN.md` に dev/prod の記述が無く、Cloud Run も Neon も 1 つ |
| 課金アカウント | 既存の 1 件を使う | `OPEN=True` を確認済み。**ID はリポジトリに書かない** |
| 予算アラート | このプロジェクト単位で作る | 無料枠を外れたことに気づく手段。**アラートは支出を止めない**点は承知の上 |
| CLI | `gcloud storage`（`gsutil` は使わない） | gsutil は 2027 年 3 月に gcloud CLI 同梱から外れる |
| スクリプトの性質 | 冪等化しない直列スクリプト | 一度きりの手順。存在チェックを足すと「読めば再現できる記録」という狙いが薄れる |

## 成果物

- `scripts/bootstrap.sh`（実行権限付き）
- `scripts/tfstate-lifecycle.json`
- `docs/DESIGN.md` への決定値の追記
- issue #1 本文の更新

## `scripts/bootstrap.sh`

```bash
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
```

## `scripts/tfstate-lifecycle.json`

```json
{
  "rule": [
    {
      "action": { "type": "Delete" },
      "condition": {
        "isLive": false,
        "numNewerVersions": 5,
        "daysSinceNoncurrentTime": 30
      }
    }
  ]
}
```

条件は AND で評価される。**「現行でない」かつ「新しい世代が 5 つ以上ある」かつ「非現行になって
30 日経った」**世代だけを消す。直近 5 世代は日数に関係なく必ず残るので、事故った直後の巻き戻しは
常に効く。

## 有効化する API

issue 本文には 5 個しか書いていないが、実際には 11 個必要。

| API | なぜ必要か |
|---|---|
| `cloudresourcemanager.googleapis.com` | Terraform provider がプロジェクト情報を読む。新規プロジェクトでは無効 |
| `serviceusage.googleapis.com` | #5 で `google_project_service` を使うなら必須。**これだけは手で有効化しないと Terraform から他 API を有効化できない**（鶏と卵その 2） |
| `storage.googleapis.com` | バケット作成と Terraform backend |
| `iam.googleapis.com` | WIF（#6） |
| `iamcredentials.googleapis.com` | WIF の `generateAccessToken` |
| `sts.googleapis.com` | WIF のトークン交換 |
| `run.googleapis.com` | Cloud Run |
| `artifactregistry.googleapis.com` | コンテナイメージ |
| `secretmanager.googleapis.com` | `DATABASE_URL` |
| `cloudbilling.googleapis.com` | 予算アラート |
| `billingbudgets.googleapis.com` | 予算アラート |

## コスト

**このプロジェクトは無料枠の中で運用する。** 現時点（2026-08）の見込み。

| サービス | 無料枠 | このプロジェクト | 判定 |
|---|---|---|---|
| プロジェクト作成 / API 有効化 / 課金紐付け | — | — | 無料 |
| Cloud Storage（state） | 5 GB-月、Class A 5,000 回、Class B 50,000 回。**US リージョン限定** | 数十 KB の state | `us-central1` なら無料 |
| Cloud Run | 200 万リクエスト/月ほか | `min-instances=0` でアイドル課金なし | 無料の見込み（後述） |
| Artifact Registry | **0.5 GB/月** | distroless で約 20 MB/イメージ | **⚠️ 30〜40 回のデプロイで超える** |
| Secret Manager | 6 バージョン、10,000 アクセス/月 | `DATABASE_URL` 1 個 | 無料 |
| Cloud Logging | 50 GiB/プロジェクト/月 | デモ規模 | 無料 |
| Neon | GCP 外の Free plan | Phase 2〜 | 無料 |
| Firebase Hosting / Auth | Spark 無料枠、Auth は 50k MAU まで | Phase 3〜4 | 無料（`docs/DESIGN.md` §7） |

無料トライアルは $300 / 90 日で、Google は "no automatic charges, no commitment" と明記している。
トライアル終了で自動的に課金が始まることはない。

**未確認の論点が 1 つある。** Cloud Run の無料枠にリージョン制限があるかを確定できなかった。
公式の Free Tier ページは Cloud Storage には「US リージョンのみ」と明記する一方、Cloud Run には
制限を書いていない。一方で二次情報は「US 3 リージョンのみ」と主張している。
**#5 で Cloud Run のリージョンを確定する前に、公式ページで確認すること。**
US 限定であれば `docs/DESIGN.md` の `asia-northeast1` 前提そのものを見直す話になる。

## 検証

完了条件の確認。結果は PR 本文に貼る。

```sh
gcloud projects describe taktiks2-go-todo --format='value(projectId,lifecycleState)'
gcloud billing projects describe taktiks2-go-todo --format='value(billingEnabled)'
gcloud services list --enabled --project taktiks2-go-todo
gcloud storage buckets describe gs://taktiks2-go-todo-tfstate \
  --format='value(location,versioning_enabled,soft_delete_policy.retentionDurationSeconds,lifecycle_config)'
gcloud billing budgets list --billing-account="${BILLING_ACCOUNT}" --billing-project=taktiks2-go-todo
```

期待する結果:

- プロジェクトが `ACTIVE`
- `billingEnabled` が `True`
- 上記 11 個の API が一覧に並ぶ
- `location` が `US-CENTRAL1`
- `versioning_enabled` が `true`
- `soft_delete_policy.retentionDurationSeconds` が `604800`（既定の 7 日。無効化していない）
- `lifecycle_config` にルールが 1 件入っている
- 予算 `go-todo` が 1 件返る

**フィールド名に注意。** `gcloud storage buckets describe` の出力キーはスネークケースで、
`versioning_enabled` である。`versioning.enabled` と書くと**エラーにならず静かに空を返す**ため、
バケットが正しいのに検証が落ちたように見える。`soft_delete_policy.retentionDurationSeconds` は
入れ子の中だけキャメルのままなので、両者は綴りが揃っていない。

`budgets list` にも `--billing-project` が要る（`create` と同じ理由）。

## ドキュメントと issue の更新（同じ PR 内）

`CONTRIBUTING.md` §8「設計判断が変わったら、それを起こした PR の中で `docs/DESIGN.md` を直す」
に従い、別 PR に切り出さない。

- `docs/DESIGN.md` §9 IaC — 決定値（PROJECT_ID / バケット名 / バケットの location /
  有効化 API 一覧）と、**state バケットだけ `us-central1` に置く理由**、soft delete を
  無効化した理由を追記
- `docs/DESIGN.md` §14「着手時に決めること」の表に PROJECT_ID とバケットの location を追加
- `docs/DESIGN.md` §14 リスク — Artifact Registry の 0.5 GB 無料枠と、Cloud Run 無料枠の
  リージョン制限が未確認である点を追記
- issue #1 本文 — API を 5 → 11 に、`gsutil` の記述を `gcloud storage` に、
  完了条件に soft delete / lifecycle / 予算アラートを追加

## 作業の流れ

```sh
gh issue develop 1 --checkout
# scripts/bootstrap.sh と scripts/tfstate-lifecycle.json を作る（bootstrap.sh は chmod +x）
# 人間が BILLING_ACCOUNT=... ./scripts/bootstrap.sh を実行
# 検証コマンドを実行して結果を控える
# docs/DESIGN.md と issue 本文を更新
# /code-review → PR（squash merge）
```

`mode:ai-only` なので Claude がファイルを書いて適用する。GCP 側の実行は人間。

TDD の対象外とする。一度きりの手動スクリプトで、テストで守れる振る舞いが無い。
`CONTRIBUTING.md` §2 の pair-tdd ループは Go 実装に対する規律であり、この issue には適用しない。

## スコープ外

- Terraform コード本体（#5）
- Workload Identity Federation（#6）
- CI/CD ワークフロー（#7）
- Firebase 関連（Phase 3 / 4）。`docs/DESIGN.md` §9 の方針どおり Terraform 管理外
- `gcloud` / `terraform` のインストール（`~/dotfiles` 側の作業。着手前に済ませる）
- state バケットへの IAM 付与。#6 で WIF のサービスアカウントに
  `roles/storage.objectAdmin` を渡すときにまとめて行う
- **Artifact Registry の cleanup policy**。無料枠 0.5 GB を超える唯一の現実的な課金源だが、
  リポジトリを作るのは #5 なのでそちらに申し送る

## 調査で確定した前提（2026-08-14 時点）

| 事実 | 影響 |
|---|---|
| Cloud Storage の Always Free は `us-east1` / `us-west1` / `us-central1` 限定 | state バケットを `us-central1` に置く |
| `gcloud storage buckets create` に versioning フラグが無い | 作成と更新の 2 コマンドに分かれる |
| uniform bucket-level access の既定は `False` | `--uniform-bucket-level-access` を明示する |
| soft delete と Object Versioning は守る範囲が違う | versioning は「上書き」と「現行世代の削除」しか守らない。`rm --all-versions` やバケット削除から state を守るのは soft delete だけ。当初「役割が重複する」として無効化したのは誤りで、既定の 7 日のまま残す |
| `iamcredentials` は有効化の伝播に約 30 秒 | `sleep 30` を挟む |
| `backend "gcs"` は state locking をネイティブ対応 | ロック用の追加リソースは作らない |
| `backend "gcs"` に必要なロールは Storage Object Admin | #6 で SA に渡す権限が確定 |
| 予算の作成には `roles/billing.admin` か `roles/billing.costsManager` が要る | 前提条件としてスクリプト冒頭に明記 |
| 予算アラートは通知するだけで支出を止めない | 過信しない。`max-instances=3` と併用する |
| `budgets create` は ADC 経由で動き quota project を要求する | `--billing-project` を付ける。付けないと gcloud 共有クライアントプロジェクト（32555940559）にフォールバックし `SERVICE_DISABLED` になる。ADC ログイン自体を前提条件に追加した |
| `--filter-projects` はプロジェクト **番号**（gcloud のリファレンスは `projects/{project_id}` と書いているが、ID では `INVALID_ARGUMENT`） | 番号は作成後にしか分からないのでスクリプト内で `projects describe` から引く |
| `--budget-amount` の通貨サフィックスは省略できる | 省略すると課金アカウントの通貨に従う。明示して不一致だと `INVALID_ARGUMENT` |
| この課金アカウントの通貨は **USD** | `1000JPY` は通らない。金額は `1`（= $1）とし、閾値 10% で $0.10 から通知が飛ぶ |
| 課金アカウントに既存の全体予算 `$20 1 か月の予算のアラート` がある | プロジェクト単位の予算はそれより絞ってよい。最後の砦は確保済み |
| `gcloud storage buckets describe` の出力キーはスネークケース | `versioning_enabled`。`versioning.enabled` は静かに空を返す |
| Artifact Registry の無料枠は 0.5 GB/月、超過は $0.10/GB/月 | cleanup policy を #5 に申し送る |
| gsutil は 2027 年 3 月に gcloud CLI 同梱から外れる | `gcloud storage` に統一 |
| Nix 管理下では `gcloud components install` が使えない | 追加コンポーネントは `withExtraComponents` で宣言的に |
| gcloud 最新 580.0.0 / nixpkgs unstable 579.0.0 | 1 週遅れ。実用上の問題なし |
| Terraform 1.15.8 / google provider 7.44.0 | #5 では `~> 7.0` で固定する |

## 却下した選択肢

| 却下したもの | 理由 |
|---|---|
| state バケットを `asia-northeast1` に置く | Cloud Storage の無料枠は US 限定。Cloud Run から触られないバケットを東京に置く理由が無い |
| 完全冪等なスクリプト（全ステップに存在チェック） | 行数が倍近くになり、「読めば再現できる記録」という狙いが埋もれる。一度きりの手順に自動再実行は要らない |
| `just bootstrap` として justfile に統合 | justfile は #2 の成果物。この issue が #2 に依存してしまう |
| `BILLING_ACCOUNT` をスクリプトにハードコード | 口座に紐づく ID をリポジトリに残さない。実行時に環境変数で渡す |
| lifecycle ルールをヒアドキュメントで埋め込む | JSON が独立していた方がレビューしやすい |
| `gcloud auth application-default login` をスクリプトに含める | ブラウザが開く対話コマンド。末尾の `echo` で案内するに留める |
| soft delete を `--clear-soft-delete` で無効化する | 「versioning と役割が重複する」という前提が誤りだった。守る範囲が違い、しかも state は Always Free 枠内なので費用も発生しない。得るものが無いのに最後の復旧手段を捨てることになる |
| 予算アラートを Console で手作業 | 課金を紐付けた直後が仕掛けどきで、作業が 1 回で済む |
