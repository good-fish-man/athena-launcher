package deployment

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	operationsv1 "github.com/good-fish-man/athena-protocol/protocol/operations/v1"
)

const (
	managedRecoveryMagic       = "ATHENA-MANAGED-POSTGRES-AESGCM-1\n"
	managedRecoveryProtocol    = "managed-postgres-physical-v1"
	managedRecoveryArtifact    = "postgres-data"
	managedRecoveryArchiveName = "postgres-data.tar.gz.enc"
	managedRecoveryChunkSize   = 1 << 20
	managedRecoveryMaxManifest = 1 << 20
	managedRecoveryMaxEntries  = 5_000_000
	managedRecoveryMaxBytes    = int64(2) << 40
	managedRecoveryRetention   = 10
)

var managedRecoveryIDPattern = regexp.MustCompile(`^backup-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{8}$`)

type managedRecoveryPoint = operationsv1.BackupManifest

type managedRecoveryStore struct {
	home          string
	dataDir       string
	directory     string
	key           []byte
	sourceVersion string
	now           func() time.Time
}

func createManagedPostgresRecoveryPoint(ctx context.Context, home, encodedKey, sourceVersion string) (*managedRecoveryPoint, error) {
	store, err := newManagedRecoveryStore(home, encodedKey, sourceVersion)
	if err != nil {
		return nil, err
	}
	return store.create(ctx)
}

func verifyManagedPostgresRecoveryPoint(ctx context.Context, home, encodedKey, backupID string) (*managedRecoveryPoint, error) {
	store, err := newManagedRecoveryStore(home, encodedKey, LauncherVersion)
	if err != nil {
		return nil, err
	}
	return store.verify(ctx, backupID)
}

// inspectManagedPostgresRecoveryPoint authenticates the retained manifest and
// encrypted artifact without decrypting the archive. Readiness uses this
// cheaper integrity check; a release drill still calls verify to inspect the
// decrypted PostgreSQL archive before restore.
func inspectManagedPostgresRecoveryPoint(home, encodedKey, backupID string) (*managedRecoveryPoint, error) {
	store, err := newManagedRecoveryStore(home, encodedKey, LauncherVersion)
	if err != nil {
		return nil, err
	}
	manifest, _, err := store.loadAuthenticated(backupID)
	if err != nil {
		return nil, err
	}
	if manifest.Status != operationsv1.BackupVerified {
		return nil, fmt.Errorf("managed recovery point status is %q, want %q", manifest.Status, operationsv1.BackupVerified)
	}
	return manifest, nil
}

func restoreManagedPostgresRecoveryPoint(ctx context.Context, home, encodedKey, backupID string) (*managedRecoveryPoint, error) {
	store, err := newManagedRecoveryStore(home, encodedKey, LauncherVersion)
	if err != nil {
		return nil, err
	}
	return store.restore(ctx, backupID)
}

