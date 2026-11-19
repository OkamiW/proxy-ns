let
  cfg = {};
  getName = pkg: (builtins.parseDrvName (pkg.name or pkg.pname or "")).name;
  unfreePackageError = pkg:
    let
      name = getName pkg;
    in
      throw ''
        devenv: package '${name}' has an unfree license.

        To allow all unfree packages, add this to devenv.yaml:

          allow_unfree: true

        To allow only this package, add this to devenv.yaml:

          nixpkgs:
            permitted_unfree_packages:
              - ${name}
      '';
in cfg // {
  allowUnfreePredicate =
    if cfg.allowUnfree or false then
      (_: true)
    else if (cfg.permittedUnfreePackages or []) != [] then
      (pkg: builtins.elem (getName pkg) (cfg.permittedUnfreePackages or []) || unfreePackageError pkg)
    else
      unfreePackageError;
}