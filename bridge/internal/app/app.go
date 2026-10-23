package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/config"
	"github.com/OliverPanda/sub2api/bridge/internal/domain"
	"github.com/OliverPanda/sub2api/bridge/internal/dujiao"
	"github.com/OliverPanda/sub2api/bridge/internal/money"
	"github.com/OliverPanda/sub2api/bridge/internal/store"
	"github.com/OliverPanda/sub2api/bridge/internal/sub2api"
	bridgeStatic "github.com/OliverPanda/sub2api/bridge/static"
)

type App struct {
	cfg     *config.Config
	store   *store.Store
	dujiao  *dujiao.Client
	sub2api *sub2api.Client
	logger  *log.Logger
}

func New(cfg *config.Config, st *store.Store, dj *dujiao.Client, s2a *sub2api.Client, logger *log.Logger) *App {
	if logger == nil {
		logger = log.Default()
	}
	return &App{cfg: cfg, store: st, dujiao: dj, sub2api: s2a, logger: logger}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /", a.handleIndex)
	mux.Handle("GET /app.css", http.FileServer(http.FS(bridgeStatic.Files)))
	mux.Handle("GET /app.js", http.FileServer(http.FS(bridgeStatic.Files)))
	mux.HandleFunc("GET /api/catalog", a.handleCatalog)
	mux.HandleFunc("GET /api/payment-channels", a.handlePaymentChannels)
	mux.HandleFunc("GET /api/orders", a.handleListOrders)
	mux.HandleFunc("POST /api/orders", a.handleCreateOrder)
	mux.HandleFunc("GET /api/orders/{orderNo}", a.handleGetOrder)
	mux.HandleFunc("POST /api/orders/{orderNo}/pay", a.handlePayOrder)
	mux.HandleFunc("POST /api/orders/{orderNo}/refresh", a.handleRefreshOrder)
	mux.HandleFunc("POST /internal/order-paid", a.handleCallback)
	mux.HandleFunc("POST /internal/order-fulfilled", a.handleCallback)
	mux.HandleFunc("GET /admin/orders", a.handleAdminOrders)
	mux.HandleFunc("POST /admin/orders/{orderNo}/retry", a.handleAdminRetry)
	return a.securityHeaders(a.limitBody(mux))
}

func (a *App) RunWorker(ctx context.Context) {
	a.runWorkerOnce(ctx)
	jitter := time.Duration(randomInt(5000)) * time.Millisecond
	timer := time.NewTimer(jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runWorkerOnce(ctx)
		}
	}
}

func (a *App) runWorkerOnce(ctx context.Context) {
	if !a.cfg.DujiaoConfigured() {
		return
	}
	if callbacks, err := a.store.ListPendingCallbacks(ctx, 20); err != nil {
		a.logger.Printf("worker: list callbacks: %v", err)
	} else {
		for _, event := range callbacks {
			a.processCallback(ctx, event)
		}
	}
	orders, err := a.store.ListDueOrders(ctx, 100)
	if err != nil {
		a.logger.Printf("worker: list due orders: %v", err)
		return
	}
	for _, order := range orders {
		processCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		if _, err := a.syncOrder(processCtx, &order); err != nil {
			a.logger.Printf("worker: order %s: %v", order.BridgeOrderNo, err)
		}
		cancel()
	}
}

func (a *App) processCallback(ctx context.Context, event domain.CallbackEvent) {
	order, err := a.store.GetOrderByDujiaoID(ctx, event.DujiaoOrderID)
	if err != nil {
		_ = a.store.MarkCallbackFailed(ctx, event.ID, err.Error())
		return
	}
	_, channelUserID, err := dujiao.ParseCallback(event.Payload)
	if err != nil || channelUserID != channelUserIDFor(order.UserID) {
		if err == nil {
			err = fmt.Errorf("callback user does not match bridge order")
		}
		_ = a.store.MarkCallbackFailed(ctx, event.ID, err.Error())
		return
	}
	if _, err := a.syncOrder(ctx, order); err != nil {
		_ = a.store.MarkCallbackFailed(ctx, event.ID, err.Error())
		return
	}
	_ = a.store.MarkCallbackProcessed(ctx, event.ID)
}

