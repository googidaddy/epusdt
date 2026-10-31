package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/shopspring/decimal"
)

// RecordFixedTransferObservation returns handled=true for a historical fixed
// slot, even when evidence is late, ambiguous or inconsistent. Never send it
// to the legacy settlement path. This is discovery, not independent checking.
func RecordFixedTransferObservation(observation mdb.PaymentCandidate) (bool, error) {
	decimals := 6
	switch observation.Network {
	case mdb.NetworkTron:
	case mdb.NetworkBsc:
		decimals = 18
	default:
		return false, nil
	}
	if len(observation.RawAmount) > 80 {
		return false, fmt.Errorf("candidate amount too long")
	}
	raw, ok := new(big.Int).SetString(observation.RawAmount, 10)
	if !ok || raw.Sign() < 0 || raw.String() != observation.RawAmount {
		return false, fmt.Errorf("invalid candidate amount")
	}
	if raw.Sign() == 0 {
		return false, nil
	}
	amount := decimal.NewFromBigInt(raw, -int32(decimals))
	orders, err := data.FixedOrdersForObservedAmount(observation.Network, observation.Recipient, amount.InexactFloat64())
	if err != nil {
		return false, err
	}
	if len(orders) == 0 {
		return false, nil
	}
	// Float lookup only discovers slots. It never authorizes the raw amount.
	if observation.BlockNumber <= 0 || observation.BlockHash == "" || observation.TransactionHash == "" {
		return true, fmt.Errorf("missing candidate block identity")
	}
	hash := strings.TrimPrefix(observation.TransactionHash, "0x")
	blockHash := strings.TrimPrefix(observation.BlockHash, "0x")
	if len(hash) != 64 || len(blockHash) != 64 || hash == strings.Repeat("0", 64) || blockHash == strings.Repeat("0", 64) {
		return true, fmt.Errorf("invalid candidate hash")
	}
	if _, err = hex.DecodeString(hash); err != nil {
		return true, err
	}
	if _, err = hex.DecodeString(blockHash); err != nil {
		return true, err
	}
	observation.Contract = normalizeOrderAddressByNetwork(observation.Network, observation.Contract)
	observation.Recipient = normalizeOrderAddressByNetwork(observation.Network, observation.Recipient)
	observation.TransactionHash = strings.ToLower(observation.TransactionHash)
	observation.BlockHash = strings.ToLower(observation.BlockHash)
	rows := make([]mdb.PaymentCandidate, 0, len(orders))
	for i := range orders {
		order := &orders[i]
		if order.ApiKeyID == 0 {
			return true, fmt.Errorf("fixed order has no merchant identity")
		}
		row := observation
		row.ID = 0
		row.ObservationKey = ""
		row.ObservedAtMs = 0
		row.AcknowledgedAtMs = 0
		row.APIKeyID = order.ApiKeyID
		row.TradeID = order.TradeId
		row.OrderID = order.OrderId
		row.ReviewReason = "independent_check_required"
		switch {
		case validatePaymentTerms(order) != nil || order.PaymentTerms == nil:
			row.ReviewReason = "invalid_terms"
		case row.Contract != order.PaymentTerms.Contract || row.RawAmount != order.PaymentTerms.RawAmount:
			row.ReviewReason = "terms_mismatch"
		case row.Removed:
			row.ReviewReason = "removed"
		case row.BlockTimeMs <= 0:
			row.ReviewReason = "missing_block_time"
		case row.BlockTimeMs < order.PaymentTerms.CreatedAtMs:
			row.ReviewReason = "before_intent"
		case row.BlockTimeMs >= order.PaymentTerms.ExpiresAtMs:
			row.ReviewReason = "late"
		default:
			count, countErr := data.PaymentSlotOrderCount(order.Network, order.ReceiveAddress, order.Token, order.ActualAmount)
			if countErr != nil {
				return true, countErr
			}
			if count != 1 {
				row.ReviewReason = "ambiguous"
			}
		}
		// Content-address the observation, not a presumed canonical chain event.
		// A reorg/removal or changed evidence appends another record, never erases.
		b, marshalErr := json.Marshal(row)
		if marshalErr != nil {
			return true, marshalErr
		}
		digest := sha256.Sum256(b)
		row.ObservationKey = hex.EncodeToString(digest[:])
		row.ObservedAtMs = time.Now().UnixMilli()
		rows = append(rows, row)
	}
	return true, data.SavePaymentCandidates(rows)
}

func RecordFixedEVMLog(network string, event types.Log, blockTimeMs int64) (bool, error) {
	if network != mdb.NetworkBsc {
		return false, nil
	}
	transferTopic := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	if len(event.Topics) != 3 || event.Topics[0] != transferTopic || len(event.Data) != 32 ||
		!bytes.Equal(event.Topics[1].Bytes()[:12], make([]byte, 12)) || !bytes.Equal(event.Topics[2].Bytes()[:12], make([]byte, 12)) {
		return true, fmt.Errorf("invalid ERC20 candidate shape")
	}
	if event.BlockNumber > uint64(^uint64(0)>>1) {
		return true, fmt.Errorf("invalid candidate block number")
	}
	index := uint64(event.Index)
	return RecordFixedTransferObservation(mdb.PaymentCandidate{Network: network, Kind: "evm_log", TransactionHash: event.TxHash.Hex(), EventIndex: &index,
		BlockHash: event.BlockHash.Hex(), BlockNumber: int64(event.BlockNumber), BlockTimeMs: blockTimeMs, Contract: event.Address.Hex(),
		Recipient: common.BytesToAddress(event.Topics[2].Bytes()).Hex(), RawAmount: new(big.Int).SetBytes(event.Data).String(), Removed: event.Removed})
}
