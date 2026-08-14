# 引数なしで実行したらレシピ一覧を出す
default:
    @just --list

# go.mod は backend/ にあるが justfile はルートに置くので、
# working-directory 属性で寄せる（just 1.38.0 以降）。
# `cd backend &&` を各行に書かないためのもの。

[working-directory('backend')]
dev:
    go run ./cmd/api

[working-directory('backend')]
test:
    go test ./...

[working-directory('backend')]
lint:
    golangci-lint run

[working-directory('backend')]
fmt:
    golangci-lint fmt