func (a *App) syncOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	if order.Status.Terminal() {
		return order, nil
	}
	if !a.cfg.DujiaoConfigured() {
		return order, fmt.Errorf("Dujiao channel is not configured")
	}
	if order.DujiaoOrderNo == "" {
		return order, fmt.Errorf("order has no Dujiao order number")
	}
	remote, err := a.dujiao.GetOrder(ctx, channelUserIDFor(order.UserID), order.DujiaoOrderNo)
	if err != nil {
		_ = a.store.SetLastError(ctx, order.BridgeOrderNo, err.Error())
		return order, fmt.Errorf("query Dujiao order: %w", err)
	}
	if remote.ID != order.DujiaoOrderID {
		_ = a.store.SetLastError(ctx, order.BridgeOrderNo, "Dujiao order id mismatch")
		return order, fmt.Errorf("Dujiao order id mismatch")
	}
	if !strings.EqualFold(remote.Currency, order.Currency) {
		_ = a.store.SetLastError(ctx, order.BridgeOrderNo, "Dujiao currency mismatch")
		return order, fmt.Errorf("Dujiao currency mismatch: got %s want %s", remote.Currency, order.Currency)
	}
	if remote.TotalAmount != order.OrderAmountCents {
		_ = a.store.SetLastError(ctx, order.BridgeOrderNo, "Dujiao amount mismatch")
		return order, fmt.Errorf("Dujiao amount mismatch: got %s want %s", remote.TotalAmount, order.OrderAmountCents)
	}

	switch remote.Status {
	case "canceled":
		return a.reloadAfter(ctx, order.BridgeOrderNo, order.UserID, func() error {
			return a.store.MarkCanceled(ctx, order.BridgeOrderNo, false)
		})
	case "refunded", "partially_refunded":
		return a.reloadAfter(ctx, order.BridgeOrderNo, order.UserID, func() error {
			return a.store.MarkCanceled(ctx, order.BridgeOrderNo, true)
		})
	case "pending_payment":
		if err := a.store.MarkPendingPayment(ctx, order.BridgeOrderNo); err != nil {
			return order, err
		}
		return a.store.GetOrderForUser(ctx, order.BridgeOrderNo, order.UserID)
	case "paid", "fulfilling", "partially_delivered", "delivered", "completed":
		paid := remote.OnlinePaidAmount
		if !paid.Positive() {
			paid = remote.PaidAmount
		}
		if paid < order.OrderAmountCents {
			_ = a.store.SetLastError(ctx, order.BridgeOrderNo, "Dujiao paid amount mismatch")
			return order, fmt.Errorf("Dujiao paid amount %s is less than order amount %s", paid, order.OrderAmountCents)
		}
		if err := a.store.MarkPaid(ctx, order.BridgeOrderNo); err != nil {
			return order, err
		}
		return a.creditOrder(ctx, order)
	default:
		_ = a.store.SetLastError(ctx, order.BridgeOrderNo, "unsupported Dujiao status")
		return order, fmt.Errorf("unsupported Dujiao order status %q", remote.Status)
	}
}

func (a *App) reloadAfter(ctx context.Context, orderNo string, userID int64, fn func() error) (*domain.Order, error) {
	if err := fn(); err != nil {
		return nil, err
	}
	return a.store.GetOrderForUser(ctx, orderNo, userID)
}

