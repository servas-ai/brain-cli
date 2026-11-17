# Signed update preparation

`brain self-update` prepares the shared clidist client required by the CLI
standard. This unsigned release candidate does **not** enable binary replacement
or background update traffic. A stable, approved release must be built with
`-X main.selfUpdateReleaseContract=approved` only after the release contract
below, signing/publication and native acceptance have been completed. Merely
setting a stable VERSION, an environment variable or `enable` does not approve it.

## Separate Human update login

The existing public `servas-cli` app is used only for update downloads:

- Issuer: `https://auth.servas.ai`.
- Native client: `397986586068780672`; resource project: `397986582964995712`.
- Scopes: exactly `openid offline_access
  urn:zitadel:iam:org:project:id:397986582964995712:aud`.
- PKCE S256 / State / nonce, native 127.0.0.1 loopback and verified JWT/RS256.
- Distribution origin: `https://cli.servas.ai`; the existing `cli-user` role is
  required. This client does not create or request grants.

WRK confirmed these public app values through a read-only inventory. They do
not grant KIRA Git/API access and are never used as a Forge-login fallback.
`update-profile.json` / `update-session.json` use the same private storage
contract as Forge authentication, including current-user DPAPI on Windows.
No real update login or production publication was performed during development.

For an approved stable release and an already authorized update user:

```sh
brain self-update login
brain self-update status
brain self-update check       # 0: current, 11: newer authenticated signed version
brain self-update             # verify then atomically install on supported OS
brain self-update rollback    # restore the independently stored, hash-checked .prev
brain self-update disable     # save auto_update=false
brain self-update enable
brain self-update logout
```

`BRAIN_NO_AUTO_UPDATE=1` disables automatic checks. A configured stable release
attempts a bounded background check at most once in 24 hours, without opening a
browser. Authentication failures require an explicit update login; Forge tokens,
Git stores, cookies, PATs and keys are not fallback credentials. Development,
prerelease and custom builds are not automatically replaced. Rollback is an
explicit exception to the no-downgrade rule; the version rolled back from is
skipped automatically. Tampered binaries, state or rollback copies fail closed.

The atomic binary replacement is implemented/tested on Linux. macOS and Windows
native runtime acceptance is open; Windows replacement itself is not implemented
in this preparation and returns a clear failure. Cross-compilation is not native
acceptance. Do not enable the release contract for an unsupported platform.

## Exact release envelope for WRK

The existing clidist protocol returns `GET /v1/brain/latest?os=<os>&arch=<arch>`
with relative version-bound binary, SHA256SUMS and signature URLs. The client
accepts only that origin and exact Brain/version/platform paths, refuses
redirects, and verifies Ed25519 before accepting checksums or version history.
The public release key is embedded; no signing private key exists in this repo.

Existing clidist manifests sign unversioned filenames; unsigned `latest.version`
alone cannot prevent replay under a newer version label. Therefore Brain requires
one additional signed manifest entry. For a stable release `vX.Y.Z`:

1. Build each approved raw binary as `brain-<os>-<arch>` with exactly that release
   version. Keep archive manifests and source revision for the handoff.
2. Create a file named `brain-version-vX.Y.Z` whose exact bytes are `vX.Y.Z` plus
   one newline. Include its SHA256 alongside all raw binaries in SHA256SUMS.
3. Sign the exact SHA256SUMS bytes with the existing authorized Ed25519 release
   key in WRK. The marker and selected binary must be covered by that signature.
4. Publish through the existing clidist release process only after its Brain
   allowlist entry, authorization and acceptance are established. The marker file
   should be uploaded too. The read-only inspected server accepts the marker name
   and extra listed files; comments in SHA256SUMS are rejected.
5. Check the target native platforms, authenticated download, signature/version
   binding, failed download, no-downgrade, replacement and rollback before enabling
   any platform's release contract. Existing unrelated CLI releases are unchanged.

A legacy manifest without the signed Brain version marker is rejected. No
existing release was re-signed, no private key was generated and no central
catalogue/configuration was changed by this source preparation.

Protocol provenance: vmcli `internal/selfupdate` and clidist
`internal/store/verify.go`, as referenced in `selfupdate.go`. This is an independent
standard-library implementation; no source from a repository with unverified
licensing was copied. The embedded public key is public protocol data.
