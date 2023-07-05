package config

import (
	"encoding/json"
	"os"

	"github.com/pkg/errors"
)

type CLI struct {
	File string `arg:"--config,env:SPONGIX_CONFIG_FILE" help:"Configuration file to load"`
}

type Config struct {
	Listen     string                `json:"listen"`
	LogLevel   string                `json:"log_level"`
	LogMode    string                `json:"log_mode"`
	Chunks     *Chunks               `json:"chunks"`
	Namespaces map[string]*Namespace `json:"namespaces"`
}

func (c *Config) Prepare() error {
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
}

func (n *Namespace) Prepare() error {
	if n == nil {
		return errors.New("namespace configuration is missing")
	}

	if n.S3 == nil {
		return errors.Errorf("namespace S3 configuration is missing")
	}

	n.S3.CredentialsFile = os.ExpandEnv(n.S3.CredentialsFile)

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
	S3      *S3    `json:"s3"`
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

	if c.S3 == nil {
		return errors.New("chunks S3 configuration is missing")
	}

	c.S3.CredentialsFile = os.ExpandEnv(c.S3.CredentialsFile)

	return nil
}

func LoadBytes(input []byte) (*Config, error) {
	config := &Config{}
	return config, json.Unmarshal(input, config)
}

func LoadFile(path string) (*Config, error) {
	config := &Config{}

	if fd, err := os.Open(path); err != nil {
		return nil, errors.WithMessagef(err, "while opening file %s", path)
	} else if err := json.NewDecoder(fd).Decode(&config); err != nil {
		return nil, errors.WithMessagef(err, "while decoding file %s", path)
	} else {
		return config, nil
	}
}
