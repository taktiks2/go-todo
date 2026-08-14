# GCP bootstrap 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** GCP プロジェクト・課金・API・Terraform state バケット・予算アラートを一度だけ手で作り、その手順を `scripts/bootstrap.sh` に残す。

**Architecture:** 冪等化しない直列シェルスクリプト 1 本 + lifecycle rule の JSON 1 本。Claude がファイルを書き、人間が GCP に対して実行する。Terraform の `backend "gcs"` はバケットの存在を前提にするため、このスクリプトは Terraform より手前に立つ（#5 の前提）。

**Tech Stack:** bash / gcloud CLI 580 系（`gcloud storage`。`gsutil` は使わない）

**Spec:** `docs/superpowers/specs/2026-08-14-gcp-bootstrap-design.md`

## Global Constraints

- `PROJECT_ID` は `taktiks2-go-todo`。**GCP の仕様上あとから変更できない**
- バケットは `taktiks2-go-todo-tfstate`、location は `us-central1`（Cloud Storage の Always Free は `us-east1` / `us-west1` / `us-central1` 限定）
- Cloud Run のリージョンは `asia-northeast1`。ただし**このスクリプトには登場しない**（#5 の管轄）
- `BILLING_ACCOUNT` はリポジトリに書かない。実行時に環境変数で渡す
- `gsutil` は使わない。`gcloud storage` に統一する
- スクリプトは冪等化しない。一度きりの手順として書く
- `main` に直接コミットしない。ブランチ `1-chore-gcp-プロジェクトと-terraform-state-バケットを用意する` は作成済みで、チェックアウト済み
- `mode:ai-only` の issue なので Claude がファイルを書いて適用してよい（`CONTRIBUTING.md` §1）
- TDD は適用しない。一度きりの手動スクリプトで、テストで守れる振る舞いが無い
- コミットは Conventional Commits（`CONTRIBUTING.md` §4）

## File Structure

| ファイル | 責務 |
|---|---|
| `scripts/bootstrap.sh` | GCP 側のリソースを作る手順そのもの。上から読めば再現できる記録を兼ねる |
| `scripts/tfstate-lifecycle.json` | state バケットの lifecycle rule。`--lifecycle-file` がパスを取るので独立ファイルにする |
| `docs/DESIGN.md` | 確定した決定値と、その判断理由を残す |

---

### Task 1: bootstrap スクリプトと lifecycle ルールを作る

**Files:**
- Create: `scripts/tfstate-lifecycle.json`
- Create: `scripts/bootstrap.sh`

**Interfaces:**
- Consumes: なし（最初のタスク）
- Produces: 環境変数の契約 `PROJECT_ID`（既定 `taktiks2-go-todo`）/ `BUCKET`（既定 `${PROJECT_ID}-tfstate`）/ `BUCKET_LOCATION`（既定 `us-central1`）/ `BILLING_ACCOUNT`（必須）。Task 2 以降はこの既定値を前提にする

- [ ] **Step 1: `scripts/tfstate-lifecycle.json` を作る**

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

条件は AND で評価される。「現行でない」かつ「新しい世代が 5 つ以上ある」かつ「非現行になって 30 日経った」世代だけを消す。直近 5 世代は日数に関係なく必ず残る。

- [ ] **Step 2: JSON として妥当か確認する**

Run: `jq . scripts/tfstate-lifecycle.json`
Expected: 整形された JSON が出力される。パースエラーが出ないこと

- [ ] **Step 3: `scripts/bootstrap.sh` を作る**

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

- [ ] **Step 4: 実行権限を付ける**

Run: `chmod +x scripts/bootstrap.sh`

- [ ] **Step 5: 構文チェック**

Run: `bash -n scripts/bootstrap.sh`
Expected: 出力なし・終了コード 0

- [ ] **Step 6: shellcheck をかける**

`shellcheck` は未インストールだが、nix があるのでインストールせずに実行できる。

Run: `nix run nixpkgs#shellcheck -- scripts/bootstrap.sh`
Expected: 指摘なし。出たら直す（`SC2086` の未クォート変数など）

