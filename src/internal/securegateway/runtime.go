package securegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GMWalletApp/epusdt/config"
	"github.com/GMWalletApp/epusdt/model/dao"
	"github.com/GMWalletApp/epusdt/model/data"
	"github.com/GMWalletApp/epusdt/model/mdb"
	"github.com/GMWalletApp/epusdt/task"
	applog "github.com/GMWalletApp/epusdt/util/log"
	"github.com/shopspring/decimal"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// InitStorage is an explicit, offline operation. Serve never creates missing
// mount markers or a missing database, so an empty/missing bind mount fails closed.
func InitStorage(c Config) error {
	for _, dir := range []string{c.DataDir, c.RuntimeDir, c.SecurityDir} {
		stat, err := os.Stat(dir)
		if err != nil || !stat.IsDir() {
			return errors.New("create and mount storage directories before initialization")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			return errors.New("initialization requires empty dedicated directories")
		}
	}
	for _, dir := range []string{c.DataDir, c.RuntimeDir, c.SecurityDir} {
		if err := os.WriteFile(filepath.Join(dir, ".epusdt-storage"), []byte("secure-gateway-v1\n"), 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(c.SecurityDir, "revocations.jsonl"), nil, 0600); err != nil {
		return err
	}
	return nil
}
func Open(c Config, initializing bool) (func(), error) {
	for _, dir := range []string{c.DataDir, c.RuntimeDir, c.SecurityDir} {
		b, err := os.ReadFile(filepath.Join(dir, ".epusdt-storage"))
		if err != nil || string(b) != "secure-gateway-v1\n" {
			return nil, errors.New("storage marker missing; refusing implicit initialization")
		}
	}
	if !initializing {
		for _, path := range []string{filepath.Join(c.DataDir, "epusdt.db"), filepath.Join(c.RuntimeDir, "epusdt-runtime.db")} {
			if stat, err := os.Stat(path); err != nil || !stat.Mode().IsRegular() {
				return nil, errors.New("persistent database missing")
			}
		}
	}
	viper.Set("db_type", "sqlite")
	viper.Set("sqlite_database_filename", filepath.Join(c.DataDir, "epusdt.db"))
	viper.Set("runtime_sqlite_filename", filepath.Join(c.RuntimeDir, "epusdt-runtime.db"))
	viper.Set("queue_concurrency", 2)
	viper.Set("order_expiration_time", 15)
	viper.Set("app_uri", "https://"+c.PublicHost)
	config.RuntimePath = c.RuntimeDir
	config.SQLDebug = false
	config.HTTPAccessLog = false
	zapLogger, err := zap.NewProduction()
	if err != nil {
		return nil, err
	}
	applog.Sugar = zapLogger.Sugar()
	if err := dao.DBInit(); err != nil {
		return nil, errors.New("primary database initialization failed")
	}
	dao.Mdb.Logger = logger.Default.LogMode(logger.Silent)
	if err := dao.RuntimeInit(); err != nil {
		return nil, errors.New("runtime database initialization failed")
	}
	close := func() {
		for _, db := range []*gorm.DB{dao.RuntimeDB, dao.Mdb} {
			if db != nil {
				raw, err := db.DB()
				if err == nil {
					_ = raw.Close()
				}
			}
		}
		_ = zapLogger.Sync()
	}
	if err := dao.Mdb.AutoMigrate(&nonceRecord{}); err != nil {
		close()
		return nil, err
	}
	return close, nil
}
func SeedPolicy(c Config) error {
	rate, _ := decimal.NewFromString(c.QuoteRate)
	rates, _ := json.Marshal(map[string]map[string]float64{"cny": {"usdt": rate.InexactFloat64()}})
	// Runtime rate settings never consult the mutable upstream settings table.
	config.SettingsGetString = func(key string) string {
		switch key {
		case "rate.mode":
			return "fixed"
		case "rate.forced_rate_list":
			return string(rates)
		}
		return ""
	}
	err := dao.Mdb.Transaction(func(tx *gorm.DB) error {
		// Match the merchant's reviewed four-decimal quote and 0.0001 tail step.
		// Upstream defaults to two decimals, which would reject most fixed quotes.
		precision := mdb.Setting{Group: mdb.SettingGroupSystem, Key: mdb.SettingKeyAmountPrecision, Value: "4", Type: mdb.SettingTypeInt}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&precision).Error; err != nil {
			return err
		}
		if err := tx.Model(&mdb.Chain{}).Where("1 = 1").Update("enabled", false).Error; err != nil {
			return err
		}
		if err := tx.Model(&mdb.ChainToken{}).Where("1 = 1").Update("enabled", false).Error; err != nil {
			return err
		}
		if err := tx.Model(&mdb.RpcNode{}).Where("1 = 1").Update("enabled", false).Error; err != nil {
			return err
		}
		if err := tx.Model(&mdb.WalletAddress{}).Where("1 = 1").Update("status", 0).Error; err != nil {
			return err
		}
		if err := tx.Model(&mdb.ApiKey{}).Where("1 = 1").Update("status", 0).Error; err != nil {
			return err
		}
		for _, network := range []string{mdb.NetworkTron, mdb.NetworkBsc} {
			if err := tx.Model(&mdb.Chain{}).Where("network = ?", network).Updates(map[string]interface{}{"enabled": true, "min_confirmations": 20, "scan_interval_sec": 5}).Error; err != nil {
				return err
			}
			contract, decimals, address := "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", 6, c.TronRecipient
			if network == mdb.NetworkBsc {
				contract, decimals, address = "0x55d398326f99059ff775485246999027b3197955", 18, strings.ToLower(c.BscRecipient)
			}
			if err := tx.Model(&mdb.ChainToken{}).Where("network = ? AND symbol = ?", network, "USDT").Updates(map[string]interface{}{"enabled": true, "contract_address": contract, "decimals": decimals}).Error; err != nil {
				return err
			}
			var wallet mdb.WalletAddress
			if err := tx.Where("network = ? AND address = ?", network, address).Limit(1).Find(&wallet).Error; err != nil {
				return err
			}
			if wallet.ID == 0 {
				wallet = mdb.WalletAddress{Network: network, Address: address, Status: 1}
				if err := tx.Create(&wallet).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Model(&wallet).Update("status", 1).Error; err != nil {
					return err
				}
			}
		}
		// Primary endpoint first so it keeps its API key and wins selection while
		// healthy; reviewed fallbacks follow and are only used after the gateway
		// marks the primary as failing. Fallbacks carry no API key: their
		// credentials must be embedded in the URL (e.g. a provider path token).
		nodes := make([]mdb.RpcNode, 0, len(c.TronFallbacks)+len(c.BscFallbacks)+3)
		for i, tronURL := range c.TronURLs() {
			node := mdb.RpcNode{Network: mdb.NetworkTron, Url: tronURL, Type: mdb.RpcNodeTypeHttp}
			if i == 0 {
				node.ApiKey = c.TronAPIKey
			}
			nodes = append(nodes, node)
		}
		for _, bscURL := range c.BscHTTPSURLs() {
			nodes = append(nodes, mdb.RpcNode{Network: mdb.NetworkBsc, Url: bscURL, Type: mdb.RpcNodeTypeHttp})
		}
		nodes = append(nodes, mdb.RpcNode{Network: mdb.NetworkBsc, Url: c.BscWSS, Type: mdb.RpcNodeTypeWs})
		for _, node := range nodes {
			var existing mdb.RpcNode
			if tx.Where("network = ? AND url = ?", node.Network, node.Url).Limit(1).Find(&existing).Error != nil {
				return errors.New("RPC policy read failed")
			}
			node.ID = existing.ID
			node.Enabled = true
			node.Weight = 1
			node.Purpose = mdb.RpcNodePurposeGeneral
			node.Status = mdb.RpcNodeStatusUnknown
			if tx.Save(&node).Error != nil {
				return errors.New("RPC policy write failed")
			}
		}
		var key mdb.ApiKey
		if tx.Where("pid = ?", c.PID).Limit(1).Find(&key).Error != nil {
			return errors.New("merchant policy unavailable")
		}
		key.Pid = c.PID
		key.SecretKey = c.MerchantSecret
		key.Name = "dedicated-merchant"
		key.Status = mdb.ApiKeyStatusEnable
		key.IpWhitelist = ""
		if tx.Save(&key).Error != nil {
			return errors.New("merchant policy write failed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return data.ReloadSettings()
}
func Lock(c Config) (func(), error) {
	marker, err := os.ReadFile(filepath.Join(c.RuntimeDir, ".epusdt-storage"))
	if err != nil || string(marker) != "secure-gateway-v1\n" {
		return nil, errors.New("storage marker missing; refusing implicit initialization")
	}

	file, err := os.OpenFile(filepath.Join(c.RuntimeDir, "process.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another secure gateway process holds the runtime lock")
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
}

func Serve(ctx context.Context, c Config) error {
	if err := SeedPolicy(c); err != nil {
		return err
	}
	server, err := New(dao.Mdb, c)
	if err != nil {
		return err
	}
	if err := task.ConfigureSecureRPC(c.TronURLs(), c.BscHTTPSURLs(), c.BscWSS, c.BscHeaderHTTP); err != nil {
		return err
	}
	go task.StartTronBlockScannerListener()
	go task.StartBscWebSocketListener()
	go task.StartBscBackfillScannerListener()
	// Only replay nonces expire. Payment/evidence/allocation history is never pruned.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = dao.Mdb.Where("at < ?", time.Now().Add(-5*time.Minute)).Delete(&nonceRecord{}).Error
			}
		}
	}()
	e := server.Handler()
	httpServer := &http.Server{Addr: c.Listen, Handler: e, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	result := make(chan error, 1)
	go func() { result <- httpServer.ListenAndServe() }()
	select {
	case err := <-result:
		return fmt.Errorf("gateway listener stopped: %w", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}
