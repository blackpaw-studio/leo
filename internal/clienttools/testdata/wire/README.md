# Client-tools wire fixtures

Golden JSON for the client-tools relay. Identical copies live in leoterm at
`Packages/LeoKit/Tests/LeoKitTests/Fixtures/client-tools/`; leoterm's
`scripts/check-client-tools-fixtures.sh` fails on drift. Change both together.

Events are the SSE `data:` payload (`seq`/`at` flat at the top level, as every
`internal/observe` payload). Route responses use the `{ok,data,error,code}` envelope.
`ct_`/`ctr_` ids are 26-char ULIDs; timestamps are Go RFC 3339 (trailing zeros trimmed).

| File | Route / event |
|---|---|
| hello-provider.json | `hello` on `GET /events?client_tools=1` (socket) / `GET /api/v1/events?client_tools=1` (TCP); adds `client_id`, features include `client_tools` |
| event-client-tools-changed.json | `client_tools.changed`, all streams |
| event-client-tool-request.json | `client_tool.request`, provider streams (full) |
| event-client-tool-request-redacted.json | `client_tool.request`, non-provider streams (id only) |
| event-client-tool-cancelled.json | `client_tool.cancelled`, provider streams (full) |
| event-client-tool-cancelled-redacted.json | `client_tool.cancelled`, non-provider streams (id only) |
| register-request.json | `POST /api/v1/client-tools` body |
| register-response.json | `POST /api/v1/client-tools` 200 |
| register-error-invalid-tool.json | same, 400 `invalid_tool` |
| register-error-too-many-tools.json | same, 400 `too_many_tools` |
| register-error-unknown-client.json | same, 404 `unknown_client` |
| register-error-name-conflict.json | same, 409 `name_conflict` (`data.tool`) |
| list-response.json | `GET /api/v1/client-tools`, `GET /api/agent/{name}/client-tools` |
| call-request.json | `POST /api/agent/{name}/client-tools/call` body |
| call-response-ok.json | call 200, `data.ok:true` with text + image blocks |
| call-response-error.json | call 200, `data.ok:false` with `{code,message}` |
| claim-request.json | `POST /api/v1/client-tools/requests/{id}/claim` body |
| claim-response.json | claim 200 (`{request}`) |
| claim-409-owned-by-other.json | claim 409 |
| claim-409-already-claimed.json | claim 409 |
| claim-404-unknown-request.json | claim 404 (also when settled) |
| result-request-ok.json | `POST /api/v1/client-tools/requests/{id}/result` body, ok |
| result-request-error.json | result body, error |
| result-response.json | result 200 (`{}`) |
| result-409-not-claimant.json | result 409 |
| result-409-already-settled.json | result 409 |
| result-404-unknown-request.json | result 404 |
| pending-response.json | `GET /api/v1/client-tools/requests?pending=1` (request fields flattened + `claimed_by`) |
