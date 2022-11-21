package assembler

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/folbricht/desync"
	"github.com/smartystreets/assertions"
)

var defaultStoreOptions = desync.StoreOptions{
	N:            1,
	Timeout:      1 * time.Second,
	ErrorRetry:   0,
	Uncompressed: false,
	SkipVerify:   false,
}

const defaultThreads = 2

func TestAssemble(t *testing.T) {
	a := assertions.New(t)

	var index desync.IndexWriteStore

	indexDir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(filepath.Join(indexDir, "nar"), 0700); err != nil {
		t.Fatal(err)
	} else if index, err = desync.NewLocalIndexStore(indexDir); err != nil {
		t.Fatal(err)
	}

	var store desync.WriteStore
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	} else if store, err = desync.NewLocalStore(storeDir, defaultStoreOptions); err != nil {
		t.Fatal(err)
	}

	key := "hello"
	value := bytes.Repeat([]byte("hello world"), 200)
	input := bytes.NewBuffer(value)

	if chunker, err := desync.NewChunker(input, 48, 192, 768); err != nil {
		t.Fatal(err)
	} else if idx, err := desync.ChunkStream(context.Background(), chunker, store, defaultThreads); err != nil {
		t.Fatal(err)
	} else if err := index.StoreIndex(key, idx); err != nil {
		t.Fatal(err)
	} else {
		asm := NewAssembler(store, idx)

		buf := &bytes.Buffer{}
		n, err := io.Copy(buf, asm)
		a.So(err, assertions.ShouldBeNil)
		a.So(n, assertions.ShouldEqual, 2200)
		a.So(buf.Bytes(), assertions.ShouldResemble, value)
	}
}
