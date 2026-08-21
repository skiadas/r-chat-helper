package controlplane

import (
	"database/sql"
	"log"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// App is the control plane: config, database, OIDC client, and a small
// HTTP client to the per-student key upstream (opencode.ai/zen/go/v1).
type App struct {
	cfg    *Config
	db     *sql.DB
	jwtKey []byte
	client *goClient

	oidcMu       sync.Mutex
	oidcProvider *oidc.Provider
	oauth2Config *oauth2.Config
	adminEmails  map[string]bool
}

func New(cfg *Config) (*App, error) {
	if len(cfg.JWTSecret) == 0 {
		key, err := randomSecret()
		if err != nil {
			return nil, err
		}
		cfg.JWTSecret = key
		log.Printf("auth: RC_JWT_SECRET unset, using an ephemeral signing key (sessions will not survive a restart)")
	}
	if cfg.ProviderKey == "" {
		log.Printf("upstream: RC_PROVIDER_KEY unset, model requests will fail until it is set")
	}
	db, err := OpenDB(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	admin := make(map[string]bool, len(cfg.AdminEmails))
	for _, e := range cfg.AdminEmails {
		admin[e] = true
	}
	return &App{
		cfg:         cfg,
		db:          db,
		jwtKey:      cfg.JWTSecret,
		client:      newGoClient(cfg),
		adminEmails: admin,
	}, nil
}

func (a *App) Close() error { return a.db.Close() }
