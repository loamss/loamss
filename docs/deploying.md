# Deploying Loamss

Patterns for running Loamss anywhere other than a laptop. The
canonical "what does Loamss assume?" path is still `loamss start`
on your own machine — that's covered in
[`getting-started.md`](getting-started.md). This doc is the cloud
equivalent.

> **Status (v0.2.0-alpha):** Cloud Run is the path with a working
> deploy script (`deploy/cloud-run.sh`) and the smoke test that
> closed the cold-start bug (commit 7d162b6). Fly.io, GKE, and
> bare-metal Kubernetes work in principle — same binary, same env
> vars — but the recipes below are sketched, not run.

---

## What changes vs. a laptop install

Three properties of laptop installs that don't hold in the cloud:

| Property | Laptop | Cloud |
| --- | --- | --- |
| Network perimeter | `127.0.0.1` binding | public URL behind load balancer |
| Filesystem | persistent | ephemeral (Cloud Run, Lambda, most K8s pods) |
| Process lifetime | as long as the user keeps `loamss start` running | scales to zero between requests |

The runtime adapts to all three when `LOAMSS_PROFILE=cloud` (set
explicitly, or auto-detected from `K_SERVICE` / `FLY_APP_NAME` /
`KUBERNETES_SERVICE_HOST` / `RENDER` / `RAILWAY_ENVIRONMENT`):

- **Listener** binds `0.0.0.0:$PORT` instead of `127.0.0.1:7777`
- **Setup-token gate** activates on `/console/*` and `/pair`
- **Consumption marker** writes to `runtime_state` in the runtime DB
  instead of a file (so it survives cold starts)

What you, the operator, supply:

- A **Postgres** instance for the runtime DB (and ideally for the
  audit DB — same instance, different DB or schema)
- A **container runtime** that injects `$PORT` (Cloud Run, Fly,
  Render, App Runner, K8s all do this)
- Outbound HTTPS so the runtime can reach model providers and OAuth
  endpoints

What you don't need to supply yet (alpha.2 caveats):

- **Persistent disk** — only needed if you use `storage:fs-encrypted`.
  Use `storage:s3` or `storage:gcs` for proper object-storage
  semantics. The setup-token consumption marker no longer needs the
  filesystem.
- **Capsule code persistence** — installing capsules from local
  paths still writes to the ephemeral filesystem. Cloud-installed
  capsules disappear on cold start. Tracked for alpha.3.
- **A config-file durability story** — `loamss init` writes
  `config.yaml` to `<data_dir>`, which is ephemeral on Cloud Run.
  Workaround today: bake the config into the container image, or
  set every relevant `LOAMSS_*` env var so defaults are sufficient.
  Tracked for alpha.3.

---

## Cloud Run (Google Cloud)

The deploy script does the whole thing in one command:

```bash
PROJECT_ID=your-gcp-project \
  ./deploy/cloud-run.sh
```

The script:

1. Provisions a `db-f1-micro` Cloud SQL Postgres instance named
   `<service>-db` (or reuses one if it exists) with the
   `loamss` database created inside
2. Submits the container build via Cloud Build using the repo's
   `Dockerfile`
3. Deploys the image to Cloud Run with:
   - `LOAMSS_PROFILE=cloud`
   - `LOAMSS_DATABASE_URL` + `LOAMSS_AUDIT_DATABASE_URL` pointing at
     the Cloud SQL instance via the unix-socket connector
   - `LOAMSS_SETUP_TOKEN` (auto-generated unless you pass one in)
4. Prints back the service URL and the wizard link
   `<url>/?setup=<token>`

Output sample:

```
==> Deploying Loamss to Cloud Run
    Project:   your-gcp-project
    Region:    us-central1
    Service:   loamss
    Image:     gcr.io/your-gcp-project/loamss:a1b2c3d

==> [1/5] Provisioning Cloud SQL: loamss-db
==> [2/5] Building image: gcr.io/your-gcp-project/loamss:a1b2c3d
==> [3/5] Deploying Cloud Run service: loamss
==> [4/5] Resolving service URL
==> [5/5] Deploy complete

    Service URL:  https://loamss-xyz-uc.a.run.app
    Setup token:  6f4c8e5a91...

    Open the wizard:
      https://loamss-xyz-uc.a.run.app/?setup=6f4c8e5a91...
```

Tear down (Cloud Run service only, leaves Cloud SQL intact for the
next deploy):

```bash
PROJECT_ID=your-gcp-project ./deploy/cloud-run-teardown.sh
```

To drop Cloud SQL too: `DELETE_DB=1 ./deploy/cloud-run-teardown.sh`
(the script prompts for confirmation because this is irreversible).

