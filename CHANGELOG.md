# Changelog

## 0.2.0-rc.1 — 2026-11-16

- Replace Forge PAT stdin login and global credential-store writes with a pinned
  Zitadel browser/loopback PKCE client, private session storage and verified refresh.
- Add isolated `brain git` commands and `brain propose` for the first PR; reject
  ambient .netrc/header/cookie/TLS overrides and non-HTTPS transport fallbacks.
- Add explicit API/Git adapter contract and installation instructions. Existing
  KIRA Pocket-ID browser login remains separate. No client/grant/adapter activation.
- Add local synthetic TLS IdP and real Git HTTP fixture coverage.
- Windows session encryption uses current-user DPAPI; native Windows/macOS login
  acceptance and live Zitadel/KIRA acceptance remain open.
