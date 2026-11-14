package raftkv

import (
	"fmt"
	"time"

	"github.com/vedantsahu06/raftkv/internal/kverr"
)

// Final initial WAL defaults from D-066. These are configurable starting values,
// not performance guarantees.
const (
	// DefaultWALSegmentSize is the default maximum WAL segment size in bytes.
	DefaultWALSegmentSize int64 = 64 << 20

	// DefaultWALMaxBatchRecords is the default maximum number of records in one
	// group-commit batch.
	DefaultWALMaxBatchRecords = 1_024

	// DefaultWALBatchByteTarget is the default group-commit collection target in
	// bytes. It does not cap an individual record.
	DefaultWALBatchByteTarget int64 = 1 << 20

	// DefaultWALQueueCapacity is the default bounded WAL request queue size.
	DefaultWALQueueCapacity = 4_096

	// DefaultWALGroupCommitInterval is the default group-commit collection
	// interval.
	DefaultWALGroupCommitInterval = 200 * time.Microsecond
)

// Provisional Memtable defaults. AQ-004 requires estimator calibration before
// these values can be treated as final. The switching threshold is strictly
// below the hard limit.
const (
	// DefaultMemtableHardLimit is the provisional Memtable hard maximum in
	// bytes.
	DefaultMemtableHardLimit int64 = 100 << 20

	// DefaultMemtableSwitchThreshold is the provisional pre-admission
	// generation-switch threshold in bytes.
	DefaultMemtableSwitchThreshold int64 = 98 << 20
)

// Config configures a single-node Engine.
//
// Dir is required. Zero-valued numeric and duration fields are replaced by the
// package defaults when the configuration is validated or passed to Open.
type Config struct {
	// Dir is the database directory. Open resolves it to an absolute path.
	Dir string

	// WAL configures WAL group commit and segment sizing.
	WAL WALConfig

	// Memtable configures Memtable limits and automatic switching.
	Memtable MemtableConfig
}

// WALConfig configures WAL operational parameters.
type WALConfig struct {
	// SegmentSize is the maximum WAL segment size in bytes.
	SegmentSize int64

	// MaxBatchRecords is the maximum number of records in one group-commit
	// batch.
	MaxBatchRecords int

	// BatchByteTarget is the group-commit collection byte target. A single
	// larger record forms its own batch up to the format limit.
	BatchByteTarget int64

	// QueueCapacity is the bounded WAL request queue capacity.
	QueueCapacity int

	// GroupCommitInterval is the group-commit collection interval.
	GroupCommitInterval time.Duration
}

// MemtableConfig configures Memtable capacity and switching.
type MemtableConfig struct {
	// HardLimit is the Memtable hard maximum in bytes. It is never exceeded.
	HardLimit int64

	// SwitchThreshold is the pre-admission generation-switch threshold in
	// bytes. It must be strictly below HardLimit.
	SwitchThreshold int64
}

// DefaultConfig returns a configuration with every field set to its default.
// The caller must supply a non-empty database directory.
func DefaultConfig(dir string) Config {
	return Config{
		Dir: dir,
		WAL: WALConfig{
			SegmentSize:         DefaultWALSegmentSize,
			MaxBatchRecords:     DefaultWALMaxBatchRecords,
			BatchByteTarget:     DefaultWALBatchByteTarget,
			QueueCapacity:       DefaultWALQueueCapacity,
			GroupCommitInterval: DefaultWALGroupCommitInterval,
		},
		Memtable: MemtableConfig{
			HardLimit:       DefaultMemtableHardLimit,
			SwitchThreshold: DefaultMemtableSwitchThreshold,
		},
	}
}

// WithDefaults returns a copy of c with zero-valued settings replaced by the
// package defaults. It does not validate the result.
func (c Config) WithDefaults() Config {
	if c.WAL.SegmentSize == 0 {
		c.WAL.SegmentSize = DefaultWALSegmentSize
	}
	if c.WAL.MaxBatchRecords == 0 {
		c.WAL.MaxBatchRecords = DefaultWALMaxBatchRecords
	}
	if c.WAL.BatchByteTarget == 0 {
		c.WAL.BatchByteTarget = DefaultWALBatchByteTarget
	}
	if c.WAL.QueueCapacity == 0 {
		c.WAL.QueueCapacity = DefaultWALQueueCapacity
	}
	if c.WAL.GroupCommitInterval == 0 {
		c.WAL.GroupCommitInterval = DefaultWALGroupCommitInterval
	}
	if c.Memtable.HardLimit == 0 {
		c.Memtable.HardLimit = DefaultMemtableHardLimit
	}
	if c.Memtable.SwitchThreshold == 0 {
		c.Memtable.SwitchThreshold = DefaultMemtableSwitchThreshold
	}
	return c
}

// Validate applies defaults to a copy of c and validates the result.
func (c Config) Validate() error {
	c = c.WithDefaults()
	if c.Dir == "" {
		return invalidConfig("Dir", "must not be empty")
	}
	if c.WAL.SegmentSize <= 0 {
		return invalidConfig("WAL.SegmentSize", "must be positive")
	}
	if c.WAL.MaxBatchRecords <= 0 {
		return invalidConfig("WAL.MaxBatchRecords", "must be positive")
	}
	if c.WAL.BatchByteTarget <= 0 {
		return invalidConfig("WAL.BatchByteTarget", "must be positive")
	}
	if c.WAL.QueueCapacity <= 0 {
		return invalidConfig("WAL.QueueCapacity", "must be positive")
	}
	if c.WAL.GroupCommitInterval <= 0 {
		return invalidConfig("WAL.GroupCommitInterval", "must be positive")
	}
	if c.Memtable.HardLimit <= 0 {
		return invalidConfig("Memtable.HardLimit", "must be positive")
	}
	if c.Memtable.SwitchThreshold <= 0 {
		return invalidConfig("Memtable.SwitchThreshold", "must be positive")
	}
	if c.Memtable.SwitchThreshold >= c.Memtable.HardLimit {
		return invalidConfig("Memtable.SwitchThreshold", "must be strictly below Memtable.HardLimit")
	}
	return nil
}

type configError struct {
	field  string
	reason string
}

func (e *configError) Error() string {
	return fmt.Sprintf("raftkv: invalid config: %s %s", e.field, e.reason)
}

func (e *configError) Unwrap() error {
	return kverr.ErrInvalidArgument
}

func invalidConfig(field, reason string) error {
	return &configError{field: field, reason: reason}
}
