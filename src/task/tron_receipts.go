package task

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/service"
)

// Discovery reads every receipt in the solid block. Inspecting the top-level
// calldata misses transferFrom, routers and internal contract calls.
type tronBlockReceipt struct {
	ID             string `json:"id"`
	BlockNumber    int64  `json:"blockNumber"`
	BlockTimeStamp int64  `json:"blockTimeStamp"`
	Log            []struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	} `json:"log"`
}

func processTronReceipts(block *Block, payload []byte) error {
	var receipts []tronBlockReceipt
	if err := json.Unmarshal(payload, &receipts); err != nil {
		return fmt.Errorf("invalid block receipts")
	}
	wanted := make(map[string]bool, len(block.Transactions))
	for _, tx := range block.Transactions {
		if !validTronHash(tx.TxID) || wanted[tx.TxID] {
			return fmt.Errorf("invalid block transaction identity")
		}
		wanted[tx.TxID] = true
	}
	if len(receipts) != len(wanted) {
		return fmt.Errorf("incomplete block receipt coverage")
	}
	for _, receipt := range receipts {
		if !wanted[receipt.ID] || receipt.BlockNumber != block.BlockHeader.RawData.Number || receipt.BlockTimeStamp != block.BlockHeader.RawData.Timestamp {
			return fmt.Errorf("receipt does not belong to solid block")
		}
		delete(wanted, receipt.ID)
		for i, event := range receipt.Log {
			contract := normalizeTronHexAddress(event.Address)
			contract = strings.TrimPrefix(contract, "41") // RPC logs usually use the 20-byte form.
			if contract != "a614f803b6fd780986a42c78ec9c7f77e6ded13c" {
				continue
			}
			if len(event.Topics) == 0 || strings.ToLower(event.Topics[0]) != "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
				continue
			}
			if len(event.Topics) != 3 || len(event.Data) != 64 {
				return fmt.Errorf("invalid USDT Transfer shape")
			}
			for _, topic := range event.Topics[1:] {
				if len(topic) != 64 || topic[:24] != strings.Repeat("0", 24) {
					return fmt.Errorf("invalid Transfer address word")
				}
				if _, err := hex.DecodeString(topic); err != nil {
					return fmt.Errorf("invalid Transfer address encoding")
				}
			}
			raw, err := hex.DecodeString(event.Data)
			if err != nil {
				return fmt.Errorf("invalid Transfer amount encoding")
			}
			to, err := HexToTronAddress(event.Topics[2][24:])
			if err != nil {
				return err
			}
			index := uint64(i)
			_, err = service.RecordFixedTransferObservation(mdb.PaymentCandidate{
				Network: mdb.NetworkTron, Kind: "tron_log", TransactionHash: receipt.ID, EventIndex: &index,
				BlockHash: block.BlockID, BlockNumber: receipt.BlockNumber, BlockTimeMs: receipt.BlockTimeStamp,
				Contract: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", Recipient: to, RawAmount: new(big.Int).SetBytes(raw).String(),
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func validTronHash(v string) bool {
	if len(v) != 64 || v == strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
