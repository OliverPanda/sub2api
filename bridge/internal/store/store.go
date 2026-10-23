package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/domain"
	"github.com/OliverPanda/sub2api/bridge/internal/money"

	"github.com/lib/pq"
)

var ErrDuplicate = errors.New("duplicate order request")
var ErrNotFound = errors.New("order not found")
var ErrConflict = errors.New("order conflict")

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open bridge database: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping bridge database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS bridge_orders (
    id BIGSERIAL PRIMARY KEY,
    bridge_order_no TEXT NOT NULL UNIQUE,
    user_id BIGINT NOT NULL CHECK (user_id > 0),
    user_email TEXT NOT NULL DEFAULT '',
    sku_key TEXT NOT NULL,
    product_id BIGINT NOT NULL CHECK (product_id > 0),
    sku_id BIGINT NOT NULL CHECK (sku_id > 0),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    currency TEXT NOT NULL,
    order_amount_cents BIGINT NOT NULL CHECK (order_amount_cents > 0),
    credit_cents BIGINT NOT NULL CHECK (credit_cents > 0),
    status TEXT NOT NULL CHECK (status IN ('created','pending_payment','paid','crediting','credited','credit_failed','canceled','refunded')),
    dujiao_order_id BIGINT,
    dujiao_order_no TEXT,
    dujiao_payment_id BIGINT,
    selected_channel_id BIGINT,
    payment_url TEXT NOT NULL DEFAULT '',
    qr_code TEXT NOT NULL DEFAULT '',
    client_idempotency_key TEXT NOT NULL UNIQUE,
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    paid_at TIMESTAMPTZ,
    credited_at TIMESTAMPTZ,
    credited_code TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS bridge_orders_dujiao_order_id_uidx
    ON bridge_orders (dujiao_order_id) WHERE dujiao_order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS bridge_orders_user_created_idx
    ON bridge_orders (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS bridge_orders_due_idx
    ON bridge_orders (next_attempt_at) WHERE status IN ('created','pending_payment','paid','crediting','credit_failed');

CREATE TABLE IF NOT EXISTS bridge_callback_events (
    id BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL,
    dujiao_order_id BIGINT NOT NULL,
    body_sha256 TEXT NOT NULL,
    payload JSONB NOT NULL,
    processed BOOLEAN NOT NULL DEFAULT FALSE,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    UNIQUE (event_type, dujiao_order_id, body_sha256)
);
CREATE INDEX IF NOT EXISTS bridge_callback_events_pending_idx
    ON bridge_callback_events (created_at) WHERE processed = FALSE;
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate bridge database: %w", err)
	}
	return nil
}

const orderColumns = `
    id, bridge_order_no, user_id, user_email, sku_key, product_id, sku_id, quantity,
    currency, order_amount_cents, credit_cents, status, COALESCE(dujiao_order_id, 0),
    COALESCE(dujiao_order_no, ''), COALESCE(dujiao_payment_id, 0), COALESCE(selected_channel_id, 0),
    payment_url, qr_code, client_idempotency_key, retry_count, last_error, paid_at, credited_at,
    credited_code, created_at, updated_at, expires_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(row scanner) (*domain.Order, error) {
	var order domain.Order
	var paidAt, creditedAt, expiresAt sql.NullTime
	var status string
	var orderAmount, creditAmount int64
	if err := row.Scan(
		&order.ID, &order.BridgeOrderNo, &order.UserID, &order.UserEmail, &order.SKUKey,
		&order.ProductID, &order.SKUID, &order.Quantity, &order.Currency, &orderAmount, &creditAmount,
		&status, &order.DujiaoOrderID, &order.DujiaoOrderNo, &order.DujiaoPaymentID,
		&order.SelectedChannelID, &order.PaymentURL, &order.QRCode, &order.IdempotencyKey,
		&order.RetryCount, &order.LastError, &paidAt, &creditedAt, &order.CreditedCode,
		&order.CreatedAt, &order.UpdatedAt, &expiresAt,
	); err != nil {
		return nil, err
	}
	order.Status = domain.OrderStatus(status)
	order.OrderAmountCents = money.FromInt64(orderAmount)
	order.CreditCents = money.FromInt64(creditAmount)
	if paidAt.Valid {
		order.PaidAt = &paidAt.Time
	}
	if creditedAt.Valid {
		order.CreditedAt = &creditedAt.Time
	}
	if expiresAt.Valid {
		order.ExpiresAt = &expiresAt.Time
	}
	return &order, nil
}

func (s *Store) CreateOrder(ctx context.Context, order *domain.Order) error {
	err := s.db.QueryRowContext(ctx, `