func newManagedRecoveryStore(home, encodedKey, sourceVersion string) (*managedRecoveryStore, error) {
	home = filepath.Clean(strings.TrimSpace(home))
	if home == "." || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("managed recovery home must be an absolute path")
	}
	key, err := decodeManagedRecoveryKey(encodedKey)
	if err != nil {
		return nil, err
	}
	sourceVersion = strings.TrimSpace(sourceVersion)
	if sourceVersion == "" {
		sourceVersion = LauncherVersion
	}
	return &managedRecoveryStore{
		home:          home,
		dataDir:       filepath.Join(home, "data", "postgres"),
		directory:     filepath.Join(home, "recovery", "managed-postgres"),
		key:           key,
		sourceVersion: sourceVersion,
		now:           func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *managedRecoveryStore) create(ctx context.Context) (*managedRecoveryPoint, error) {
	if err := ensureColdManagedPostgresData(s.dataDir); err != nil {
		return nil, err
	}
	databaseVersion, err := readManagedPostgresVersion(s.dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return nil, fmt.Errorf("create managed recovery directory: %w", err)
	}
	now := s.now().UTC()
	backupID, err := newManagedRecoveryID(now)
	if err != nil {
		return nil, err
	}
	backupDir := filepath.Join(s.directory, backupID)
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		return nil, fmt.Errorf("create managed recovery point: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(backupDir)
		}
	}()

	artifactPath := filepath.Join(backupDir, managedRecoveryArchiveName)
	artifact, err := os.OpenFile(artifactPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create encrypted managed database artifact: %w", err)
	}
	reader, writer := io.Pipe()
	archiveDone := make(chan error, 1)
	go func() {
		archiveErr := writeManagedPostgresArchive(ctx, writer, s.dataDir)
		_ = writer.CloseWithError(archiveErr)
		archiveDone <- archiveErr
	}()
	encryptErr := encryptManagedRecovery(artifact, reader, s.key, backupID)
	if encryptErr != nil {
		_ = reader.CloseWithError(encryptErr)
	}
	closeErr := artifact.Close()
	archiveErr := <-archiveDone
	if encryptErr != nil {
		return nil, fmt.Errorf("encrypt managed database recovery point: %w", encryptErr)
	}
	if archiveErr != nil {
		return nil, fmt.Errorf("archive managed database recovery point: %w", archiveErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close managed database recovery artifact: %w", closeErr)
	}
	digest, size, err := managedRecoveryFileSHA256(artifactPath)
	if err != nil {
		return nil, err
	}
	completedAt := s.now().UTC()
	manifest := &operationsv1.BackupManifest{
		Schema:          operationsv1.Schema,
		BackupID:        backupID,
		SourceVersion:   s.sourceVersion,
		ProtocolVersion: managedRecoveryProtocol,
		Status:          operationsv1.BackupComplete,
		Artifacts: []operationsv1.BackupArtifact{{
			Name: managedRecoveryArtifact, RelativePath: managedRecoveryArchiveName,
			SHA256: digest, SizeBytes: size, Classification: operationsv1.ClassificationSensitive, Encrypted: true,
		}},
		DatabaseEngine: "postgres", DatabaseVersion: databaseVersion,
		CreatedAt: now, CompletedAt: &completedAt,
	}
	if err := sealManagedRecoveryManifest(manifest, s.key); err != nil {
		return nil, err
	}
	if err := writeManagedRecoveryManifest(backupDir, manifest); err != nil {
		return nil, err
	}
	verified, err := s.verify(ctx, backupID)
	if err != nil {
		return nil, fmt.Errorf("verify newly-created managed recovery point: %w", err)
	}
	if err := sealManagedRecoveryManifest(verified, s.key); err != nil {
		return nil, err
	}
	if err := writeManagedRecoveryManifest(backupDir, verified); err != nil {
		return nil, err
	}
	complete = true
	if err := s.prune(); err != nil {
		return nil, err
	}
	return verified, nil
}

func (s *managedRecoveryStore) verify(ctx context.Context, backupID string) (*managedRecoveryPoint, error) {
	manifest, artifactPath, err := s.loadAuthenticated(backupID)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("open managed recovery artifact: %w", err)
	}
	defer file.Close()
	reader, writer := io.Pipe()
	decryptDone := make(chan error, 1)
	go func() {
		decryptErr := decryptManagedRecovery(writer, file, s.key, backupID)
		_ = writer.CloseWithError(decryptErr)
		decryptDone <- decryptErr
	}()
	archiveErr := inspectManagedPostgresArchive(ctx, reader)
	if archiveErr != nil {
		_ = reader.CloseWithError(archiveErr)
	}
	decryptErr := <-decryptDone
	if decryptErr != nil {
		return nil, fmt.Errorf("authenticate managed recovery artifact: %w", decryptErr)
	}
	if archiveErr != nil {
		return nil, fmt.Errorf("validate managed recovery archive: %w", archiveErr)
	}
	verified := *manifest
	verified.Status = operationsv1.BackupVerified
	return &verified, nil
}

