package mdb

// PaymentTerms is a versioned snapshot for explicitly fixed-network orders.
// It is gateway evidence, not a merchant-authorized quote or an RPC proof.
type PaymentTerms struct {
	Version       int    `json:"version"`
	TradeID       string `json:"trade_id"`
	OrderID       string `json:"order_id"`
	APIKeyID      uint64 `json:"api_key_id"`
	Network       string `json:"network"`
	Token         string `json:"token"`
	Contract      string `json:"contract"`
	Decimals      int    `json:"decimals"`
	Recipient     string `json:"recipient"`
	RawAmount     string `json:"raw_amount"`
	FiatAmount    string `json:"fiat_amount"`
	Currency      string `json:"currency"`
	RequestAmount string `json:"request_amount"`
	Rate          string `json:"rate"`
	CreatedAtMs   int64  `json:"created_at_ms"`
	ExpiresAtMs   int64  `json:"expires_at_ms"`
}
