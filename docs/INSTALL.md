# Install and submit the first proposal

This release candidate is a **client preparation**, not a live KIRA activation.
A successful fixture test does not establish a registered Zitadel client, a
customer grant or an installed KIRA adapter. See [OIDC-CONTRACT.md](OIDC-CONTRACT.md).
The existing Pocket-ID browser login remains separate and cannot be used as a
CLI issuer or linked by email. No customer server is needed for local tests.

## Prerequisites supplied by the operator

- Git 2.40+ and a normal browser on the same computer as `brain`.
- Existing GitHub workflows additionally require the existing authenticated `gh` CLI.
  Forge workflows never use `gh` as an authentication fallback.
- An existing authorized public native client at `https://auth.servas.ai` with
  Authorization Code + PKCE S256, no client secret; JWT/RS256 access tokens with jti, nbf, iat and exact native-client azp (ID tokens are rejected as credentials).
- Loopback registration `http://127.0.0.1:<ephemeral-port>/callback` with the
  native-client port-matching rule of RFC 8252. Support for the actual dynamic
  port must be confirmed on the registered client before activation.
- The resource **project** ID as the expected access-token audience, and
  `openid offline_access urn:zitadel:iam:org:project:id:RESOURCE_PROJECT_ID:aud`.
  Only these three minimal scopes are accepted (derived by default from the
  audience). Additional resource or management API scopes are rejected.
- The existing HTTPS Forge origin, repository path, existing Zitadel Human
  identity and explicit `(issuer, sub) -> existing Forgejo user ID` mapping.
  Existing repository/team/branch rights must cover read, a proposal branch
  and PR creation. No new account, grant or client is created by this CLI.
- The KIRA owner has implemented and accepted the narrow API/Git adapter
  described in the protocol document. Direct native Forgejo cannot accept
  these Zitadel tokens. No adapter is included or deployed by this PR.
- TLS certificates trusted by the OS/browser, Go and Git. For private KIRA
  TLS, import the operator-provided CA; never disable certificate verification.

The current reviewed sources do **not** establish the public native client ID,
resource project ID, authorized Human mapping or adapter activation. These are
explicit activation prerequisites; no existing unrelated servas CLI client may
be reused merely because its ID is known.

## Obtain the client

Use the archive and `SHA256SUMS` attached to the implementation issue. The
candidate version is `0.2.0-rc.7`; it is not a signed clidist production release.
All six OS/architecture archives are cross-built; Linux amd64 is exercised.
Windows DPAPI and macOS runtime behavior need their native acceptance before
claiming support for production Human login on those platforms.

Linux amd64 (macOS: use the matching darwin archive):

```sh
sha256sum --check --ignore-missing SHA256SUMS
# Require the selected archive's line to say OK; no missing/all-skipped result.
tar -xzf brain_0.2.0-rc.7_linux_amd64.tar.gz
install -d "$HOME/.local/bin"
install -m 0755 brain "$HOME/.local/bin/brain"
export PATH="$HOME/.local/bin:$PATH"
brain version
brain guide
```

On macOS use `shasum -a 256 -c SHA256SUMS` with the selected archive's checksum
line. On Windows extract the matching ZIP, compare `Get-FileHash -Algorithm
SHA256` against the exact archive line in `SHA256SUMS`, then place `brain.exe`
in a user PATH directory. Run the shell workflow below in Git Bash (included
with Git for Windows). Keep the included `LICENSE`, `THIRD-PARTY.md` and docs.

## Configure and sign in

The operator supplies the following **public** values. The examples use shell
variables for those values; they are not tokens. Do not copy browser cookies,
PATs, API keys or SSH keys into this flow.

```sh
FORGE_URL='https://git.example.invalid' # replace with the accepted Forge origin
CLIENT_ID='operator-supplied-native-client-id'
RESOURCE_PROJECT_ID='operator-supplied-resource-project-id'
REPOSITORY='brain/brain'              # replace with the allowed owner/repository

brain auth configure --url "$FORGE_URL" --client-id "$CLIENT_ID" \
  --audience "$RESOURCE_PROJECT_ID" \
  --scopes "openid offline_access urn:zitadel:iam:org:project:id:$RESOURCE_PROJECT_ID:aud"
brain login
brain auth status
```

`brain login` opens the normal browser. Complete sign-in within three minutes
on this computer. The callback listens only on `127.0.0.1`, validates State,
issuer, PKCE S256 and nonce, and closes after the attempt. No manual token input
or browser-cookie extraction is supported. Status returns secret-free JSON
`{"configured":true,"authenticated":true}` after successful token validation.

Unix session/config files are atomically written to the OS user config
location `brain/auth`, directory 0700/files 0600, outside all repositories.
Symlinks and public token files are rejected. On Windows the session is encrypted
for the current user with OS DPAPI; POSIX mode bits alone are not treated as
confidentiality there. No portable app key is stored. Do not copy session files
between users or systems. Changing profile identity/origin requires a new login.

## Clone, commit and open the first proposal