func (s *managedRecoveryStore) restore(ctx context.Context, backupID string) (*managedRecoveryPoint, error) {
	manifest, artifactPath, err := s.loadAuthenticated(backupID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(s.dataDir, "postmaster.pid")); err == nil {
		return nil, fmt.Errorf("refuse to restore while managed PostgreSQL may be running")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect managed PostgreSQL process marker: %w", err)
	}
	parent := filepath.Dir(s.dataDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(parent, ".postgres-restore-")
	if err != nil {
		return nil, fmt.Errorf("create managed restore staging directory: %w", err)
	}
	activated := false
	defer func() {
		if !activated {
			_ = os.RemoveAll(staging)
		}
	}()
	file, err := os.Open(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("open managed recovery artifact: %w", err)
	}
	reader, writer := io.Pipe()
	decryptDone := make(chan error, 1)
	go func() {
		decryptErr := decryptManagedRecovery(writer, file, s.key, backupID)
		_ = writer.CloseWithError(decryptErr)
		decryptDone <- decryptErr
	}()
	extractErr := extractManagedPostgresArchive(ctx, reader, staging)
	if extractErr != nil {
		_ = reader.CloseWithError(extractErr)
	}
	decryptErr := <-decryptDone
	closeErr := file.Close()
	if decryptErr != nil {
		return nil, fmt.Errorf("decrypt managed recovery artifact: %w", decryptErr)
	}
	if extractErr != nil {
		return nil, fmt.Errorf("extract managed recovery artifact: %w", extractErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if restoredVersion, err := readManagedPostgresVersion(staging); err != nil || restoredVersion != manifest.DatabaseVersion {
		return nil, fmt.Errorf("restored PostgreSQL version does not match authenticated manifest")
	}
	if err := replaceManagedDataDirectory(staging, s.dataDir); err != nil {
		return nil, err
	}
	activated = true
	verified := *manifest
	verified.Status = operationsv1.BackupVerified
	return &verified, nil
}

func (s *managedRecoveryStore) loadAuthenticated(backupID string) (*operationsv1.BackupManifest, string, error) {
	if !managedRecoveryIDPattern.MatchString(backupID) {
		return nil, "", fmt.Errorf("managed recovery point id is invalid")
	}
	backupDir := filepath.Join(s.directory, backupID)
	manifest, err := readManagedRecoveryManifest(backupDir)
	if err != nil {
		return nil, "", err
	}
	if manifest.BackupID != backupID || manifest.ProtocolVersion != managedRecoveryProtocol || manifest.DatabaseEngine != "postgres" {
		return nil, "", fmt.Errorf("managed recovery manifest identity or format is invalid")
	}
	if err := authenticateManagedRecoveryManifest(manifest, s.key); err != nil {
		return nil, "", err
	}
	if len(manifest.Artifacts) != 1 || manifest.Artifacts[0].Name != managedRecoveryArtifact || manifest.Artifacts[0].RelativePath != managedRecoveryArchiveName {
		return nil, "", fmt.Errorf("managed recovery manifest must contain exactly one database artifact")
	}
	artifactPath := filepath.Join(backupDir, managedRecoveryArchiveName)
	digest, size, err := managedRecoveryFileSHA256(artifactPath)
	if err != nil {
		return nil, "", err
	}
	if !strings.EqualFold(digest, manifest.Artifacts[0].SHA256) || size != manifest.Artifacts[0].SizeBytes {
		return nil, "", fmt.Errorf("managed recovery artifact integrity verification failed")
	}
	return manifest, artifactPath, nil
}

func (s *managedRecoveryStore) prune() error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && managedRecoveryIDPattern.MatchString(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	if len(ids) <= managedRecoveryRetention {
		return nil
	}
	// IDs begin with a sortable UTC timestamp; ReadDir is already filename-sorted.
	for _, id := range ids[:len(ids)-managedRecoveryRetention] {
		if err := os.RemoveAll(filepath.Join(s.directory, id)); err != nil {
			return fmt.Errorf("prune managed recovery point %s: %w", id, err)
		}
	}
	return nil
}

func ensureColdManagedPostgresData(dataDir string) error {
	info, err := os.Lstat(dataDir)
	if err != nil {
		return fmt.Errorf("inspect managed PostgreSQL data directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed PostgreSQL data path must be a real directory")
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "postmaster.pid")); err == nil {
		return fmt.Errorf("refuse to archive managed PostgreSQL while postmaster.pid exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect managed PostgreSQL process marker: %w", err)
	}
	_, err = readManagedPostgresVersion(dataDir)
	return err
}

func readManagedPostgresVersion(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "PG_VERSION")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 32 {
		return "", fmt.Errorf("managed PostgreSQL PG_VERSION is unavailable or invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if version == "" || strings.ContainsAny(version, "/\\\x00\r\n") {
		return "", fmt.Errorf("managed PostgreSQL version is invalid")
	}
	return version, nil
}

func writeManagedPostgresArchive(ctx context.Context, output io.Writer, root string) (returnErr error) {
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	defer func() {
		if err := tarWriter.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
		if err := gzipWriter.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		relative = filepath.Clean(relative)
		if skipManagedPostgresArchivePath(relative, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("managed PostgreSQL data contains unsupported entry %s", filepath.ToSlash(relative))
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		copyErr := copyManagedRecoveryContext(ctx, tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func skipManagedPostgresArchivePath(relative string, directory bool) bool {
	normalized := filepath.ToSlash(relative)
	if normalized == "postmaster.pid" || normalized == "postmaster.opts" {
		return true
	}
	return directory && normalized == "pg_stat_tmp"
}

func inspectManagedPostgresArchive(ctx context.Context, input io.Reader) error {
	return consumeManagedPostgresArchive(ctx, input, "")
}

func extractManagedPostgresArchive(ctx context.Context, input io.Reader, target string) error {
	return consumeManagedPostgresArchive(ctx, input, target)
}

func consumeManagedPostgresArchive(ctx context.Context, input io.Reader, target string) error {
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	seen := make(map[string]struct{})
	entries := 0
	var total int64
	hasVersion := false
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > managedRecoveryMaxEntries {
			return fmt.Errorf("managed recovery archive exceeds entry limit")
		}
		name, err := safeManagedRecoveryArchiveName(header.Name)
		if err != nil {
			return err
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("managed recovery archive contains duplicate entry %s", name)
		}
		seen[name] = struct{}{}
		if name == "PG_VERSION" {
			hasVersion = true
		}
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("managed recovery archive contains unsupported entry type")
		}
		if header.Size < 0 || total > managedRecoveryMaxBytes-header.Size {
			return fmt.Errorf("managed recovery archive exceeds size limit")
		}
		total += header.Size
		if target == "" {
			if _, err := io.Copy(io.Discard, tarReader); err != nil {
				return err
			}
			continue
		}
		destination := filepath.Join(target, filepath.FromSlash(name))
		mode := os.FileMode(header.Mode) & 0o777
		if header.Typeflag == tar.TypeDir {
			if mode == 0 {
				mode = 0o700
			}
			if err := os.MkdirAll(destination, mode); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if mode == 0 {
			mode = 0o600
		}
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		copyErr := copyManagedRecoveryContext(ctx, file, tarReader)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if !hasVersion {
		return fmt.Errorf("managed recovery archive does not contain PG_VERSION")
	}
	return nil
}

func safeManagedRecoveryArchiveName(value string) (string, error) {
	if strings.Contains(value, "\\") {
		return "", fmt.Errorf("managed recovery archive path is not portable")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if value == "" || filepath.IsAbs(filepath.FromSlash(value)) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("managed recovery archive contains unsafe path")
	}
	return clean, nil
}

func replaceManagedDataDirectory(staging, target string) error {
	rollback := target + ".pre-restore"
	if err := os.RemoveAll(rollback); err != nil {
		return fmt.Errorf("remove stale managed database restore rollback: %w", err)
	}
	hadTarget := false
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed PostgreSQL data path must be a real directory")
		}
		if err := os.Rename(target, rollback); err != nil {
			return fmt.Errorf("preserve current managed database: %w", err)
		}
		hadTarget = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		if hadTarget {
			_ = os.Rename(rollback, target)
		}
		return fmt.Errorf("activate restored managed database: %w", err)
	}
	if hadTarget {
		if err := os.RemoveAll(rollback); err != nil {
			return fmt.Errorf("remove pre-restore managed database after activation: %w", err)
		}
	}
	return nil
}

func encryptManagedRecovery(output io.Writer, input io.Reader, key []byte, backupID string) error {
	gcm, err := managedRecoveryGCM(key)
	if err != nil {
		return err
	}
	buffered := bufio.NewWriterSize(output, 64<<10)
	if _, err := io.WriteString(buffered, managedRecoveryMagic); err != nil {
		return err
	}
	buffer := make([]byte, managedRecoveryChunkSize)
	var sequence uint64
	for {
		count, readErr := input.Read(buffer)
		if count > 0 {
			nonce := make([]byte, gcm.NonceSize())
			if _, err := rand.Read(nonce); err != nil {
				return err
			}
			sealed := gcm.Seal(nil, nonce, buffer[:count], managedRecoveryAAD(backupID, sequence))
			if err := binary.Write(buffered, binary.BigEndian, uint32(len(sealed))); err != nil {
				return err
			}
			if _, err := buffered.Write(nonce); err != nil {
				return err
			}
			if _, err := buffered.Write(sealed); err != nil {
				return err
			}
			sequence++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := binary.Write(buffered, binary.BigEndian, uint32(0)); err != nil {
		return err
	}
	return buffered.Flush()
}

func decryptManagedRecovery(output io.Writer, input io.Reader, key []byte, backupID string) error {
	gcm, err := managedRecoveryGCM(key)
	if err != nil {
		return err
	}
	buffered := bufio.NewReaderSize(input, 64<<10)
	header := make([]byte, len(managedRecoveryMagic))
	if _, err := io.ReadFull(buffered, header); err != nil || string(header) != managedRecoveryMagic {
		return fmt.Errorf("managed recovery encryption header is invalid")
	}
	var sequence uint64
	for {
		var size uint32
		if err := binary.Read(buffered, binary.BigEndian, &size); err != nil {
			return err
		}
		if size == 0 {
			return nil
		}
		if size > managedRecoveryChunkSize+uint32(gcm.Overhead()) {
			return fmt.Errorf("managed recovery encrypted chunk exceeds limit")
		}
		nonce := make([]byte, gcm.NonceSize())
		if _, err := io.ReadFull(buffered, nonce); err != nil {
			return err
		}
		sealed := make([]byte, size)
		if _, err := io.ReadFull(buffered, sealed); err != nil {
			return err
		}
		plain, err := gcm.Open(nil, nonce, sealed, managedRecoveryAAD(backupID, sequence))
		if err != nil {
			return err
		}
		if _, err := output.Write(plain); err != nil {
			return err
		}
		sequence++
	}
}

func managedRecoveryGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func managedRecoveryAAD(backupID string, sequence uint64) []byte {
	return []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", managedRecoveryProtocol, backupID, managedRecoveryArtifact, sequence))
}

func decodeManagedRecoveryKey(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("managed recovery key must be 32 bytes encoded as 64 hexadecimal characters")
	}
	return decoded, nil
}

func newManagedRecoveryID(now time.Time) (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "backup-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random), nil
}

func managedRecoveryFileSHA256(path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("managed recovery artifact must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func managedRecoveryManifestDigest(manifest *operationsv1.BackupManifest) (string, error) {
	copy := *manifest
	copy.ManifestSHA256 = ""
	copy.Integrity.Value = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func sealManagedRecoveryManifest(manifest *operationsv1.BackupManifest, key []byte) error {
	manifest.Integrity = operationsv1.IntegrityProof{Algorithm: operationsv1.IntegrityHMACSHA256, KeyID: managedRecoveryKeyID(key)}
	digest, err := managedRecoveryManifestDigest(manifest)
	if err != nil {
		return err
	}
	manifest.ManifestSHA256 = digest
	manifest.Integrity.Value, err = managedRecoveryManifestMAC(manifest, key)
	return err
}

func authenticateManagedRecoveryManifest(manifest *operationsv1.BackupManifest, key []byte) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("validate managed recovery manifest: %w", err)
	}
	expectedDigest, err := managedRecoveryManifestDigest(manifest)
	if err != nil || !strings.EqualFold(expectedDigest, manifest.ManifestSHA256) {
		return fmt.Errorf("managed recovery manifest digest verification failed")
	}
	if manifest.Integrity.Algorithm != operationsv1.IntegrityHMACSHA256 || manifest.Integrity.KeyID != managedRecoveryKeyID(key) {
		return fmt.Errorf("managed recovery manifest key does not match this installation")
	}
	expected, err := managedRecoveryManifestMAC(manifest, key)
	if err != nil {
		return err
	}
	expectedBytes, expectedErr := hex.DecodeString(expected)
	actualBytes, actualErr := hex.DecodeString(manifest.Integrity.Value)
	if expectedErr != nil || actualErr != nil || !hmac.Equal(expectedBytes, actualBytes) {
		return fmt.Errorf("managed recovery manifest authentication failed")
	}
	return nil
}

func managedRecoveryManifestMAC(manifest *operationsv1.BackupManifest, key []byte) (string, error) {
	copy := *manifest
	copy.Integrity.Value = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func managedRecoveryKeyID(key []byte) string {
	digest := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(digest[:8])
}

func writeManagedRecoveryManifest(directory string, manifest *operationsv1.BackupManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "manifest.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func readManagedRecoveryManifest(directory string) (*operationsv1.BackupManifest, error) {
	path := filepath.Join(directory, "manifest.json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read managed recovery manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > managedRecoveryMaxManifest {
		return nil, fmt.Errorf("managed recovery manifest must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var manifest operationsv1.BackupManifest
	decoder := json.NewDecoder(io.LimitReader(file, managedRecoveryMaxManifest+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse managed recovery manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("managed recovery manifest contains trailing data")
	}
	return &manifest, nil
}

func copyManagedRecoveryContext(ctx context.Context, output io.Writer, input io.Reader) error {
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			if _, err := output.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
