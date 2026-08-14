---
name: 垂直スライス（Go 実装）
about: domain → service → repository → http を縦に一本通す。実装は人間が手で書く
title: ''
labels: 'mode:pair-tdd'
assignees: ''
---

## 何を通すか

<!-- 1 エンドポイント相当に収める。例: POST /api/todos -->

## スコープ

**含む**

-

**含まない**

<!-- 「今回やらないこと」を書く。書かないと着手後に膨らむ -->

-

## 受け入れ条件

- [ ] `just test` が緑
- [ ] `just lint` が緑
- [ ]

## 動作確認コマンド

マージ前に手で実行する。**テストが緑でも実際は動かないケースを捕まえる唯一の手段。**
実行結果は PR 本文に貼る。

```sh

```

## 参照

- `docs/DESIGN.md` §

---

<!--
進め方は CONTRIBUTING.md を参照。

  gh issue develop <N> --checkout
  /handle-issue <N>
  → pair-tdd ループ（Claude がテスト、あなたが実装）
  → gh pr create → CI / code-review / 上の curl → squash merge

Claude は Go の実装コードを適用しない。テストを書き、実装は提示に留める。
-->
