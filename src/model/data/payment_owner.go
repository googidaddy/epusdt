package data

import (
	"fmt"

	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/mdb"
)

// EnsureUniquePaymentOwner refuses reused amount/address slots, including paid,
// expired and soft-deleted history. A late transfer carries no merchant order ID.
// Never prune this history while its address is still used to collect payments.
func EnsureUniquePaymentOwner(order *mdb.Orders) error {
	count, err := PaymentSlotOrderCount(order.Network, order.ReceiveAddress, order.Token, order.ActualAmount)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("ambiguous payment ownership: matching historical orders=%d", count)
	}
	return nil
}

func PaymentSlotOrderCount(network, recipient, token string, amount float64) (int64, error) {
	var count int64
	query := dao.Mdb.Unscoped().Model(&mdb.Orders{}).
		Where("network = ? AND token = ? AND actual_amount = ?", network, token, amount).
		Where("pay_provider = ? OR pay_provider = ''", mdb.PaymentProviderOnChain)
	query = applyOrderReceiveAddressFilter(query, network, recipient)
	err := query.Count(&count).Error
	return count, err
}
