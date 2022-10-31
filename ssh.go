package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
	"unicode"

	"github.com/gliderlabs/ssh"
	"github.com/google/go-github/v43/github"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
)

var (
	allowedTeams = map[string][]string{
		"input-output-hk": {"devops"},
	}
	connectionWait        = 10 * time.Second
	maxConcurrentSessions = 10
	listenAddress         = ":2222"
	hostKeyFile           = "./user1"
)

// Magic numbers used in the store protocol
const (
	StderrLast      = 0x616C7473 // stla
	StderrError     = 0x63787470 // ptxc
	WorkerMagic1    = 0x6E697863 // cxin
	WorkerMagic2    = 0x6478696F // ioxd
	ProtocolVersion = 1<<8 | 34  // 290
)

func sshServer() {
	sessions := make(chan bool, maxConcurrentSessions)

	handler := func(s ssh.Session) {
		select {
		case sessions <- true:
			defer func() { <-sessions }()
		case <-time.After(connectionWait):
			io.WriteString(s, "Too many connections\n")
			s.Exit(1)
		}

		if err := nixDaemon(s); err != nil {
			if err != io.EOF {
				fmt.Println("ERROR", err.Error())
				writeInt(s, StderrError)
				writeString(s, "Error")
				writeInt(s, 1)
				writeString(s, "error-name")
				writeString(s, err.Error())
				writeInt(s, 0)
				writeInt(s, 0)
				s.Exit(1)
			} else {
				s.Exit(0)
			}
		}
	}

	allowedKeys := syncAllowedKeys()
	fmt.Printf("Serving at %s\n", listenAddress)

	ssh.ListenAndServe(listenAddress, handler,
		ssh.HostKeyFile(hostKeyFile),
		ssh.PublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			allow := false
			allowedKeys.Range(func(userNameI, userKeysI interface{}) bool {
				for _, userKey := range userKeysI.([]ssh.PublicKey) {
					if ssh.KeysEqual(key, userKey) {
						fmt.Printf("login allowed for %s\n", userNameI)
						allow = true
						return false
					}
				}

				return true
			})

			if !allow {
				pp("deny access to ", key)
			}
			return allow
		}),
	)
}

func nixDaemon(s ssh.Session) error {
	if workerMagic1, err := readInt(s); err != nil {
		return errors.WithMessage(err, "reading magic1")
	} else if workerMagic1 != WorkerMagic1 {
		return errors.WithMessagef(err, "worker magic 1 mismatch: %x != %x", workerMagic1, WorkerMagic1)
	} else if err := writeInt(s, WorkerMagic2); err != nil {
		return errors.WithMessage(err, "writing magic2")
	} else if err := writeInt(s, ProtocolVersion); err != nil {
		return errors.WithMessage(err, "writing protocol version")
	} else if _, err := readInt(s); err != nil { // clientProtocolVersion
		return errors.WithMessage(err, "reading protocol version")
	} else if err := writeString(s, "2.11.2"); err != nil {
		return errors.WithMessage(err, "writing version")
	} else if err := writeInt(s, StderrLast); err != nil {
		return errors.WithMessage(err, "writing StderrLast")
	} else {
		// throw away bytes used by old versions (cpu affinity and reserve space)
		s.Read(make([]byte, 16))

		for {
			if operation, err := readInt(s); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			} else {
				op := WOP(operation)
				fmt.Printf("WOP: %s\n", op)
				if err := func() error {
					switch op {
					case WOPIsValidPath:
						return isValidPath(s)
					case WOPNarFromPath:
						return narFromPath(s)
					case WOPQueryValidPaths:
						return queryValidPaths(s)
					case WOPAddMultipleToStore:
						return addMultipleToStore(s)
					case WOPAddTextToStore:
						return addTextToStore(s)
					case WOPRegisterDrvOutput:
						return registerDrvOutput(s)
					case WOPAddTempRoot:
						return addTempRoot(s)
					case WOPQueryPathInfo:
						return queryPathInfo(s)
					default:
						return errors.Errorf("unknown operation: %s", op)
					}
				}(); err != nil {
					return err
				}
			}
		}
	}
}

func queryPathInfo(s io.ReadWriter) error {
	pp(readString(s))

	if err := writeInt(s, StderrLast); err != nil {
		return err
	}

	writeBool(s, true)

	return nil
}

