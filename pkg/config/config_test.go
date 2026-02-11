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
		"store_type": "s3",
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
			"store_type": "s3",
			"s3": {
				"url": "s3+http://127.0.0.1:9000/public/indices",
				"region": "auto",
				"credentials_file": "$PUBLIC_CREDENTIALS_FILE"
			},
			"substituters": ["https://cache.nixos.org"],
      "cache_info_priority": 50
    }
  }
}
`

func TestConfigPebbleDefaults(t *testing.T) {
	a := assertions.New(t)

	c, err := LoadBytes([]byte(`{
		"listen": "0.0.0.0:7745",
		"chunks": {},
		"namespaces": {
			"default": {
				"substituters": ["https://cache.nixos.org"]
			}
		}
	}`))
	a.So(err, assertions.ShouldBeNil)
	a.So(c.Prepare(), assertions.ShouldBeNil)
	a.So(c.Chunks.StoreType, assertions.ShouldEqual, "pebble")
	a.So(c.Chunks.PebbleDir, assertions.ShouldEqual, "./data")
	a.So(c.Namespaces["default"].StoreType, assertions.ShouldEqual, "pebble")
}

func TestConfig(t *testing.T) {
	a := assertions.New(t)

	_ = os.Setenv("CHUNKS_CREDENTIALS_FILE", "/tmp/chunks-credentials")
	_ = os.Setenv("PUBLIC_CREDENTIALS_FILE", "/tmp/public-credentials")

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
			StoreType: "s3",
			MinSize:   16384,
			AvgSize:   65536,
			MaxSize:   262144,
		},
		Namespaces: map[string]*Namespace{
			"public": {
				S3: &S3{
					Url:             "s3+http://127.0.0.1:9000/public/indices",
					Region:          "auto",
					CredentialsFile: "/tmp/public-credentials",
				},
				StoreType:         "s3",
				Substituters:      []string{"https://cache.nixos.org"},
				CacheInfoPriority: 50,
			},
		}})
}
