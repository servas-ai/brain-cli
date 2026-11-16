# Changelog

## 0.2.0-rc.6 — 2026-11-16

- Replace Forge PAT stdin login and global credential-store writes with a pinned
  Zitadel browser/loopback PKCE client, private session storage and verified refresh.
- Add isolated `brain git` commands and `brain propose` for the first PR; reject
  ambient .netrc/header/cookie/TLS overrides and non-HTTPS transport fallbacks.
- Add explicit API/Git adapter contract and installation instructions. Existing
  KIRA Pocket-ID browser login remains separate. No client/grant/adapter activation.
- Add local synthetic TLS IdP and real Git HTTP fixture coverage.
- Require OIDC for every non-GitHub sync/clone-cell remote, including missing or
  corrupt profiles and removed Forge markers; preflight workspaces and Git URL
  rewrites before pulling. Disable redirects on the existing HTTPS GitHub path.
- Add a gated signed clidist updater with a separate Human session, version-bound
  checksums, Linux atomic replacement and verified rollback. No live publication;
  Windows replacement remains open. Convert legacy CLI output/guidance to English.
- Isolate the GitHub transport as well, retaining only its explicit existing gh
  identity instead of inheriting global Git remote/credential configuration.
- Windows session encryption uses current-user DPAPI; native Windows/macOS login
  acceptance and live Zitadel/KIRA acceptance remain open.