func addTempRoot(s io.ReadWriter) error {
	pp(readString(s))

	if err := writeInt(s, StderrLast); err != nil {
		return err
	}

	writeInt(s, 0)

	return nil
}

func registerDrvOutput(s io.ReadWriter) error {
	realisation, err := readString(s)
	if err != nil {
		return err
	}
	pp(realisation)

	if err := writeInt(s, StderrLast); err != nil {
		return err
	}

	return nil
}

func addTextToStore(s io.ReadWriter) error {
	return nil
}

type framedSource struct {
	from    io.Reader
	pending *bytes.Buffer
	eof     bool
}

func newFramedSource(from io.Reader) *framedSource {
	return &framedSource{from: from, pending: &bytes.Buffer{}}
}

func (s framedSource) Read(buf []byte) (int, error) {
	if s.eof {
		return 0, io.EOF
	}

	if s.pending.Len() == 0 {
		size, err := readInt(s.from)
		if size == 0 {
			s.eof = true
			return 0, io.EOF
		}
		if err != nil {
			if err == io.EOF {
				s.eof = true
			}
			return int(size), err
		}
		io.Copy(s.pending, io.LimitReader(s.from, size))
	}

	return s.pending.Read(buf)
}

func addMultipleToStore(s io.ReadWriter) error {
	repair, err := readBool(s)
	if err != nil {
		return err
	}
	pp("repair", repair)

	dontCheckSigs, err := readBool(s)
	if err != nil {
		return err
	}
	pp("dontCheckSigs:", dontCheckSigs)

	narSource := newFramedSource(s)

	if err := parseSource(narSource); err != nil {
		return errors.WithMessage(err, "parsing source")
	}

	if err := writeInt(s, StderrLast); err != nil {
		return err
	}

	if n, err := readInt(s); err != nil {
		return errors.WithMessage(err, "reading result status")
	} else if n != 0 {
		return errors.New("Invalid result status")
	}

	return nil
}

func parseSource(s io.Reader) error {
	expected, err := readInt(s)
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return errors.WithMessage(err, "reading expected")
	}

	for i := int64(0); i < expected; i += 1 {
		if narinfo, err := readNarinfo(s); err != nil {
			return errors.WithMessage(err, "reading Narinfo")
		} else {
			swallow(s, narinfo.NarSize)
		}
	}

	return nil
}

func writePathInfo(s io.Writer, validPathInfo ValidPathInfo) error {
	if err := writeString(s, "storepath"); err != nil {
		return err
	}

	if validPathInfo.Deriver != nil {
		if err := writeString(s, string(*validPathInfo.Deriver)); err != nil {
			return err
		}
	} else {
		if err := writeString(s, ""); err != nil {
			return err
		}
	}

	if err := writeString(s, string(validPathInfo.NarHash)); err != nil {
		return err
	} else if err := validPathInfo.References.Write(s); err != nil {
		return err
	}

	return nil
}

func readNarinfo(s io.Reader) (*Narinfo, error) {
	info := &Narinfo{}

	if storePath, err := readString(s); err != nil {
		return nil, errors.WithMessage(err, "reading StorePath")
	} else if err := info.SetStorePath(storePath); err != nil {
		return nil, errors.WithMessage(err, "setting StorePath")
	} else if deriver, err := readString(s); err != nil {
		return nil, errors.WithMessage(err, "reading Deriver")
	} else if err := info.SetDeriver(deriver); err != nil {
		return nil, errors.WithMessage(err, "reading Deriver")
	} else if narHash, err := readString(s); err != nil {
		return nil, errors.WithMessage(err, "reading NarHash")
	} else if err := info.SetNarHash(narHash); err != nil {
		return nil, errors.WithMessage(err, "setting NarHash")
	} else if references, err := readStrings(s); err != nil {
		return nil, errors.WithMessage(err, "reading References")
	} else if err := info.SetReferences(references); err != nil {
		return nil, errors.WithMessage(err, "setting References")
	}

	registrationTimeUnix, err := readInt(s)
	if err != nil {
		return nil, errors.WithMessage(err, "reading registrationTime")
	}
	registrationTime := time.Unix(registrationTimeUnix, 0)

	if narSize, err := readInt(s); err != nil {
		return nil, errors.WithMessage(err, "reading narSize")
	} else if err := info.SetNarSize(narSize); err != nil {
		return nil, errors.WithMessage(err, "setting narSize")
	}

	ultimate, err := readBool(s)
	if err != nil {
		return nil, errors.WithMessage(err, "reading ultimate")
	}
	pp("registrationTime:", registrationTime, "ultimate:", ultimate)

	if sigs, err := readStrings(s); err != nil {
		return nil, errors.WithMessage(err, "reading Sigs")
	} else if info.AddSigs(sigs); err != nil {
		return nil, errors.WithMessage(err, "setting Sigs")
	} else if ca, err := readString(s); err != nil {
		return nil, errors.WithMessage(err, "reading CA")
	} else if info.SetCA(ca); err != nil {
		return nil, errors.WithMessage(err, "setting CA")
	}

	return info, nil
}

