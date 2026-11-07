# pdp-emulator

A stand-in for the [Permit.io](https://www.permit.io/) Policy Decision Point (PDP) whose decisions your tests control.

The real PDP (`permitio/pdp-v2`) needs a Permit API key and fetches its policies from Permit's cloud, so it cannot
answer offline, and a test cannot make it say "deny" on cue. `pdp-emulator` serves the same check API, answers from
rules your tests set over HTTP, and records every check it receives so tests can assert on them.

It is a single static Go binary (no dependencies) in an ~8 MB `scratch` image for linux/amd64 and linux/arm64.

## Run

```bash
docker run --rm -p 7000:7000 ghcr.io/kharabasz/pdp-emulator:edge
```

Or from source: `go run .`

To swap it in where something deploys the real PDP image, push it under that image's name (for example into an ECR
pull-through-cache repository `dockerhub/permitio/pdp-v2:<tag>` on an AWS emulator). It listens on the PDP's port
(7000), answers the PDP's health checks, and ignores `PDP_API_KEY` unless told to check it.

## PDP API

| Route | |
| --- | --- |
| `POST /allowed` | One check. Response `{"allow": bool, "query": <request>, "debug": {"pdp_emulator": "rule:<id>" \| "default"}}` |
| `POST /allowed/bulk` | A list of checks (or `{"checks": [...]}`). Response `{"allow": [<result>, ...]}`. A bad item rejects the whole request. |
| `GET /healthy`, `/ready`, `/health`, `/` | `200 {"status":"ok"}` |

A check is `{"user": {"key": ...} | "<key>", "action": "...", "resource": {"type", "key", "tenant"} | "<type>:<key>", "tenant"?: "..."}`.
User, action and resource type are required (422 otherwise).

Every route, including the control API, matches on its path **suffix**, so the emulator works behind a load balancer
that forwards a prefix unchanged: `/pdp/allowed` and `/pdp/_emulator/answers` work like `/allowed` and `/_emulator/answers`.

## Control API (`/_emulator`)

Decisions come from **rules**, tried in the order they were added; the first that matches and is not used up answers.
With no match, the **default** answers (allow, unless changed).

A rule is `{"match": {...}, "allow": bool, "times": n}`. `match` takes any of `user`, `action`, `resource_type`,
`resource_key`, `tenant`; a missing field matches anything. `times` is how many checks the rule answers before it is
used up; leave it out for a standing rule.

| Route | |
| --- | --- |
| `POST /_emulator/answers` `{"answers": [false, true], "match": {...}}` | Queue one-shot answers: the next matching checks get them in order. `match` is optional. |
| `POST /_emulator/rules` `<rule>` or `[<rule>, ...]` | Add rules. Returns them with their ids. |
| `GET /_emulator/rules` | The rules that can still answer, with `remaining` uses. |
| `DELETE /_emulator/rules` | Drop all rules. |
| `PUT /_emulator/default` `{"allow": false}` | Set the default decision. |
| `GET /_emulator/checks?user=&action=&resource_type=&resource_key=&tenant=` | Checks received (filters optional): parsed fields, raw `body`, `path`, `authorization` header, `allow`, `decided_by`. |
| `DELETE /_emulator/checks` | Forget the checks received. |
| `POST /_emulator/reset` | Drop rules and checks; restore the start-up default and rules. |
| `GET /_emulator/state` | `{"default_allow", "rules", "checks"}` counts. |

Tests that share one emulator should scope their answers with `match` (say, a user key per test), so they don't take
each other's answers.

### Example

```bash
# The next check by alice is denied; everything else is allowed.
curl -XPOST localhost:7000/_emulator/answers -d '{"match":{"user":"alice"},"answers":[false]}'

curl -XPOST localhost:7000/allowed -d '{"user":{"key":"alice"},"action":"read","resource":{"type":"document","tenant":"default"}}'
# {"allow":false,"debug":{"pdp_emulator":"rule:1"},"query":{...}}

curl 'localhost:7000/_emulator/checks?user=alice'
```

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `PDP_EMULATOR_PORT` (or `PORT`) | `7000` | Listen port |
| `PDP_EMULATOR_DEFAULT_ALLOW` | `true` | Decision when no rule matches |
| `PDP_EMULATOR_RULES_FILE` | | JSON list of rules loaded at start and on reset |
| `PDP_EMULATOR_REQUIRE_API_KEY` | `false` | Require `Authorization: Bearer $PDP_API_KEY` on checks (401 otherwise) |
| `PDP_API_KEY` | | The key checked when the above is set |

`pdp-emulator healthcheck` exits 0 when the server on `PDP_EMULATOR_PORT` is healthy (used by the image's `HEALTHCHECK`).

## What it is not

It does not evaluate Permit policies (RBAC, ReBAC, ABAC), serve the Management API, or emulate `/user-permissions`,
`/allowed/url` or OPA's data API. Responses carry `allow`, `query` and `debug` only. If you need real policy
evaluation offline, look at the PDP's own
[offline mode](https://docs.permit.io/how-to/deploy/offline-mode) (needs a backup taken from Permit's cloud once).

## License

MIT
