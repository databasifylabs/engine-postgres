package postgres

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultRoot            = "/srv/postgres"
	defaultPort            = 25060
	defaultSocketDirectory = "/srv/postgres/run"
)

type Settings struct {
	Port            int
	SocketDirectory string
	PatroniURL      string
}

func loadSettings(root string) (Settings, error) {
	settings := Settings{Port: defaultPort, SocketDirectory: defaultSocketDirectory}
	if err := applyFile(&settings, filepath.Join(root, "conf", "patroni.yml"), applyPatroniConf); err != nil {
		return settings, err
	}
	if err := applyFile(&settings, filepath.Join(root, "conf", "postgresql.conf"), applyServerConf); err != nil {
		return settings, err
	}

	return settings, nil
}

func applyFile(settings *Settings, path string, apply func(*Settings, []string) error) error {
	lines, err := linesOf(path)
	if err != nil {
		return err
	}
	if err := apply(settings, lines); err != nil {
		return fmt.Errorf("%w in %s", err, path)
	}

	return nil
}

func linesOf(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, scanner.Err()
}

func applyServerConf(settings *Settings, lines []string) error {
	for _, line := range lines {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if err := applySetting(settings, strings.TrimSpace(key), unquoted(value)); err != nil {
			return err
		}
	}

	return nil
}

func applySetting(settings *Settings, key string, value string) error {
	if value == "" {
		return nil
	}
	switch key {
	case "port":
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid port %q", value)
		}
		settings.Port = port
	case "unix_socket_directories":
		first, _, _ := strings.Cut(value, ",")
		settings.SocketDirectory = strings.TrimSpace(first)
	}

	return nil
}

func unquoted(value string) string {
	value = strings.TrimSpace(value)
	if rest, found := strings.CutPrefix(value, "'"); found {
		quoted, _, _ := strings.Cut(rest, "'")

		return quoted
	}
	withoutComment, _, _ := strings.Cut(value, "#")

	return strings.TrimSpace(withoutComment)
}

func applyPatroniConf(settings *Settings, lines []string) error {
	restapiPort, err := listenPortIn(lines, "restapi")
	if err != nil {
		return err
	}
	if restapiPort != "" {
		settings.PatroniURL = "http://127.0.0.1:" + restapiPort
	}
	serverPort, err := listenPortIn(lines, "postgresql")
	if err != nil {
		return err
	}
	if err := applySetting(settings, "port", serverPort); err != nil {
		return err
	}

	return applySetting(settings, "unix_socket_directories", patroniValueIn(lines, "postgresql.parameters.unix_socket_directories"))
}

func listenPortIn(lines []string, section string) (string, error) {
	listen := patroniValueIn(lines, section+".listen")
	if listen == "" {
		return "", nil
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("invalid %s listen %q", section, listen)
	}

	return port, nil
}

func patroniValueIn(lines []string, path string) string {
	var keys []string
	var indents []int
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		key, value, found := strings.Cut(trimmed, ":")
		if !found || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		indent := indentOf(line)
		for len(indents) > 0 && indents[len(indents)-1] >= indent {
			keys, indents = keys[:len(keys)-1], indents[:len(indents)-1]
		}
		keys, indents = append(keys, key), append(indents, indent)
		if strings.Join(keys, ".") == path {
			return unquoted(value)
		}
	}

	return ""
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
