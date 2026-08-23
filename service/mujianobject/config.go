package mujianobject

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const AuthModeECS = "ecs"

type Config struct {
	Region        string
	Endpoint      string
	Bucket        string
	Prefix        string
	PublicBaseURL string
	AuthMode      string
}

type cachedStoreKey struct {
	db     *gorm.DB
	config Config
}

var configuredStores = struct {
	sync.Mutex
	entries map[cachedStoreKey]Store
}{entries: make(map[cachedStoreKey]Store)}

func LoadConfigFromEnv() (Config, error) {
	config := Config{
		Region:        strings.TrimSpace(os.Getenv("OBS_REGION")),
		Endpoint:      strings.TrimSpace(os.Getenv("OBS_ENDPOINT")),
		Bucket:        strings.TrimSpace(os.Getenv("OBS_BUCKET")),
		Prefix:        strings.Trim(strings.TrimSpace(os.Getenv("OBS_PREFIX")), "/"),
		PublicBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("OBS_PUBLIC_BASE_URL")), "/"),
		AuthMode:      strings.ToLower(strings.TrimSpace(os.Getenv("OBS_AUTH_MODE"))),
	}
	if config.AuthMode == "" {
		config.AuthMode = AuthModeECS
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if config.Region == "" || config.Endpoint == "" || config.Bucket == "" || config.Prefix == "" || config.PublicBaseURL == "" {
		return errors.New("OBS_REGION, OBS_ENDPOINT, OBS_BUCKET, OBS_PREFIX, and OBS_PUBLIC_BASE_URL are required")
	}
	if err := validateHTTPSURL(config.Endpoint, "OBS_ENDPOINT"); err != nil {
		return err
	}
	if err := validateHTTPSURL(config.PublicBaseURL, "OBS_PUBLIC_BASE_URL"); err != nil {
		return err
	}
	if err := validateObjectPath(config.Prefix); err != nil {
		return fmt.Errorf("invalid OBS_PREFIX: %w", err)
	}
	if config.AuthMode != AuthModeECS {
		return fmt.Errorf("unsupported OBS_AUTH_MODE %q", config.AuthMode)
	}
	return nil
}

func validateHTTPSURL(rawURL, name string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an HTTPS origin without credentials, path, query, or fragment", name)
	}
	return nil
}

// FromEnvironment enables new OBS writes only when MUJIAN_IMAGE_STORAGE=obs.
// An OBS configuration error is returned instead of silently falling back.
func FromEnvironment() (Store, bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MUJIAN_IMAGE_STORAGE"))) {
	case "", "db", "database":
		return nil, false, nil
	case "obs":
		store, err := OpenFromEnvironment()
		return store, true, err
	default:
		return nil, false, fmt.Errorf("unsupported MUJIAN_IMAGE_STORAGE value")
	}
}

// OpenFromEnvironment constructs OBS access independently from the write mode.
// Database-write rollback mode uses it to read rows already migrated to OBS.
func OpenFromEnvironment() (Store, error) {
	config, err := LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	if model.DB == nil {
		return nil, errors.New("database is not initialized")
	}
	key := cachedStoreKey{db: model.DB, config: config}
	configuredStores.Lock()
	defer configuredStores.Unlock()
	if store := configuredStores.entries[key]; store != nil {
		return store, nil
	}
	backend, err := NewOBSBackend(config)
	if err != nil {
		return nil, err
	}
	store, err := NewStore(model.DB, backend)
	if err != nil {
		return nil, err
	}
	configuredStores.entries[key] = store
	return store, nil
}

// OpenConfiguredFromEnvironment lets the process run the lifecycle worker in
// database-write rollback mode. A completely absent OBS configuration is
// disabled; a partial configuration is an error rather than a silent skip.
func OpenConfiguredFromEnvironment() (Store, bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MUJIAN_IMAGE_STORAGE"))) {
	case "obs":
		store, err := OpenFromEnvironment()
		return store, true, err
	case "", "db", "database":
		// OBS may still be configured for reads and lifecycle cleanup while new
		// images are written to the database during a compatible rollback.
	default:
		return nil, false, fmt.Errorf("unsupported MUJIAN_IMAGE_STORAGE value")
	}
	for _, name := range []string{
		"OBS_REGION", "OBS_ENDPOINT", "OBS_BUCKET", "OBS_PREFIX",
		"OBS_PUBLIC_BASE_URL", "OBS_AUTH_MODE",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			store, err := OpenFromEnvironment()
			return store, true, err
		}
	}
	return nil, false, nil
}

func resetConfiguredStoresForTest() {
	configuredStores.Lock()
	defer configuredStores.Unlock()
	for _, store := range configuredStores.entries {
		if closer, ok := store.(interface{ close() }); ok {
			closer.close()
		}
	}
	configuredStores.entries = make(map[cachedStoreKey]Store)
}
