//go:build darwin

package codexobserver

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maximumLsofOutput = 1 << 20
	maximumLsofError  = 4 << 10
	lsofTimeout       = 2 * time.Second
)

type darwinPlatform struct{}

type boundedBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	overflow bool
}

func newPlatformDiscoverer() platformDiscoverer { return darwinPlatform{} }

func (buffer *boundedBuffer) Write(document []byte) (int, error) {
	written := len(document)
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return written, nil
	}
	if len(document) > remaining {
		_, _ = buffer.buffer.Write(document[:remaining])
		buffer.overflow = true
		return written, nil
	}
	_, _ = buffer.buffer.Write(document)
	return written, nil
}

func (buffer *boundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

func (buffer *boundedBuffer) Clear() {
	clear(buffer.buffer.Bytes())
	buffer.buffer.Reset()
}

func (darwinPlatform) discover(
	ctx context.Context,
	candidates []string,
) (mappingStrength, []processObservation, error) {
	observations, err := darwinCodexProcesses()
	if err != nil {
		return mappingStrong, nil, err
	}
	if len(candidates) == 0 {
		return mappingStrong, []processObservation{}, nil
	}
	if len(observations) > maximumProcesses {
		return mappingStrong, nil, errors.New("macOS Codex process bound exceeded")
	}
	if len(observations) == 0 {
		return mappingStrong, observations, nil
	}
	pids := make([]string, len(observations))
	for index := range observations {
		pids[index] = strconv.Itoa(observations[index].identity.pid)
	}
	discoveryContext, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()
	command := exec.CommandContext(
		discoveryContext,
		"/usr/sbin/lsof",
		"-nP",
		"-p",
		strings.Join(pids, ","),
		"-Fn",
	)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	command.WaitDelay = time.Second
	stdout := &boundedBuffer{maximum: maximumLsofOutput}
	stderr := &boundedBuffer{maximum: maximumLsofError}
	defer stdout.Clear()
	defer stderr.Clear()
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	if stdout.overflow || stderr.overflow || discoveryContext.Err() != nil {
		return mappingStrong, nil, errors.New("macOS process/file discovery exceeded its bound")
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 &&
			len(bytes.TrimSpace(stderr.Bytes())) == 0 {
			return mappingStrong, observations, nil
		}
		return mappingStrong, nil, errors.New("macOS process/file discovery failed")
	}
	allowedPaths := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		allowedPaths[candidate] = struct{}{}
	}
	pathsByPID := parseLsofNames(stdout.Bytes(), allowedPaths)
	current, err := darwinCodexProcesses()
	if err != nil {
		return mappingStrong, nil, err
	}
	stable, err := reconcileDarwinProcesses(observations, current, pathsByPID)
	if err != nil {
		return mappingStrong, nil, err
	}
	return mappingStrong, stable, nil
}

func darwinCodexProcesses() ([]processObservation, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	observations := make([]processObservation, 0, 8)
	for _, process := range processes {
		name := cString(process.Proc.P_comm[:])
		if !isCodexProcessName(name) || process.Proc.P_pid <= 0 ||
			process.Proc.P_starttime.Sec <= 0 {
			continue
		}
		started := time.Unix(
			process.Proc.P_starttime.Sec,
			int64(process.Proc.P_starttime.Usec)*int64(time.Microsecond),
		)
		observations = append(observations, processObservation{
			identity: processIdentity{
				pid:             int(process.Proc.P_pid),
				startedUnixNano: started.UnixNano(),
			},
		})
	}
	return observations, nil
}

func reconcileDarwinProcesses(
	before []processObservation,
	after []processObservation,
	pathsByPID map[int][]string,
) ([]processObservation, error) {
	afterByPID := make(map[int]processIdentity, len(after))
	for _, process := range after {
		afterByPID[process.identity.pid] = process.identity
	}
	stable := make([]processObservation, 0, len(before))
	for _, process := range before {
		current, exists := afterByPID[process.identity.pid]
		if !exists {
			continue
		}
		if current != process.identity {
			return nil, errors.New("macOS process identity changed during discovery")
		}
		process.openFiles = append([]string(nil), pathsByPID[process.identity.pid]...)
		stable = append(stable, process)
	}
	return stable, nil
}

func cString(value []byte) string {
	if end := bytes.IndexByte(value, 0); end >= 0 {
		value = value[:end]
	}
	return string(value)
}

func parseLsofNames(document []byte, allowed map[string]struct{}) map[int][]string {
	result := make(map[int][]string)
	currentPID := 0
	for _, line := range bytes.Split(document, []byte{'\n'}) {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			parsed, err := strconv.Atoi(string(line[1:]))
			if err != nil || parsed <= 0 {
				currentPID = 0
				continue
			}
			currentPID = parsed
		case 'n':
			if currentPID == 0 {
				continue
			}
			path := string(line[1:])
			if _, accepted := allowed[path]; accepted {
				result[currentPID] = append(result[currentPID], path)
			}
		}
	}
	return result
}
