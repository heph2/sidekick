{
  description = "Sidekick agent workflow orchestrator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f:
        nixpkgs.lib.genAttrs systems (system:
          f (import nixpkgs { inherit system; }));
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.git
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.tmux
          ];

          shellHook = ''
            command -v treehouse >/dev/null || echo "warning: treehouse is not in PATH"
            command -v no-mistakes >/dev/null || echo "warning: no-mistakes is not in PATH"
          '';
        };
      });
    };
}
