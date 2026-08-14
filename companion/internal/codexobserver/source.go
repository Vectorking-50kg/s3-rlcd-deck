package codexobserver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maximumWalkEntries = 10_000
	maximumCandidates  = 64
	maximumFirstLine   = 64 << 10
	maximumProcesses   = 32
)

type platformDiscoverer interface {
	discover(context.Context, []string) (mappingStrength, []processObservation, error)
}

type systemSource struct {
	root      string
	platform  platformDiscoverer
	retention time.Duration
	now       func() time.Time
}

type scannedFile struct {
	parts []string
	path  string
	info  os.FileInfo
}

func newSystemSource(
	configuredRoot string,
	retention time.Duration,
	now func() time.Time,
) (snapshotSource, error) {
	root := configuredRoot
	if root == "" {
		root = os.Getenv("CODEX_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, ErrInvalidConfig
			}
			root = filepath.Join(home, ".codex")
		}
		root = filepath.Join(root, "sessions")
	}
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil || absolute == string(filepath.Separator) {
		return nil, ErrInvalidConfig
	}
	return &systemSource{
		root: absolute, platform: newPlatformDiscoverer(), retention: retention, now: now,
	}, nil
}

func (source *systemSource) discover(ctx context.Context) (discoverySnapshot, error) {
	files, overflow, err := source.scan(ctx)
	if err != nil {
		return discoverySnapshot{}, err
	}
	paths := make([]string, len(files))
	for index := range files {
		paths[index] = files[index].path
	}
	strength, processes, err := source.platform.discover(ctx, paths)
	if err != nil {
		clearFileCandidateLines(files)
		return discoverySnapshot{}, err
	}
	return discoverySnapshot{
		strength: strength, processes: processes, files: files, candidatesOverflow: overflow,
	}, nil
}

func (source *systemSource) scan(ctx context.Context) ([]fileCandidate, bool, error) {
	rootInfo, err := os.Lstat(source.root)
	if errors.Is(err, os.ErrNotExist) {
		return []fileCandidate{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, ErrInvalidConfig
	}
	root, err := secureOpenRoot(source.root)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	openedRootInfo, err := root.Stat()
	if err != nil {
		return nil, false, err
	}
	if !os.SameFile(rootInfo, openedRootInfo) {
		return nil, false, ErrInvalidConfig
	}
	files := make([]scannedFile, 0, maximumCandidates)
	entries := 0
	if err = walkPinnedTree(ctx, source.root, root, nil, 0, &entries, &files); err != nil {
		return nil, false, err
	}
	if source.retention > 0 && source.now != nil {
		cutoff := source.now().UTC().Add(-source.retention)
		retained := files[:0]
		for _, candidate := range files {
			if !candidate.info.ModTime().Before(cutoff) {
				retained = append(retained, candidate)
			}
		}
		files = retained
	}
	sort.Slice(files, func(left, right int) bool {
		if !files[left].info.ModTime().Equal(files[right].info.ModTime()) {
			return files[left].info.ModTime().After(files[right].info.ModTime())
		}
		return files[left].path < files[right].path
	})
	overflow := len(files) > maximumCandidates
	if overflow {
		files = files[:maximumCandidates]
	}
	result := make([]fileCandidate, 0, len(files))
	for _, candidate := range files {
		line, openedInfo, readErr := readPinnedFirstLine(root, candidate.parts, candidate.info)
		if readErr != nil {
			clearFileCandidateLines(result)
			return nil, false, readErr
		}
		if len(line) == 0 {
			continue
		}
		result = append(result, fileCandidate{
			path:      candidate.path,
			size:      openedInfo.Size(),
			modified:  openedInfo.ModTime().UTC(),
			firstLine: line,
			info:      openedInfo,
		})
	}
	return result, overflow, nil
}

func clearFileCandidateLines(files []fileCandidate) {
	for index := range files {
		clear(files[index].firstLine)
		files[index].firstLine = nil
	}
}

func walkPinnedTree(
	ctx context.Context,
	rootPath string,
	directory *os.File,
	parts []string,
	depth int,
	visited *int,
	files *[]scannedFile,
) error {
	for {
		children, readErr := directory.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, child := range children {
			if err := visitPinnedChild(
				ctx, rootPath, directory, child, parts, depth, visited, files,
			); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func visitPinnedChild(
	ctx context.Context,
	rootPath string,
	directory *os.File,
	child os.DirEntry,
	parts []string,
	depth int,
	visited *int,
	files *[]scannedFile,
) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	(*visited)++
	if *visited > maximumWalkEntries {
		return errors.New("Codex session tree exceeds observer bound")
	}
	if child.Type()&os.ModeSymlink != 0 {
		return nil
	}
	name := child.Name()
	if name == "." || name == ".." || filepath.Base(name) != name {
		return ErrInvalidConfig
	}
	nextParts := append(append([]string(nil), parts...), name)
	if depth < 3 {
		if !child.IsDir() {
			return nil
		}
		opened, openErr := secureOpenChild(directory, name, true)
		if openErr != nil {
			return openErr
		}
		walkErr := walkPinnedTree(ctx, rootPath, opened, nextParts, depth+1, visited, files)
		closeErr := opened.Close()
		if walkErr != nil {
			return walkErr
		}
		if closeErr != nil {
			return closeErr
		}
		return nil
	}
	if child.IsDir() || !strings.EqualFold(filepath.Ext(name), ".jsonl") {
		return nil
	}
	info, infoErr := child.Info()
	if infoErr != nil {
		return infoErr
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil
	}
	*files = append(*files, scannedFile{
		parts: nextParts,
		path:  filepath.Join(append([]string{rootPath}, nextParts...)...),
		info:  info,
	})
	return nil
}

func readPinnedFirstLine(
	root *os.File,
	parts []string,
	expected os.FileInfo,
) ([]byte, os.FileInfo, error) {
	if len(parts) != 4 {
		return nil, nil, ErrInvalidConfig
	}
	parent := root
	openedDirectories := make([]*os.File, 0, 3)
	defer func() {
		for index := len(openedDirectories) - 1; index >= 0; index-- {
			_ = openedDirectories[index].Close()
		}
	}()
	for _, name := range parts[:3] {
		opened, err := secureOpenChild(parent, name, true)
		if err != nil {
			return nil, nil, err
		}
		openedDirectories = append(openedDirectories, opened)
		parent = opened
	}
	file, err := secureOpenChild(parent, parts[3], false)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !openedInfo.Mode().IsRegular() || expected != nil && !os.SameFile(openedInfo, expected) {
		return nil, nil, ErrInvalidConfig
	}
	return readBoundedLine(file, openedInfo)
}

func readBoundedLine(file *os.File, openedInfo os.FileInfo) ([]byte, os.FileInfo, error) {
	reader := bufio.NewReaderSize(io.LimitReader(file, maximumFirstLine+1), maximumFirstLine+1)
	line, err := reader.ReadBytes('\n')
	if len(line) > maximumFirstLine {
		clear(line)
		return nil, openedInfo, nil
	}
	if errors.Is(err, io.EOF) {
		return line, openedInfo, nil
	}
	if err != nil {
		clear(line)
		return nil, openedInfo, nil
	}
	return line, openedInfo, nil
}

func isCodexProcessName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	base = strings.TrimSuffix(base, ".exe")
	return base == "codex" || base == "codex-cli"
}
