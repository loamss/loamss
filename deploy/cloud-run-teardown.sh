#!/usr/bin/env bash
#
# Tear down a Loamss Cloud Run deployment provisioned by cloud-run.sh.
#
# Usage:
#
#   PROJECT_ID=marketplace-487603 \
#   REGION=us-central1 \
#   SERVICE=loamss \
#   ./deploy/cloud-run-teardown.sh
#
# By default this deletes only the Cloud Run service. To also drop the
# Cloud SQL instance and the container images (i.e., make the deploy
# fully reversible at the cost of losing data), pass:
#
#   DELETE_DB=1 DELETE_IMAGES=1 ./deploy/cloud-run-teardown.sh
#
# Cloud SQL deletes are irreversible — the Postgres data goes with
# them. The script prompts before doing it.

set -euo pipefail

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

echo ""
echo "==> Tearing down Loamss Cloud Run"
echo "    Project:   $PROJECT_ID"
echo "    Region:    $REGION"
echo "    Service:   $SERVICE"
echo ""

# --- Cloud Run service -----------------------------------------------------

if gcloud run services describe "$SERVICE" \
    --project="$PROJECT_ID" \
    --region="$REGION" >/dev/null 2>&1; then
  echo "==> Deleting Cloud Run service: $SERVICE"
  gcloud run services delete "$SERVICE" \
    --project="$PROJECT_ID" \
    --region="$REGION" \
    --quiet
else
  echo "==> Cloud Run service $SERVICE not found; skipping"
fi

# --- Cloud SQL instance (only with DELETE_DB=1) ----------------------------

if [ "${DELETE_DB:-0}" = "1" ]; then
  INSTANCE_NAME="${SERVICE}-db"
  if gcloud sql instances describe "$INSTANCE_NAME" \
      --project="$PROJECT_ID" >/dev/null 2>&1; then
    echo ""
    echo "    WARNING: about to DELETE Cloud SQL instance $INSTANCE_NAME"
    echo "    This is irreversible. All Postgres data will be lost."
    read -r -p "    Type 'delete' to confirm: " CONFIRM
    if [ "$CONFIRM" = "delete" ]; then
      gcloud sql instances delete "$INSTANCE_NAME" \
        --project="$PROJECT_ID" \
        --quiet
    else
      echo "    aborted"
    fi
  fi
fi

# --- Container images (only with DELETE_IMAGES=1) --------------------------

if [ "${DELETE_IMAGES:-0}" = "1" ]; then
  echo "==> Deleting container images under gcr.io/$PROJECT_ID/loamss"
  # gcloud's `--format="value(digest)"` emits the digest *without* the
  # "sha256:" prefix, but `images delete` requires that prefix. Add it
  # back at the call site.
  for digest in $(gcloud container images list-tags \
      "gcr.io/$PROJECT_ID/loamss" \
      --project="$PROJECT_ID" \
      --format="value(digest)" 2>/dev/null); do
    case "$digest" in
      sha256:*) ;;
      *) digest="sha256:$digest" ;;
    esac
    gcloud container images delete "gcr.io/$PROJECT_ID/loamss@$digest" \
      --project="$PROJECT_ID" \
      --force-delete-tags \
      --quiet || true
  done
fi

echo ""
echo "==> Teardown complete"
echo ""
