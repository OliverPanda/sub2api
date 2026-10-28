package service

import (
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestClassifyCreatePaymentErrorIdentifiesZPayBalanceInsufficient(t *testing.T) {
	err := classifyCreatePaymentError(
		CreateOrderRequest{PaymentType: payment.TypeAlipay},
		payment.TypeEasyPay,
		errors.New("easypay error: ZPAY账户余额不足，无法发起支付，请提前充值余额，抵扣手续费使用。"),
	)

	if got := infraerrors.Reason(err); got != "ZPAY_BALANCE_INSUFFICIENT" {
		t.Fatalf("reason = %q, want ZPAY_BALANCE_INSUFFICIENT", got)
	}
}

func TestClassifyCreatePaymentErrorKeepsOtherGatewayErrorsGeneric(t *testing.T) {
	err := classifyCreatePaymentError(
		CreateOrderRequest{PaymentType: payment.TypeAlipay},
		payment.TypeEasyPay,
		errors.New("easypay error: upstream request rejected"),
	)

	if got := infraerrors.Reason(err); got != "PAYMENT_GATEWAY_ERROR" {
		t.Fatalf("reason = %q, want PAYMENT_GATEWAY_ERROR", got)
	}
}
