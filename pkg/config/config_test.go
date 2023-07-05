package config

import (
	"os"
	"testing"

	"github.com/smarty/assertions"
)

const exampleConfig = `
{
  "listen": "0.0.0.0:7745",
  "log_level": "debug",
  "log_mode": "production",
	"chunks": {
		"minimum_size": 16384,
		"average_size": 65536,
		"s3": {
			"url": "s3+http://127.0.0.1:9000/chunks",
			"region": "auto",
			"credentials_file": "$CHUNKS_CREDENTIALS_FILE"
		}
	},
  "namespaces": {
    "public": {
			"s3": {
				"url": "s3+http://127.0.0.1:9000/public/indices",
				"region": "auto",
				"credentials_file": "$PUBLIC_CREDENTIALS_FILE"
			},
			"substituters": ["https://cache.nixos.org"],
      "trusted_public_keys": [],
      "cache_info_priority": 50
    }
  }
}
`

func TestConfig(t *testing.T) {
	a := assertions.New(t)

	os.Setenv("CHUNKS_CREDENTIALS_FILE", "/tmp/chunks-credentials")
	os.Setenv("PUBLIC_CREDENTIALS_FILE", "/tmp/public-credentials")

	c, err := LoadBytes([]byte(exampleConfig))
	a.So(c.Prepare(), assertions.ShouldBeNil)
	a.So(err, assertions.ShouldBeNil)
	a.So(c, assertions.ShouldResemble, &Config{
		Listen:   "0.0.0.0:7745",
		LogLevel: "debug",
		LogMode:  "production",
		Chunks: &Chunks{
			S3: &S3{
				Url:             "s3+http://127.0.0.1:9000/chunks",
				Region:          "auto",
				CredentialsFile: "/tmp/chunks-credentials",
			},
			MinSize: 16384,
			AvgSize: 65536,
			MaxSize: 262144,
		},
		Namespaces: map[string]*Namespace{
			"public": {
				S3: &S3{
					Url:             "s3+http://127.0.0.1:9000/public/indices",
					Region:          "auto",
					CredentialsFile: "/tmp/public-credentials",
				},
				Substituters:      []string{"https://cache.nixos.org"},
				CacheInfoPriority: 50,
			},
		}})
}