func (a *App) creditOrder(ctx context.Context, order *domain.Order) (*domain.Order, error) {
	if !a.cfg.Sub2APIAdminConfigured() {
		if err := a.store.DeferCredit(ctx, order.BridgeOrderNo, "Sub2API admin API key is not configured", a.cfg.CreditRetryBackoff); err != nil {
			return order, err
		}
		return a.store.GetOrderForUser(ctx, order.BridgeOrderNo, order.UserID)
	}
	claimed, ok, err := a.store.ClaimCredit(ctx, order.BridgeOrderNo, a.cfg.CreditRetryBackoff, false)
	if err != nil {
		return order, err
	}
	if !ok {
		return a.store.GetOrderForUser(ctx, order.BridgeOrderNo, order.UserID)
	}
	code := "xddshop-" + claimed.BridgeOrderNo
	notes := fmt.Sprintf("Dujiao order %s, bridge order %s", claimed.DujiaoOrderNo, claimed.BridgeOrderNo)
	err = a.sub2api.RedeemBalance(ctx, claimed.UserID, claimed.CreditCents, code, notes, redeemIdempotencyKey(claimed.BridgeOrderNo))
	if err != nil {
		_ = a.store.MarkCreditFailed(ctx, claimed.BridgeOrderNo, err.Error(), a.cfg.CreditRetryBackoff)
		return a.store.GetOrderForUser(ctx, claimed.BridgeOrderNo, claimed.UserID)
	}
	if err := a.store.MarkCredited(ctx, claimed.BridgeOrderNo, code); err != nil {
		return claimed, fmt.Errorf("redeem succeeded but saving credited state failed: %w", err)
	}
	return a.store.GetOrderForUser(ctx, claimed.BridgeOrderNo, claimed.UserID)
}

func redeemIdempotencyKey(orderNo string) string {
	return "redeem-" + orderNo
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":               "ok",
		"payment_configured":   a.cfg.DujiaoConfigured(),
		"crediting_configured": a.cfg.Sub2APIAdminConfigured(),
	})
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(bridgeStatic.Index))
}

func (a *App) handleCatalog(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "请重新登录 XDD API 后再打开充值页")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id": user.ID, "email": user.Email, "username": user.Username, "balance": user.Balance.String(),
		},
		"skus":          config.SortedSKUs(a.cfg.SKUs),
		"support_url":   a.cfg.SupportURL,
		"support_label": a.cfg.SupportLabel,
	})
}

