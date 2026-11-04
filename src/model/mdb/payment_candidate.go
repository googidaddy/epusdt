package mdb

// PaymentCandidate is an untrusted scanner observation, never a payment proof.
// Economic fields are append-only. Acknowledgement means merchant receipt only.
// Do not soft/hard-delete these records as part of log or runtime cleanup.
type PaymentCandidate struct {
	ID               uint64  `gorm:"primaryKey" json:"id,string"`
	ObservationKey   string  `gorm:"size:64;uniqueIndex" json:"observation_key"`
	APIKeyID         uint64  `gorm:"index:payment_candidate_delivery;not null" json:"-"`
	TradeID          string  `gorm:"size:64;index;not null" json:"trade_id"`
	OrderID          string  `gorm:"size:64;not null" json:"order_id"`
	Network          string  `gorm:"size:32;not null" json:"network"`
	Kind             string  `gorm:"size:32;not null" json:"kind"`
	TransactionHash  string  `gorm:"size:66;not null" json:"transaction_hash"`
	EventIndex       *uint64 `json:"event_index"` // nil for calldata; receipt event not identified yet.
	BlockHash        string  `gorm:"size:66;not null" json:"block_hash"`
	BlockNumber      int64   `json:"block_number"`
	BlockTimeMs      int64   `json:"block_time_ms"`
	Contract         string  `gorm:"size:64;not null" json:"contract"`
	Recipient        string  `gorm:"size:64;not null" json:"recipient"`
	RawAmount        string  `gorm:"size:80;not null" json:"raw_amount"`
	Removed          bool    `json:"removed"`
	ReviewReason     string  `gorm:"size:32;not null" json:"review_reason"`
	ObservedAtMs     int64   `json:"observed_at_ms"`
	AcknowledgedAtMs int64   `gorm:"default:0;index:payment_candidate_delivery" json:"-"`
}

func (PaymentCandidate) TableName() string { return "payment_candidates" }

// TronScanCheckpoint advances only after all candidate writes for a block.
// Kept in the primary DB beside candidates, not in an ephemeral runtime cache.
type TronScanCheckpoint struct {
	Network   string `gorm:"size:32;primaryKey"`
	LastBlock int64
	LastHash  string `gorm:"size:66"`
}

func (TronScanCheckpoint) TableName() string { return "tron_scan_checkpoints" }

// ScanHealth is a primary-DB progress record, independent of RPC success counters.
// HeadAtMs measures fresh chain time; PolledAtMs alone cannot prove liveness.
type ScanHealth struct {
	Network      string `gorm:"size:32;primaryKey" json:"network"`
	Head         int64  `json:"head"`
	HeadAtMs     int64  `json:"head_at_ms"`
	PolledAtMs   int64  `json:"polled_at_ms"`
	Scanned      int64  `json:"scanned"`
	ScannedHash  string `gorm:"size:66" json:"-"`
	ProgressAtMs int64  `json:"progress_at_ms"`
}

func (ScanHealth) TableName() string { return "payment_scan_health" }
