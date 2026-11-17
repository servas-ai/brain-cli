# KIRA / Forgejo / Zitadel: belegter Anschlussvertrag

Stand 2026-11-16, ausschließlich lesende Quellenprüfung. KIRA `servas-ai/kira-nas` main = **788827c4a889e0854c571cf5475109b261098ee3**; Forgejo-Pin **15.0.10-rootless** (`ansible/roles/stack/defaults/main.yml:29`). Der vorhandene GitHub-Connector liest das private Repo; die lokale gh-Auth konnte es nicht auflösen. Kein Kundenserver, Login, Konto, Grant, Infra, KIRA-Code, Push oder Merge wurde ausgeführt.

## Urteil und Bestand

**Kein vorhandenes Zitadel→Forgejo-Gateway belegt.** Der KIRA-Main-Stack bietet Pocket-ID-Web-OIDC sowie einen privilegierten internen API-Sudo-Weg; er akzeptiert Zitadel-Access-Tokens nicht unmittelbar. Ein rein lokaler CLI-Prototyp muss vor einem späteren Einsatz an einen vom KIRA-Lead implementierten und abgenommenen Gateway-Vertrag gebunden werden. Ohne belegte bestehende Zitadel-Parameter bleibt der Live-Weg geschlossen.

- [OIDC-Verifier](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/services/kira-brain/oidc.go#L61): RS256, Signatur/JWKS, exakt `iss`, Zeitprüfung, `aud` enthält **Issuer**, `client_id == konfigurierter Client` (:79–119). Das ist der bestehende Pocket-ID-Vertrag, keine erlaubte Ausnahme für den neuen strikt Zitadel-gepinnten Weg.
- [Identitätsauflösung](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/services/kira-brain/clients.go#L178): `GET /api/v1/admin/users?login_name=<sub>&source_id=<konfigurierte Quelle>`; Treffer muss `LoginName == sub` sein (:178–201). Ohne SourceID wird jede externe Quelle akzeptiert: für neuen Vertrag unzureichend. [rights.go:45–83](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/services/kira-brain/rights.go#L45) verlangt UserInfo.sub-Gleichheit, lehnt Site-Admins ab und schneidet Forgejo-Sicht mit Gruppen-/Mirror-Sicht.
- [Interner API-Weg](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/services/kira-brain/clients.go#L102): `Authorization: token <kira-svc>` + optional `Sudo: <login>`, **Site-Admin-Identität erforderlich**, kein eng begrenztes Human-Git-Token (:102–129). Bestehende Service-Scopes: `read:admin,read:user,write:organization,write:repository,write:issue` ([bootstrap/defaults:17](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/ansible/roles/bootstrap/defaults/main.yml#L17)); für neuen Gateway-Zugang nicht übernehmen.
- [Caddyfile:57–59](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/ansible/roles/stack/templates/Caddyfile.j2#L57) leitet git-Host direkt an `forgejo:3000`; kein Auth-Gateway. [compose:110–118](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/ansible/roles/stack/templates/compose.yaml.j2#L110) aktiviert externe Auto-Registrierung und `ACCOUNT_LINKING=auto`, `USERNAME=email`. Diesen E-Mail-Linkweg nicht auf Zitadel ausweiten.

## Vorhandene öffentliche Parameter / offene Live-Parameter

| Parameter | Belegt |
| --- | --- |
| Neuer Human-Issuer | `https://auth.servas.ai` laut Auftrag; Brain `public/entscheidungen/cli-standard/README.md:20` verlangt PKCE und Loopback 127.0.0.1. |
| KIRA-Bestand | Pocket-ID-Clients **forgejo**, **open-webui**, **kira-dash**, Scopes `openid email profile groups`; [bootstrap/defaults:42–58](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/ansible/roles/bootstrap/defaults/main.yml#L42), [forgejo.yml:525–532](https://github.com/servas-ai/kira-nas/blob/788827c4a889e0854c571cf5475109b261098ee3/ansible/roles/bootstrap/tasks/forgejo.yml#L525). Das sind **keine belegten Zitadel-Client-IDs**. |
| KIRA-Callbacks | Forgejo `<git>/user/oauth2/pocketid/callback`; Dash `<dash>/auth/callback`; Testbed-only `http://127.0.0.1:8976/cb`. |
| KIRA-MCP-Client/Audience | `open-webui`, `aud ∋ kira_url_id`, nicht `auth.servas.ai` oder eine KIRA-Zitadel-Resource. |
| Brain-Keycloak-Zweig | `workspaces/ws-ki-ra/konzepte/kira-nas/README.md:86` nennt uncommitteten `idp-keycloak`-Stand mit `aud=kira-brain`; `docs/login.md` fehlt auf geprüftem main. Kein Nachweis für Zitadel. |
| Fremde vorhandene Zitadel-App | Brain `tools/clidist/README.md:22`: Projekt `servas-cli` ID `397986582964995712`, PKCE/Loopback 18007. Kein Nachweis für KIRA-Zugriff; nicht wiederverwenden. |
| **Fehlt** | Bestehende zugelassene native Zitadel-Client-ID, exakte Redirect-URI, Resource-Audience, zugelassene Scopes/Projektrollen und bestehende Human-Grants; aktuelle verifizierte Human-`(iss,sub)` und dazu bestätigte vorhandene Forgejo-ID/Login; Gateway-URL, erreichbarer nur interner Forgejo-Upstream und aktuelle notwendige Forgejo-Flags. Nicht erraten, keine Registrierung/Grants anlegen. |

## Forgejo Reverse-Proxy- und Git-Vertrag

Forgejo v15.0.10 [reverseproxy.go:51–66](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.10/services/auth/method/reverseproxy.go#L51) löst den konfigurierten Identitätsheader zu **GetUserByName** auf. Default ist `X-WEBAUTH-USER`; mit ausgeschalteter Auto-Registrierung muss der Account schon bestehen. E-Mail-Fallback ist separat schaltbar (:81–99). API/Git/LFS erzeugen bei dieser Auth keine Browser-Session (:123–124). [web.go:109–118](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.10/routers/web/web.go#L109) ordnet OAuth2/Basic **vor** ReverseProxy/Session an. Die Methode validiert den Identitätsheader nicht selbst und enthält keine Quell-IP-Prüfung.

Für einen später umzusetzenden Adapter sind `[service] ENABLE_REVERSE_PROXY_AUTHENTICATION=true` und `ENABLE_REVERSE_PROXY_AUTHENTICATION_API=true` erforderlich; `ENABLE_REVERSE_PROXY_AUTO_REGISTRATION=false` und `ENABLE_REVERSE_PROXY_EMAIL=false`. Backend nur vom Gateway erreichbar, Identitätsheader ausschließlich dort erzeugen. Proxy-Vertrauensnetze eng setzen. Das sind Anforderungen, kein auf KIRA bereits aktivierter Zustand. [Forgejo-Konfiguration](https://forgejo.org/docs/v15.0/admin/config-cheat-sheet/#service-service).

Native Forgejo-OAuth-Tokens werden mit **Forgejos eigenem Signing-Key und lokalem OAuth-Grant** geprüft, nicht mit Zitadel-JWKS ([oauth2.go:75–99](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.10/services/auth/method/oauth2.go#L75)); Git-Basic akzeptiert diese Tokens/PATs als Passwort ([basic.go:58–100](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.10/services/auth/method/basic.go#L58)). Ein Basic-Paar `oidc:<Zitadel-access_token>` ist daher **ein neuer Gateway-Eingangsvertrag**, keine native Forgejo-Funktion. Die offizielle [Git-OAuth-Doku](https://forgejo.org/docs/v15.0/user/authentication/oauth2-provider/#git-authentication) nennt den eingebauten git-credential-oauth-Client `a4792ccc-144e-407e-86c9-5e7d8d9c3269`; er gehört zu Forgejo und ersetzt keine bestehende Zitadel-App. Die Doku warnt vor fehlender OAuth-Scope-Begrenzung. Der gepinnte Code verarbeitet inzwischen zusätzliche gültige API-Scopes, fällt ohne solche aber auf **all** zurück (oauth2.go:34–70, basic.go:83–89). Daher kein Ausweg für diesen Auftrag.

## Engster späterer Gateway-Vertrag

1. Außen nur verifiziertes Human-Access-Token des festen Zitadel-Issuers, der belegten Resource-Audience, zugelassenen Client-/Projektrollen und Scopes; PKCE S256. Kein ID-Token als API-Token, keine Pocket-ID-Ausnahme. Pro Request nach fixer serverseitiger **(issuer, sub) → vorhandene Forgejo-ID → aktueller Login**-Zuordnung handeln. Keine E-Mail-, Token-Username-, Domain- oder Client-Label-Zuordnung; keine fehlenden Accounts erzeugen. Account muss aktiv und ein erlaubter Human sein; feste Authority/Upstream-URL.
2. Außen `Bearer` für API, Git-Basic `oidc:<access_token>` nur an diesem Gateway; danach Credentials verwerfen. Vor Upstream **Authorization, Cookie, Sudo** und alle kundenseitigen Identitäts-/Proxyheader (insbesondere `X-WEBAUTH*`, `X-Gitea*`, `X-Forgejo*`, `X-Remote-*`, `X-Forwarded-*`) entfernen; kontrollierte Proxyheader neu setzen und allein den aus Mapping aufgelösten Forgejo-Login injizieren.
3. Methoden-/Pfad-/Query-Allowlist: `GET /api/v1/user`; `GET /api/v1/orgs/{org}/repos` nur feste erlaubte Org, ausschließlich validierte `limit/page`; `POST /api/v1/repos/{owner}/{repo}/pulls` nur zugelassene Repos und Vorschlagsrechte. Forgejo prüft weiterhin existierende Team-/Repo-/Branchrechte. Keine zusätzliche Sichtbarkeit durch Org-Mitgliedschaft behaupten; jede Repository-Antwort muss als dieser Nutzer gefiltert bleiben.
4. Git nur feste erlaubte `/{owner}/{repo}.git/info/refs?service=git-upload-pack|git-receive-pack` (GET), `/.../git-upload-pack` und `/.../git-receive-pack` (POST); Receive nur mit explizitem Schreibrecht, Upload nur mit Leserecht, Methoden/Query passend. Keine URL-Umleitung zu anderer Authority, keine beliebigen Git-/LFS-/Webpfade.
5. Alles andere schließen, insbesondere Admin, Token-/App-/Account-/Teamverwaltung, `/api/v1/users/*/tokens`, `sudo`-Query/-Header und alternative Token-Queryparameter. Kritisch: [api.go:603–606](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.10/routers/api/v1/api.go#L603) erlaubt PAT-Erzeugung/-Löschung ausdrücklich auch mit Reverse-Proxy-Auth; bloßes Weiterleiten von `/api/` wäre zu breit.

Brain-Gegencheck: `systeme/prod2/zitadel/martin-apps/README.md:43` warnt konkret vor E-Mail-Zuordnung nach fremden bestätigten Adressen; `public/entscheidungen/cli-standard/README.md:43–44` verlangt verifiziertes (issuer,sub), Resource-Audience und minimale projektgebundene Rollen. Keine Secrets wurden in diesem Bericht aufgenommen.


## Implementierter Client / gezielte Integrationsübergabe

`oidc.go` implementiert den ausschließlich auf Zitadel gepinnten nativen Human-
Client: dynamischer Callback `http://127.0.0.1:<port>/callback`, PKCE S256, State,
Nonce, RS256/JWKS und genau die konfigurierte Resource-Projekt-Audience.
Pflichtscopes sind `openid offline_access` und
`urn:zitadel:iam:org:project:id:<Resource-Projekt-ID>:aud`.
`auth.go` kapselt private Speicherung, Refresh/Logout und isolierten Git-Helper;
`forge.go` sendet Bearer für Discovery; `proposal.go` sendet den PR-POST.
Der Client erzeugt keine IdP-Registrierung und keine Forgejo-Identität.

Der KIRA-Lead setzt den obigen kleinen Adapter in eigener Zuständigkeit um und
prüft ihn gegen das tatsächlich gepinnte Forgejo; keine KIRA-Datei wurde in
diesem Auftrag geändert. Die Transport-Fixture benutzt `git http-backend`,
keine echte Forgejo-Instanz. Ein lokales GRÜN behauptet deshalb keine sichere
KIRA-Integration, bestehende IdP-Konfiguration oder Kundenfreigabe.

**Minimale Security-Vorlage nach Source-/Fixture-Prüfung:** WRK darf zunächst
nur lesend feststellen, ob eine bereits zugelassene native Human-App samt
passender KIRA-Resource existiert, und ihre öffentlichen Client-/Projekt-IDs,
Redirect-Port-Regel, JWT/RS256-Konfiguration und zugelassenen Scopes benennen.
Bestehende andere CLI-Clients/Projektrollen sind kein KIRA-Grant. Fehlt diese
Zulassung, ist vor Registrierung oder Grant eine konkrete menschliche
Security-Entscheidung nötig: genau eine öffentliche native App, die eine
KIRA-Resource-Audience anfordert, keine Management-Audience, ausschließlich
explizit zugelassene bestehende Humans und vorhandene minimale Repo-/PR-Rechte.
Kein Provisioning, Konto, Grant, Mapping oder Rollout erfolgt aus diesem PR.

Öffentliche Protokollquellen:
[Zitadel-Audience-Scopes](https://zitadel.com/docs/apis/openidoauth/scopes),
[native Loopback-Clients RFC 8252](https://www.rfc-editor.org/rfc/rfc8252#section-7.3),
[PKCE RFC 7636](https://www.rfc-editor.org/rfc/rfc7636),
[Token-Revocation RFC 7009](https://www.rfc-editor.org/rfc/rfc7009).

**Token-Klassen trennen:** Zitadel kann ID- und Access-Tokens mit Projekt-
Audience und `azp` ausstellen. Audience allein trennt die Tokenklassen nicht.
Der Clientvertrag verlangt für Access-Tokens zusätzlich `jti`, `nbf`, `iat`,
exaktes `azp` des nativen Clients und keine ID-Nonce/`at_hash`; ID-Tokens dürfen
nicht als API-/Git-Credential angenommen werden. Das muss der KIRA-Adapter
unabhängig erzwingen. Grundlage: [Zitadel Claim-Matrix](https://zitadel.com/docs/apis/openidoauth/claims).

Der Adapter soll in einen vorhandenen KIRA-Dienst integriert werden (z. B.
`services/kira-brain` mit explizitem separatem CLI-Verifier). Kein neuer Host,
IdP, externer Dienst oder Cluster ist Teil des Vertrags. Ein bloßer zusätzlicher
Caddy-Header ohne Access-Token-Verifikation und Allowlist reicht nicht.