```sh
brain git -- clone "$FORGE_URL/$REPOSITORY.git" brain-work
cd brain-work
brain git -- switch -c proposal/first-change
# Edit the intended Brain document with your normal editor.
brain git -- add README.md
# If Git asks for author details, configure your public name/email locally:
brain git -- config user.name 'Your Name'
brain git -- config user.email 'your-public-address@example.invalid'
brain git -- commit -m 'First Brain proposal'
brain git -- push -u origin proposal/first-change
brain propose --repo "$REPOSITORY" --branch proposal/first-change \
  --base main --title 'First Brain proposal'
```

The final command prints the proposal URL. Use the existing Dashboard workflow
for its review/acceptance; the client does not merge it or grant rights. Use
`brain git -- fetch origin` for subsequent fetches. The wrapper requires a valid
OIDC session, isolates ambient Git config/cookies/.netrc/client certificates,
disables redirects and credential traces, and passes the access token through
Git's internal credential pipe only to the configured HTTPS origin. Repository
HTTP/credential overrides and remote URLs outside that origin are rejected.
Branch and default-push upstreams must name an inspected remote; direct upstream
URLs are rejected before contacting even an unauthenticated foreign server.
Network Git accepts only the documented options and explicit HTTPS URLs or
configuration-backed remote names. Legacy .git/remotes and .git/branches files,
remote groups and additional bundle transports are unsupported. Fetch/push and
ls-remote require an explicit remote; pull uses the inspected branch upstream.
Use the OS trust store or `GIT_SSL_CAINFO` for a private CA. The wrapper supports
the documented Git workflow and common local commands; arbitrary Git aliases
and authentication overrides are not supported. Clone accepts the normal HTTPS
URL/target plus --no-checkout, --single-branch, --quiet, --depth, --branch and
--filter=blob:none. It uses an empty template; user-supplied templates and
recursive submodule transports are rejected. The config command is limited to
your public user.name/user.email. It does not install a global
helper or populate a Git credential store. Repository permissions remain server
decisions. Ordinary bare `git push` is not configured by login.

For a Forge Brain keep the existing non-secret `.brain/forge.yml`:

```yaml
typ: forgejo
url: https://git.example.invalid
org: brain
```

`brain sync` uses the verified OIDC session for Forge discovery and network Git;
returned clone URLs must remain on the configured origin. Failed authentication
aborts a Forge sync before any pull, without falling back to a stored PAT.
HTTPS GitHub brains retain their existing `gh` identity in a separate isolated
Git process. Only the fixed GitHub credential helper sees the original gh login
configuration; Git itself receives an empty home/global configuration and template.
Ambient Git credential stores, headers, remote overrides and cookies are ignored.
Redirects are disabled. Every other remote requires the isolated OIDC worker,
even if `.brain/forge.yml` is missing or the saved profile is absent or invalid.
`sync` checks the root, workspaces and personal remotes before its first pull;
Git URL rewrites and direct branch-remote URLs outside GitHub cannot opt into
the GitHub exception. GitHub pulls name their checked upstream explicitly;
remote transport helpers are rejected and clones use an empty template. Legacy
SSH and local-file network workflows are not accepted by these commands.

## Refresh, logout and failures

Access tokens are verified on use. Within one minute of saved expiry the client
refreshes automatically, rechecks issuer/audience/signature/subject and saves
rotated credentials atomically. Rejected refresh requires `brain login` again;
there is no fallback. `brain auth logout` requests revocation of refresh and
access tokens, then deletes the local session. It reports unconfirmed revocation
as a failure and does not claim an immediate ban of already issued JWTs: the
server must enforce its own revocation/expiry policy. Logout leaves the IdP's
browser session intact and does not sign other applications out.

Wrong issuer/audience/State, expired or invalid tokens, unsafe endpoints,
untrusted TLS, origin mismatch and redirects fail closed. Exit codes are 0
(success), 1 (auth/network/operation error), 2 (invalid arguments). `brain git`
passes through Git's nonzero exit status. Authentication errors never include
OAuth response bodies, tokens or callback codes. Do not enable packet capture
or tracing on authentication requests. A normal authorization-code redirect
necessarily carries its single-use code on loopback; access/refresh/ID tokens
never enter URLs or process arguments.

## Build and verify without a real account

Packaging also needs Python 3, tar and sha256sum (zip is optional).
Each archive includes MANIFEST.json with its source revision and unsigned-build status.
For a focused Linux build use TARGETS=linux/amd64; the default builds all six targets.
Run in your own test directory, with caches/temp outside a Paperclip workspace:

```sh
export TMPDIR=/srv/test/pc-tool-verify/DKS-735/tmp
export GOPATH=/srv/test/pc-tool-verify/DKS-735/cache/go
export GOCACHE=/srv/test/pc-tool-verify/DKS-735/cache/go-build
mkdir -p "$TMPDIR" "$GOPATH" "$GOCACHE"
go test -race ./...
go vet ./...
VERSION=0.2.0-rc.7 ./build.sh
```

Tests generate their own local TLS IdP, signing keys and identities. The Git
fixture uses real `git http-backend` for Clone/Fetch/Push and a minimal synthetic
PR endpoint with denied foreign identity/repository cases. This verifies the
client wire contract, not an actual Forgejo deployment or the pending KIRA adapter.
There is no runtime issuer override, insecure TLS mode, test login or new grant.
