# brain-cli

Build with Go >= 1.24.13; Git >= 2.40 is required by integration tests.
Run `go test -race ./...` and `go vet ./...`, then `VERSION=<version> ./build.sh`.
Use your assigned test directory for TMPDIR/GOPATH/GOCACHE and build outputs.
See docs/INSTALL.md for the customer path and docs/OIDC-CONTRACT.md for boundaries.
Authentication output is English. Never introduce PAT/cookie/SSH fallbacks,
unverified TLS, issuer overrides or automatic client/account/grant creation.
Only local synthetic IdP/Forgejo fixtures may run unattended. Tokens must not
appear in argv, URLs, logs or repositories; credential helper stdout is an
internal Git pipe. Production issuer is https://auth.servas.ai.
