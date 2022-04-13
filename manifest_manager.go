package main

import "github.com/folbricht/desync"

type manifestManager struct {
	c chan manifestMsg
}

func newManifestManager(store desync.WriteStore, index desync.IndexWriteStore) manifestManager {
	return manifestManager{c: manifestLoop(store, index)}
}

func (m manifestManager) set(name, reference string, manifest *DockerManifest) {
	m.c <- manifestMsg{t: manifestMsgSet, name: name, reference: reference, manifest: manifest}
}

func (m manifestManager) get(name, reference string) (manifest *DockerManifest) {
	c := make(chan *DockerManifest)
	m.c <- manifestMsg{t: manifestMsgSet, name: name, reference: reference, manifest: manifest, c: c}
	return <-c
}

type manifestMsgType int

const (
	manifestMsgGet manifestMsgType = iota
	manifestMsgSet manifestMsgType = iota
)

type manifestMsg struct {
	t         manifestMsgType
	c         chan *DockerManifest
	manifest  *DockerManifest
	name      string
	reference string
}

func manifestLoop(store desync.WriteStore, index desync.IndexWriteStore) chan manifestMsg {
	manifests := map[string]*DockerManifest{}

	ch := make(chan manifestMsg)
	go func() {
		for msg := range ch {
			switch msg.t {
			case manifestMsgGet:
				if manifest, ok := manifests[msg.name+"'"+msg.reference]; !ok {
					msg.c <- manifest
				} else {
					msg.c <- nil
				}
			case manifestMsgSet:
				manifests[msg.name+"'"+msg.reference] = msg.manifest
			default:
				panic(msg)
			}
		}
	}()

	return ch
}
