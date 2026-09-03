package deployment

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagedRecoveryCreateVerifyRestore(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data", "postgres")
	writeRecoveryFixture(t, dataDir, "before-upgrade")
	if err := os.WriteFile(filepath.Join(dataDir, "postmaster.opts"), []byte("transient"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a1", 32)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	point, err := createManagedPostgresRecoveryPoint(ctx, home, key, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if point.Status != "VERIFIED" || point.ProtocolVersion != managedRecoveryProtocol {
		t.Fatalf("unexpected recovery point: %+v", point)
	}
	if _, err := verifyManagedPostgresRecoveryPoint(ctx, home, key, point.BackupID); err != nil {
		t.Fatalf("verify managed recovery point: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "base", "42", "row"), []byte("after-upgrade"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManagedPostgresRecoveryPoint(ctx, home, key, point.BackupID); err != nil {
		t.Fatalf("restore managed recovery point: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "base", "42", "row"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before-upgrade" {
		t.Fatalf("restored data = %q, want pre-upgrade value", data)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "postmaster.opts")); !os.IsNotExist(err) {
		t.Fatalf("transient postmaster.opts was restored: %v", err)
	}
}

func TestManagedRecoveryRejectsTampering(t *testing.T) {
	home := t.TempDir()
	writeRecoveryFixture(t, filepath.Join(home, "data", "postgres"), "protected")
	key := strings.Repeat("b2", 32)
	point, err := createManagedPostgresRecoveryPoint(context.Background(), home, key, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(home, "recovery", "managed-postgres", point.BackupID, managedRecoveryArchiveName)
	file, err := os.OpenFile(artifact, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tampered")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyManagedPostgresRecoveryPoint(context.Background(), home, key, point.BackupID); err == nil || !strings.Contains(err.Error(), "integrity verification failed") {
		t.Fatalf("tampered artifact was not rejected: %v", err)
	}
}

func TestManagedRecoveryReadinessInventoryAuthenticatesEveryPoint(t *testing.T) {
	home := t.TempDir()
	writeRecoveryFixture(t, filepath.Join(home, "data", "postgres"), "protected")
	key := strings.Repeat("b3", 32)
	point, err := createManagedPostgresRecoveryPoint(context.Background(), home, key, "v0.9.0")
	if err != nil {
		t.Fatal(err)
	}
	count, err := authenticatedManagedRecoveryInventory(home, key)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("authenticated managed recovery count = %d, want 1", count)
	}

	artifact := filepath.Join(home, "recovery", "managed-postgres", point.BackupID, managedRecoveryArchiveName)
	file, err := os.OpenFile(artifact, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tampered")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticatedManagedRecoveryInventory(home, key); err == nil || !strings.Contains(err.Error(), "integrity verification failed") {
		t.Fatalf("tampered recovery point was counted as ready: %v", err)
	}
}

func TestManagedRecoveryReadinessRejectsUntrustedManifest(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "recovery", "managed-postgres", "backup-20260818T120000Z-deadbeef")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticatedManagedRecoveryInventory(home, strings.Repeat("c4", 32)); err == nil {
		t.Fatal("untrusted manifest was counted as an authenticated recovery point")
	}
}

func TestManagedRecoveryRequiresStoppedDatabase(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "data", "postgres")
	writeRecoveryFixture(t, dataDir, "protected")
	if err := os.WriteFile(filepath.Join(dataDir, "postmaster.pid"), []byte("123"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := createManagedPostgresRecoveryPoint(context.Background(), home, strings.Repeat("c3", 32), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "postmaster.pid") {
		t.Fatalf("active-looking database was archived: %v", err)
	}
}

func TestManagedDatabaseValidationDoesNotRequireLogicalBackupTools(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "postgres", "16")
	writeTestDatabaseBinaries(t, root)
	database := newManagedDatabase(home, root, "bin", "")
	if err := database.validateBinaries(); err != nil {
		t.Fatalf("server-only embedded package was rejected: %v", err)
	}
	if database.optionalBinary("pg_dump") != "" || database.optionalBinary("pg_restore") != "" {
		t.Fatal("missing logical backup tools were reported as available")
	}
}

func TestGeneratedConfigDisablesLogicalBackupWithoutClientTools(t *testing.T) {
	home := t.TempDir()
	state := &launcherState{
		DBPassword: "secret", InternalServiceToken: "token",
		BackupEncryptionKey: strings.Repeat("d4", 32),
	}
	paths, err := writeGeneratedConfigs(home, state, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, expected := range []string{`backup_dir: ""`, `encryption_key_file: ""`, `pg_dump_path: ""`, `pg_restore_path: ""`} {
		if !strings.Contains(content, expected) {
			t.Fatalf("generated config does not fail closed without client tools; missing %q\n%s", expected, content)
		}
	}
}

func TestManagedPostgresBackupUpgradeRollbackDrill(t *testing.T) {
	postgresRoot := strings.TrimSpace(os.Getenv("ATHENA_W0_POSTGRES_ROOT"))
	if postgresRoot == "" {
		t.Skip("set ATHENA_W0_POSTGRES_ROOT to run the production-like PostgreSQL recovery drill")
	}
	if !filepath.IsAbs(postgresRoot) {
		t.Fatal("ATHENA_W0_POSTGRES_ROOT must be absolute")
	}
	home := t.TempDir()
	database := newManagedDatabase(home, postgresRoot, "bin", "w0-drill-password")
	database.port = reserveTestPort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Logf("DRILL step=start_database package=%s port=%d", filepath.Base(postgresRoot), database.port)
	if err := database.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	beforeSQL := `CREATE TABLE athena_w0_recovery_probe (id integer PRIMARY KEY, value text NOT NULL); INSERT INTO athena_w0_recovery_probe VALUES (1, 'before-upgrade');`
	runPostgresSingleUser(t, ctx, database, beforeSQL)

	keyBytes := make([]byte, 32)
	for index := range keyBytes {
		keyBytes[index] = byte(index + 1)
	}
	key := hex.EncodeToString(keyBytes)
	t.Log("DRILL step=create_encrypted_recovery_point database_state=before-upgrade")
	point, err := createManagedPostgresRecoveryPoint(ctx, home, key, "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DRILL step=verify_recovery_point backup_id=%s status=%s", point.BackupID, point.Status)

	runPostgresSingleUser(t, ctx, database, `ALTER TABLE athena_w0_recovery_probe ADD COLUMN migration_marker text; UPDATE athena_w0_recovery_probe SET value = 'after-upgrade', migration_marker = 'v-next';`)
	t.Log("DRILL step=apply_upgrade_probe schema_change=true data_change=true")
	if _, err := restoreManagedPostgresRecoveryPoint(ctx, home, key, point.BackupID); err != nil {
		t.Fatal(err)
	}
	t.Log("DRILL step=restore_pre_upgrade_recovery_point result=activated")

	output := runPostgresSingleUser(t, ctx, database, `SELECT value FROM athena_w0_recovery_probe WHERE id = 1;`)
	if !strings.Contains(output, "before-upgrade") || strings.Contains(output, "after-upgrade") {
		t.Fatalf("rollback verification returned unexpected data: %s", output)
	}
	if err := database.Start(ctx); err != nil {
		t.Fatalf("restored database failed health start: %v", err)
	}
	if err := database.Stop(ctx); err != nil {
		t.Fatalf("restored database failed clean stop: %v", err)
	}
	t.Log("DRILL step=verify_restored_database value=before-upgrade health=start-stop-pass result=PASS")
}

func writeRecoveryFixture(t *testing.T, dataDir, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dataDir, "base", "42"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "base", "42", "row"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reserveTestPort(t *testing.T) uint32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return uint32(port)
}

func runPostgresSingleUser(t *testing.T, ctx context.Context, database *managedDatabase, sql string) string {
	t.Helper()
	command := exec.CommandContext(ctx, database.binary("postgres"), "--single", "-D", database.dataDir, database.database)
	command.Stdin = strings.NewReader(sql + "\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("single-user postgres command failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

func TestManagedRecoveryRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges on Windows")
	}
	home := t.TempDir()
	dataDir := filepath.Join(home, "data", "postgres")
	writeRecoveryFixture(t, dataDir, "protected")
	if err := os.Symlink(filepath.Join(dataDir, "PG_VERSION"), filepath.Join(dataDir, "unsafe-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := createManagedPostgresRecoveryPoint(context.Background(), home, strings.Repeat("e5", 32), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "unsupported entry") {
		t.Fatalf("symlink was archived: %v", err)
	}
}
