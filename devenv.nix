{ pkgs, ... }:

{
  languages.go = {
    enable = true;
    lsp.enable = true;
  };

  packages = [ pkgs.gnumake ];

  # Match the Makefile's build environment in Go tools, including gopls.
  env.CGO_ENABLED = "0";
}