func (a *App) handlePaymentChannels(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效，请重新登录")
		return
	}
	if !a.cfg.DujiaoConfigured() {
		writeJSON(w, http.StatusOK, map[string]any{
			"channels": []any{}, "payment_configured": false, "maintenance": true,
		})
		return
	}
	orderNo := strings.TrimSpace(r.URL.Query().Get("order_no"))
	channelUserID := ""
	if orderNo != "" {
		order, orderErr := a.store.GetOrderForUser(r.Context(), orderNo, user.ID)
		if orderErr != nil {
			writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
			return
		}
		if strings.TrimSpace(order.DujiaoOrderNo) == "" {
			writeError(w, http.StatusConflict, "ORDER_NOT_READY", "订单尚未创建完成，请稍后重试")
			return
		}
		orderNo = order.DujiaoOrderNo
		channelUserID = channelUserIDFor(user.ID)
	}
	channels, err := a.dujiao.PaymentChannels(r.Context(), orderNo, channelUserID)
	if err != nil {
		a.logger.Printf("list payment channels: %v", err)
		writeError(w, http.StatusBadGateway, "PAYMENT_CHANNELS_UNAVAILABLE", "支付方式暂时不可用")
		return
	}
	result := make([]map[string]any, 0, len(channels))
	for _, channel := range channels {
		if channel.ID <= 0 || strings.TrimSpace(channel.Name) == "" {
			continue
		}
		result = append(result, map[string]any{
			"id": channel.ID, "name": channel.Name, "provider_type": channel.ProviderType,
			"channel_type": channel.ChannelType, "interaction_mode": channel.InteractionMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": result})
}

type createOrderRequest struct {
	SKUKey    string `json:"sku_key"`
	Quantity  int    `json:"quantity"`
	ChannelID int64  `json:"channel_id"`
}

func (a *App) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效，请重新登录")
		return
	}
	if !a.cfg.DujiaoConfigured() {
		writeError(w, http.StatusServiceUnavailable, "PAYMENT_NOT_CONFIGURED", "支付通道正在配置中，请稍后再试")
		return
	}
	var req createOrderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求格式不正确")
		return
	}
	req.SKUKey = strings.TrimSpace(req.SKUKey)
	sku, ok := a.cfg.SKUs[req.SKUKey]
	if !ok {
		writeError(w, http.StatusBadRequest, "UNKNOWN_SKU", "充值套餐不存在")
		return
	}
	if req.Quantity < sku.MinQuantity || req.Quantity > sku.MaxQuantity {
		writeError(w, http.StatusBadRequest, "INVALID_QUANTITY", fmt.Sprintf("购买数量须在 %d–%d 之间", sku.MinQuantity, sku.MaxQuantity))
		return
	}
	clientKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if clientKey == "" {
		clientKey = randomHex(16)
	}
	scopedKey := fmt.Sprintf("user:%d:%s", user.ID, clientKey)
	if existing, err := a.store.GetOrderByClientKey(r.Context(), scopedKey); err == nil {
		if existing.UserID != user.ID || existing.SKUKey != req.SKUKey || existing.Quantity != req.Quantity {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "重复请求与原订单不一致")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"order": orderView(existing)})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "订单读取失败")
		return
	}

	now := time.Now().UTC()
	expiresAt := now.Add(30 * time.Minute)
	order := &domain.Order{
		BridgeOrderNo: "xdd-" + now.Format("20060102T150405") + "-" + randomHex(5),
		UserID:        user.ID, UserEmail: user.Email, SKUKey: sku.Key, ProductID: sku.ProductID,
		SKUID: sku.SKUID, Quantity: req.Quantity, Currency: sku.Currency,
		OrderAmountCents: money.FromInt64(sku.PriceCents.Int64() * int64(req.Quantity)),
		CreditCents:      money.FromInt64(sku.CreditCents.Int64() * int64(req.Quantity)),
		Status:           domain.StatusCreated, SelectedChannelID: req.ChannelID,
		IdempotencyKey: scopedKey, ExpiresAt: &expiresAt,
	}
	if err := a.store.CreateOrder(r.Context(), order); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeError(w, http.StatusConflict, "DUPLICATE_REQUEST", "订单请求重复，请刷新订单列表")
			return
		}
		a.logger.Printf("create order: %v", err)
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "订单创建失败")
		return
	}

	remote, err := a.dujiao.CreateOrder(r.Context(), channelUserIDFor(user.ID), sku.ProductID, sku.SKUID, req.Quantity)
	if err != nil {
		_ = a.store.MarkCanceled(r.Context(), order.BridgeOrderNo, false)
		a.logger.Printf("create Dujiao order %s: %v", order.BridgeOrderNo, err)
		writeError(w, http.StatusBadGateway, "UPSTREAM_ERROR", "商城暂时无法创建订单，请稍后重试")
		return
	}
	if remote.TotalAmount != order.OrderAmountCents || !strings.EqualFold(remote.Currency, order.Currency) {
		_ = a.dujiao.CancelOrder(r.Context(), channelUserIDFor(user.ID), remote.ID, "SKU price mismatch")
		_ = a.store.MarkCanceled(r.Context(), order.BridgeOrderNo, false)
		a.logger.Printf("SKU price drift for %s: got %s %s, configured %s %s", order.SKUKey, remote.TotalAmount, remote.Currency, order.OrderAmountCents, order.Currency)
		writeError(w, http.StatusConflict, "PRICE_CHANGED", "商品价格已变化，请联系管理员更新充值配置")
		return
	}
	if err := a.store.SetDujiaoOrder(r.Context(), order.BridgeOrderNo, remote.ID, remote.OrderNo, remote.Currency, remote.TotalAmount, remote.ExpiresAt); err != nil {
		a.logger.Printf("save Dujiao order %s: %v", order.BridgeOrderNo, err)
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "订单保存失败")
		return
	}
	if req.ChannelID > 0 {
		if err := a.createPayment(r.Context(), order.BridgeOrderNo, remote.ID, user.ID, req.ChannelID); err != nil {
			a.logger.Printf("create payment for %s: %v", order.BridgeOrderNo, err)
			writeError(w, http.StatusBadGateway, "PAYMENT_ERROR", "订单已创建，但支付页面生成失败，请重试")
			return
		}
	}
	updated, err := a.store.GetOrderForUser(r.Context(), order.BridgeOrderNo, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "订单读取失败")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order": orderView(updated)})
}

type payOrderRequest struct {
	ChannelID int64 `json:"channel_id"`
}

