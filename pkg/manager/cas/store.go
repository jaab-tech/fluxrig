package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Store defines the Content Addressable Storage interface
type Store interface {
	// Put saves content and returns its SHA256 hash
	Put(content []byte) (string, error)
	// Get retrieves content by hash
	Get(hash string) ([]byte, error)
	// Has checks if content exists
	Has(hash string) bool
}

type DiskStore struct {
	rootDir string
}

func NewDiskStore(rootDir string) (*DiskStore, error) {
	// Ensure root dir exists with secure permissions
	if err := os.MkdirAll(rootDir, 0750); err != nil {
		return nil, err
	}
	// Ensure blobs dir exists
	blobsDir := filepath.Join(rootDir, "blobs")
	if err := os.MkdirAll(blobsDir, 0750); err != nil {
		return nil, err
	}
	return &DiskStore{rootDir: rootDir}, nil
}

func (s *DiskStore) Put(content []byte) (string, error) {
	hash := sha256.Sum256(content)
	hashStr := hex.EncodeToString(hash[:])

	// Sharding: blobs/ab/c123...
	shard := hashStr[:2]
	dir := filepath.Join(s.rootDir, "blobs", shard)

	// G301: Expect directory permissions to be 0750 or less
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", err
	}

	path := filepath.Join(dir, hashStr)
	// G306: Expect WriteFile permissions to be 0600 or less
	return hashStr, os.WriteFile(path, content, 0600)
}

// validHex matches exactly 64 lowercase hex characters.
var validHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *DiskStore) Get(hash string) ([]byte, error) {
	if !validHex.MatchString(hash) {
		return nil, fmt.Errorf("invalid hash: must be 64 lowercase hex characters")
	}

	path := filepath.Join(s.rootDir, "blobs", hash[:2], hash)
	// G304: Potential file inclusion via variable
	return os.ReadFile(filepath.Clean(path))
}

func (s *DiskStore) Has(hash string) bool {
	if !validHex.MatchString(hash) {
		return false
	}
	path := filepath.Join(s.rootDir, "blobs", hash[:2], hash)
	_, err := os.Stat(path)
	return err == nil
}

// ShortHash returns the first 12 hex characters of a CAS hash.
// Used for log output and metadata; CAS storage always uses the full 64-char hash.
func ShortHash(hash string) string {
	if len(hash) < 12 {
		return hash
	}
	return hash[:12]
}

// Ref formats a CAS reference as "name:tag@shortHash" for use in logs and metadata.
func Ref(name, tag, hash string) string {
	return fmt.Sprintf("%s:%s@%s", name, tag, ShortHash(hash))
}
