//go:build windows

package codexobserver

import (
	"context"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsPlatform struct{}

func newPlatformDiscoverer() platformDiscoverer { return windowsPlatform{} }

func (windowsPlatform) discover(
	ctx context.Context,
	_ []string,
) (mappingStrength, []processObservation, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return mappingWeak, nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err = windows.Process32First(snapshot, &entry); err != nil {
		return mappingWeak, nil, err
	}
	observations := make([]processObservation, 0, 8)
	for {
		select {
		case <-ctx.Done():
			return mappingWeak, nil, ctx.Err()
		default:
		}
		name := windows.UTF16ToString(entry.ExeFile[:])
		if isCodexProcessName(name) && entry.ProcessID != 0 {
			if len(observations) >= maximumProcesses {
				return mappingWeak, nil, errors.New("Windows Codex process bound exceeded")
			}
			observed, identityErr := windowsProcessIdentity(entry.ProcessID)
			if identityErr != nil {
				return mappingWeak, nil, errors.New("Windows process identity unavailable")
			}
			observations = append(observations, processObservation{identity: observed})
		}
		err = windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return mappingWeak, nil, err
		}
	}
	return mappingWeak, observations, nil
}

func windowsProcessIdentity(pid uint32) (processIdentity, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return processIdentity{}, err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &created, &exited, &kernel, &user) != nil {
		return processIdentity{}, errors.New("process start time unavailable")
	}
	return processIdentity{pid: int(pid), startedUnixNano: created.Nanoseconds()}, nil
}
