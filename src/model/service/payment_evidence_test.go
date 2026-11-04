package service

import (
	"context"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/GMWalletApp/epusdt/internal/testutil"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/dromara/carbon/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func evmPaymentEvidenceFixture() (*fakeEvmReader, *mdb.Orders, *mdb.ChainToken, common.Hash) {
	txHash := common.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	contract := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	header := &types.Header{Number: big.NewInt(10), Time: uint64(time.Now().Unix())}
	reader := &fakeEvmReader{
		receipt: &types.Receipt{
			Status: types.ReceiptStatusSuccessful, TxHash: txHash,
			BlockNumber: big.NewInt(10), BlockHash: header.Hash(),
			Logs: []*types.Log{{
				Address: contract, TxHash: txHash, BlockNumber: 10, BlockHash: header.Hash(),
				Topics: []common.Hash{erc20TransferEventHash, {}, common.BytesToHash(to.Bytes())},
				Data:   common.LeftPadBytes(big.NewInt(1230000).Bytes(), 32),
			}},
		},
		headers: map[string]*types.Header{
			"10":     header,
			"latest": {Number: big.NewInt(12), Time: header.Time + 6},
		},
	}
	order := &mdb.Orders{
		Network: mdb.NetworkBsc, Token: "USDT", ActualAmount: 1.23, ReceiveAddress: to.Hex(),
		BaseModel: mdb.BaseModel{CreatedAt: *carbon.NewTime(carbon.CreateFromTimestampMilli(time.Now().Add(-time.Minute).UnixMilli()))},
	}
	token := &mdb.ChainToken{Network: order.Network, Symbol: "USDT", ContractAddress: contract.Hex(), Decimals: 6}
	return reader, order, token, txHash
}

func TestEvmPaymentRejectsIncompleteOrMismatchedEvidence(t *testing.T) {
	cleanup := testutil.SetupTestDatabases(t)
	defer cleanup()
	for _, tc := range []struct {
		name    string
		corrupt func(*fakeEvmReader)
	}{
		{"missing receipt", func(r *fakeEvmReader) { r.receipt = nil }},
		{"failed receipt", func(r *fakeEvmReader) { r.receipt.Status = types.ReceiptStatusFailed }},
		{"wrong transaction", func(r *fakeEvmReader) { r.receipt.TxHash = common.Hash{} }},
		{"missing block number", func(r *fakeEvmReader) { r.receipt.BlockNumber = nil }},
		{"wrong canonical block", func(r *fakeEvmReader) { r.receipt.BlockHash = common.Hash{} }},
		{"missing timestamp", func(r *fakeEvmReader) {
			r.headers["10"].Time = 0
			r.receipt.BlockHash = r.headers["10"].Hash()
			r.receipt.Logs[0].BlockHash = r.receipt.BlockHash
		}},
		{"missing head number", func(r *fakeEvmReader) { r.headers["latest"].Number = nil }},
		{"head before receipt", func(r *fakeEvmReader) { r.headers["latest"].Number = big.NewInt(9) }},
		{"removed event", func(r *fakeEvmReader) { r.receipt.Logs[0].Removed = true }},
		{"event from other transaction", func(r *fakeEvmReader) { r.receipt.Logs[0].TxHash = common.Hash{} }},
		{"event from other block", func(r *fakeEvmReader) { r.receipt.Logs[0].BlockHash = common.Hash{} }},
		{"event with other height", func(r *fakeEvmReader) { r.receipt.Logs[0].BlockNumber = 11 }},
		{"event with other transaction index", func(r *fakeEvmReader) { r.receipt.Logs[0].TxIndex = 1 }},
		{"wrong token", func(r *fakeEvmReader) { r.receipt.Logs[0].Address = common.Address{} }},
		{"wrong recipient", func(r *fakeEvmReader) { r.receipt.Logs[0].Topics[2] = common.Hash{} }},
		{"invalid address padding", func(r *fakeEvmReader) { r.receipt.Logs[0].Topics[2][0] = 1 }},
		{"non ERC20 topics", func(r *fakeEvmReader) { r.receipt.Logs[0].Topics = append(r.receipt.Logs[0].Topics, common.Hash{}) }},
		{"short amount", func(r *fakeEvmReader) { r.receipt.Logs[0].Data = big.NewInt(1230000).Bytes() }},
		{"underpayment", func(r *fakeEvmReader) { r.receipt.Logs[0].Data = common.LeftPadBytes(big.NewInt(1229999).Bytes(), 32) }},
		{"overpayment", func(r *fakeEvmReader) { r.receipt.Logs[0].Data = common.LeftPadBytes(big.NewInt(1230001).Bytes(), 32) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, order, token, hash := evmPaymentEvidenceFixture()
			tc.corrupt(r)
			if err := validateManualEvmPaymentWithClient(context.Background(), r, order, hash, token); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			r, order, token, hash = evmPaymentEvidenceFixture()
			if err := validateManualEvmPaymentWithClient(context.Background(), r, order, hash, token); err != nil {
				t.Fatalf("retry with valid evidence: %v", err)
			}
		})
	}
}

func TestPaymentAmountRequiresExactPositiveValue(t *testing.T) {
	for _, decimals := range []int{6, 18} {
		raw := new(big.Int).Mul(big.NewInt(123), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-2)), nil))
		if !amountMatchesRaw(1.23, raw, decimals) {
			t.Fatalf("exact amount rejected for decimals=%d", decimals)
		}
		for _, offset := range []int64{-1, 1} {
			if amountMatchesRaw(1.23, new(big.Int).Add(raw, big.NewInt(offset)), decimals) {
				t.Fatalf("amount offset %d accepted for decimals=%d", offset, decimals)
			}
		}
	}
	for _, invalid := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if amountMatchesRaw(invalid, big.NewInt(1), 6) || amountMatchesFloat(invalid, invalid) {
			t.Fatalf("invalid amount accepted: %v", invalid)
		}
	}
	if amountMatchesRaw(1.0000001, big.NewInt(1000000), 6) || amountMatchesRaw(1, big.NewInt(1), -1) || amountMatchesRaw(1, big.NewInt(1), 256) {
		t.Fatal("unrepresentable amount or invalid decimals accepted")
	}
	if amountMatchesFloat(1.23, 1.229999) || amountMatchesFloat(1.23, 1.230001) || !amountMatchesFloat(1.23, 1.23) {
		t.Fatal("float payment comparison must preserve exact positive decimal values")
	}
}