INSERT INTO bridge_orders (
    bridge_order_no, user_id, user_email, sku_key, product_id, sku_id, quantity, currency,
    order_amount_cents, credit_cents, status, selected_channel_id, client_idempotency_key, expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
RETURNING id, created_at, updated_at`,
		order.BridgeOrderNo, order.UserID, order.UserEmail, order.SKUKey, order.ProductID, order.SKUID,
		order.Quantity, order.Currency, order.OrderAmountCents.Int64(), order.CreditCents.Int64(),
		string(order.Status), nullableInt64(order.SelectedChannelID), order.IdempotencyKey, order.ExpiresAt,
	).Scan(&order.ID, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return ErrDuplicate
		}
		return fmt.Errorf("create bridge order: %w", err)
	}
	return nil
}

func nullableInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (s *Store) GetOrderByID(ctx context.Context, id int64) (*domain.Order, error) {
	return s.getOrder(ctx, `SELECT `+orderColumns+` FROM bridge_orders WHERE id = $1`, id)
}

func (s *Store) GetOrderForUser(ctx context.Context, orderNo string, userID int64) (*domain.Order, error) {
	return s.getOrder(ctx, `SELECT `+orderColumns+` FROM bridge_orders WHERE bridge_order_no = $1 AND user_id = $2`, orderNo, userID)
}

func (s *Store) GetOrderByDujiaoID(ctx context.Context, dujiaoOrderID int64) (*domain.Order, error) {
	return s.getOrder(ctx, `SELECT `+orderColumns+` FROM bridge_orders WHERE dujiao_order_id = $1`, dujiaoOrderID)
}

func (s *Store) GetOrderByClientKey(ctx context.Context, key string) (*domain.Order, error) {
	return s.getOrder(ctx, `SELECT `+orderColumns+` FROM bridge_orders WHERE client_idempotency_key = $1`, key)
}

func (s *Store) getOrder(ctx context.Context, query string, args ...any) (*domain.Order, error) {
	order, err := scanOrder(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bridge order: %w", err)
	}
	return order, nil
}

func (s *Store) ListUserOrders(ctx context.Context, userID int64, limit int) ([]domain.Order, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+orderColumns+` FROM bridge_orders WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list user orders: %w", err)
	}
	defer rows.Close()
	return collectOrders(rows)
}

func (s *Store) ListOrders(ctx context.Context, status string, limit int) ([]domain.Order, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + orderColumns + ` FROM bridge_orders`
	args := []any{}
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list bridge orders: %w", err)
	}
	defer rows.Close()
	return collectOrders(rows)
}

func collectOrders(rows *sql.Rows) ([]domain.Order, error) {
	result := make([]domain.Order, 0)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *order)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) ListDueOrders(ctx context.Context, limit int) ([]domain.Order, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+orderColumns+`
FROM bridge_orders
WHERE next_attempt_at <= NOW()
  AND (
    status IN ('created','pending_payment','paid')
    OR (status = 'credit_failed' AND retry_count < 5)
    OR (status = 'crediting' AND updated_at < NOW() - INTERVAL '2 minutes')
  )
ORDER BY next_attempt_at ASC, created_at ASC
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list due bridge orders: %w", err)
	}
	defer rows.Close()
	return collectOrders(rows)
}

func (s *Store) SetDujiaoOrder(ctx context.Context, orderNo string, orderID int64, dujiaoOrderNo, currency string, amount money.Cents, expiresAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET dujiao_order_id = $2, dujiao_order_no = $3, currency = $4, order_amount_cents = $5,
    expires_at = COALESCE($6, expires_at), status = 'pending_payment', updated_at = NOW(), next_attempt_at = NOW()
WHERE bridge_order_no = $1`, orderNo, orderID, dujiaoOrderNo, currency, amount.Int64(), expiresAt)
	if err != nil {
		return fmt.Errorf("save dujiao order: %w", err)
	}
	return nil
}

