{
  description = "go-todo devShell (Go 1.26)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      forAllSystems = nixpkgs.lib.genAttrs [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
    in
    {
      devShells = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            name = "go-todo";

            # マイナーを明示して固定する。`pkgs.go` にすると `nix flake update` で
            # 勝手に 1.27 に上がり、go.mod の go directive と backend/Dockerfile の
            # golang イメージだけ取り残される。
            #
            # 揃えるのはマイナーまで（#4 で改定。docs/DESIGN.md §10）。
            # パッチは nixpkgs / Docker Hub / go.mod の 3 系統が独立に動くので
            # 人手では揃わない。下限は go.mod の go directive が保証する。
            packages = with pkgs; [
              go_1_26
              gopls           # 公式 LSP
              gotools         # goimports / godoc / stringer
              delve           # デバッガ (dlv)

              # nixpkgs にバージョン付きの attr が無いため golangci-lint は固定できない。
              # 現在 2.12.2、backend/.golangci.yml は `version: "2"` 形式に依存している。
              # 将来 v3 が来て `nix flake update` を打つと設定が読めなくなるので、
              # そのときは `golangci-lint migrate` を流す。flake.lock がある限り
              # 勝手には上がらない。
              golangci-lint

              just
              jq
            ];

            shellHook = ''
              # `go install` の出力をプロジェクトローカルに分離する。
              # GOPATH (= モジュールキャッシュ) はあえて触らず global 共有を維持。
              #
              # $PWD ではなくリポジトリルートから引く。backend/ で `nix develop` を
              # 叩いた人だけ backend/.gobin という別の置き場を持ってしまうため。
              export GOBIN="$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")/.gobin"
              export PATH="$GOBIN:$PATH"
              mkdir -p "$GOBIN"
              echo "→ devShell: $(go version | awk '{print $3}') / golangci-lint $(golangci-lint version --short 2>/dev/null)"
            '';
          };
        });
    };
}
