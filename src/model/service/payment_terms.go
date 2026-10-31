package service

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/GMWalletApp/epusdt/config"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/request"
	"github.com/dromara/carbon/v2"
	"github.com/shopspring/decimal"
)

func bindPaymentTerms(order *mdb.Orders, req *request.CreateTransactionRequest, rate float64) error {
	token, err := ensureEnabledOrderAsset(order.Network, order.Token)
	if err != nil {
		return err
	}
	if token.Decimals < 0 || token.Decimals > 255 || token.ContractAddress == "" || !validPaymentAmount(order.ActualAmount) || !validPaymentAmount(req.Amount) {
		return fmt.Errorf("invalid fixed payment asset or amount")
	}
	if order.Network == mdb.NetworkTron {
		if _, err = tronAddressToHex(order.ReceiveAddress); err != nil {
			return err
		}
		if _, err = tronAddressToHex(token.ContractAddress); err != nil {
			return err
		}
	} else {
		if _, err = normalizeEvmAddress(order.ReceiveAddress); err != nil {
			return err
		}
		if _, err = normalizeEvmAddress(token.ContractAddress); err != nil {
			return err
		}
	}
	raw := decimal.NewFromFloat(order.ActualAmount).Shift(int32(token.Decimals))
	if !raw.Equal(raw.Truncate(0)) {
		return fmt.Errorf("amount cannot be represented in token base units")
	}
	now := time.Now().UnixMilli()
	expires := now + config.GetOrderExpirationTimeDuration().Milliseconds()
	if req.PaymentExpiresAtMs != 0 {
		if req.PaymentExpiresAtMs <= now || req.PaymentExpiresAtMs > expires {
			return fmt.Errorf("invalid merchant payment deadline")
		}
		expires = req.PaymentExpiresAtMs
	}
	if expires <= now {
		return fmt.Errorf("invalid payment expiration")
	}
	order.CreatedAt = *carbon.NewTime(carbon.CreateFromTimestampMilli(now))
	order.PaymentExpiresAtMs = expires
	order.PaymentTerms = &mdb.PaymentTerms{
		Version: 1, TradeID: order.TradeId, OrderID: order.OrderId, APIKeyID: order.ApiKeyID,
		Network: order.Network, Token: order.Token, Contract: normalizeOrderAddressByNetwork(order.Network, token.ContractAddress),
		Decimals: token.Decimals, Recipient: order.ReceiveAddress, RawAmount: raw.StringFixed(0),
		FiatAmount: decimal.NewFromFloat(order.Amount).String(), Currency: order.Currency,
		RequestAmount: decimal.NewFromFloat(req.Amount).String(), Rate: decimal.NewFromFloat(rate).String(),
		CreatedAtMs: now, ExpiresAtMs: expires,
	}
	return nil
}

func validatePaymentTerms(order *mdb.Orders) error {
	t := order.PaymentTerms
	if t == nil {
		if order.PaymentExpiresAtMs != 0 {
			return fmt.Errorf("fixed payment terms missing")
		}
		return nil
	}
	if t.Version != 1 || t.TradeID != order.TradeId || t.OrderID != order.OrderId || t.APIKeyID != order.ApiKeyID ||
		t.Network != order.Network || t.Token != order.Token || t.Recipient != order.ReceiveAddress || t.Currency != order.Currency ||
		t.ExpiresAtMs != order.PaymentExpiresAtMs || t.ExpiresAtMs <= t.CreatedAtMs || t.CreatedAtMs <= 0 ||
		t.Decimals < 0 || t.Decimals > 255 || t.Contract == "" || !validPaymentAmount(order.Amount) || !validPaymentAmount(order.ActualAmount) ||
		t.FiatAmount != decimal.NewFromFloat(order.Amount).String() {
		return fmt.Errorf("fixed payment terms mismatch")
	}
	raw, ok := new(big.Int).SetString(t.RawAmount, 10)
	if !ok || raw.Sign() <= 0 || raw.String() != t.RawAmount || !amountMatchesRaw(order.ActualAmount, raw, t.Decimals) {
		return fmt.Errorf("fixed payment amount mismatch")
	}
	return nil
}

func fixedRequestMatches(order *mdb.Orders, req *request.CreateTransactionRequest, key *mdb.ApiKey) bool {
	if order.PaymentTerms == nil || validatePaymentTerms(order) != nil || apiKeyID(key) == 0 || order.ApiKeyID != apiKeyID(key) || !validPaymentAmount(req.Amount) {
		return false
	}
	paymentType := mdb.PaymentTypeGmpay
	if strings.EqualFold(req.PaymentType, mdb.PaymentTypeEpay) {
		paymentType = mdb.PaymentTypeEpay
	}
	return order.PaymentTerms.RequestAmount == decimal.NewFromFloat(req.Amount).String() &&
		(req.PaymentExpiresAtMs == 0 || req.PaymentExpiresAtMs == order.PaymentTerms.ExpiresAtMs) &&
		order.Network == strings.ToLower(strings.TrimSpace(req.Network)) && order.Token == strings.ToUpper(strings.TrimSpace(req.Token)) &&
		order.Currency == strings.ToUpper(strings.TrimSpace(req.Currency)) && order.NotifyUrl == strings.TrimSpace(req.NotifyUrl) &&
		order.RedirectUrl == req.RedirectUrl && order.Name == req.Name && order.PaymentType == paymentType
}

func paymentExpirationMs(order *mdb.Orders) int64 {
	if order.PaymentTerms != nil {
		return order.PaymentTerms.ExpiresAtMs
	}
	return order.CreatedAt.AddMinutes(config.GetOrderExpirationTime()).TimestampMilli()
}

func paymentTokenForVerification(order *mdb.Orders) (*mdb.ChainToken, error) {
	if err := validatePaymentTerms(order); err != nil {
		return nil, err
	}
	if t := order.PaymentTerms; t != nil {
		return &mdb.ChainToken{BaseModel: mdb.BaseModel{ID: 1}, Network: t.Network, Symbol: t.Token, ContractAddress: t.Contract, Decimals: t.Decimals, Enabled: true}, nil
	}
	return data.GetEnabledChainTokenBySymbol(order.Network, order.Token)
}

func validatePaymentWindow(order *mdb.Orders, blockMs int64) error {
	if err := validatePaymentTerms(order); err != nil {
		return err
	}
	if t := order.PaymentTerms; t != nil {
		if blockMs < t.CreatedAtMs || blockMs >= t.ExpiresAtMs {
			return fmt.Errorf("transfer outside fixed payment window")
		}
	}
	return nil
}

func validateFixedTransferCandidate(order *mdb.Orders, contract string, raw *big.Int, blockMs int64) error {
	if err := validatePaymentWindow(order, blockMs); err != nil {
		return err
	}
	t := order.PaymentTerms
	if raw == nil || raw.String() != t.RawAmount || normalizeOrderAddressByNetwork(t.Network, contract) != t.Contract {
		return fmt.Errorf("transfer does not match fixed token or amount")
	}
	return data.EnsureUniquePaymentOwner(order)
}
