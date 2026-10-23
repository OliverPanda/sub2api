package dujiao

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/money"
)

const (
	headerChannelKey       = "Dujiao-Next-Channel-Key"
	headerChannelTimestamp = "Dujiao-Next-Channel-Timestamp"
	headerChannelSignature = "Dujiao-Next-Channel-Signature"
)

type Client struct {
	baseURL    string
	channelKey string
	secret     string
	httpClient *http.Client
	now        func() time.Time
}

type APIError struct {
	HTTPStatus int
	StatusCode int
	Message    string
	ErrorCode  string
	RequestID  string
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("dujiao API error: status=%d code=%d error_code=%s message=%s request_id=%s", e.HTTPStatus, e.StatusCode, e.ErrorCode, e.Message, e.RequestID)
	}
	return fmt.Sprintf("dujiao API error: status=%d code=%d message=%s", e.HTTPStatus, e.StatusCode, e.Message)
}

type Order struct {
	ID               int64       `json:"order_id"`
	OrderNo          string      `json:"order_no"`
	Status           string      `json:"status"`
	Currency         string      `json:"currency"`
	TotalAmount      money.Cents `json:"total_amount"`
	PaidAmount       money.Cents `json:"paid_amount"`
	OnlinePaidAmount money.Cents `json:"online_paid_amount"`
	ExpiresAt        *time.Time  `json:"expires_at"`
	CreatedAt        *time.Time  `json:"created_at"`
	PaidAt           *time.Time  `json:"paid_at"`
}

type Payment struct {
	ID              int64       `json:"payment_id"`
	OrderID         int64       `json:"order_id"`
	Status          string      `json:"status"`
	ChannelID       int64       `json:"channel_id"`
	ProviderType    string      `json:"provider_type"`
	ChannelType     string      `json:"channel_type"`
	InteractionMode string      `json:"interaction_mode"`
	Amount          money.Cents `json:"amount"`
	Currency        string      `json:"currency"`
	PayURL          string      `json:"pay_url"`
	QRCode          string      `json:"qr_code"`
	ExpiresAt       *time.Time  `json:"expires_at"`
	PaidAt          *time.Time  `json:"paid_at"`
	OrderNo         string      `json:"order_no"`
	TotalAmount     money.Cents `json:"total_amount"`
}

type PaymentChannel struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	ProviderType    string `json:"provider_type"`
	ChannelType     string `json:"channel_type"`
	InteractionMode string `json:"interaction_mode"`
}

type callbackBody struct {
	OrderID        int64  `json:"order_id"`
	TelegramUserID string `json:"telegram_user_id"`
}

func New(baseURL, channelKey, secret string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), channelKey: channelKey, secret: secret,
		httpClient: client, now: time.Now,
	}
}

// PaymentChannels lists active channels. When orderNo is provided, Dujiao
// filters the result to channels allowed by that order's products.
func (c *Client) PaymentChannels(ctx context.Context, orderNo, channelUserID string) ([]PaymentChannel, error) {
	var data struct {
		Items []PaymentChannel `json:"items"`
	}
	query := url.Values{}
	if value := strings.TrimSpace(orderNo); value != "" {
		query.Set("order_no", value)
	}
	if value := strings.TrimSpace(channelUserID); value != "" {
		query.Set("channel_user_id", value)
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/channel/payment-channels", query, nil, &data); err != nil {
		return nil, err
	}
	return data.Items, nil
}

func (c *Client) CreateOrder(ctx context.Context, channelUserID string, productID, skuID int64, quantity int) (*Order, error) {
	payload := map[string]any{
		"channel_user_id": channelUserID,
		"items": []map[string]any{{
			"product_id": productID,
			"sku_id":     skuID,
			"quantity":   quantity,
		}},
	}
	var order Order
	if err := c.do(ctx, http.MethodPost, "/api/v1/channel/orders", nil, payload, &order); err != nil {
		return nil, err
	}
	if order.ID <= 0 || strings.TrimSpace(order.OrderNo) == "" {
		return nil, fmt.Errorf("dujiao create order returned no order identifier")
	}
	return &order, nil
}

func (c *Client) CreatePayment(ctx context.Context, channelUserID string, orderID, channelID int64) (*Payment, error) {
	payload := map[string]any{
		"channel_user_id": channelUserID,
		"order_id":        orderID,
		"channel_id":      channelID,
		"use_balance":     false,
	}
	var payment Payment
	if err := c.do(ctx, http.MethodPost, "/api/v1/channel/payments", nil, payload, &payment); err != nil {
		return nil, err
	}
	if payment.ID <= 0 {
		return nil, fmt.Errorf("dujiao create payment returned no payment_id")
	}
	return &payment, nil
}