### Variables

All optional except `PROJECT_ID`:

| Variable | Default | Notes |
| --- | --- | --- |
| `PROJECT_ID` | *required* | GCP project id |
| `REGION` | `us-central1` | Cloud Run + Cloud SQL region |
| `SERVICE` | `loamss` | Cloud Run service name |
| `IMAGE` | `gcr.io/$PROJECT_ID/loamss:$GIT_SHA` | override to skip build |
| `CLOUD_SQL` | auto-created | `project:region:instance` form |
| `DB_NAME` / `DB_USER` / `DB_PASSWORD` | `loamss` / `postgres` / auto-gen | |
| `SETUP_TOKEN` | auto-generated | set explicitly to skip log scraping |
| `ALLOW_UNAUTH` | `true` | set to `false` for IAM-gated access |

### Manual `gcloud` (if you don't want the script)

```bash
# 1. build & push
gcloud builds submit . --tag=gcr.io/$PROJECT_ID/loamss:latest

# 2. create Postgres (one-time)
gcloud sql instances create loamss-db \
  --database-version=POSTGRES_16 \
  --tier=db-f1-micro --region=us-central1 \
  --root-password=$(openssl rand -hex 16)
gcloud sql databases create loamss --instance=loamss-db

# 3. deploy
PG_DSN="postgres://postgres:<password>@/loamss?host=/cloudsql/$PROJECT_ID:us-central1:loamss-db&sslmode=disable"
gcloud run deploy loamss \
  --image=gcr.io/$PROJECT_ID/loamss:latest \
  --region=us-central1 \
  --set-cloudsql-instances=$PROJECT_ID:us-central1:loamss-db \
  --set-env-vars="LOAMSS_PROFILE=cloud,LOAMSS_DATABASE_URL=$PG_DSN,LOAMSS_AUDIT_DATABASE_URL=$PG_DSN" \
  --allow-unauthenticated
```

The setup token is auto-generated on first start; grab it from
the logs:

```bash
gcloud run services logs read loamss --region=us-central1 \
  --limit=50 | grep "Setup token:"
```

---

## Fly.io

`fly launch` from the repo root picks up the `Dockerfile`. After
the first run, edit `fly.toml` to add the env vars:

```toml
[env]
  LOAMSS_PROFILE = "cloud"
  LOAMSS_DATABASE_URL = "postgres://..."  # use a Fly Postgres
  LOAMSS_AUDIT_DATABASE_URL = "postgres://..."

[http_service]
  internal_port = 8080
  force_https = true
  auto_stop_machines = true
  min_machines_running = 0

[[services]]
  protocol = "tcp"
  internal_port = 8080
  [[services.http_checks]]
    interval = "30s"
    method = "get"
    path = "/healthz"
```

Set the setup token as a Fly secret so it survives restarts:

```bash
fly secrets set LOAMSS_SETUP_TOKEN=$(openssl rand -hex 32)
```

Then `fly deploy`.

---

## Kubernetes / GKE

The same image works as a stateless Deployment in front of a
Postgres-backed StatefulSet (or a managed Cloud SQL / RDS / Aiven
instance). Minimal manifest:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: loamss
spec:
  replicas: 1
  selector: { matchLabels: { app: loamss } }
  template:
    metadata: { labels: { app: loamss } }
    spec:
      containers:
        - name: loamss
          image: ghcr.io/your-org/loamss:v0.2.0
          ports: [{ containerPort: 8080 }]
          env:
            - { name: LOAMSS_PROFILE, value: cloud }
            - { name: PORT, value: "8080" }
            - name: LOAMSS_DATABASE_URL
              valueFrom: { secretKeyRef: { name: loamss-secrets, key: pg_dsn } }
            - name: LOAMSS_AUDIT_DATABASE_URL
              valueFrom: { secretKeyRef: { name: loamss-secrets, key: pg_dsn } }
            - name: LOAMSS_SETUP_TOKEN
              valueFrom: { secretKeyRef: { name: loamss-secrets, key: setup_token } }
          livenessProbe:
            httpGet: { path: /healthz, port: 8080 }
            periodSeconds: 30
          readinessProbe:
            httpGet: { path: /healthz, port: 8080 }
            periodSeconds: 10
