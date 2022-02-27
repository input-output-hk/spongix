package main

import (
	"errors"

	"github.com/folbricht/desync"
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

// very simple implementation, mostly used for assembling narinfo which is
// usually tiny to avoid overhead of creating files.
func assemble(store desync.Store, index desync.Index) ([]byte, error) {
	if index.Length() == 0 {
		return nil, errors.New("resulting size is 0")
	}
	if index.Length() > 4096 {
		return nil, errors.New("resulting size too large")
	}

	content := make([]byte, index.Length())

	for _, indexChunk := range index.Chunks {
		chunk, err := store.GetChunk(indexChunk.ID)
		if err != nil {
			return nil, err
		}
		data, err := chunk.Data()
		if err != nil {
			return nil, err
		}
		for i, b := range data {
			content[int(indexChunk.Start)+i] = b
		}
	}

	return content, nil
}