func (c *Client) CancelOrder(ctx context.Context, channelUserID string, orderID int64, reason string) error {
	if strings.TrimSpace(channelUserID) == "" {
		return fmt.Errorf("dujiao channel user id is empty")
	}
	if orderID <= 0 {
		return fmt.Errorf("dujiao order id is empty")
	}
	payload := map[string]any{
		"channel_user_id": channelUserID,
		"reason":          strings.TrimSpace(reason),
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/channel/orders/%d/cancel", orderID), nil, payload, nil)
}

func (c *Client) GetOrder(ctx context.Context, channelUserID, orderNo string) (*Order, error) {
	if strings.TrimSpace(channelUserID) == "" {
		return nil, fmt.Errorf("dujiao channel user id is empty")
	}
	if strings.TrimSpace(orderNo) == "" {
		return nil, fmt.Errorf("dujiao order number is empty")
	}
	var order Order
	path := "/api/v1/channel/orders/by-order-no/" + url.PathEscape(orderNo)
	query := url.Values{"channel_user_id": []string{channelUserID}}
	if err := c.do(ctx, http.MethodGet, path, query, nil, &order); err != nil {
		return nil, err
	}
	if order.ID <= 0 {
		return nil, fmt.Errorf("dujiao get order returned no order identifier")
	}
	return &order, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, payload any, out any) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode dujiao request: %w", err)
		}
	} else {
		body = []byte{}
	}
	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create dujiao request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	req.Header.Set(headerChannelKey, c.channelKey)
	req.Header.Set(headerChannelTimestamp, timestamp)
	req.Header.Set(headerChannelSignature, SignRequest(c.secret, method, path, timestamp, body))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call dujiao: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read dujiao response: %w", err)
	}
	var envelope struct {
		StatusCode int             `json:"status_code"`
		Msg        string          `json:"msg"`
		ErrorCode  string          `json:"error_code"`
		RequestID  string          `json:"request_id"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &APIError{HTTPStatus: resp.StatusCode, Message: "invalid JSON response"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.StatusCode != 0 {
		return &APIError{
			HTTPStatus: resp.StatusCode, StatusCode: envelope.StatusCode,
			Message: envelope.Msg, ErrorCode: envelope.ErrorCode, RequestID: envelope.RequestID,
		}
	}
	if out != nil {
		if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
			return fmt.Errorf("dujiao returned empty data")
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode dujiao data: %w", err)
		}
	}
	return nil
}

func SignRequest(secret, method, path, timestamp string, body []byte) string {
	bodyHash := md5.Sum(body)
	signString := strings.ToUpper(method) + "\n" + path + "\n" + timestamp + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signString))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyCallback validates the same channel signature used by outgoing calls.
func (c *Client) VerifyCallback(channelKey, method, path, timestamp, signature string, body []byte, maxSkew time.Duration) error {
	if subtle.ConstantTimeCompare([]byte(channelKey), []byte(c.channelKey)) != 1 {
		return fmt.Errorf("invalid callback channel key")
	}
	unix, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid callback timestamp")
	}
	receivedAt := time.Unix(unix, 0)
	skew := c.now().Sub(receivedAt)
	if skew < 0 {
		skew = -skew
	}
	if maxSkew <= 0 {
		maxSkew = 60 * time.Second
	}
	if skew > maxSkew {
		return fmt.Errorf("callback timestamp outside allowed window")
	}
	expected := SignRequest(c.secret, method, path, timestamp, body)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(expected)) != 1 {
		return fmt.Errorf("invalid callback signature")
	}
	return nil
}

// ParseCallback extracts the identifiers sent by Dujiao-Next. Callers must
// still query the order before crediting because callback bodies are only hints.
func ParseCallback(body []byte) (orderID int64, channelUserID string, err error) {
	var parsed callbackBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, "", fmt.Errorf("decode callback: %w", err)
	}
	if parsed.OrderID <= 0 || strings.TrimSpace(parsed.TelegramUserID) == "" {
		return 0, "", fmt.Errorf("callback is missing order_id or telegram_user_id")
	}
	return parsed.OrderID, parsed.TelegramUserID, nil
}