func (s *Store) SetPayment(ctx context.Context, orderNo string, paymentID, channelID int64, payURL, qrCode string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET dujiao_payment_id = $2, selected_channel_id = $3, payment_url = $4, qr_code = $5,
    updated_at = NOW(), next_attempt_at = NOW()
WHERE bridge_order_no = $1`, orderNo, paymentID, channelID, payURL, qrCode)
	if err != nil {
		return fmt.Errorf("save dujiao payment: %w", err)
	}
	return nil
}

func (s *Store) MarkPendingPayment(ctx context.Context, orderNo string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET status = 'pending_payment', updated_at = NOW(), next_attempt_at = NOW() + INTERVAL '20 seconds'
WHERE bridge_order_no = $1 AND status IN ('created','pending_payment')`, orderNo)
	if err != nil {
		return fmt.Errorf("mark pending payment: %w", err)
	}
	return nil
}

func (s *Store) MarkPaid(ctx context.Context, orderNo string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET status = 'paid', paid_at = COALESCE(paid_at, NOW()), updated_at = NOW(), next_attempt_at = NOW()
WHERE bridge_order_no = $1 AND status IN ('created','pending_payment','paid','credit_failed')`, orderNo)
	if err != nil {
		return fmt.Errorf("mark paid: %w", err)
	}
	return nil
}

func (s *Store) MarkCanceled(ctx context.Context, orderNo string, refunded bool) error {
	status := string(domain.StatusCanceled)
	if refunded {
		status = string(domain.StatusRefunded)
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders SET status = $2, updated_at = NOW(), next_attempt_at = NOW() + INTERVAL '1 day'
WHERE bridge_order_no = $1 AND status NOT IN ('credited','refunded')`, orderNo, status)
	if err != nil {
		return fmt.Errorf("mark order terminal: %w", err)
	}
	return nil
}

func (s *Store) ClaimCredit(ctx context.Context, orderNo string, retryBackoff time.Duration, force bool) (*domain.Order, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := scanOrder(tx.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM bridge_orders WHERE bridge_order_no = $1 FOR UPDATE`, orderNo))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if order.Status == domain.StatusCredited || order.Status.Terminal() {
		return order, false, nil
	}
	if !force {
		if order.Status == domain.StatusCreditFailed && order.RetryCount >= 5 {
			return order, false, nil
		}
		if order.Status == domain.StatusCrediting && time.Since(order.UpdatedAt) < 2*time.Minute {
			return order, false, nil
		}
	}
	if order.Status != domain.StatusPaid && order.Status != domain.StatusCreditFailed && order.Status != domain.StatusCrediting {
		return order, false, nil
	}
	backoffSeconds := int(retryBackoff.Seconds())
	if backoffSeconds < 10 {
		backoffSeconds = 10
	}
	_, err = tx.ExecContext(ctx, `
UPDATE bridge_orders
SET status = 'crediting', retry_count = retry_count + 1, last_error = '', updated_at = NOW(),
    next_attempt_at = NOW() + ($2 * INTERVAL '1 second')
WHERE bridge_order_no = $1`, orderNo, backoffSeconds)
	if err != nil {
		return nil, false, fmt.Errorf("claim credit: %w", err)
	}
	order.Status = domain.StatusCrediting
	order.RetryCount++
	order.LastError = ""
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return order, true, nil
}

func (s *Store) MarkCredited(ctx context.Context, orderNo, code string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET status = 'credited', credited_at = COALESCE(credited_at, NOW()), credited_code = $2,
    last_error = '', updated_at = NOW(), next_attempt_at = NOW() + INTERVAL '1 day'
WHERE bridge_order_no = $1 AND status <> 'refunded'`, orderNo, code)
	if err != nil {
		return fmt.Errorf("mark credited: %w", err)
	}
	return nil
}

