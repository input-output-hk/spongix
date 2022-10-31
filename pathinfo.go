package main

import (
	"io"
	"sort"
	"time"
)

type StorePath string

func (s StorePath) String() string { return string(s) }

type String string

func (s String) String() string { return string(s) }

type SetValue interface {
	comparable
	String() string
}

type StorePathSet Set[StorePath]
type Hash string
type StringSet Set[String]
type ContentAddress string

type Set[T SetValue] map[T]struct{}

func (s Set[T]) Equal(other Set[T]) bool {
	for k, v := range s {
		if ov, ok := other[k]; ok {
			if ov != v {
				return false
			}
		} else {
			return false
		}
	}

	return true
}

func (s Set[T]) Write(wr io.Writer) error {
	keys := make([]string, len(s))
	i := 0
	for k := range s {
		keys[i] = k.String()
		i += 1
	}
	sort.Strings(keys)

	return writeStrings(wr, keys)
}

type ValidPathInfo struct {
	Path             StorePath
	Deriver          *StorePath
	NarHash          Hash
	References       Set[StorePath]
	RegistrationTime time.Time
	NarSize          uint64
	// Whether the path is ultimately trusted, that is, it's a derivation
	// output that was built locally.
	Ultimate bool
	// note: not necessarily verified
	Sigs StringSet
	/* If non-empty, an assertion that the path is content-addressed,
	   i.e., that the store path is computed from a cryptographic hash
	   of the contents of the path, plus some other bits of data like
	   the "name" part of the path. Such a path doesn't need
	   signatures, since we don't have to trust anybody's claim that
	   the path is the output of a particular derivation. (In the
	   extensional store model, we have to trust that the *contents*
	   of an output path of a derivation were actually produced by
	   that derivation. In the intensional model, we have to trust
	   that a particular output path was produced by a derivation; the
	   path then implies the contents.)

	   Ideally, the content-addressability assertion would just be a Boolean,
	   and the store path would be computed from the name component, ‘narHash’
	   and ‘references’. However, we support many types of content addresses.
	*/
	CA *ContentAddress
}

func (vpi ValidPathInfo) Equal(other ValidPathInfo) bool {
	return vpi.Path == other.Path &&
		vpi.NarHash == other.NarHash &&
		vpi.References.Equal(other.References)
}

func (vpi ValidPathInfo) Write(wr io.Writer) error {
	writeString(wr, vpi.Path.String())
	deriver := ""
	if vpi.Deriver != nil {
		deriver = vpi.Deriver.String()
	}
	if err := writeString(wr, deriver); err != nil {
		return err
	}

	return nil
}

// void ValidPathInfo::write(
//     Sink & sink,
//     const Store & store,
//     unsigned int format,
//     bool includePath) const
// {
//     if (includePath)
//         sink << store.printStorePath(path);
//     sink << (deriver ? store.printStorePath(*deriver) : "")
//          << narHash.to_string(Base16, false);
//     worker_proto::write(store, sink, references);
//     sink << registrationTime << narSize;
//     if (format >= 16) {
//         sink << ultimate
//              << sigs
//              << renderContentAddress(ca);
//     }
// }
//
// }
