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

            # メジャーを明示して固定する。`pkgs.go` にすると `nix flake update` で
            # 勝手に 1.27 に上がり、go.mod の go directive と Dockerfile (#4) の
            # golang:1.26 だけ取り残される。
            packages = with pkgs; [
              go_1_26
              gopls           # 公式 LSP
              gotools         # goimports / godoc / stringer
              delve           # デバッガ (dlv)
              golangci-lint   # 統合 linter (v2 系)
              just
              jq
            ];

            shellHook = ''
              # `go install` の出力をプロジェクトローカルに分離する。
              # GOPATH (= モジュールキャッシュ) はあえて触らず global 共有を維持。
              export GOBIN="$PWD/.gobin"
              export PATH="$GOBIN:$PATH"
              mkdir -p "$GOBIN"
              echo "→ devShell: $(go version | awk '{print $3}') / golangci-lint $(golangci-lint version --short 2>/dev/null)"
            '';
          };
        });
    };
}
