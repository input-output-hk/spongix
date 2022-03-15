package main

import (
	"bytes"
	"io"
	"sync"
)

type teeCloser struct {
	tee  io.ReadCloser
	body io.ReadCloser
}

func (t teeCloser) Close() error {
	t.tee.Close()
	return t.body.Close()
}
func (t teeCloser) Read(b []byte) (int, error) { return t.tee.Read(b) }

type teeCombiner struct {
	reader  io.ReadCloser
	readers []*teeReader
	wg      *sync.WaitGroup
	total   int64
}

func newTeeCombiner(reader io.ReadCloser, readers ...*teeReader) *teeCombiner {
	wg := &sync.WaitGroup{}
	wg.Add(len(readers))
	for _, r := range readers {
		r.wg = wg
	}
	return &teeCombiner{reader: reader, readers: readers, wg: wg}
}

func (t *teeCombiner) AddReader(reader *teeReader) {
	t.readers = append(t.readers, reader)
	reader.wg = t.wg
	t.wg.Add(1)
}

func (t *teeCombiner) Close() error {
	t.wg.Wait()
	t.reader.Close()

	return nil
}

func (t *teeCombiner) Read(p []byte) (int, error) {
	n, err := t.reader.Read(p)
	t.total += int64(n)
	pc := make([]byte, n)
	copy(pc, p)
	for _, r := range t.readers {
		r.cha <- teeRead{n: n, err: err, b: pc}
		if err == io.EOF {
			close(r.cha)
		}
	}

	return n, err
}

type teeReader struct {
	eof      bool
	cha      chan teeRead
	mut      *sync.Mutex
	buf      *bytes.Buffer
	wg       *sync.WaitGroup
	totalIn  int64
	totalOut int64
}

type teeRead struct {
	err error
	n   int
	b   []byte
}

func newTeeReader() *teeReader {
	return &teeReader{
		cha: make(chan teeRead, 1),
		mut: &sync.Mutex{},
		buf: &bytes.Buffer{},
	}
}

func (t *teeReader) Close() error {
	return nil
}

func (t *teeReader) Read(p []byte) (int, error) {
	read, more := <-t.cha
	if more {
		m, _ := t.buf.Write(read.b)
		t.totalIn += int64(m)
		n, _ := t.buf.Read(p)
		t.totalOut += int64(n)
		return n, nil
	} else {
		if t.buf.Len() > 0 {
			n, err := t.buf.Read(p)
			t.totalOut += int64(n)
			return n, err
		} else {
			t.wg.Done()
			return 0, io.EOF
		}
	}
}
