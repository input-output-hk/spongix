package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type manifestManager struct {
	c chan manifestMsg
}

func newManifestManager(dir string) manifestManager {
	return manifestManager{c: manifestLoop(dir)}
}

func (m manifestManager) set(name, reference string, manifest *DockerManifest) error {
	c := make(chan *manifestMsg)
	m.c <- manifestMsg{t: manifestMsgSet, name: name, reference: reference, manifest: manifest, c: c}
	return (<-c).err
}

func (m manifestManager) get(name, reference string) (*DockerManifest, error) {
	c := make(chan *manifestMsg)
	m.c <- manifestMsg{t: manifestMsgGet, name: name, reference: reference, c: c}
	res := <-c
	return res.manifest, res.err
}

type manifestMsgType int

const (
	manifestMsgGet manifestMsgType = iota
	manifestMsgSet manifestMsgType = iota
)

type manifestMsg struct {
	t         manifestMsgType
	c         chan *manifestMsg
	manifest  *DockerManifest
	name      string
	reference string
	err       error
}

func manifestLoop(dir string) chan manifestMsg {
	ch := make(chan manifestMsg)
	go func() {
		for msg := range ch {
			switch msg.t {
			case manifestMsgGet:
				subdir := filepath.Join(dir, msg.name)

				if fd, err := os.Open(filepath.Join(subdir, msg.reference)); err != nil {
					if err == os.ErrNotExist {
						msg.c <- nil
					} else {
						msg.c <- &manifestMsg{err: err}
					}
				} else {
					manifest := &DockerManifest{}
					if err := json.NewDecoder(fd).Decode(manifest); err != nil {
						msg.c <- &manifestMsg{err: err}
					} else {
						msg.c <- &manifestMsg{manifest: manifest}
					}
				}
			case manifestMsgSet:
				subdir := filepath.Join(dir, msg.name)

				if err := os.MkdirAll(subdir, 0755); err != nil {
					msg.c <- &manifestMsg{err: err}
				} else if fd, err := os.Create(filepath.Join(subdir, msg.reference)); err != nil {
					msg.c <- &manifestMsg{err: err}
				} else if err := json.NewEncoder(fd).Encode(msg.manifest); err != nil {
					msg.c <- &manifestMsg{err: err}
				} else {
					msg.c <- &manifestMsg{}
				}
			default:
				panic(msg)
			}
		}
	}()

	return ch
}
