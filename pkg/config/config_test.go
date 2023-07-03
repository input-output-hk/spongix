package config

import (
	"testing"

	"github.com/smarty/assertions"
)

const exampleConfig = `
{
  "dir": "./cache",
  "listen": "0.0.0.0:7745",
  "log_level": "debug",
  "log_mode": "production",
  "average_chunk_size": 65536,
	"s3_bucket_url": "s3+http://127.0.0.1:9000/public",
	"s3_bucket_region": "eu-central",
  "namespaces": {
    "public": {
      "substituters": [],
      "secret_key_file": "public.pkey",
      "trusted_public_keys": [],
      "cache_info_priority": 50
    }
  }
}
`

func TestConfig(t *testing.T) {
	a := assertions.New(t)

	c, err := LoadBytes([]byte(exampleConfig))
	a.So(err, assertions.ShouldBeNil)
	a.So(c, assertions.ShouldResemble, &Config{
		AverageChunkSize: 65536,
		Dir:              "./cache",
		Listen:           "0.0.0.0:7745",
		LogLevel:         "debug",
		LogMode:          "production",
		S3BucketUrl:      "s3+http://127.0.0.1:9000/public",
		S3BucketRegion:   "eu-central",
		Namespaces: map[string]Namespace{
			"public": {
				Substituters:      []string{},
				SecretKeyFile:     "public.pkey",
				TrustedPublicKeys: []string{},
				CacheInfoPriority: 50,
			},
		}})
}
