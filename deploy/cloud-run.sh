#!/usr/bin/env bash
#
# Deploy Loamss to Google Cloud Run.
#
# Single-shot script for "I have a GCP project, I want a working
# loamss URL". Not idempotent in the sophisticated sense — re-running
# it deploys a new revision over the existing service. Tear down with
# the matching teardown.sh.
#
# Usage:
#
#   PROJECT_ID=marketplace-487603 \
#   REGION=us-central1 \
#   SERVICE=loamss \
#   ./deploy/cloud-run.sh
#
# Required:
#   PROJECT_ID  GCP project id
#
# Optional (all have sane defaults):
#   REGION         deploy region                    [us-central1]
#   SERVICE        Cloud Run service name           [loamss]
#   IMAGE          override image tag               [computed]
#   CLOUD_SQL      Cloud SQL instance connection    [auto-create db-f1-micro]
#   DB_NAME        Postgres database name           [loamss]
#   DB_USER        Postgres user                    [postgres]
#   DB_PASSWORD    Postgres password                [auto-generated]
#   SETUP_TOKEN    operator-supplied setup token    [runtime auto-generates]
#   ALLOW_UNAUTH   allow public access              [true]
#
# The script's intent is the laptop equivalent for cloud: one command,
# you get a URL back, you open the URL in a browser. Production deploys
# should pin every variable explicitly in their own scripts / CI.

set -euo pipefail

# --- preflight --------------------------------------------------------------

require() {
  local var=$1
  if [ -z "${!var:-}" ]; then
    echo "ERROR: $var is required" >&2
    exit 2
  fi
}

require PROJECT_ID

REGION=${REGION:-us-central1}
SERVICE=${SERVICE:-loamss}
DB_NAME=${DB_NAME:-loamss}
DB_USER=${DB_USER:-postgres}
ALLOW_UNAUTH=${ALLOW_UNAUTH:-true}

# Resolve the image tag. Defaults to gcr.io/<project>/loamss:<git-sha>.
GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE=${IMAGE:-gcr.io/$PROJECT_ID/loamss:$GIT_SHA}

# Generate an auto-token only if the operator didn't supply one. The
# value is printed back at the end so the operator can use it.
if [ -z "${SETUP_TOKEN:-}" ]; then
  if command -v openssl >/dev/null 2>&1; then
    SETUP_TOKEN=$(openssl rand -hex 32)
  else
    SETUP_TOKEN=$(head -c 32 /dev/urandom | xxd -p -c 64)
  fi
  GENERATED_TOKEN=1
fi

echo ""
echo "==> Deploying Loamss to Cloud Run"
echo "    Project:   $PROJECT_ID"
echo "    Region:    $REGION"
echo "    Service:   $SERVICE"
echo "    Image:     $IMAGE"
echo ""

# --- 1. Cloud SQL ----------------------------------------------------------
#
# We need a Postgres for runtime.db + audit.db (the cold-start
# durability path — see commit 7d162b6). When CLOUD_SQL is unset, the
# script tries to create a db-f1-micro instance. Existing operators
# should set CLOUD_SQL to skip this.

if [ -z "${CLOUD_SQL:-}" ]; then
  INSTANCE_NAME="${SERVICE}-db"
  echo "==> [1/5] Provisioning Cloud SQL: $INSTANCE_NAME"

  if gcloud sql instances describe "$INSTANCE_NAME" --project="$PROJECT_ID" >/dev/null 2>&1; then
    echo "    instance already exists; reusing"
    if [ -z "${DB_PASSWORD:-}" ]; then
      echo "    WARNING: DB_PASSWORD not supplied and the instance pre-exists."
      echo "    The deploy will set a fresh password on the postgres user so the DSN works."
      DB_PASSWORD=$(openssl rand -hex 16)
      GENERATED_DB_PASSWORD=1
      gcloud sql users set-password "$DB_USER" \
        --instance="$INSTANCE_NAME" \
        --project="$PROJECT_ID" \
        --password="$DB_PASSWORD" \
        --quiet
    fi
    # Ensure the database exists; create is idempotent-ish via || true.
    gcloud sql databases create "$DB_NAME" \
      --instance="$INSTANCE_NAME" \
      --project="$PROJECT_ID" \
      --quiet 2>/dev/null || true
  else
    if [ -z "${DB_PASSWORD:-}" ]; then
      DB_PASSWORD=$(openssl rand -hex 16)
      GENERATED_DB_PASSWORD=1
    fi
    gcloud sql instances create "$INSTANCE_NAME" \
      --project="$PROJECT_ID" \
      --database-version=POSTGRES_16 \
      --edition=ENTERPRISE \
      --tier=db-f1-micro \
      --region="$REGION" \
      --root-password="$DB_PASSWORD" \
      --storage-size=10GB \
      --backup \
      --quiet
    gcloud sql databases create "$DB_NAME" \
      --instance="$INSTANCE_NAME" \
      --project="$PROJECT_ID" \
      --quiet || true
  fi

  CLOUD_SQL="$PROJECT_ID:$REGION:$INSTANCE_NAME"