```

A single replica is the recommended starting shape — multi-replica
correctness is exercised by the multi-process Postgres concurrency
test (commit ad1b565), but the runtime hasn't been load-tested
against a real multi-replica deployment.

---

## What the gate looks like, end to end

A cloud deploy with the gate active. The numbers below correspond
to the lines in the deploy script's output.

1. **Deploy.** Operator runs `deploy/cloud-run.sh`. Image builds,
   Cloud SQL provisions, Cloud Run launches.
2. **First boot.** Runtime starts in cloud profile, prints the
   setup token banner to stderr (captured by Cloud Logging). Writes
   one `setup_token.issued` audit entry.
3. **Operator opens** `<url>/?setup=<token>` in a browser. Console
   reads the token, stashes in `localStorage`, strips it from the
   URL.
4. **Wizard submit.** Console sends `Authorization: Bearer <token>`
   with `/console/init`. Runtime writes config, burns token, persists
   consumption to the `runtime_state` table.
5. **Cold start.** Cloud Run scales to zero, fires up a new
   instance later. The new instance reads `runtime_state` from
   Cloud SQL, sees the prior consumption, refuses the setup-token
   path. No fresh token printed.
6. **Operator pairs a real client.** Today this means SSH into the
   running instance or use `gcloud run jobs execute` to invoke
   `loamss client pair --name "..."`. v0.2 alpha.3 will add a
   dashboard paste field for the bearer credential. (See `cli.md`
   for the pair command.)

---

## Costs (approximate)

For a single-user deploy on us-central1, idle most of the time:

| Service | Tier | Monthly |
| --- | --- | --- |
| Cloud Run | min-instances=0, 1 vCPU, 512MB | ~$0 — $5 |
| Cloud SQL Postgres | db-f1-micro, 10 GB SSD | ~$10 — $15 |
| Cloud Build | <120 build-minutes/month free tier | $0 |
| Container Registry | <0.5 GB tier | $0 |
| Secret Manager | 1 secret | ~$0.06 |
| **Total minimum** | | **~$15 — $20** |

That's the awkward shape — Loamss as a cloud substrate doesn't
have laptop economics. The substrate thesis answer is "users who
self-host on hardware they own" or, eventually, a hosted "Loamss
Cloud" that amortizes infra across many users. Today's deploy
recipe is for operators who want to run Loamss in their own GCP
project, not for end-users.

---

## Known platform quirks

### Cloud Run intercepts `/healthz` at the GFE

Verified end-to-end during a live deploy (commit 08fccd0 against
`marketplace-487603`): Google's frontend serves a 404 page for the
paths `/healthz`, `/health`, `/readyz`, and `/robots.txt` on the
default `*.run.app` URLs. These requests **never reach our
container**.

The Loamss runtime's `/healthz` handler IS registered and works
fine locally (`docker run` smoke test) and through any non-`run.app`
ingress (custom domain on Cloud Run, a load balancer in front, GKE
ClusterIP). It's only the public default URL that strips them.

Implication: don't point an external uptime monitor at
`https://<svc>.run.app/healthz` expecting our handler. Use one of:

- `/version` — also always public, returns the runtime's version
  string. Confirmed reachable through the GFE.
- A custom domain mapped to the Cloud Run service. Custom domains
  bypass the path-stripping behavior.
- An internal monitor inside the VPC.

Cloud Run's own startup health-check is a TCP probe on the
container port, not an HTTP probe — so this quirk doesn't affect
the platform's own liveness/readiness signal.

---

## Troubleshooting

### Wizard 401s even with the token

The console strips the `?setup=` param after capturing it. If you
reload the page after that, the token is in `localStorage` and
should still attach to fetches automatically. If a 401 persists:

- Open DevTools → Application → Local Storage. Verify
  `loamss.setup_token` is set to the value from the deploy script.
- The token might have been consumed already (someone else
  completed init, or you submitted twice). Check
  `loamss audit log --type setup_token.consumed` over the Postgres
  backend.

### "previously consumed" log on every boot

Means the gate is reading the `runtime_state` row from Postgres
and refusing to re-issue. Correct behavior. To re-open the wizard
on purpose (e.g., you broke the config and want to start over):

```sql
DELETE FROM runtime_state WHERE key = 'setup_token_consumed';
```

Then redeploy (or `fly machine restart` / `gcloud run services
update-traffic ... --to-latest`). The next start prints a fresh
token.

### Image build is slow

The console build (Next.js static export) is the long pole. After
the first build, the bun layer caches; only console source edits
re-trigger it. If you're iterating on Go-only changes, build
locally with `make build-go` and skip the container until the
final deploy.

---

## What's planned for alpha.3

The remaining cloud gaps:

- `LOAMSS_CONFIG_URL` so `config.yaml` can live in GCS / S3
  instead of on the ephemeral container filesystem
- Installed-capsule persistence (registry-backed pull at startup,
  or GCS-backed install path)
- `loamss setup-token reset` CLI to clear the consumption row
- Actually run this against a live Cloud Run deploy and capture
  any latency / IAM / connection-pooling issues that the unit
  tests didn't surface