func readNar(s io.Reader) error {
	n, err := nar.NewReader(s)
	if err != nil {
		return err
	}
	for {
		header, err := n.Next()
		pp(header, err)

		if err != nil {
			if err.Error() == "unexpected EOF" {
				return nil
			}
			return errors.WithMessage(err, "getting NAR header")
		}

		switch header.Type {
		case nar.TypeSymlink:
			pp("sym", header.Path, header.LinkTarget)
		case nar.TypeDirectory:
			pp("dir", header.Path)
		case nar.TypeRegular:
			pp("reg", header.Path, header.Size, header.Executable)
			// buf := bytes.Buffer{}
			// pp(io.Copy(&buf, n))
		}
	}
}

func queryValidPaths(s io.ReadWriter) error {
	paths, err := readStrings(s)
	if err != nil {
		return err
	}
	pp("paths:", paths)

	substitute, err := readInt(s)
	if err != nil {
		return err
	}
	pp("substitute:", substitute)
	pp(writeInt(s, StderrLast))

	writeStrings(s, []string{})

	return nil
}

func dbgBytes(s io.Reader, n int64) {
	rd := io.LimitReader(s, n)
	// x, err := io.Copy(io.Discard, io.LimitReader(s, n))

	buf := make([]byte, 16)

	for i := int64(0); i < n; i += 16 {
		rd.Read(buf)

		fmt.Printf("%08x  ", i)
		for _, d := range buf[0:8] {
			fmt.Printf("%02x ", d)
		}

		fmt.Printf(" ")

		for _, d := range buf[8:16] {
			fmt.Printf("%02x ", d)
		}

		fmt.Printf(" |")
		for _, d := range buf[0:16] {
			if unicode.IsPrint(rune(d)) {
				fmt.Printf("%c", d)
			} else {
				fmt.Printf(".")
			}
		}
		fmt.Printf("| ")

		n1 := binary.LittleEndian.Uint64(buf[0:8])
		n2 := binary.LittleEndian.Uint64(buf[8:16])

		fmt.Printf("% 20d 0x%-18x", n1, n1)
		fmt.Printf(" | ")
		fmt.Printf("% 20d 0x%-18x", n2, n2)

		fmt.Printf("\n")
	}
}

func swallow(s io.Reader, n int64) {
	fd, err := os.Create("swallow")
	if err != nil {
		panic(err)
	}

	copied, err := io.Copy(fd, io.LimitReader(s, n))

	if err != nil {
		panic(err)
	}

	if copied != n {
		panic(fmt.Sprintf("copied %d of %d bytes", copied, n))
	}
}

func isValidPath(s io.ReadWriter) error {
	path, err := readString(s)
	if err != nil {
		return err
	}
	writeInt(s, StderrLast)
	writeInt(s, 1)
	pp(path)
	return nil
}

func narFromPath(s io.ReadWriter) error {
	path, err := readString(s)
	if err != nil {
		return err
	}
	pp(path)
	return nil
}

func readInt(s io.Reader) (int64, error) {
	var num int64
	err := binary.Read(s, binary.LittleEndian, &num)
	// pp("rd", uint64(num), int64(num))
	return num, err
}

func writeBool(s io.Writer, b bool) error {
	if b {
		return writeInt(s, 1)
	} else {
		return writeInt(s, 0)
	}
}

func readBool(s io.Reader) (bool, error) {
	b, err := readInt(s)
	return b != 0, err
}