- [ ] **Step 7: 必須環境変数のガードが効くことを確認する**

`BILLING_ACCOUNT` 未設定なら、GCP に何も作らずに落ちるはず。**これが唯一の安全な実行前チェック**。

Run: `env -u BILLING_ACCOUNT bash scripts/bootstrap.sh; echo "exit=$?"`
Expected: `BILLING_ACCOUNT: required. Run: gcloud billing accounts list` が stderr に出て `exit=1`。`gcloud projects create` は**実行されない**

- [ ] **Step 8: コミット**

```bash
git add scripts/bootstrap.sh scripts/tfstate-lifecycle.json
git commit -m "$(cat <<'MSG'
chore: GCP bootstrap スクリプトを追加

Terraform state バケットは backend の前提なので Terraform で作れない。
gcloud コマンドとして scripts/bootstrap.sh に記録し、手で流す。

state バケットは Cloud Storage の Always Free に合わせて us-central1 に置く。
Cloud Run から触られないので東京に置く理由が無い。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 2: bootstrap を実行して GCP 側を作り、完了条件を検証する

**Files:**
- 変更なし（GCP 側の状態だけが変わる）

**Interfaces:**
- Consumes: Task 1 の `scripts/bootstrap.sh` と `scripts/tfstate-lifecycle.json`
- Produces: 検証コマンドの実行結果。Task 4 の PR 本文にそのまま貼る

**このタスクは人間が実行する。** Claude はコマンドを提示し、結果を受け取って判定する。GCP に課金アカウントを紐付ける操作を含むため、Claude が勝手に走らせない。

- [ ] **Step 1: 課金アカウント ID を確認する**

Run: `gcloud billing accounts list`
Expected: `ACCOUNT_ID` が 1 件以上、`OPEN` が `True`。確認済みの値は `XXXXXX-XXXXXX-XXXXXX`

- [ ] **Step 2: bootstrap を実行する**

```sh
env BILLING_ACCOUNT=XXXXXX-XXXXXX-XXXXXX ./scripts/bootstrap.sh
```

`VAR=value cmd` の形は fish では構文エラーになる。ユーザーのシェルは fish なので `env` を前置する（bash / zsh でもそのまま動く）。

Expected: 手順 3 の後に 30 秒待ち、最後に `bootstrap 完了。` のブロックが出る。

途中で落ちた場合は、スクリプト冒頭のコメントの対処表に従って原因を直し、**そのステップ以降のコマンドを手で流し直す**。スクリプト全体を再実行すると手順 1 の `projects create` で必ず落ちる（冪等化していないので、これは想定どおりの挙動）。

- [ ] **Step 3: プロジェクトと課金を確認する**

```bash
gcloud projects describe taktiks2-go-todo --format='value(projectId,lifecycleState)'
gcloud billing projects describe taktiks2-go-todo --format='value(billingEnabled)'
```

Expected: `taktiks2-go-todo	ACTIVE` と `True`

- [ ] **Step 4: API が 11 個有効になっていることを確認する**

```bash
gcloud services list --enabled --project taktiks2-go-todo
```

Expected: 以下 11 個が含まれる。

```
artifactregistry.googleapis.com
billingbudgets.googleapis.com
cloudbilling.googleapis.com
cloudresourcemanager.googleapis.com
iam.googleapis.com
iamcredentials.googleapis.com
run.googleapis.com
secretmanager.googleapis.com
serviceusage.googleapis.com
storage.googleapis.com
sts.googleapis.com
```

GCP が自動で有効化した API（`logging` など）が余分に並ぶのは正常。上記 11 個が**欠けていない**ことだけを見る。

- [ ] **Step 5: バケットの設定を確認する**

```sh
gcloud storage buckets describe gs://taktiks2-go-todo-tfstate \
  --format='value(location,versioning_enabled,soft_delete_policy.retentionDurationSeconds,lifecycle_config)'
