package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	if _, err := os.Stat(filepath.Join(d.dataDir, "PG_VERSION")); os.IsNotExist(err) {
		if err := d.initialize(ctx); err != nil {
			return err
		}
	}
	if d.ready(ctx) {
		if d.runningFromData(ctx) {
			d.started = true
			return d.createDatabase(ctx)
		}
		return fmt.Errorf("database port %d is already occupied by another PostgreSQL instance", d.port)
	}
	if !portAvailable(d.port) {
		return fmt.Errorf("database port %d is occupied by another program", d.port)
	}
	args := []string{"-D", d.dataDir, "-l", d.logPath, "-w", "-t", "60", "start", "-o", fmt.Sprintf("-h 127.0.0.1 -p %d", d.port)}
	if output, err := exec.CommandContext(ctx, d.binary("pg_ctl"), args...).CombinedOutput(); err != nil {
		return fmt.Errorf("start postgres: %w: %s", err, strings.TrimSpace(string(output)))
	}
	d.started = true
	if err := d.waitReady(ctx, 60*time.Second); err != nil {
		return err
	}
	return d.createDatabase(ctx)
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
	env := append(os.Environ(), "PGPASSWORD="+d.password)
	query := "SELECT 1 FROM pg_database WHERE datname='" + strings.ReplaceAll(d.database, "'", "''") + "'"
	check := exec.CommandContext(ctx, d.binary("psql"), "-h", "127.0.0.1", "-p", strconv.Itoa(int(d.port)), "-U", d.user, "-d", "postgres", "-tAc", query)
	check.Env = env
	output, err := check.CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect postgres database: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) == "1" {
		return nil
	}
	create := exec.CommandContext(ctx, d.binary("createdb"), "-h", "127.0.0.1", "-p", strconv.Itoa(int(d.port)), "-U", d.user, d.database)
	create.Env = env
	if output, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("create database %s: %w: %s", d.database, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *managedDatabase) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("postgres did not become ready within %s; check %s", timeout, d.logPath)
		case <-ticker.C:
			if d.ready(ctx) {
				return nil
			}
		}
	}
}

func (d *managedDatabase) ready(ctx context.Context) bool {
	requestCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	command := exec.CommandContext(requestCtx, d.binary("pg_isready"), "-h", "127.0.0.1", "-p", strconv.Itoa(int(d.port)), "-U", d.user)
	return command.Run() == nil
}

func (d *managedDatabase) runningFromData(ctx context.Context) bool {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return exec.CommandContext(requestCtx, d.binary("pg_ctl"), "-D", d.dataDir, "status").Run() == nil
}

func (d *managedDatabase) validateBinaries() error {
	for _, name := range []string{"initdb", "pg_ctl", "pg_isready", "psql", "createdb"} {
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
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
