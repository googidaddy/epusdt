package task

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/model/service"
	"github.com/GMWalletApp/epusdt/util/log"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	evmBackfillInitialLookbackBlocks = 2048
	evmBackfillBatchBlocks           = 1000
	evmBackfillRetryDelay            = 3 * time.Second
	evmBackfillCatchupDelay          = 250 * time.Millisecond
	evmBackfillHeaderTimeout         = 10 * time.Second
	evmBackfillQueryTimeout          = 30 * time.Second
	evmPartialBatchMaxWait           = 5 * time.Second
	evmPartialBatchRecheck           = 2 * time.Second
)

type evmRecipientStoreFunc func([]mdb.WalletAddress) int
type evmRecipientCheckerFunc func(common.Address) bool

func StartEthereumBackfillScannerListener() {
	startEvmBackfillScanner(mdb.NetworkEthereum, "[ETH-BACKFILL]", StoreEthRecipientsFromWallets, isWatchedEthRecipient)
}

func StartBscBackfillScannerListener() {
	startEvmBackfillScanner(mdb.NetworkBsc, "[BSC-BACKFILL]", storeBscRecipientsFromWallets, isWatchedBscRecipient)
}

func StartPolygonBackfillScannerListener() {
	startEvmBackfillScanner(mdb.NetworkPolygon, "[POLYGON-BACKFILL]", storePolygonRecipientsFromWallets, isWatchedPolygonRecipient)
}

func StartPlasmaBackfillScannerListener() {
	startEvmBackfillScanner(mdb.NetworkPlasma, "[PLASMA-BACKFILL]", storePlasmaRecipientsFromWallets, isWatchedPlasmaRecipient)
}

func startEvmBackfillScanner(network, logPrefix string, storeRecipients evmRecipientStoreFunc, isWatchedRecipient evmRecipientCheckerFunc) {
	for {
		if data.IsChainEnabled(network) {
			if contracts := loadChainTokenContracts(network, logPrefix); len(contracts) > 0 {
				runEvmBackfillScanner(network, logPrefix, contracts, storeRecipients, isWatchedRecipient)
			}
		}
		time.Sleep(10 * time.Second)
	}
}

func runEvmBackfillScanner(network, logPrefix string, contracts []common.Address, storeRecipients evmRecipientStoreFunc, isWatchedRecipient evmRecipientCheckerFunc) {
	ctx, cancel := chainEnabledWatchdog(network, logPrefix, chainTokenFingerprint(network))
	defer cancel()

	wallets, err := data.GetScanWallets(network)
	if err != nil {
		log.Sugar.Errorf("%s failed to get wallet addresses: %v", logPrefix, err)
		return
	}
	if len(evmRecipientTopicsFromWallets(wallets)) == 0 {
		log.Sugar.Warnf("%s no enabled wallet addresses, scanner idle", logPrefix)
		return
	}
	storeRecipients(wallets)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w, err := data.GetScanWallets(network)
				if err != nil {
					log.Sugar.Warnf("%s refresh wallet addresses: %v", logPrefix, err)
					continue
				}
				storeRecipients(w)
			}
		}
	}()

	node, ok := resolveChainHttpNode(network, logPrefix)
	nodeKind := "HTTP"
	if !ok {
		node, ok = resolveChainWsNode(network, logPrefix)
		nodeKind = "WSS"
	}
	if !ok {
		return
	}
	log.Sugar.Infof("%s connecting using %s node %s watching %d contract(s)", logPrefix, nodeKind, data.RpcNodeLogLabel(node), len(contracts))

	query := ethereum.FilterQuery{
		Addresses: contracts,
	}

	failWait := 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		dialCtx, cancelDial := context.WithTimeout(ctx, evmNodeDialTimeout)
		client, closeClient, err := dialBackfillEVM(dialCtx, node.Url, network)
		cancelDial()
		if err != nil {
			log.Sugar.Warnf("%s dial: %v, retry in %s", logPrefix, safeRPCError(err), failWait)
			if recordEvmNodeFailure(logPrefix, network, node, "dial") {
				return
			}
			if !sleepOrDone(ctx, failWait) {
				return
			}
			failWait = nextBackoff(failWait, 60*time.Second)
			continue
		}

		err = runEvmBackfillLoop(ctx, client, network, logPrefix, node, query, isWatchedRecipient)
		closeClient()

		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Sugar.Warnf("%s backfill loop stopped: %v, retry in %s", logPrefix, safeRPCError(err), failWait)
			if recordEvmNodeFailure(logPrefix, network, node, safeRPCError(err).Error()) {
				return
			}
			if !sleepOrDone(ctx, failWait) {
				return
			}
			failWait = nextBackoff(failWait, 60*time.Second)
			continue
		}

		failWait = 2 * time.Second
		if !sleepOrDone(ctx, 3*time.Second) {
			return
		}
	}
}

