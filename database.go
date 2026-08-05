package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type managedDatabase struct {
	home     string
	binDir   string
	dataDir  string
	logPath  string
	port     uint32
	user     string
	password string
	database string
	started  bool
}

func newManagedDatabase(home, installDir, binDir, password string) *managedDatabase {
	if strings.TrimSpace(binDir) == "" {
		binDir = "bin"
	}
	return &managedDatabase{
		home: home, binDir: filepath.Join(installDir, filepath.FromSlash(binDir)),
		dataDir: filepath.Join(home, "data", "postgres"), logPath: filepath.Join(home, "logs", "postgres.log"),
		port: defaultDatabasePort, user: defaultDatabaseUser, password: password, database: defaultDatabaseName,
	}
}

func (d *managedDatabase) Start(ctx context.Context) error {
	if err := d.validateBinaries(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.logPath), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(d.dataDir, "PG_VERSION")); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect postgres data directory: %w", err)
		}
		if err := d.initialize(ctx); err != nil {
			return err
		}
	}
	if d.runningFromData(ctx) {
		d.started = true
		return nil
	}
	if !portAvailable(d.port) {
		return fmt.Errorf("database port %d is occupied by another program", d.port)
	}
	args := []string{"-D", d.dataDir, "-l", d.logPath, "-w", "-t", "60", "start", "-o", fmt.Sprintf("-h 127.0.0.1 -p %d", d.port)}
	if output, err := exec.CommandContext(ctx, d.binary("pg_ctl"), args...).CombinedOutput(); err != nil {
		return fmt.Errorf("start postgres: %w: %s", err, strings.TrimSpace(string(output)))
	}
	d.started = true
	return nil
}

func (d *managedDatabase) Stop(ctx context.Context) error {
	if !d.started {
		return nil
	}
	output, err := exec.CommandContext(ctx, d.binary("pg_ctl"), "-D", d.dataDir, "-w", "-t", "30", "stop", "-m", "fast").CombinedOutput()
	d.started = false
	if err != nil {
		return fmt.Errorf("stop postgres: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *managedDatabase) initialize(ctx context.Context) error {
	if err := os.MkdirAll(d.dataDir, 0o700); err != nil {
		return err
	}
	passwordFile := filepath.Join(d.home, "postgres-password.tmp")
	if err := os.WriteFile(passwordFile, []byte(d.password+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(passwordFile)
	args := []string{"-D", d.dataDir, "-U", d.user, "--pwfile", passwordFile, "--encoding", "UTF8", "--auth-host", "scram-sha-256", "--auth-local", "trust"}
	if output, err := exec.CommandContext(ctx, d.binary("initdb"), args...).CombinedOutput(); err != nil {
		return fmt.Errorf("initialize postgres: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := d.createDatabase(ctx); err != nil {
		return err
	}
	settings := fmt.Sprintf("\nlisten_addresses = '127.0.0.1'\nport = %d\nmax_connections = 100\n", d.port)
	file, err := os.OpenFile(filepath.Join(d.dataDir, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(settings)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (d *managedDatabase) createDatabase(ctx context.Context) error {
	command := exec.CommandContext(ctx, d.binary("postgres"), "--single", "-D", d.dataDir, "postgres")
	command.Stdin = strings.NewReader("CREATE DATABASE " + quoteIdentifier(d.database) + ";\n")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("create database %s: %w: %s", d.database, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (d *managedDatabase) runningFromData(ctx context.Context) bool {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return exec.CommandContext(requestCtx, d.binary("pg_ctl"), "-D", d.dataDir, "status").Run() == nil
}

func (d *managedDatabase) validateBinaries() error {
	for _, name := range []string{"initdb", "pg_ctl", "postgres"} {
		if _, err := os.Stat(d.binary(name)); err != nil {
			return fmt.Errorf("postgres package is missing %s: %w", d.binary(name), err)
		}
	}
	return nil
}

func (d *managedDatabase) binary(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(d.binDir, name)
}

func portAvailable(port uint32) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
