package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/domain"
	"github.com/OliverPanda/sub2api/bridge/internal/money"
)

type Config struct {
	HTTPAddr            string
	PublicURL           string
	SupportURL          string
	SupportLabel        string
	DatabaseURL         string
	Sub2APIBaseURL      string
	Sub2APIAdminKey     string
	DujiaoBaseURL       string
	DujiaoChannelKey    string
	DujiaoChannelSecret string
	AdminToken          string
	SKUConfigPath       string
	PollInterval        time.Duration
	RequestTimeout      time.Duration
	CallbackMaxSkew     time.Duration
	CreditRetryBackoff  time.Duration
	SKUs                map[string]domain.SKU
}

type skuFileEntry struct {
	DisplayName string `json:"display_name"`
	ProductID   int64  `json:"product_id"`
	SKUID       int64  `json:"sku_id"`
	Price       string `json:"price"`
	Credit      string `json:"credit"`
	Currency    string `json:"currency"`
	CreditUnit  string `json:"credit_unit"`
	MinQuantity int    `json:"min_quantity"`
	MaxQuantity int    `json:"max_quantity"`
	Recommended bool   `json:"recommended"`
	Description string `json:"description"`
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:            envOr("BRIDGE_HTTP_ADDR", ":8080"),
		PublicURL:           strings.TrimRight(strings.TrimSpace(os.Getenv("BRIDGE_PUBLIC_URL")), "/"),
		SupportURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("BRIDGE_SUPPORT_URL")), "/"),
		SupportLabel:        envOr("BRIDGE_SUPPORT_LABEL", "联系客服"),
		DatabaseURL:         strings.TrimSpace(os.Getenv("BRIDGE_DATABASE_URL")),
		Sub2APIBaseURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("SUB2API_BASE_URL")), "/"),
		Sub2APIAdminKey:     strings.TrimSpace(os.Getenv("SUB2API_ADMIN_API_KEY")),
		DujiaoBaseURL:       strings.TrimRight(envOr("DUJIAO_BASE_URL", "http://dujiao-next:8080"), "/"),
		DujiaoChannelKey:    strings.TrimSpace(os.Getenv("DUJIAO_CHANNEL_KEY")),
		DujiaoChannelSecret: strings.TrimSpace(os.Getenv("DUJIAO_CHANNEL_SECRET")),
		AdminToken:          strings.TrimSpace(os.Getenv("BRIDGE_ADMIN_TOKEN")),
		SKUConfigPath:       envOr("BRIDGE_SKU_CONFIG", "/app/config/skus.json"),
		PollInterval:        envDuration("BRIDGE_POLL_INTERVAL", 20*time.Second),
		RequestTimeout:      envDuration("BRIDGE_REQUEST_TIMEOUT", 20*time.Second),
		CallbackMaxSkew:     envDuration("BRIDGE_CALLBACK_MAX_SKEW", 60*time.Second),
		CreditRetryBackoff:  envDuration("BRIDGE_CREDIT_RETRY_BACKOFF", 2*time.Minute),
	}
	required := map[string]string{
		"BRIDGE_PUBLIC_URL":   cfg.PublicURL,
		"BRIDGE_DATABASE_URL": cfg.DatabaseURL,
		"SUB2API_BASE_URL":    cfg.Sub2APIBaseURL,
		"BRIDGE_ADMIN_TOKEN":  cfg.AdminToken,
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	if (cfg.DujiaoChannelKey == "") != (cfg.DujiaoChannelSecret == "") {
		return nil, errors.New("DUJIAO_CHANNEL_KEY and DUJIAO_CHANNEL_SECRET must be configured together")
	}
	for key, raw := range map[string]string{
		"BRIDGE_PUBLIC_URL": cfg.PublicURL,
		"SUB2API_BASE_URL":  cfg.Sub2APIBaseURL,
		"DUJIAO_BASE_URL":   cfg.DujiaoBaseURL,
	} {
		if err := validateHTTPURL(key, raw); err != nil {
			return nil, err
		}
	}
	if cfg.SupportURL != "" {
		if err := validateHTTPURL("BRIDGE_SUPPORT_URL", cfg.SupportURL); err != nil {
			return nil, err
		}
	}
	if cfg.PollInterval < 5*time.Second {
		return nil, errors.New("BRIDGE_POLL_INTERVAL must be at least 5s")
	}
	if cfg.RequestTimeout < time.Second {
		return nil, errors.New("BRIDGE_REQUEST_TIMEOUT must be at least 1s")
	}
	if cfg.CallbackMaxSkew < 30*time.Second {
		return nil, errors.New("BRIDGE_CALLBACK_MAX_SKEW must be at least 30s")
	}
	skus, err := loadSKUs(cfg.SKUConfigPath)
	if err != nil {
		return nil, err
	}
	cfg.SKUs = skus
	return cfg, nil
}