else
  echo "==> [1/5] Using existing Cloud SQL: $CLOUD_SQL"
  if [ -z "${DB_PASSWORD:-}" ]; then
    echo "ERROR: when CLOUD_SQL is set explicitly, DB_PASSWORD must also be supplied" >&2
    exit 2
  fi
fi

# --- 2. Build + push image -------------------------------------------------

echo "==> [2/5] Building image: $IMAGE"
gcloud builds submit . \
  --project="$PROJECT_ID" \
  --tag="$IMAGE" \
  --machine-type=e2-highcpu-8 \
  --timeout=20m \
  --quiet

# --- 3. Deploy Cloud Run ---------------------------------------------------

echo "==> [3/5] Deploying Cloud Run service: $SERVICE"

# Build the DSN. Cloud Run reaches Cloud SQL via the unix socket the
# Cloud SQL Auth Proxy mounts when --add-cloudsql-instances is set,
# at /cloudsql/<connection-name>.
PG_DSN="postgres://${DB_USER}:${DB_PASSWORD:-}@/${DB_NAME}?host=/cloudsql/${CLOUD_SQL}&sslmode=disable"

DEPLOY_FLAGS=(
  --image="$IMAGE"
  --project="$PROJECT_ID"
  --region="$REGION"
  --platform=managed
  --port=8080
  --memory=512Mi
  --cpu=1
  --min-instances=0
  --max-instances=3
  --set-cloudsql-instances="$CLOUD_SQL"
  # Single --set-env-vars flag with a `|` delimiter (the DSN contains
  # the standard `,` so we can't use it). Multiple --set-env-vars
  # flags would clobber each other — only the last wins.
  "--set-env-vars=^|^LOAMSS_PROFILE=cloud|LOAMSS_DATABASE_URL=${PG_DSN}|LOAMSS_AUDIT_DATABASE_URL=${PG_DSN}|LOAMSS_SETUP_TOKEN=${SETUP_TOKEN}"
)

if [ "$ALLOW_UNAUTH" = "true" ]; then
  DEPLOY_FLAGS+=(--allow-unauthenticated)
fi

gcloud run deploy "$SERVICE" "${DEPLOY_FLAGS[@]}" --quiet

# --- 4. Resolve URL --------------------------------------------------------

echo "==> [4/5] Resolving service URL"
URL=$(gcloud run services describe "$SERVICE" \
  --project="$PROJECT_ID" \
  --region="$REGION" \
  --format="value(status.url)")

# --- 5. Print summary ------------------------------------------------------

echo ""
echo "==> [5/5] Deploy complete"
echo ""
echo "    Service URL:  $URL"
echo "    Setup token:  $SETUP_TOKEN"
echo ""
echo "    Open the wizard:"
echo "      $URL/?setup=$SETUP_TOKEN"
echo ""

if [ -n "${GENERATED_DB_PASSWORD:-}" ]; then
  echo "    Cloud SQL password (save this; we won't show it again):"
  echo "      $DB_PASSWORD"
  echo ""
fi

if [ -n "${GENERATED_TOKEN:-}" ]; then
  echo "    The setup token was auto-generated. Save it now — after the"
  echo "    wizard burns it, the runtime will not accept it again."
  echo ""
fi

echo "    /healthz check:"
echo "      curl -fsS $URL/healthz"
echo ""
echo "    Tear down:"
echo "      PROJECT_ID=$PROJECT_ID SERVICE=$SERVICE REGION=$REGION ./deploy/cloud-run-teardown.sh"
echo ""
