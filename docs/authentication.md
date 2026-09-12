# Authentication

Every webhook request must pass two independent checks. Both are enforced by
middleware in `app/auth` before any handler runs.

## Tier 1: platform API key

```http
X-API-Key: gluzo_pk_...
```

Identifies the **platform** sending the request (for example EasyEcom). One
key exists per source platform. It answers "is this a system we accept
events from at all?".

## Tier 2: integration access token

```http
Authorization: Bearer gluzo_at_...
```

Identifies the **integration** the event belongs to (for example
`easyecom-dabur`). Tokens have an optional expiry, can be revoked at any time,
and several may exist per integration so they can be rotated without
downtime. It answers "which configured pipeline may this caller feed?".

After both checks pass, the token's integration must belong to the platform
that presented the key; otherwise the request is rejected with 403. This stops
a token issued for one platform's integration from being replayed through
another platform's key.

## Responses

| Situation | Status | `reason` |
| --- | --- | --- |
| No `X-API-Key` | 401 | `missing platform API key` |
| Unknown key or token | 401 | `invalid credentials` |
| Token past `expires_at` | 401 | `access token has expired` |
| Token revoked | 401 | `access token has been revoked` |
| No or malformed `Authorization` | 401 | `missing or malformed bearer token` |
| Platform disabled | 403 | `platform is disabled` |
| Integration disabled | 403 | `integration is disabled` |
| Token bound to another platform | 403 | `integration is not bound to the authenticated platform` |
| Credential store unreachable | 503 | `authentication temporarily unavailable` |

A 503 is deliberately distinct from 401 so that a database outage never makes
the sender conclude its credentials are wrong. Every rejection carries the
request's `correlation_id`.

## Storage

Only SHA-256 digests are stored (`platforms.api_key_hash`,
`integration_access_tokens.token_hash`). Verification hashes the presented
secret and looks the digest up; the plaintext never touches the database or
the logs. Secrets carry 256 bits of random entropy, so a fast hash is the
correct choice; slow password hashes exist to protect low-entropy passwords.

Secrets are prefixed (`gluzo_pk_`, `gluzo_at_`) so that a leaked value is
recognisable by secret scanners and its tier is obvious to operators.

## Provisioning

Credentials are created with the operator CLI, which prints each secret
exactly once:

```bash
export DATABASE_URL=postgres://...
go run ./cmd/gatewayctl migrate
go run ./cmd/gatewayctl platform create --name easyecom --type source
go run ./cmd/gatewayctl platform create --name dabur --type destination
go run ./cmd/gatewayctl integration create --name easyecom-dabur --source easyecom --destination dabur
go run ./cmd/gatewayctl token issue --integration easyecom-dabur --name "easyecom webhook" --ttl 8760h
```

Rotation and revocation:

```bash
go run ./cmd/gatewayctl platform rotate-key --name easyecom   # old key stops working immediately
go run ./cmd/gatewayctl token issue --integration easyecom-dabur --name "webhook 2027"
go run ./cmd/gatewayctl token revoke --id <old token id>
go run ./cmd/gatewayctl token list --integration easyecom-dabur
```

Issue the replacement token before revoking the old one so the sender can be
reconfigured without a gap.

## Logging policy

Authentication failures are logged at warn level with the tier, the coarse
reason, the client IP and the correlation ID. The presented credential is
never logged, at any level, and the query string is never logged either.