// DujiaoConfigured reports whether order creation and payment calls can be
// made. The bridge is deliberately allowed to boot before the Dujiao channel
// client has been created in the admin panel.
func (c *Config) DujiaoConfigured() bool {
	return c != nil && c.DujiaoBaseURL != "" && c.DujiaoChannelKey != "" && c.DujiaoChannelSecret != ""
}

// Sub2APIAdminConfigured reports whether paid orders can be credited. Missing
// admin credentials must defer fulfilment, never discard an already paid order.
func (c *Config) Sub2APIAdminConfigured() bool {
	return c != nil && c.Sub2APIBaseURL != "" && c.Sub2APIAdminKey != ""
}

func validateHTTPURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute http(s) URL", name)
	}
	if u.User != nil {
		return fmt.Errorf("%s must not contain credentials", name)
	}
	return nil
}

func loadSKUs(path string) (map[string]domain.SKU, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read BRIDGE_SKU_CONFIG: %w", err)
	}
	var entries map[string]skuFileEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse BRIDGE_SKU_CONFIG: %w", err)
	}
	result := make(map[string]domain.SKU, len(entries))
	for key, entry := range entries {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("SKU key must not be empty")
		}
		if entry.ProductID <= 0 || entry.SKUID <= 0 {
			return nil, fmt.Errorf("SKU %q: product_id and sku_id must be positive", key)
		}
		price, err := money.Parse(entry.Price)
		if err != nil || !price.Positive() {
			return nil, fmt.Errorf("SKU %q: invalid price", key)
		}
		credit, err := money.Parse(entry.Credit)
		if err != nil || !credit.Positive() {
			return nil, fmt.Errorf("SKU %q: invalid credit", key)
		}
		currency := strings.ToUpper(strings.TrimSpace(entry.Currency))
		if currency == "" {
			return nil, fmt.Errorf("SKU %q: currency is required", key)
		}
		if entry.MinQuantity < 1 {
			entry.MinQuantity = 1
		}
		if entry.MaxQuantity < entry.MinQuantity {
			return nil, fmt.Errorf("SKU %q: max_quantity must be >= min_quantity", key)
		}
		displayName := strings.TrimSpace(entry.DisplayName)
		if displayName == "" {
			displayName = key
		}
		creditUnit := strings.TrimSpace(entry.CreditUnit)
		if creditUnit == "" {
			creditUnit = "余额"
		}
		result[key] = domain.SKU{
			Key: key, DisplayName: displayName, ProductID: entry.ProductID, SKUID: entry.SKUID,
			PriceCents: price, CreditCents: credit, Currency: currency, CreditUnit: creditUnit,
			MinQuantity: entry.MinQuantity, MaxQuantity: entry.MaxQuantity,
			Recommended: entry.Recommended, Description: strings.TrimSpace(entry.Description),
		}
	}
	return result, nil
}

func SortedSKUs(skus map[string]domain.SKU) []domain.SKU {
	result := make([]domain.SKU, 0, len(skus))
	for _, sku := range skus {
		result = append(result, sku)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PriceCents == result[j].PriceCents {
			return result[i].Key < result[j].Key
		}
		return result[i].PriceCents < result[j].PriceCents
	})
	return result
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return value
}