```

Expected:
- `location` が `US-CENTRAL1`
- `versioning_enabled` が `true`
- `soft_delete_policy.retentionDurationSeconds` が `604800`（既定の 7 日）
- `lifecycle_config` に `isLive: False` / `numNewerVersions: 5` / `daysSinceNoncurrentTime: 30` を含むルールが 1 件

**フィールド名に注意。** 出力キーはスネークケースで `versioning_enabled`。`versioning.enabled` と書くと**エラーにならず静かに空を返す**ので、バケットが正しいのに検証が落ちたように見える。入れ子の中（`retentionDurationSeconds`）だけはキャメルのままで、綴りが揃っていない。

- [ ] **Step 6: 予算アラートを確認する**

```sh
gcloud billing budgets list --billing-account=XXXXXX-XXXXXX-XXXXXX --billing-project=taktiks2-go-todo
```

Expected: `displayName: go-todo` の予算が 1 件。`amount.specifiedAmount.units` が `1`、`budgetFilter.projects` が `projects/14452670131`、`thresholdRules` に 0.1 / 0.5 / 1.0 の 3 件。

`list` にも `--billing-project` が要る（`create` と同じ理由）。課金アカウントには既存の全体予算 `$20 1 か月の予算のアラート` も並ぶが、これは #1 の成果物ではない。

- [ ] **Step 7: 実行結果を控える**

Step 3〜6 の出力をそのままコピーしておく。Task 4 の PR 本文に貼る。

このタスクにコミットは無い（GCP 側の状態変更のみ）。

---

### Task 3: `docs/DESIGN.md` に決定値と判断理由を残す

**Files:**
- Modify: `docs/DESIGN.md`（§9 IaC / §14 リスク / §14 着手時に決めること）

**Interfaces:**
- Consumes: Task 2 で確定した実際の値
- Produces: なし（#5 以降が読む記録）

`CONTRIBUTING.md` §8「設計判断が変わったら、それを起こした PR の中で `docs/DESIGN.md` を直す」に従う。別 PR に切り出さない。

- [ ] **Step 1: §9 の「IaC: Terraform」小節の末尾に決定値を追記する**

`### IaC: Terraform` の 4 つの箇条書きの直後、`### CI/CD: GitHub Actions + Workload Identity Federation` の手前に挿入する。

```markdown
#### 決定値（#1 で確定）

| 項目 | 値 |
|---|---|
| `PROJECT_ID` | `taktiks2-go-todo`（GCP の仕様上あとから変更できない） |
| state バケット | `gs://taktiks2-go-todo-tfstate` |
| バケットの location | `us-central1` |
| 有効化した API | `cloudresourcemanager` / `serviceusage` / `storage` / `iam` / `iamcredentials` / `sts` / `run` / `artifactregistry` / `secretmanager` / `cloudbilling` / `billingbudgets` |

**state バケットだけ `us-central1` に置く。** Cloud Storage の Always Free は US リージョン（`us-east1` / `us-west1` / `us-central1`）限定で `asia-northeast1` は対象外。state バケットは Cloud Run から一切触られず、触るのはローカルの `terraform` と GitHub Actions（US ランナー）だけなので、東京に置く理由が無い。Cloud Run 自身は `asia-northeast1` のまま。

**バケットの soft delete は既定の 7 日のまま残す。** 当初は「Object Versioning と役割が重複する」として無効化したが、これは誤りだった。versioning が守るのは「上書き」と「現行世代の削除」だけで、`rm --all-versions` やバケットごと消す操作からは守れない。soft delete はそこを埋める別のレイヤで、state を吹き飛ばしたときの最後の復旧手段になる。state は数十 KB で Always Free の 5 GB 枠に収まるため、保持コストはゼロに丸まる。旧世代の刈り取りは lifecycle rule が別途担当する（非現行かつ新しい世代が 5 つ以上あり、非現行になって 30 日経ったものを削除）。

**予算アラートをプロジェクト単位で張っている。** 課金アカウントの通貨は USD で、予算額は $1、閾値は 10% / 50% / 100%。$0.10 の支出で最初の通知が飛ぶ。無料枠を外れたことに気づくのが目的で、**アラートは支出を止めない**。課金アカウント全体には既存の $20 予算が別途あり、そちらが最後の砦になる。

