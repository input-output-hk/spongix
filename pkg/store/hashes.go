package store

// StorePathHash32 is the 32-character nix-base32 hash from a Nix store path.
type StorePathHash32 string

// NarHash52 is the 52-character nix-base32 hash of a NAR file.
type NarHash52 string

func (h StorePathHash32) String() string { return string(h) }
func (h NarHash52) String() string       { return string(h) }