func runEvmBackfillLoop(ctx context.Context, client evmBackfillRPC, network, logPrefix string, node mdb.RpcNode, baseQuery ethereum.FilterQuery, isWatchedRecipient evmRecipientCheckerFunc) error {
	lastBlock, initialized, err := loadEvmScanCursor(network)
	if err != nil {
		return err
	}

	var partialDeadline time.Time
	for {
		if ctx.Err() != nil {
			return nil
		}

		chain, interval := evmBackfillChainConfig(network)
		if chain == nil || !chain.Enabled {
			return nil
		}

		latest, err := latestEvmHeader(ctx, client, network, logPrefix)
		if err != nil {
			return err
		}
		if latest == nil {
			return nil
		}
		if latest.Number == nil || !latest.Number.IsInt64() {
			return fmt.Errorf("latest block number exceeds int64 range")
		}

		confirmedHead := confirmedEvmHead(latest.Number.Int64(), chain.MinConfirmations)
		if network == mdb.NetworkBsc {
			// BSC discovery follows finalized blocks; WSS observations remain hints.
			rpcCtx, cancel := context.WithTimeout(ctx, evmBackfillHeaderTimeout)
			chainID, e := client.ChainID(rpcCtx)
			if e != nil || chainID.Cmp(big.NewInt(56)) != 0 {
				cancel()
				return fmt.Errorf("BSC chain identity unavailable")
			}
			final, e := client.HeaderByNumber(rpcCtx, big.NewInt(int64(rpc.FinalizedBlockNumber)))
			cancel()
			if e != nil || final == nil || final.Number == nil || !final.Number.IsInt64() || final.Number.Sign() <= 0 || final.Number.Cmp(latest.Number) > 0 {
				return fmt.Errorf("BSC finalized head unavailable")
			}
			confirmedHead = final.Number.Int64()
			if err := data.RecordScanHead(network, confirmedHead, int64(final.Time)*1000); err != nil {
				return err
			}
			state, e := data.GetScanHealth(network)
			if e != nil {
				return e
			}
			if initialized && state.ScannedHash != "" {
				previous, e := latestEvmBlockHeader(ctx, client, uint64(lastBlock))
				if e != nil || previous == nil || previous.Hash().Hex() != state.ScannedHash {
					return fmt.Errorf("finalized checkpoint changed; reconciliation required")
				}
			}
			if initialized && lastBlock > confirmedHead {
				return fmt.Errorf("checkpoint ahead of finalized head")
			}
		}
		if !initialized {
			if network == mdb.NetworkBsc {
				history, e := data.HasFixedOrderHistory(network)
				if e != nil {
					return e
				}
				if history {
					return fmt.Errorf("BSC checkpoint missing with fixed history; explicit replay required")
				}
			}
			lastBlock = confirmedHead - evmBackfillInitialLookbackBlocks
			if lastBlock < 0 {
				lastBlock = 0
			}
			if err := saveEvmProgress(ctx, client, network, lastBlock); err != nil {
				return fmt.Errorf("initialize backfill cursor: %w", err)
			}
			initialized = true
			log.Sugar.Infof("%s initialized backfill cursor at block=%d confirmed_head=%d lookback=%d", logPrefix, lastBlock, confirmedHead, evmBackfillInitialLookbackBlocks)
		}

		if lastBlock >= confirmedHead {
			partialDeadline = time.Time{}
			if !sleepOrDone(ctx, interval) {
				return nil
			}
			continue
		}

		// Let a partial BSC range fill briefly instead of paying for many tiny
		// getLogs calls. The deadline is fixed for this range, so a stalled or
		// slow chain still flushes it; no block is skipped or marked scanned.
		if network == mdb.NetworkBsc && confirmedHead-lastBlock < evmBackfillBatchSize(network) {
			if partialDeadline.IsZero() {
				partialDeadline = time.Now().Add(min(interval, evmPartialBatchMaxWait))
			}
			if remaining := time.Until(partialDeadline); remaining > 0 {
				if !sleepOrDone(ctx, min(remaining, evmPartialBatchRecheck)) {
					return nil
				}
				continue
			}
		}
		partialDeadline = time.Time{}

		fromBlock := lastBlock + 1
		toBlock := fromBlock + evmBackfillBatchSize(network) - 1
		if toBlock > confirmedHead {
			toBlock = confirmedHead
		}

		rpcCtx, cancel := context.WithTimeout(ctx, evmBackfillQueryTimeout)
		recipientTopics := loadEvmRecipientTopics(network, logPrefix)
		if len(recipientTopics) == 0 {
			cancel()
			if !sleepOrDone(ctx, interval) {
				return nil
			}
			continue
		}
		batchQuery := baseQuery
		batchQuery.FromBlock = big.NewInt(fromBlock)
		batchQuery.ToBlock = big.NewInt(toBlock)
		batchQuery.Topics = evmTransferTopics(recipientTopics)
		logs, err := client.FilterLogs(rpcCtx, batchQuery)
		cancel()
		if err != nil {
			return fmt.Errorf("filter logs range=%d-%d: %w", fromBlock, toBlock, safeRPCError(err))
		}

		if err := processEvmBackfillLogs(ctx, client, network, logPrefix, logs, isWatchedRecipient); err != nil {
			return err
		}
		if err := saveEvmProgress(ctx, client, network, toBlock); err != nil {
			return fmt.Errorf("save backfill cursor range=%d-%d: %w", fromBlock, toBlock, safeRPCError(err))
		}
		lastBlock = toBlock

		data.RecordRpcSuccess(network)
		data.RecordRpcNodeSuccess(node.ID)

		if toBlock < confirmedHead {
			if !sleepOrDone(ctx, evmBackfillCatchupDelay) {
				return nil
			}
			continue
		}
		if !sleepOrDone(ctx, interval) {
			return nil
		}
	}
}

