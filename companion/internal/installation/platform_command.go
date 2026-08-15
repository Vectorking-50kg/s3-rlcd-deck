package installation

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

const maximumPlatformOutputBytes = 64 << 10

func runPlatformCommand(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, name, arguments...)
	var output bytes.Buffer
	command.Stdout = &limitedBuffer{buffer: &output, maximum: maximumPlatformOutputBytes}
	command.Stderr = &limitedBuffer{maximum: maximumPlatformOutputBytes}
	err := command.Run()
	if errors.Is(err, errPlatformOutputLimit) {
		return nil, ErrPlatform
	}
	if err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

var errPlatformOutputLimit = errors.New("platform command output exceeded bound")

type limitedBuffer struct {
	buffer  *bytes.Buffer
	written int
	maximum int
}

func (writer *limitedBuffer) Write(document []byte) (int, error) {
	if writer.written+len(document) > writer.maximum {
		return 0, errPlatformOutputLimit
	}
	writer.written += len(document)
	if writer.buffer == nil {
		return len(document), nil
	}
	return writer.buffer.Write(document)
}
