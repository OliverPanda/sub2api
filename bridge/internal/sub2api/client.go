package sub2api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/money"
)

type Client struct {
	baseURL    string
	adminKey   string
	httpClient *http.Client
}

// Decimal is a display-only JSON decimal. Sub2API stores balances with more
// than two decimal places, so they cannot be decoded into money.Cents.
type Decimal string

func (d *Decimal) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*d = ""
		return nil
	}
	value := string(raw)
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("invalid decimal string: %w", err)
		}
	}
	if _, ok := new(big.Rat).SetString(value); !ok {
		return fmt.Errorf("invalid decimal %q", value)
	}
	*d = Decimal(value)
	return nil
}

func (d Decimal) String() string { return string(d) }

type User struct {
	ID       int64   `json:"id"`
	Email    string  `json:"email"`
	Username string  `json:"username"`
	Role     string  `json:"role"`
	Balance  Decimal `json:"balance"`
}

type APIError struct {
	HTTPStatus int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sub2api API error: status=%d code=%d message=%s", e.HTTPStatus, e.Code, e.Message)
}

func New(baseURL, adminKey string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), adminKey: adminKey, httpClient: client}
}

func (c *Client) VerifyUser(ctx context.Context, token string) (*User, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("missing Sub2API token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/auth/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	var user User
	if err := c.do(req, &user); err != nil {
		return nil, err
	}
	if user.ID <= 0 {
		return nil, fmt.Errorf("Sub2API /auth/me returned no user id")
	}
	return &user, nil
}

func (c *Client) RedeemBalance(ctx context.Context, userID int64, value money.Cents, code, notes, idempotencyKey string) error {
	if userID <= 0 || !value.Positive() {
		return fmt.Errorf("invalid redeem balance request")
	}
	payload := map[string]any{
		"code":    code,
		"type":    "balance",
		"value":   value.JSONNumber(),
		"user_id": userID,
		"notes":   notes,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/admin/redeem-codes/create-and-redeem", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.adminKey)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	if err := c.do(req, nil); err != nil {
		return err
	}
	return nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call Sub2API: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read Sub2API response: %w", err)
	}
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &APIError{HTTPStatus: resp.StatusCode, Message: "invalid JSON response"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0 {
		return &APIError{HTTPStatus: resp.StatusCode, Code: envelope.Code, Message: envelope.Message}
	}
	if out != nil {
		if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
			return fmt.Errorf("Sub2API returned empty data")
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode Sub2API data: %w", err)
		}
	}
	return nil
}
