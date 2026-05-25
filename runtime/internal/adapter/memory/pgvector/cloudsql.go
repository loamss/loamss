package pgvector

import (
	"context"
	"fmt"
	"net"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// `cloudsql.go` wires optional Cloud SQL (Google Cloud) IAM auth
// into the pgvector adapter without changing its public surface
// or adapter id.
//
// Two add-ins:
//
//   - `cloud_sql_instance` config: when set, the connection
//     pool's dial path goes through Google's cloud-sql-go-connector
//     instead of plain TCP. The connector authenticates to Cloud
//     SQL via the runtime's Application Default Credentials
//     (Workload Identity on GKE / Cloud Run / GCE, gcloud user
//     creds elsewhere). No DB password required; the connector
//     opens a TLS tunnel and presents IAM credentials.
//
//   - `cloud_sql_iam_auth: true` config: when also set, the
//     connector uses automatic IAM database authentication —
//     the runtime's service-account principal becomes the
//     Postgres user. No password column lookup; Postgres
//     accepts the IAM-signed credential. Cleaner than mapping
//     a static password to a service account.
//
// The DSN still controls the rest of the connection
// (`pool_max_conns`, search_path, etc.); the connector just
// owns the dial layer.

// applyCloudSQLDialerIfConfigured inspects the config map for
// cloud_sql_instance. If present, it replaces the pgxpool
// Config's DialFunc with the Cloud SQL Connector's dialer +
// returns a cleanup function the adapter calls on Close. If
// absent, it returns nils and the adapter falls through to the
// standard TCP dial.
func applyCloudSQLDialerIfConfigured(
	ctx context.Context, cfg *pgxpool.Config, config map[string]any,
) (cleanup func() error, err error) {
	instance := optionalString(config, "cloud_sql_instance", "")
	if instance == "" {
		return nil, nil
	}

	// Build the connector. Each adapter instance gets its own;
	// the connector caches TLS material per (instance, principal),
	// so reuse within the adapter's lifetime is automatic.
	opts := []cloudsqlconn.Option{}
	if optionalBool(config, "cloud_sql_iam_auth", false) {
		opts = append(opts, cloudsqlconn.WithIAMAuthN())
	}
	d, err := cloudsqlconn.NewDialer(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("pgvector: cloud_sql_instance dialer: %w", err)
	}

	// Wire the dialer in. pgxpool's ConnConfig has a DialFunc
	// hook that runs instead of the default net.Dial. We ignore
	// the host/port the DSN specifies — the connector resolves
	// them from the GCP instance metadata.
	cfg.ConnConfig.DialFunc = func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		return d.Dial(ctx, instance)
	}

	return d.Close, nil
}

// optionalBool reads a bool field with a default. Tolerant of
// YAML decoding shapes (int 0/1, string "true"/"false") since
// users edit YAML by hand.
func optionalBool(config map[string]any, key string, fallback bool) bool {
	v, ok := config[key]
	if !ok {
		return fallback
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1" || t == "yes"
	case int:
		return t != 0
	case float64:
		return t != 0
	}
	return fallback
}
