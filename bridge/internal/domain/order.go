package domain

import (
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/money"
)

type OrderStatus string

const (
	StatusCreated        OrderStatus = "created"
	StatusPendingPayment OrderStatus = "pending_payment"
	StatusPaid           OrderStatus = "paid"
	StatusCrediting      OrderStatus = "crediting"
	StatusCredited       OrderStatus = "credited"
	StatusCreditFailed   OrderStatus = "credit_failed"
	StatusCanceled       OrderStatus = "canceled"
	StatusRefunded       OrderStatus = "refunded"
)

func (s OrderStatus) Trackable() bool {
	switch s {
	case StatusCreated, StatusPendingPayment, StatusPaid, StatusCrediting, StatusCreditFailed:
		return true
	default:
		return false
	}
}

func (s OrderStatus) Terminal() bool {
	return s == StatusCredited || s == StatusCanceled || s == StatusRefunded
}

type Order struct {
	ID                int64       `json:"id"`
	BridgeOrderNo     string      `json:"order_no"`
	UserID            int64       `json:"user_id"`
	UserEmail         string      `json:"user_email,omitempty"`
	SKUKey            string      `json:"sku_key"`
	ProductID         int64       `json:"-"`
	SKUID             int64       `json:"-"`
	Quantity          int         `json:"quantity"`
	Currency          string      `json:"currency"`
	OrderAmountCents  money.Cents `json:"order_amount_cents"`
	CreditCents       money.Cents `json:"credit_cents"`
	Status            OrderStatus `json:"status"`
	DujiaoOrderID     int64       `json:"-"`
	DujiaoOrderNo     string      `json:"dujiao_order_no,omitempty"`
	DujiaoPaymentID   int64       `json:"-"`
	SelectedChannelID int64       `json:"-"`
	PaymentURL        string      `json:"pay_url,omitempty"`
	QRCode            string      `json:"qr_code,omitempty"`
	IdempotencyKey    string      `json:"-"`
	RetryCount        int         `json:"retry_count"`
	LastError         string      `json:"last_error,omitempty"`
	PaidAt            *time.Time  `json:"paid_at,omitempty"`
	CreditedAt        *time.Time  `json:"credited_at,omitempty"`
	CreditedCode      string      `json:"-"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
	ExpiresAt         *time.Time  `json:"expires_at,omitempty"`
}

type SKU struct {
	Key         string      `json:"key"`
	DisplayName string      `json:"display_name"`
	ProductID   int64       `json:"product_id"`
	SKUID       int64       `json:"sku_id"`
	PriceCents  money.Cents `json:"price_cents"`
	CreditCents money.Cents `json:"credit_cents"`
	Currency    string      `json:"currency"`
	CreditUnit  string      `json:"credit_unit"`
	MinQuantity int         `json:"min_quantity"`
	MaxQuantity int         `json:"max_quantity"`
	Recommended bool        `json:"recommended"`
	Description string      `json:"description,omitempty"`
}

type CallbackEvent struct {
	ID            int64
	EventType     string
	DujiaoOrderID int64
	BodySHA256    string
	Payload       []byte
	Processed     bool
	Attempts      int
	LastError     string
	CreatedAt     time.Time
	ProcessedAt   *time.Time
}