func processEvmBackfillLogs(ctx context.Context, client evmBackfillRPC, network, logPrefix string, logs []types.Log, isWatchedRecipient evmRecipientCheckerFunc) error {
	headerCache := make(map[uint64]*types.Header)
	for _, vLog := range logs {
		if len(vLog.Topics) < 3 {
			continue
		}
		if vLog.Topics[0] != transferEventHash {
			continue
		}

		toAddr := common.HexToAddress(vLog.Topics[2].Hex())
		if !isWatchedRecipient(toAddr) {
			continue
		}

		header, ok := headerCache[vLog.BlockNumber]
		if !ok {
			var err error
			header, err = latestEvmBlockHeader(ctx, client, vLog.BlockNumber)
			if err != nil {
				return fmt.Errorf("fetch block header network=%s block=%d: %w", network, vLog.BlockNumber, err)
			}
			if header == nil || header.Time == 0 {
				return fmt.Errorf("missing block header timestamp network=%s block=%d", network, vLog.BlockNumber)
			}
			headerCache[vLog.BlockNumber] = header
		}

		if header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != vLog.BlockNumber || header.Hash() != vLog.BlockHash {
			return fmt.Errorf("log block is no longer canonical")
		}
		blockTsMs := int64(header.Time) * 1000
		handled, err := service.RecordFixedEVMLog(network, vLog, blockTsMs)
		if err != nil {
			return fmt.Errorf("persist candidate network=%s: %w", network, err)
		}
		if handled || vLog.Removed {
			continue
		}
		service.TryProcessEvmERC20Transfer(network, vLog.Address, toAddr, new(big.Int).SetBytes(vLog.Data), vLog.TxHash.Hex(), blockTsMs)
	}
	return nil
}

func loadEvmScanCursor(network string) (int64, bool, error) {
	if network == mdb.NetworkBsc {
		row, err := data.GetScanHealth(network)
		if err != nil {
			return 0, false, err
		}
		return row.Scanned, row.ScannedHash != "", nil
	}
	row, err := data.GetEvmScanCursor(network)
	if err != nil {
		return 0, false, err
	}
	if row == nil || row.ID == 0 || row.LastBlock <= 0 {
		return 0, false, nil
	}
	return row.LastBlock, true, nil
}

func evmBackfillChainConfig(network string) (*mdb.Chain, time.Duration) {
	chain, err := data.GetChainByNetwork(network)
	if err != nil || chain == nil {
		return nil, 5 * time.Second
	}
	interval := time.Duration(chain.ScanIntervalSec) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return chain, interval
}

func evmBackfillBatchSize(network string) int64 {
	if network == mdb.NetworkBsc {
		return 10 // Compatible with the selected Alchemy free eth_getLogs range.
	}
	return evmBackfillBatchBlocks
}

func confirmedEvmHead(head int64, minConfirmations int) int64 {
	if minConfirmations <= 1 {
		return head
	}
	conf := int64(minConfirmations)
	if head < conf-1 {
		return 0
	}
	return head - conf + 1
}

func latestEvmHeader(ctx context.Context, client evmBackfillRPC, network, logPrefix string) (*types.Header, error) {
	headerCtx, cancel := context.WithTimeout(ctx, evmBackfillHeaderTimeout)
	defer cancel()

	header, err := client.HeaderByNumber(headerCtx, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil
		}
		return nil, fmt.Errorf("latest header: %w", safeRPCError(err))
	}
	data.RecordRpcSuccess(network)
	if header != nil {
		if header.Number != nil && header.Number.IsInt64() {
			data.RecordRpcBlockHeight(network, header.Number.Int64())
			log.Sugar.Debugf("%s latest confirmed block=%s", logPrefix, header.Number.String())
		}
	}
	return header, nil
}

func latestEvmBlockHeader(ctx context.Context, client evmBackfillRPC, blockNumber uint64) (*types.Header, error) {
	headerCtx, cancel := context.WithTimeout(ctx, evmBackfillHeaderTimeout)
	defer cancel()
	header, err := client.HeaderByNumber(headerCtx, big.NewInt(int64(blockNumber)))
	return header, safeRPCError(err)
}

func saveEvmProgress(ctx context.Context, client evmBackfillRPC, network string, height int64) error {
	if network != mdb.NetworkBsc {
		return data.UpsertEvmScanCursor(network, height)
	}
	header, err := latestEvmBlockHeader(ctx, client, uint64(height))
	if err != nil || header == nil || header.Number == nil || header.Number.Int64() != height {
		return fmt.Errorf("checkpoint header unavailable")
	}
	return data.SaveScanProgress(network, height, header.Hash().Hex())
}
