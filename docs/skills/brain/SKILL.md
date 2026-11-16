---
name: brain
summary: Use the installed Brain client for synchronisation and authenticated Human proposals.
---

Run `brain guide` for supported commands and `brain version` for the installed
version. Use `brain auth status` to check authentication without printing credentials.
Human sign-in uses the normal browser with `brain login`; never request a PAT,
cookie, API key or SSH key. Use `brain git -- ...` for network Git and
`brain propose --repo owner/repo --branch branch --title 'Title'` to submit a PR.
The operator must already have provided the registered Zitadel client/resource
and accepted KIRA adapter. Do not register clients, create accounts or grants,
change infrastructure, or weaken TLS to make login work. Stop on auth failures
and report the secret-free error. Use docs/INSTALL.md for installation and scope.
