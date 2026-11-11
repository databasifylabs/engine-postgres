package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConf(t *testing.T, name string, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestSettingsParsePortAndFirstSocketDirectory(t *testing.T) {
	root := writeConf(t, "postgresql.conf", "listen_addresses = '*'\nport = '6543'\nunix_socket_directories = '/data/run,/tmp'\n")

	settings, err := loadSettings(root)

	if err != nil || settings.Port != 6543 || settings.SocketDirectory != "/data/run" {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

func TestSettingsDefaultsApplyWhenConfIsMissing(t *testing.T) {
	settings, err := loadSettings(t.TempDir())

	want := Settings{Port: 25060, SocketDirectory: "/srv/postgres/run"}
	if err != nil || settings != want {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

func TestSettingsReadPatroniURL(t *testing.T) {
	root := writeConf(t, "patroni.yml", "scope: 'main'\nrestapi:\n  listen: '0.0.0.0:8009'\n  connect_address: '10.0.0.1:8009'\netcd3:\n  hosts: 'a:2379'\n")

	settings, err := loadSettings(root)

	if err != nil || settings.PatroniURL != "http://127.0.0.1:8009" {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

const clusterPatroniConf = `scope: 'main'
name: '10.0.0.5'
restapi:
  listen: '0.0.0.0:8008'
  connect_address: '10.0.0.5:8008'
  authentication:
    username: patroni
    password: s3cr:et
etcd3:
  hosts: '10.0.0.5:2379,10.0.0.6:2379'
bootstrap:
  dcs:
    ttl: 30
    postgresql:
      use_pg_rewind: true
      parameters:
        wal_level: 'replica'
        unix_socket_directories: '/wrong/bootstrap'
  initdb:
    - encoding: UTF8
    - data-checksums
postgresql:
  listen: '*:25061'
  connect_address: '10.0.0.5:25061'
  data_dir: '/srv/postgres/data'
  authentication:
    superuser:
      username: postgres
      password: pw
  parameters:
    ssl: 'on'
    unix_socket_directories: '/srv/postgres/socket'
    log_destination: 'stderr'
  pg_hba:
    - 'local all postgres peer'
tags:
  nofailover: false
`

func TestSettingsReadAClusterNodeFromPatroniYml(t *testing.T) {
	root := writeConf(t, "patroni.yml", clusterPatroniConf)

	settings, err := loadSettings(root)

	want := Settings{Port: 25061, SocketDirectory: "/srv/postgres/socket", PatroniURL: "http://127.0.0.1:8008"}
	if err != nil || settings != want {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

func TestSettingsRejectAnInvalidPatroniListen(t *testing.T) {
	root := writeConf(t, "patroni.yml", "postgresql:\n  listen: '25060'\n")

	_, err := loadSettings(root)

	if err == nil || !strings.Contains(err.Error(), "invalid postgresql listen") || !strings.Contains(err.Error(), "patroni.yml") {
		t.Fatalf("err=%v", err)
	}
}

func TestSettingsRejectInvalidPort(t *testing.T) {
	root := writeConf(t, "postgresql.conf", "port = 'abc'\n")

	settings, err := loadSettings(root)

	if err == nil || !strings.Contains(err.Error(), "invalid port") || !strings.Contains(err.Error(), "postgresql.conf") {
		t.Fatalf("err=%v", err)
	}
	if settings.Port != 25060 || settings.SocketDirectory != "/srv/postgres/run" {
		t.Fatalf("settings=%+v", settings)
	}
}

func TestSettingsStripTrailingComments(t *testing.T) {
	root := writeConf(t, "postgresql.conf", "port = '6543' # custom\nunix_socket_directories = /data/run # sockets\n")

	settings, err := loadSettings(root)

	if err != nil || settings.Port != 6543 || settings.SocketDirectory != "/data/run" {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
}

func TestMalformedConfMakesAFailingPsql(t *testing.T) {
	loadErr := errors.New("invalid port \"abc\" in /srv/postgres/conf/postgresql.conf")

	_, err := psqlFor(Settings{}, loadErr).Query(context.Background(), "postgres", "SELECT 1", nil)

	if !errors.Is(err, loadErr) {
		t.Fatalf("err=%v", err)
	}
}
