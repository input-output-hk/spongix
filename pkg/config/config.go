package config

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

type Config struct {
	Dir              string               `json:"dir" arg:"--dir,env:CACHE_DIR" help:"directory for the cache"`
	Listen           string               `json:"listen" arg:"--listen,env:LISTEN_ADDR" help:"Listen on this address"`
	LogLevel         string               `json:"log_level" arg:"--log-level,env:LOG_LEVEL" help:"One of debug, info, warn, error, dpanic, panic, fatal"`
	LogMode          string               `json:"log_mode" arg:"--log-mode,env:LOG_MODE" help:"development or production"`
	AverageChunkSize uint64               `json:"average_chunk_size" arg:"--average-chunk-size,env:AVERAGE_CHUNK_SIZE" help:"Chunk size will be between /4 and *4 of this value"`
	S3BucketUrl      string               `json:"s3_bucket_url"`
	S3BucketRegion   string               `json:"s3_bucket_region"`
	Namespaces       map[string]Namespace `json:"namespaces"`
}

type Namespace struct {
	Substituters      []string `json:"substituters"`
	SecretKeyFile     string   `json:"secret_key_file"`
	TrustedPublicKeys []string `json:"trusted_public_keys"`
	CacheInfoPriority uint64   `json:"cache_info_priority"`
}

func LoadBytes(input []byte) (*Config, error) {
	config := &Config{}
	return config, json.Unmarshal(input, config)
}

func LoadFile(path string) (*Config, error) {
	if fd, err := os.Open(path); err != nil {
		return nil, errors.WithMessagef(err, "while opening file %s", path)
	} else {
		config := &Config{}
		dec := json.NewDecoder(fd)
		if err := dec.Decode(&config); err != nil {
			return nil, errors.WithMessagef(err, "while decoding file %s", path)
		} else {
			return config, nil
		}
	}
}
