package data

import (
	"fmt"
	"time"

	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func FixedOrdersForObservedAmount(network, recipient string, amount float64) ([]mdb.Orders, error) {
	var orders []mdb.Orders
	query := dao.Mdb.Unscoped().Where("network = ? AND actual_amount = ?", network, amount).
		Where("payment_terms IS NOT NULL OR payment_expires_at_ms <> 0")
	err := applyOrderReceiveAddressFilter(query, network, recipient).Find(&orders).Error
	return orders, err
}

func SavePaymentCandidates(candidates []mdb.PaymentCandidate) error {
	if len(candidates) == 0 {
		return nil
	}
	// All potential owners are recorded atomically; a retry may safely replay.
	return dao.Mdb.Transaction(func(tx *gorm.DB) error {
		for i := range candidates {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidates[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// No high-water cursor: a later ID may commit before an earlier transaction.
// Return unacknowledged rows until the merchant confirms its own durable copy.
func ListPaymentCandidates(apiKeyID uint64) ([]mdb.PaymentCandidate, error) {
	if apiKeyID == 0 {
		return nil, fmt.Errorf("merchant identity required")
	}
	rows := make([]mdb.PaymentCandidate, 0)
	err := dao.Mdb.Where("api_key_id = ? AND acknowledged_at_ms = 0", apiKeyID).Order("id ASC").Limit(100).Find(&rows).Error
	return rows, err
}

func AcknowledgePaymentCandidates(apiKeyID uint64, ids []uint64) error {
	if apiKeyID == 0 || len(ids) == 0 || len(ids) > 100 {
		return fmt.Errorf("invalid candidate acknowledgement")
	}
	seen := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			return fmt.Errorf("invalid candidate IDs")
		}
		seen[id] = true
	}
	return dao.Mdb.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&mdb.PaymentCandidate{}).Where("api_key_id = ? AND id IN ?", apiKeyID, ids).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return fmt.Errorf("candidate batch not owned by merchant")
		}
		return tx.Model(&mdb.PaymentCandidate{}).Where("api_key_id = ? AND id IN ? AND acknowledged_at_ms = 0", apiKeyID, ids).
			Update("acknowledged_at_ms", time.Now().UnixMilli()).Error
	})
}

func GetTronScanCheckpoint() (*mdb.TronScanCheckpoint, error) {
	row := new(mdb.TronScanCheckpoint)
	err := dao.Mdb.Where("network = ?", mdb.NetworkTron).Limit(1).Find(row).Error
	return row, err
}

func SaveTronScanCheckpoint(lastBlock int64) error {
	if lastBlock < 0 {
		return fmt.Errorf("invalid TRON checkpoint")
	}
	return dao.Mdb.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "network"}}, DoUpdates: clause.AssignmentColumns([]string{"last_block"})}).
		Create(&mdb.TronScanCheckpoint{Network: mdb.NetworkTron, LastBlock: lastBlock}).Error
}

func HasFixedTronOrderHistory() (bool, error) {
	var count int64
	err := dao.Mdb.Unscoped().Model(&mdb.Orders{}).Where("network = ? AND (payment_terms IS NOT NULL OR payment_expires_at_ms <> 0)", mdb.NetworkTron).Count(&count).Error
	return count > 0, err
}

func SaveTronScanBlock(height int64, hash string) error {
	return dao.Mdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "network"}}, DoUpdates: clause.AssignmentColumns([]string{"last_block", "last_hash"})}).Create(&mdb.TronScanCheckpoint{Network: mdb.NetworkTron, LastBlock: height, LastHash: hash}).Error; err != nil {
			return err
		}
		return saveScanProgress(tx, mdb.NetworkTron, height, hash)
	})
}
func RecordScanHead(network string, head, headTime int64) error {
	return dao.Mdb.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "network"}}, DoUpdates: clause.AssignmentColumns([]string{"head", "head_at_ms", "polled_at_ms"})}).Create(&mdb.ScanHealth{Network: network, Head: head, HeadAtMs: headTime, PolledAtMs: time.Now().UnixMilli()}).Error
}
func saveScanProgress(tx *gorm.DB, network string, height int64, hash string) error {
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "network"}}, DoUpdates: clause.AssignmentColumns([]string{"scanned", "scanned_hash", "progress_at_ms"})}).Create(&mdb.ScanHealth{Network: network, Scanned: height, ScannedHash: hash, ProgressAtMs: time.Now().UnixMilli()}).Error
}
func SaveScanProgress(network string, height int64, hash string) error {
	return saveScanProgress(dao.Mdb, network, height, hash)
}
func GetScanHealth(network string) (*mdb.ScanHealth, error) {
	row := new(mdb.ScanHealth)
	err := dao.Mdb.Where("network = ?", network).Limit(1).Find(row).Error
	return row, err
}
func HasFixedOrderHistory(network string) (bool, error) {
	var n int64
	err := dao.Mdb.Unscoped().Model(&mdb.Orders{}).Where("network = ? AND (payment_terms IS NOT NULL OR payment_expires_at_ms <> 0)", network).Count(&n).Error
	return n > 0, err
}

// Scanner subscriptions include disabled/deleted historical recipients forever.
// A sales-address change must not hide late transfers to an old fixed intent.
func GetScanWallets(network string) ([]mdb.WalletAddress, error) {
	wallets, err := GetAvailableWalletAddressByNetwork(network)
	if err != nil {
		return nil, err
	}
	var addresses []string
	if err := dao.Mdb.Unscoped().Model(&mdb.Orders{}).Where("network = ? AND (payment_terms IS NOT NULL OR payment_expires_at_ms <> 0)", network).Distinct("receive_address").Pluck("receive_address", &addresses).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, w := range wallets {
		seen[w.Address] = true
	}
	for _, a := range addresses {
		if a != "" && !seen[a] {
			wallets = append(wallets, mdb.WalletAddress{Address: a, Network: network, Status: 1})
			seen[a] = true
		}
	}
	return wallets, nil
}
