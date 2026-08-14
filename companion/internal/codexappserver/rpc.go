package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/protocol"
)

const defaultMaximumDocument = 1 << 20

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type methodError struct {
	code    int64
	message string
}

func (problem *methodError) Error() string {
	return fmt.Sprintf("Codex App Server method failed with code %d", problem.code)
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type notification struct {
	method string
	params json.RawMessage
}

type rpcClient struct {
	connection Connection
	maximum    int

	mutex         sync.Mutex
	writeMutex    sync.Mutex
	nextID        int64
	pending       map[int64]chan rpcResponse
	notifications chan notification
	done          chan struct{}
	terminalError error
	closeOnce     sync.Once
}

func newRPCClient(connection Connection, maximum int) *rpcClient {
	if maximum <= 0 {
		maximum = defaultMaximumDocument
	}
	client := &rpcClient{
		connection:    connection,
		maximum:       maximum,
		nextID:        1,
		pending:       make(map[int64]chan rpcResponse),
		notifications: make(chan notification, 16),
		done:          make(chan struct{}),
	}
	go client.readLoop()
	return client
}

func (client *rpcClient) Close() error {
	client.fail(ErrUnavailable)
	return client.connection.Close()
}

func (client *rpcClient) Call(
	ctx context.Context,
	method string,
	params any,
	result any,
) error {
	client.mutex.Lock()
	if client.terminalError != nil {
		err := client.terminalError
		client.mutex.Unlock()
		return err
	}
	id := client.nextID
	client.nextID++
	response := make(chan rpcResponse, 1)
	client.pending[id] = response
	client.mutex.Unlock()

	document, err := json.Marshal(struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: id, Method: method, Params: params})
	if err != nil {
		client.removePending(id)
		return errors.Join(ErrSchemaChanged, err)
	}
	client.writeMutex.Lock()
	err = client.connection.Write(ctx, document)
	client.writeMutex.Unlock()
	if err != nil {
		client.removePending(id)
		return errors.Join(ErrProcessExited, err)
	}

	select {
	case received := <-response:
		if received.err != nil {
			return received.err
		}
		if result == nil {
			return nil
		}
		if err = strictDecode(received.result, result); err != nil {
			return errors.Join(ErrSchemaChanged, err)
		}
		return nil
	case <-ctx.Done():
		client.removePending(id)
		return ctx.Err()
	case <-client.done:
		client.mutex.Lock()
		err = client.terminalError
		client.mutex.Unlock()
		if err == nil {
			err = ErrProcessExited
		}
		return err
	}
}

func (client *rpcClient) Notify(
	ctx context.Context,
	method string,
	params any,
) error {
	document, err := json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{Method: method, Params: params})
	if err != nil {
		return errors.Join(ErrSchemaChanged, err)
	}
	client.writeMutex.Lock()
	err = client.connection.Write(ctx, document)
	client.writeMutex.Unlock()
	if err != nil {
		return errors.Join(ErrProcessExited, err)
	}
	return nil
}

func (client *rpcClient) Notifications() <-chan notification {
	return client.notifications
}

func (client *rpcClient) Done() <-chan struct{} {
	return client.done
}

func (client *rpcClient) TerminalError() error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	return client.terminalError
}

func (client *rpcClient) removePending(id int64) {
	client.mutex.Lock()
	delete(client.pending, id)
	client.mutex.Unlock()
}

func (client *rpcClient) readLoop() {
	for {
		document, err := client.connection.Read(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = ErrProcessExited
			} else {
				err = errors.Join(ErrProcessExited, err)
			}
			client.fail(err)
			return
		}
		if err = client.accept(document); err != nil {
			client.fail(err)
			return
		}
	}
}

func (client *rpcClient) accept(document []byte) error {
	if len(document) == 0 || len(document) > client.maximum || !utf8.Valid(document) {
		return ErrSchemaChanged
	}
	var envelope struct {
		ID          *json.RawMessage `json:"id"`
		Method      string           `json:"method"`
		Params      json.RawMessage  `json:"params"`
		Result      json.RawMessage  `json:"result"`
		Error       *rpcError        `json:"error"`
		EmittedAtMS *int64           `json:"emittedAtMs"`
	}
	if err := strictDecode(document, &envelope); err != nil {
		return errors.Join(ErrSchemaChanged, err)
	}
	if envelope.ID == nil {
		if envelope.Method == "" || len(envelope.Params) == 0 ||
			len(envelope.Result) != 0 || envelope.Error != nil ||
			(envelope.EmittedAtMS != nil &&
				(*envelope.EmittedAtMS < 0 || *envelope.EmittedAtMS > maximumSafeInteger)) {
			return ErrSchemaChanged
		}
		select {
		case client.notifications <- notification{
			method: envelope.Method,
			params: append(json.RawMessage(nil), envelope.Params...),
		}:
			return nil
		default:
			return ErrUnavailable
		}
	}
	if envelope.Method != "" || len(envelope.Params) != 0 ||
		envelope.EmittedAtMS != nil ||
		(len(envelope.Result) == 0) == (envelope.Error == nil) {
		return ErrSchemaChanged
	}
	var id int64
	if err := strictDecode(*envelope.ID, &id); err != nil || id <= 0 {
		return ErrSchemaChanged
	}
	client.mutex.Lock()
	waiter := client.pending[id]
	delete(client.pending, id)
	client.mutex.Unlock()
	if waiter == nil {
		return ErrSchemaChanged
	}
	if envelope.Error != nil {
		waiter <- rpcResponse{err: &methodError{
			code:    envelope.Error.Code,
			message: envelope.Error.Message,
		}}
		return nil
	}
	waiter <- rpcResponse{result: append(json.RawMessage(nil), envelope.Result...)}
	return nil
}

func (client *rpcClient) fail(err error) {
	client.closeOnce.Do(func() {
		client.mutex.Lock()
		client.terminalError = err
		pending := client.pending
		client.pending = make(map[int64]chan rpcResponse)
		close(client.done)
		close(client.notifications)
		client.mutex.Unlock()
		for _, waiter := range pending {
			waiter <- rpcResponse{err: err}
		}
	})
}

func strictDecode(document []byte, target any) error {
	return protocol.DecodeStrictDocumentLimit(document, defaultMaximumDocument, target)
}
