package config

import (
	"encoding/json"
	"os"
	"runtime"

	"github.com/pkg/errors"
)

type CLI struct {
	File string `arg:"--config,env:SPONGIX_CONFIG_FILE" help:"Configuration file to load"`
}

type Config struct {
	Listen         string                `json:"listen"`
	LogLevel       string                `json:"log_level"`
	LogMode        string                `json:"log_mode"`
	SigningKeyFile string                `json:"signing_key_file"`
	Chunks         *Chunks               `json:"chunks"`
	Namespaces     map[string]*Namespace `json:"namespaces"`
}

func (c *Config) Prepare() error {
	c.SigningKeyFile = os.ExpandEnv(c.SigningKeyFile)

	if err := c.Chunks.Prepare(); err != nil {
		return err
	}

	for namespace, ns := range c.Namespaces {
		if err := ns.Prepare(); err != nil {
			return errors.WithMessagef(err, "while preparing namespace '%s'", namespace)
		}
	}

	return nil
}

type Namespace struct {
	Substituters      []string `json:"substituters"`
	CacheInfoPriority uint64   `json:"cache_info_priority"`
	S3                *S3      `json:"s3"`
	// StoreType selects the index storage backend: "pebble" (default) or "s3".
	StoreType string `json:"store_type"`
}

func (n *Namespace) Prepare() error {
	if n == nil {
		return errors.New("namespace configuration is missing")
	}

	if n.StoreType == "" {
		n.StoreType = "pebble"
	}

	switch n.StoreType {
	case "s3":
		if n.S3 == nil {
			return errors.Errorf("namespace S3 configuration is missing")
		}
		n.S3.CredentialsFile = os.ExpandEnv(n.S3.CredentialsFile)
	case "pebble":
		// Indices share the chunk store's Pebble DB; no per-namespace dir needed.
	default:
		return errors.Errorf("unknown store_type %q (expected \"s3\" or \"pebble\")", n.StoreType)
	}

	return nil
}

type S3 struct {
	Url             string `json:"url"`
	Region          string `json:"region"`
	Profile         string `json:"profile"`
	CredentialsFile string `json:"credentials_file"`
}

type Chunks struct {
	MinSize uint64 `json:"minimum_size"`
	AvgSize uint64 `json:"average_size"`
	MaxSize uint64 `json:"maximum_size"`
	Threads int    `json:"threads"`
	S3      *S3    `json:"s3"`
	// StoreType selects the chunk storage backend: "pebble" (default) or "s3".
	StoreType string `json:"store_type"`
	// PebbleDir is the directory for the Pebble chunk database (used when StoreType is "pebble").
	PebbleDir string `json:"pebble_dir"`
	// Pebble tuning
	PebbleWALDir             string `json:"pebble_wal_dir"`
	PebbleMemTableSize       uint64 `json:"pebble_memtable_size"`
	PebbleL0CompactionThresh int    `json:"pebble_l0_compaction_threshold"`
	// Batching
	BatchMaxBytes uint64 `json:"batch_max_bytes"` // 0 = 256MB default
}

func (c *Chunks) Prepare() error {
	if c == nil {
		return errors.New("chunks configuration is missing")
	}

	if c.AvgSize == 0 {
		c.AvgSize = 65536
	}

	if c.MinSize == 0 {
		c.MinSize = c.AvgSize / 4
	}

	if c.MaxSize == 0 {
		c.MaxSize = c.AvgSize * 4
	}

	if c.MinSize >= c.AvgSize {
		return errors.New("minimum chunk size must be smaller than average chunk size")
	}

	if c.MaxSize <= c.AvgSize {
		return errors.New("maximum chunk size must be larger than average chunk size")
	}

	if c.Threads == 0 {
		c.Threads = runtime.NumCPU()
	}

	if c.StoreType == "" {
		c.StoreType = "pebble"
	}

	switch c.StoreType {
	case "s3":
		if c.S3 == nil {
			return errors.New("chunks S3 configuration is missing")
		}
		c.S3.CredentialsFile = os.ExpandEnv(c.S3.CredentialsFile)
	case "pebble":
		if c.PebbleDir == "" {
			c.PebbleDir = "./data"
		}
		c.PebbleDir = os.ExpandEnv(c.PebbleDir)
		if c.PebbleWALDir != "" {
			c.PebbleWALDir = os.ExpandEnv(c.PebbleWALDir)
		}
		if c.BatchMaxBytes == 0 {
			c.BatchMaxBytes = 256 << 20
		}
	default:
		return errors.Errorf("unknown store_type %q (expected \"s3\" or \"pebble\")", c.StoreType)
	}

	return nil
}

func LoadBytes(input []byte) (*Config, error) {
	config := &Config{}
	return config, json.Unmarshal(input, config)
}

func LoadFile(path string) (*Config, error) {
	fd, err := os.Open(path)
	if err != nil {
		return nil, errors.WithMessagef(err, "while opening file %s", path)
	}
	defer func() { _ = fd.Close() }()

	config := &Config{}
	if err := json.NewDecoder(fd).Decode(&config); err != nil {
		return nil, errors.WithMessagef(err, "while decoding file %s", path)
	}
	return config, nil
}
