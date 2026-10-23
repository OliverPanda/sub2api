package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSKUs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skus.json")
	raw := `{"credit-10":{"display_name":"10 元","product_id":1,"sku_id":2,"price":"10","credit":"10","currency":"CNY","credit_unit":"余额","min_quantity":1,"max_quantity":5}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	skus, err := loadSKUs(path)
	if err != nil {
		t.Fatal(err)
	}
	got := skus["credit-10"]
	if got.PriceCents.Int64() != 1000 || got.CreditCents.Int64() != 1000 || got.MaxQuantity != 5 {
		t.Fatalf("unexpected SKU: %+v", got)
	}
}

func TestLoadSKUsRejectsZeroPrice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skus.json")
	raw := `{"broken":{"product_id":1,"sku_id":2,"price":"0","credit":"10","currency":"CNY"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSKUs(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadSKUsAllowsEmptyCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skus.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	skus, err := loadSKUs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(skus) != 0 {
		t.Fatalf("expected empty SKU catalog, got %d entries", len(skus))
	}
}

func setLoadEnvironment(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skus.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRIDGE_PUBLIC_URL", "https://api.xddapi.org.uk")
	t.Setenv("BRIDGE_DATABASE_URL", "postgres://bridge:test@bridge-postgres:5432/bridge?sslmode=disable")
	t.Setenv("SUB2API_BASE_URL", "http://sub2api:8080")
	t.Setenv("BRIDGE_ADMIN_TOKEN", "test-admin-token")
	t.Setenv("BRIDGE_SKU_CONFIG", path)
	t.Setenv("DUJIAO_CHANNEL_KEY", "")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "")
	t.Setenv("SUB2API_ADMIN_API_KEY", "")
	return path
}

func TestLoadAllowsTwoPhaseStartup(t *testing.T) {
	setLoadEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DujiaoConfigured() {
		t.Fatal("Dujiao should be unconfigured")
	}
	if cfg.Sub2APIAdminConfigured() {
		t.Fatal("Sub2API admin API should be unconfigured")
	}
	if cfg.DujiaoBaseURL != "http://dujiao-next:8080" {
		t.Fatalf("unexpected default Dujiao URL: %s", cfg.DujiaoBaseURL)
	}
}

func TestLoadRejectsPartialDujiaoCredentials(t *testing.T) {
	setLoadEnvironment(t)
	t.Setenv("DUJIAO_CHANNEL_KEY", "key-only")
	if _, err := Load(); err == nil {
		t.Fatal("expected partial Dujiao credentials to be rejected")
	}
}

func TestLoadReportsConfiguredCapabilities(t *testing.T) {
	setLoadEnvironment(t)
	t.Setenv("DUJIAO_CHANNEL_KEY", "key")
	t.Setenv("DUJIAO_CHANNEL_SECRET", "secret")
	t.Setenv("SUB2API_ADMIN_API_KEY", "admin-key")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DujiaoConfigured() || !cfg.Sub2APIAdminConfigured() {
		t.Fatalf("capabilities not detected: %+v", cfg)
	}
}
