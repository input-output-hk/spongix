package main

import (
	"bytes"
	"io"

	"github.com/folbricht/desync"
	"github.com/pkg/errors"
)

func (proxy *Proxy) getCache() desync.Store {
	var store desync.Store
	if proxy.s3Store != nil {
		store = desync.NewCache(proxy.localStore, proxy.s3Store)
	} else {
		store = proxy.localStore
	}
	return store
}

func (proxy *Proxy) getIndex(id string) (index desync.Index, err error) {
	if index, err = proxy.localIndex.GetIndex(id); err == nil {
		return index, nil
	}

	if proxy.s3Index == nil {
		return index, errors.New("not found in any index")
	}

	if index, err = proxy.s3Index.GetIndex(id); err == nil {
		return index, nil
	}

	return index, errors.New("not found in any index")
}

type assembler struct {
	store desync.Store
	index desync.Index
	idx   int
	data  *bytes.Buffer
}

func newAssembler(store desync.Store, index desync.Index) *assembler {
	return &assembler{store: store, index: index, data: &bytes.Buffer{}}
}

func (a *assembler) Close() error { return nil }

func (a *assembler) Read(p []byte) (int, error) {
	if a.idx >= len(a.index.Chunks) {
		return 0, io.EOF
	}

	if a.data.Len() > 0 {
		n, _ := a.data.Read(p)
		return n, nil
	}

	indexChunk := a.index.Chunks[a.idx]
	chunk, err := a.store.GetChunk(indexChunk.ID)
	if err != nil {
		return 0, err
	}

	data, err := chunk.Data()
	if err != nil {
		return 0, err
	}

	a.idx++
	_, _ = a.data.Write(data)
	n, _ := a.data.Read(p)
	return n, nil
}

// func (a *assembler) ReadAt(p []byte, at int64) (int, error) { return 0, nil }

var _ = io.Reader(&assembler{})

// very simple implementation, mostly used for assembling narinfo which is
// usually tiny to avoid overhead of creating files.
func assemble(store desync.Store, index desync.Index) io.ReadCloser {
	return newAssembler(store, index)
}

func assembleNarinfo(store desync.Store, index desync.Index) (*Narinfo, error) {
	buf := assemble(store, index)

	info := &Narinfo{}
	err := info.Unmarshal(buf)
	if err != nil {
		return info, errors.WithMessage(err, "while unmarshaling narinfo")
	}

	return info, nil
}