func (a *App) handlePayOrder(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效，请重新登录")
		return
	}
	if !a.cfg.DujiaoConfigured() {
		writeError(w, http.StatusServiceUnavailable, "PAYMENT_NOT_CONFIGURED", "支付通道正在配置中，请稍后再试")
		return
	}
	var req payOrderRequest
	if err := decodeJSON(r, &req); err != nil || req.ChannelID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请选择支付方式")
		return
	}
	order, err := a.store.GetOrderForUser(r.Context(), r.PathValue("orderNo"), user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	if order.Status == domain.StatusCredited || order.Status == domain.StatusCanceled || order.Status == domain.StatusRefunded {
		writeJSON(w, http.StatusOK, map[string]any{"order": orderView(order)})
		return
	}
	if err := a.createPayment(r.Context(), order.BridgeOrderNo, order.DujiaoOrderID, user.ID, req.ChannelID); err != nil {
		a.logger.Printf("create payment %s: %v", order.BridgeOrderNo, err)
		writeError(w, http.StatusBadGateway, "PAYMENT_ERROR", "支付页面生成失败，请稍后重试")
		return
	}
	updated, err := a.store.GetOrderForUser(r.Context(), order.BridgeOrderNo, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "订单读取失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": orderView(updated)})
}

func (a *App) createPayment(ctx context.Context, orderNo string, dujiaoOrderID, userID, channelID int64) error {
	payment, err := a.dujiao.CreatePayment(ctx, channelUserIDFor(userID), dujiaoOrderID, channelID)
	if err != nil {
		return err
	}
	return a.store.SetPayment(ctx, orderNo, payment.ID, channelID, payment.PayURL, payment.QRCode)
}

func (a *App) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效，请重新登录")
		return
	}
	order, err := a.store.GetOrderForUser(r.Context(), r.PathValue("orderNo"), user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": orderView(order)})
}

