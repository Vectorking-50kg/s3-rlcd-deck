package codexappserver

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	maximumProcessLine  = defaultMaximumDocument + 1
	processCloseTimeout = 2 * time.Second
)

var appServerEnvironmentAllowlist = map[string]struct{}{
	"APPDATA":         {},
	"CODEX_HOME":      {},
	"COMSPEC":         {},
	"HOMEDRIVE":       {},
	"HOMEPATH":        {},
	"HOME":            {},
	"LANG":            {},
	"LC_ALL":          {},
	"LC_CTYPE":        {},
	"LOCALAPPDATA":    {},
	"LOGNAME":         {},
	"PATH":            {},
	"PATHEXT":         {},
	"SHELL":           {},
	"SYSTEMROOT":      {},
	"TEMP":            {},
	"TMP":             {},
	"TMPDIR":          {},
	"TZ":              {},
	"USER":            {},
	"USERNAME":        {},
	"USERPROFILE":     {},
	"WINDIR":          {},
	"XDG_CACHE_HOME":  {},
	"XDG_CONFIG_HOME": {},
	"XDG_DATA_HOME":   {},
}

type ProcessConnector struct {
	Binary string
}

func (connector ProcessConnector) Connect(ctx context.Context) (Connection, error) {
	binary := connector.Binary
	if binary == "" {
		binary = "codex"
	}
	command := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	command.Env = appServerEnvironment(os.Environ())
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		discardCloser(stdin)
		return nil, ErrUnavailable
	}
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		discardCloser(stdin)
		discardCloser(stdout)
		return nil, ErrUnavailable
	}
	connection := &processConnection{
		command: command,
		stdin:   stdin,
		lines:   make(chan processLine, 16),
		waited:  make(chan struct{}),
		closed:  make(chan struct{}),
	}
	go connection.scan(stdout)
	go func() {
		_ = command.Wait()
		close(connection.waited)
	}()
	return connection, nil
}

func appServerEnvironment(source []string) []string {
	environment := make([]string, 0, len(appServerEnvironmentAllowlist))
	seen := make(map[string]struct{}, len(appServerEnvironmentAllowlist))
	for _, entry := range source {
		name, _, present := strings.Cut(entry, "=")
		canonical := strings.ToUpper(name)
		if !present || name == "" {
			continue
		}
		if _, allowed := appServerEnvironmentAllowlist[canonical]; !allowed {
			continue
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		environment = append(environment, entry)
	}
	return environment
}

type processLine struct {
	document []byte
	err      error
}

type processConnection struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	lines   chan processLine
	waited  chan struct{}
	closed  chan struct{}

	writeMutex sync.Mutex
	closeOnce  sync.Once
}

func (connection *processConnection) scan(stdout io.ReadCloser) {
	defer discardCloser(stdout)
	defer close(connection.lines)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maximumProcessLine)
	for scanner.Scan() {
		document := append([]byte(nil), scanner.Bytes()...)
		select {
		case connection.lines <- processLine{document: document}:
		case <-connection.closed:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case connection.lines <- processLine{err: ErrProcessExited}:
		case <-connection.closed:
		}
	}
}

func (connection *processConnection) Read(ctx context.Context) ([]byte, error) {
	select {
	case line, open := <-connection.lines:
		if !open {
			return nil, io.EOF
		}
		return line.document, line.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-connection.closed:
		return nil, io.EOF
	}
}

func (connection *processConnection) Write(ctx context.Context, document []byte) error {
	if len(document) == 0 || len(document) > defaultMaximumDocument {
		return ErrSchemaChanged
	}
	connection.writeMutex.Lock()
	defer connection.writeMutex.Unlock()
	written := make(chan error, 1)
	go func() {
		payload := make([]byte, len(document)+1)
		copy(payload, document)
		payload[len(document)] = '\n'
		_, err := connection.stdin.Write(payload)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			return ErrProcessExited
		}
		return nil
	case <-ctx.Done():
		_ = connection.Close()
		return ctx.Err()
	}
}

func (connection *processConnection) Close() error {
	connection.closeOnce.Do(func() {
		close(connection.closed)
		discardCloser(connection.stdin)
		if connection.command.Process != nil {
			_ = connection.command.Process.Kill()
		}
	})
	timer := time.NewTimer(processCloseTimeout)
	defer timer.Stop()
	select {
	case <-connection.waited:
		return nil
	case <-timer.C:
		return ErrProcessExited
	}
}