手順は `scripts/bootstrap.sh` にある。**このスクリプトは冪等ではない。** 一度きりの記録として読む。
```

- [ ] **Step 2: §14「着手時に決めること」の表に 2 行足す**

`| GCP プロジェクト | **新規作成する** |` の直後に挿入する。

```markdown
| GCP プロジェクト ID | `taktiks2-go-todo`（#1 で確定。変更不可） |
| state バケットの location | `us-central1`（無料枠が US 限定のため Cloud Run と分ける） |
```

- [ ] **Step 3: §14「リスク」に 2 項目足す**

`- **Phase 0 が最難関**。` の箇条書きの後に追加する。

```markdown
- **Artifact Registry の無料枠は 0.5 GB/月**。distroless イメージ約 20 MB × デプロイ回数で、30〜40 回のデプロイで超える。超過は $0.10/GB/月 なので額は小さいが、#5 でリポジトリを作るときに cleanup policy を入れる
- **Cloud Run の無料枠にリージョン制限があるか未確認**。公式の Free Tier ページは Cloud Storage にだけ「US リージョンのみ」と明記し、Cloud Run には書いていないが、二次情報は US 3 リージョン限定と主張している。**#5 でリージョンを確定する前に公式ページで確認する。** US 限定なら `asia-northeast1` 前提そのものを見直すことになる
```

- [ ] **Step 4: 追記が既存の見出し階層と衝突していないか確認する**

Run: `grep -n 'IaC: Terraform\|決定値（#1 で確定）\|CI/CD: GitHub Actions' docs/DESIGN.md`
Expected: 3 件が行番号の昇順で `IaC: Terraform` → `決定値（#1 で確定）` → `CI/CD: GitHub Actions` の順に並ぶ

- [ ] **Step 5: コミット**

```bash
git add docs/DESIGN.md
git commit -m "$(cat <<'MSG'
docs: #1 で確定した GCP の決定値を DESIGN に反映

PROJECT_ID / state バケット / location / 有効化した API を記録し、
state バケットだけ us-central1 に置く理由と、soft delete を既定のまま残す理由を残した。

Artifact Registry の 0.5 GB 無料枠と、Cloud Run 無料枠のリージョン制限が
未確認である点をリスクに追加した。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 4: issue 本文を更新し、レビューを経て PR を出す

**Files:**
- 変更なし（GitHub 側のみ）

**Interfaces:**
- Consumes: Task 2 の検証結果、Task 1 と Task 3 のコミット
- Produces: マージ可能な PR

- [ ] **Step 1: issue #1 の本文を実際のスコープに合わせて更新する**

調査で API が 5 個では足りないことと、`gsutil` を使わないこと、soft delete / lifecycle / 予算アラートが加わったことを反映する。

```bash
gh issue edit 1 --body "$(cat <<'BODY'
## 何を作るか

GCP プロジェクトを新規作成し、Terraform の state を置く GCS バケットを用意する。
Terraform を書き始めるための前提を整える回。

**state 用バケットは Terraform で作れない**（鶏と卵）。だから `scripts/bootstrap.sh` に
`gcloud` コマンドとして記録し、手動実行する。

## スコープ

**含む**

- GCP プロジェクトの新規作成と課金アカウントの紐付け
- API の有効化（11 個）
- Terraform state 用 GCS バケットの作成
  - location は `us-central1`（Cloud Storage の Always Free は US リージョン限定）
  - バージョニング有効
  - soft delete は既定の 7 日のまま（versioning とは守る範囲が違う）
  - 旧世代を刈る lifecycle rule
- 予算アラートの作成（プロジェクト単位）
- `scripts/bootstrap.sh` に上記コマンドを記録

**含まない**

- Terraform のコード（#5 で書く）
- WIF（#6）
- Artifact Registry の cleanup policy（#5 で入れる）
- Firebase 関連（Phase 3 / 4）

## 完了条件

