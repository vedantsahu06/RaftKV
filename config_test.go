package raftkv

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig(`C:\db`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig.Validate() = %v, want nil", err)
	}
	if cfg.WAL.SegmentSize != 64<<20 {
		t.Errorf("WAL.SegmentSize = %d, want %d", cfg.WAL.SegmentSize, 64<<20)
	}
	if cfg.WAL.MaxBatchRecords != 1_024 {
		t.Errorf("WAL.MaxBatchRecords = %d, want 1024", cfg.WAL.MaxBatchRecords)
	}
	if cfg.WAL.BatchByteTarget != 1<<20 {
		t.Errorf("WAL.BatchByteTarget = %d, want %d", cfg.WAL.BatchByteTarget, 1<<20)
	}
	if cfg.WAL.QueueCapacity != 4_096 {
		t.Errorf("WAL.QueueCapacity = %d, want 4096", cfg.WAL.QueueCapacity)
	}
	if cfg.WAL.GroupCommitInterval != 200*time.Microsecond {
		t.Errorf("WAL.GroupCommitInterval = %v, want 200us", cfg.WAL.GroupCommitInterval)
	}
	if cfg.Memtable.SwitchThreshold >= cfg.Memtable.HardLimit {
		t.Errorf("Memtable.SwitchThreshold = %d must be below HardLimit %d", cfg.Memtable.SwitchThreshold, cfg.Memtable.HardLimit)
	}
}

func TestConfigZeroValuesUseDefaults(t *testing.T) {
	cfg := Config{Dir: `C:\db`}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Config{Dir}.Validate() = %v, want nil", err)
	}
	if got, want := cfg.WithDefaults(), DefaultConfig(`C:\db`); got != want {
		t.Errorf("Config{Dir}.WithDefaults() = %+v, want %+v", got, want)
	}
}

func TestConfigValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{
			name:   "empty directory",
			mutate: func(c *Config) { c.Dir = "" },
			field:  "Dir",
		},
		{
			name:   "negative segment size",
			mutate: func(c *Config) { c.WAL.SegmentSize = -1 },
			field:  "WAL.SegmentSize",
		},
		{
			name:   "negative batch records",
			mutate: func(c *Config) { c.WAL.MaxBatchRecords = -1 },
			field:  "WAL.MaxBatchRecords",
		},
		{
			name:   "negative batch byte target",
			mutate: func(c *Config) { c.WAL.BatchByteTarget = -1 },
			field:  "WAL.BatchByteTarget",
		},
		{
			name:   "negative queue capacity",
			mutate: func(c *Config) { c.WAL.QueueCapacity = -1 },
			field:  "WAL.QueueCapacity",
		},
		{
			name:   "negative collection interval",
			mutate: func(c *Config) { c.WAL.GroupCommitInterval = -1 },
			field:  "WAL.GroupCommitInterval",
		},
		{
			name:   "negative memtable hard limit",
			mutate: func(c *Config) { c.Memtable.HardLimit = -1 },
			field:  "Memtable.HardLimit",
		},
		{
			name:   "negative switch threshold",
			mutate: func(c *Config) { c.Memtable.SwitchThreshold = -1 },
			field:  "Memtable.SwitchThreshold",
		},
		{
			name: "switch threshold equal to hard limit",
			mutate: func(c *Config) {
				c.Memtable.HardLimit = 10 << 20
				c.Memtable.SwitchThreshold = 10 << 20
			},
			field: "Memtable.SwitchThreshold",
		},
		{
			name: "switch threshold above hard limit",
			mutate: func(c *Config) {
				c.Memtable.HardLimit = 10 << 20
				c.Memtable.SwitchThreshold = 11 << 20
			},
			field: "Memtable.SwitchThreshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig(`C:\db`)
			tt.mutate(&cfg)
			err := cfg.Validate()
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("Validate() = %v, want ErrInvalidArgument", err)
			}
			var configErr *configError
			if !errors.As(err, &configErr) {
				t.Fatalf("Validate() error = %T, want *configError", err)
			}
			if configErr.field != tt.field {
				t.Errorf("invalid field = %q, want %q", configErr.field, tt.field)
			}
		})
	}
}