func writeInt(s io.Writer, num int64) error {
	// pp("wr", uint64(num), int64(num))
	return binary.Write(s, binary.LittleEndian, num)
}

func readStrings(s io.Reader) ([]string, error) {
	size, err := readInt(s)
	if err != nil {
		return nil, err
	}

	output := make([]string, size)

	for i := int64(0); i < size; i += 1 {
		path, err := readString(s)
		if err != nil {
			return nil, err
		}
		output[i] = path
	}

	return output, nil
}

func readString(s io.Reader) (string, error) {
	var size int64
	if err := binary.Read(s, binary.LittleEndian, &size); err != nil {
		return "", err
	}

	buf := make([]byte, size)
	if _, err := s.Read(buf); err != nil {
		return "", err
	}

	pad := make([]byte, padOf(size))
	if _, err := s.Read(pad); err != nil {
		return "", err
	}

	return string(buf), nil
}

func writeStrings(s io.Writer, strings []string) error {
	if err := writeInt(s, int64(len(strings))); err != nil {
		return err
	}

	for _, str := range strings {
		if err := writeString(s, str); err != nil {
			return err
		}
	}

	return nil
}

func writeString(s io.Writer, str string) error {
	pp("wr", str)

	pad := padOf(int64(len(str)))

	// TODO: this can be optimized somewhat
	buf := bytes.Buffer{}
	writeInt(&buf, int64(len(str)))
	buf.WriteString(str)
	buf.Write(make([]byte, pad))
	res := buf.Bytes()

	_, err := s.Write(res)

	return err
}

func padOf(l int64) int64 {
	var pad int64
	mod := l % 8
	if mod > 0 {
		pad = 8 - mod
	}
	return pad
}

func syncAllowedKeys() *sync.Map {
	m := &sync.Map{}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJQnxCAgDAucoHZauKVR5BiSqL7zRFin/JPurBULETDl manveru@alpha"))
	if err != nil {
		panic(err)
	}
	m.Store("manveru", []ssh.PublicKey{key.(ssh.PublicKey)})
	return m
}

// func syncAllowedKeys() *sync.Map {
// 	m := &sync.Map{}
// 	for userName, userKeys := range syncGithub() {
// 		m.Store(userName, userKeys)
// 	}
//
// 	go func() {
// 		for range time.Tick(1 * time.Minute) {
// 			updated := syncGithub()
// 			for userName, userKeys := range updated {
// 				m.Store(userName, userKeys)
// 			}
//
// 			m.Range(func(key, value interface{}) bool {
// 				userName := key.(string)
// 				if _, found := updated[userName]; !found {
// 					fmt.Printf("removing user %s\n", userName)
// 					m.Delete(userName)
// 				}
// 				return true
// 			})
// 		}
// 	}()
//
// 	return m
// }

// Since there is no way to lookup users by their SSH keys, we simply verify
// that they are in the specified teams.
func syncGithub() map[string][]ssh.PublicKey {
	fmt.Println("Fetching allowed keys from GitHub")

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")})
	tc := oauth2.NewClient(ctx, ts)

	client := github.NewClient(tc)

	allowedKeys := map[string][]ssh.PublicKey{}

	for orgName, teamNames := range allowedTeams {
		for _, teamName := range teamNames {
			members, _, err := client.Teams.ListTeamMembersBySlug(ctx, orgName, teamName, nil)
			if err != nil {
				panic(err)
			}

			for _, member := range members {
				login := member.GetLogin()

				if _, exists := allowedKeys[login]; exists {
					continue
				}

				keys, _, err := client.Users.ListKeys(ctx, login, nil)
				if err != nil {
					panic(err)
				}

				for _, key := range keys {
					keyData := []byte(key.GetTitle() + " " + key.GetKey() + " " + login)

					key, _, _, _, err := ssh.ParseAuthorizedKey(keyData)
					if err != nil {
						fmt.Println("ERROR:", err.Error())
						continue
					}

					allowedKeys[login] = append(allowedKeys[login], key)
				}
			}
		}
	}

	return allowedKeys
}

type WOP int64