func (a *App) handleRefreshOrder(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效，请重新登录")
		return
	}
	order, err := a.store.GetOrderForUser(r.Context(), r.PathValue("orderNo"), user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	updated, err := a.syncOrder(r.Context(), order)
	if err != nil && updated == nil {
		writeError(w, http.StatusBadGateway, "SYNC_ERROR", "暂时无法查询支付状态，请稍后刷新")
		return
	}
	if err != nil && updated != nil && updated.Status != domain.StatusCreditFailed {
		writeError(w, http.StatusBadGateway, "SYNC_ERROR", "暂时无法查询支付状态，请稍后刷新")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": orderView(updated)})
}

func (a *App) handleListOrders(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticate(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效，请重新登录")
		return
	}
	orders, err := a.store.ListUserOrders(r.Context(), user.ID, 30)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "订单列表读取失败")
		return
	}
	result := make([]map[string]any, 0, len(orders))
	for i := range orders {
		result = append(result, orderView(&orders[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": result})
}

func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.DujiaoConfigured() {
		writeCallbackError(w, http.StatusServiceUnavailable, "payment callback is not configured")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeCallbackError(w, http.StatusBadRequest, "invalid callback body")
		return
	}
	path := r.URL.Path
	eventType := strings.TrimPrefix(path, "/internal/")
	if eventType != "order-paid" && eventType != "order-fulfilled" {
		writeCallbackError(w, http.StatusNotFound, "unknown callback")
		return
	}
	if err := a.dujiao.VerifyCallback(
		r.Header.Get("Dujiao-Next-Channel-Key"), r.Method, path,
		r.Header.Get("Dujiao-Next-Channel-Timestamp"), r.Header.Get("Dujiao-Next-Channel-Signature"), raw,
		a.cfg.CallbackMaxSkew,
	); err != nil {
		a.logger.Printf("callback auth failed: %v", err)
		writeCallbackError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	orderID, _, err := dujiao.ParseCallback(raw)
	if err != nil {
		writeCallbackError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash := sha256.Sum256(raw)
	created, err := a.store.PersistCallback(r.Context(), eventType, orderID, hex.EncodeToString(hash[:]), raw)
	if err != nil {
		a.logger.Printf("persist callback: %v", err)
		writeCallbackError(w, http.StatusInternalServerError, "callback persistence failed")
		return
	}
	if created {
		order, getErr := a.store.GetOrderByDujiaoID(r.Context(), orderID)
		if getErr == nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, syncErr := a.syncOrder(ctx, order); syncErr != nil {
					a.logger.Printf("callback sync order %s: %v", order.BridgeOrderNo, syncErr)
				}
			}()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status_code": 0, "msg": "success", "data": map[string]any{"accepted": true}})
}

func (a *App) handleAdminOrders(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeAdmin(r) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "管理员认证失败")
		return
	}
	orders, err := a.store.ListOrders(r.Context(), strings.TrimSpace(r.URL.Query().Get("status")), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_ERROR", "订单列表读取失败")
		return
	}
	result := make([]map[string]any, 0, len(orders))
	for i := range orders {
		result = append(result, adminOrderView(&orders[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": result})
}

func (a *App) handleAdminRetry(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeAdmin(r) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "管理员认证失败")
		return
	}
	orderNo := r.PathValue("orderNo")
	if err := a.store.ResetForRetry(r.Context(), orderNo); err != nil {
		writeError(w, http.StatusConflict, "RETRY_NOT_ALLOWED", "该订单当前不能重试")
		return
	}
	// Public lookup is unnecessary; process by order id path through a direct
	// admin query to keep this endpoint independent of the user session.
	adminOrders, err := a.findAdminOrder(r.Context(), orderNo)
	if err != nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	updated, syncErr := a.syncOrder(r.Context(), adminOrders)
	if syncErr != nil && (updated == nil || updated.Status != domain.StatusCreditFailed) {
		writeError(w, http.StatusBadGateway, "SYNC_ERROR", "重试时查询商城订单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": adminOrderView(updated)})
}

func (a *App) findAdminOrder(ctx context.Context, orderNo string) (*domain.Order, error) {
	orders, err := a.store.ListOrders(ctx, "", 200)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		if orders[i].BridgeOrderNo == orderNo {
			return &orders[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (a *App) authenticate(r *http.Request) (*sub2api.User, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, fmt.Errorf("missing bearer token")
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if token == "" {
		return nil, fmt.Errorf("empty bearer token")
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.RequestTimeout)
	defer cancel()
	return a.sub2api.VerifyUser(ctx, token)
}

func (a *App) authorizeAdmin(r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	provided := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(a.cfg.AdminToken)) == 1
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/internal/") || strings.HasPrefix(r.URL.Path, "/admin/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(r *http.Request, out any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("request must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "message": message})
}

func writeCallbackError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"status_code": status, "msg": message})
}

func channelUserIDFor(userID int64) string { return "sub2api-user-" + strconv.FormatInt(userID, 10) }

func randomHex(bytesCount int) string {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

func randomInt(max int) int {
	if max <= 0 {
		return 0
	}
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return int(time.Now().UnixNano() % int64(max))
	}
	return int(hexToUint32(raw) % uint32(max))
}

func hexToUint32(raw []byte) uint32 {
	var result uint32
	for _, b := range raw {
		result = result<<8 | uint32(b)
	}
	return result
}

func orderView(order *domain.Order) map[string]any {
	result := map[string]any{
		"order_no": order.BridgeOrderNo, "status": order.Status, "sku_key": order.SKUKey,
		"quantity": order.Quantity, "currency": order.Currency,
		"order_amount": order.OrderAmountCents.String(), "credit_amount": order.CreditCents.String(),
		"pay_url": order.PaymentURL, "qr_code": order.QRCode,
		"channel_id": order.SelectedChannelID, "created_at": order.CreatedAt,
		"updated_at": order.UpdatedAt, "expires_at": order.ExpiresAt,
		"paid_at": order.PaidAt, "credited_at": order.CreditedAt,
	}
	if order.Status == domain.StatusCreditFailed {
		result["message"] = "支付已确认，但余额到账暂时失败。系统会自动重试。"
	}
	return result
}

func adminOrderView(order *domain.Order) map[string]any {
	result := orderView(order)
	result["user_id"] = order.UserID
	result["user_email"] = order.UserEmail
	result["dujiao_order_id"] = order.DujiaoOrderID
	result["dujiao_order_no"] = order.DujiaoOrderNo
	result["retry_count"] = order.RetryCount
	result["last_error"] = order.LastError
	result["paid_at"] = order.PaidAt
	result["credited_at"] = order.CreditedAt
	return result
}
