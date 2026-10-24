# Keys and signatures

Every request of a station is signed with the station's own key, and every global model is signed with the
server's key. Both use Ed25519. TLS protects the connection on top; the signatures make sure that a stolen request
cannot be replayed, that a leak of the server's database reveals nothing that would let anyone act as a station,
and that a station only ever uses models from the server it accepted.

## Encodings

| Item | Encoding |
|---|---|
| public key | the raw 32 bytes, base64url without padding |
| signature | the raw 64 bytes, base64url without padding |
| fingerprint | `SHA256:` followed by the SHA-256 of the raw public key in standard base64 without padding (43 characters). Similar to SSH fingerprints, but over the raw key, so `ssh-keygen` shows a different value |

Test vector, with the public key of test 1 in RFC 8032
(`d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a` in hex):

| | |
|---|---|
| public key, base64url | `11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo` |
| fingerprint | `SHA256:If4x36FUomFia/hUBG/SJxt77UtqvkWqWId+9H+XIbk` |

## Station keys

The station creates one Ed25519 key pair per server when it joins and keeps the private key to itself. Only the
public key goes to the server. A station can delete its key and join again; that makes it a new station.

## Signed requests

Every request to `/v1/...` except `GET /v1/server` and `GET /healthz` carries four headers:

| Header | Content |
|---|---|
| `Vinyl-Mind-Key` | fingerprint of the station key |
| `Vinyl-Mind-Timestamp` | Unix time in whole seconds |
| `Vinyl-Mind-Nonce` | at least 16 random bytes, base64url without padding (22 to 64 characters) |
| `Vinyl-Mind-Signature` | Ed25519 signature of the message below |

The signed message is these eight lines, UTF-8, joined with a single line feed, without a line feed at the end:

```
vinyl-mind-request-v1
<fingerprint of the server key, from GET /v1/server>
<method in capitals, e.g. PUT>
<path and query exactly as sent, e.g. /v1/models?feature_version=1>
<SHA-256 of the body in lowercase hex; the hash of the empty string for a request without body>
<the timestamp header>
<the nonce header>
<the key header>
```

The server rejects a request whose timestamp is more than 300 seconds off its own clock, a nonce it has seen from
the same key within that window, a signature that does not verify, and a key that is unknown, still waiting for
approval (except for `GET /v1/me`) or revoked. The server fingerprint in the message binds a request to one server:
a request recorded on one server is worthless on another.

The path is signed as the station sends it, so a reverse proxy in front of the server must pass the path on
unchanged. Serving Vinyl-Mind at the root of its own host name avoids any rewriting.

**Joining** (`POST /v1/join`) uses the same headers. The key header must be the fingerprint of the `public_key` in
the body, and the signature is verified with that key, which proves that the station holds the private key.

## Signed models

A global model is delivered as a JWS in compact serialisation (RFC 7515) with the algorithm `EdDSA` and the curve
Ed25519 (RFC 8037):

```
base64url(protected header) . base64url(payload) . base64url(signature)
```

- The protected header is `{"alg":"EdDSA","typ":"vinyl-mind-model+json","kid":"<server fingerprint>"}`.
- The payload is the global model as UTF-8 JSON, valid against `v1/global-model.schema.json`.
- The signature covers `base64url(header) + "." + base64url(payload)`, as in every JWS.

A station verifies before it evaluates anything: `alg` is `EdDSA`, `kid` is the fingerprint it accepted when
joining, the signature verifies with the accepted server key, and the payload's `server` field equals `kid`. Only
then does it parse and validate the payload. A model that fails any of these checks is discarded.

The station learns the server key from `GET /v1/server` when it joins and shows the fingerprint so that the user
can compare it with the one the operator publishes. Changing to another server, or a server that changed its key,
means accepting the new key explicitly.