const (
	WOPIsValidPath                 WOP = 1
	WOPHasSubstitutes                  = 3
	WOPQueryReferrers                  = 6
	WOPAddToStore                      = 7
	WOPAddTextToStore                  = 8
	WOPBuildPaths                      = 9
	WOPEnsurePath                      = 10
	WOPAddTempRoot                     = 11
	WOPAddIndirectRoot                 = 12
	WOPSyncWithGC                      = 13
	WOPFindRoots                       = 14
	WOPSetOptions                      = 19
	WOPCollectGarbage                  = 20
	WOPQuerySubstitutablePathInfo      = 21
	WOPQueryAllValidPaths              = 23
	WOPQueryFailedPaths                = 24
	WOPClearFailedPaths                = 25
	WOPQueryPathInfo                   = 26
	WOPQueryPathFromHashPart           = 29
	WOPQuerySubstitutablePathInfos     = 30
	WOPQueryValidPaths                 = 31
	WOPQuerySubstitutablePaths         = 32
	WOPQueryValidDerivers              = 33
	WOPOptimiseStore                   = 34
	WOPVerifyStore                     = 35
	WOPBuildDerivation                 = 36
	WOPAddSignatures                   = 37
	WOPNarFromPath                     = 38
	WOPAddToStoreNar                   = 39
	WOPQueryMissing                    = 40
	WOPQueryDerivationOutputMap        = 41
	WOPRegisterDrvOutput               = 42
	WOPQueryRealisation                = 43
	WOPAddMultipleToStore              = 44
	WOPAddBuildLog                     = 45
	WOPBuildPathsWithResults           = 46
)

func (w WOP) String() string {
	switch w {
	case WOPIsValidPath:
		return "WOPIsValidPath"
	case WOPHasSubstitutes:
		return "WOPHasSubstitutes"
	case WOPQueryReferrers:
		return "WOPQueryReferrers"
	case WOPAddToStore:
		return "WOPAddToStore"
	case WOPAddTextToStore:
		return "WOPAddTextToStore"
	case WOPBuildPaths:
		return "WOPBuildPaths"
	case WOPEnsurePath:
		return "WOPEnsurePath"
	case WOPAddTempRoot:
		return "WOPAddTempRoot"
	case WOPAddIndirectRoot:
		return "WOPAddIndirectRoot"
	case WOPSyncWithGC:
		return "WOPSyncWithGC"
	case WOPFindRoots:
		return "WOPFindRoots"
	case WOPSetOptions:
		return "WOPSetOptions"
	case WOPCollectGarbage:
		return "WOPCollectGarbage"
	case WOPQuerySubstitutablePathInfo:
		return "WOPQuerySubstitutablePathInfo"
	case WOPQueryAllValidPaths:
		return "WOPQueryAllValidPaths"
	case WOPQueryFailedPaths:
		return "WOPQueryFailedPaths"
	case WOPClearFailedPaths:
		return "WOPClearFailedPaths"
	case WOPQueryPathInfo:
		return "WOPQueryPathInfo"
	case WOPQueryPathFromHashPart:
		return "WOPQueryPathFromHashPart"
	case WOPQuerySubstitutablePathInfos:
		return "WOPQuerySubstitutablePathInfos"
	case WOPQueryValidPaths:
		return "WOPQueryValidPaths"
	case WOPQuerySubstitutablePaths:
		return "WOPQuerySubstitutablePaths"
	case WOPQueryValidDerivers:
		return "WOPQueryValidDerivers"
	case WOPOptimiseStore:
		return "WOPOptimiseStore"
	case WOPVerifyStore:
		return "WOPVerifyStore"
	case WOPBuildDerivation:
		return "WOPBuildDerivation"
	case WOPAddSignatures:
		return "WOPAddSignatures"
	case WOPNarFromPath:
		return "WOPNarFromPath"
	case WOPAddToStoreNar:
		return "WOPAddToStoreNar"
	case WOPQueryMissing:
		return "WOPQueryMissing"
	case WOPQueryDerivationOutputMap:
		return "WOPQueryDerivationOutputMap"
	case WOPRegisterDrvOutput:
		return "WOPRegisterDrvOutput"
	case WOPQueryRealisation:
		return "WOPQueryRealisation"
	case WOPAddMultipleToStore:
		return "WOPAddMultipleToStore"
	case WOPAddBuildLog:
		return "WOPAddBuildLog"
	case WOPBuildPathsWithResults:
		return "WOPBuildPathsWithResults"
	default:
		panic(fmt.Sprintf("Unknown WOP: %d", w))
	}
}