func (s *Store) MarkCreditFailed(ctx context.Context, orderNo, message string, backoff time.Duration) error {
	message = truncate(message, 1000)
	seconds := int(backoff.Seconds())
	if seconds < 10 {
		seconds = 10
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET status = 'credit_failed', last_error = $2, updated_at = NOW(),
    next_attempt_at = NOW() + ($3 * INTERVAL '1 second')
WHERE bridge_order_no = $1 AND status <> 'credited'`, orderNo, message, seconds)
	if err != nil {
		return fmt.Errorf("mark credit failed: %w", err)
	}
	return nil
}

// DeferCredit parks an already paid order until the Sub2API admin credential
// is configured. It intentionally does not consume the limited credit retry
// budget or change the order to credit_failed.
func (s *Store) DeferCredit(ctx context.Context, orderNo, message string, backoff time.Duration) error {
	message = truncate(message, 1000)
	seconds := int(backoff.Seconds())
	if seconds < 10 {
		seconds = 10
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET status = 'paid', last_error = $2, updated_at = NOW(),
    next_attempt_at = NOW() + ($3 * INTERVAL '1 second')
WHERE bridge_order_no = $1 AND status IN ('paid','credit_failed','crediting')`, orderNo, message, seconds)
	if err != nil {
		return fmt.Errorf("defer bridge credit: %w", err)
	}
	return nil
}

func (s *Store) SetLastError(ctx context.Context, orderNo, message string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders SET last_error = $2, updated_at = NOW(), next_attempt_at = NOW() + INTERVAL '20 seconds'
WHERE bridge_order_no = $1 AND status NOT IN ('credited','canceled','refunded')`, orderNo, truncate(message, 1000))
	if err != nil {
		return fmt.Errorf("save bridge order error: %w", err)
	}
	return nil
}

func (s *Store) ResetForRetry(ctx context.Context, orderNo string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE bridge_orders
SET status = 'paid', retry_count = 0, last_error = '', updated_at = NOW(), next_attempt_at = NOW()
WHERE bridge_order_no = $1 AND status IN ('credit_failed','paid')`, orderNo)
	if err != nil {
		return fmt.Errorf("reset order for retry: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) PersistCallback(ctx context.Context, eventType string, orderID int64, bodySHA256 string, payload []byte) (bool, error) {
	if !json.Valid(payload) {
		return false, fmt.Errorf("callback payload is not valid JSON")
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO bridge_callback_events (event_type, dujiao_order_id, body_sha256, payload)
VALUES ($1,$2,$3,$4::jsonb)
ON CONFLICT (event_type, dujiao_order_id, body_sha256) DO NOTHING`, eventType, orderID, bodySHA256, string(payload))
	if err != nil {
		return false, fmt.Errorf("persist callback: %w", err)
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func (s *Store) ListPendingCallbacks(ctx context.Context, limit int) ([]domain.CallbackEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, event_type, dujiao_order_id, body_sha256, payload, processed, attempts, last_error, created_at, processed_at
FROM bridge_callback_events
WHERE processed = FALSE AND attempts < 20
ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.CallbackEvent, 0)
	for rows.Next() {
		var event domain.CallbackEvent
		var processedAt sql.NullTime
		if err := rows.Scan(&event.ID, &event.EventType, &event.DujiaoOrderID, &event.BodySHA256, &event.Payload,
			&event.Processed, &event.Attempts, &event.LastError, &event.CreatedAt, &processedAt); err != nil {
			return nil, err
		}
		if processedAt.Valid {
			event.ProcessedAt = &processedAt.Time
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) MarkCallbackProcessed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bridge_callback_events SET processed = TRUE, processed_at = NOW(), attempts = attempts + 1 WHERE id = $1`, id)
	return err
}

func (s *Store) MarkCallbackFailed(ctx context.Context, id int64, message string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE bridge_callback_events SET attempts = attempts + 1, last_error = $2
WHERE id = $1`, id, truncate(message, 1000))
	return err
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}
