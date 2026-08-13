Closes #

<!--
タイトルは squash merge 後の main のコミットメッセージになる。
Conventional Commits 形式で書くこと。

  feat: GET /api/todos      →  main に  feat: GET /api/todos (#5)
-->

## 動作確認

<!-- issue の「動作確認コマンド」を実際に実行し、結果を貼る -->

```sh

```

## マージ前チェック

- [ ] CI が緑（`go test` / `golangci-lint`）
- [ ] `/code-review` を実行し、指摘に対応した
- [ ] 上の動作確認を実際に実行した

<!--
branch protection が使えないため、CI が赤くてもマージできる。
CI は「止めてくれる仕組み」ではなく「自分で見る計器」。3 つとも自分で確認する。
-->