- [ ] 新規 GCP プロジェクトが存在し、課金が有効
- [ ] 11 個の API が有効
- [ ] state 用 GCS バケットが `us-central1` に存在し、バージョニングが有効
- [ ] バケットに lifecycle rule が設定されている
- [ ] プロジェクト単位の予算アラートが存在する
- [ ] `scripts/bootstrap.sh` に手順が残っており、読めば再現できる

## 確認手順

```sh
gcloud projects describe taktiks2-go-todo --format='value(projectId,lifecycleState)'
gcloud billing projects describe taktiks2-go-todo --format='value(billingEnabled)'
gcloud services list --enabled --project taktiks2-go-todo
gcloud storage buckets describe gs://taktiks2-go-todo-tfstate \
  --format='value(location,versioning_enabled,soft_delete_policy.retentionDurationSeconds,lifecycle_config)'
gcloud billing budgets list --billing-account=$BILLING_ACCOUNT --billing-project=taktiks2-go-todo
```

## 手動でやること

- `scripts/bootstrap.sh` の実行（`env BILLING_ACCOUNT=... ./scripts/bootstrap.sh`）

## 参照

- `docs/DESIGN.md` §9 インフラ / IaC: Terraform
- `docs/superpowers/specs/2026-08-14-gcp-bootstrap-design.md`
BODY
)"
```

- [ ] **Step 2: 更新結果を確認する**

Run: `gh issue view 1`
Expected: スコープに「API の有効化（11 個）」「予算アラート」が入り、完了条件が 6 項目になっている

- [ ] **Step 3: コードレビューをかける**

Run: `/code-review`

`CONTRIBUTING.md` §6 の品質ゲート 3 つのうち 2 つ目。CI はまだ存在しないので（#7 で作る）、この issue で確認できるのは `/code-review` と受け入れ条件の実行の 2 つ。指摘は盲信も無視もせず、根拠を確認する。

- [ ] **Step 4: ブランチを push して PR を作る**

PR 本文の `<...>` には Task 2 Step 7 で控えた**実際の出力**を貼る。貼る前に、課金アカウント ID が出力に含まれていないか確認すること（`billing budgets list` の出力にはリソース名として含まれるので、その行は伏せる）。

```bash
git push -u origin HEAD
gh pr create \
  --title "chore: GCP プロジェクトと Terraform state バケットを用意する" \
  --body "$(cat <<'BODY'
Closes #1

## 動作確認

$ gcloud projects describe taktiks2-go-todo --format='value(projectId,lifecycleState)'
<実際の出力>

$ gcloud billing projects describe taktiks2-go-todo --format='value(billingEnabled)'
<実際の出力>

$ gcloud services list --enabled --project taktiks2-go-todo
<実際の出力>

$ gcloud storage buckets describe gs://taktiks2-go-todo-tfstate --format='value(location,versioning_enabled,soft_delete_policy.retentionDurationSeconds,lifecycle_config)'
<実際の出力>

$ gcloud billing budgets list --billing-account=<伏せる> --billing-project=taktiks2-go-todo
<実際の出力（go-todo の 1 件だけに絞って貼る）>

## 申し送り

- Artifact Registry の cleanup policy は #5 で入れる（無料枠 0.5 GB/月）
- Cloud Run 無料枠のリージョン制限は #5 の着手前に公式ページで確認する

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

- [ ] **Step 5: squash merge する**

`CONTRIBUTING.md` §5 のとおり squash merge。PR タイトルがそのまま `main` のコミットメッセージになる。

```bash
gh pr merge --squash --delete-branch
```

---

## 未決事項

- **ブランチ名に日本語が入っている**（`1-chore-gcp-プロジェクトと-terraform-state-バケットを用意する`）。`gh issue develop` が issue タイトルから生成したもの。この issue では実害が無いが、CI やコンテナタグで URL エンコードが絡む前に ASCII 名の運用へ切り替えるか、`CONTRIBUTING.md` §4 に `gh issue develop N --name <ascii>` を明記するかを決める。Task 4 の Step 5 で `--delete-branch` するので、この PR 限りで消える
