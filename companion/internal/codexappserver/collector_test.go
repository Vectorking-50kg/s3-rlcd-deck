package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

var testNow = time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)

type fakeInbound struct {
	document []byte
	err      error
}

type fakeRequest struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type fakeConnection struct {
	inbound   chan fakeInbound
	closed    chan struct{}
	closeOnce sync.Once
	handler   func(*fakeConnection, fakeRequest)
}

func newFakeConnection(handler func(*fakeConnection, fakeRequest)) *fakeConnection {
	return &fakeConnection{
		inbound: make(chan fakeInbound, 64),
		closed:  make(chan struct{}),
		handler: handler,
	}
}

func (connection *fakeConnection) Read(ctx context.Context) ([]byte, error) {
	select {
	case item := <-connection.inbound:
		return append([]byte(nil), item.document...), item.err
	case <-connection.closed:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (connection *fakeConnection) Write(_ context.Context, document []byte) error {
	var request fakeRequest
	if err := json.Unmarshal(document, &request); err != nil {
		return err
	}
	if connection.handler != nil {
		connection.handler(connection, request)
	}
	return nil
}

func (connection *fakeConnection) Close() error {
	connection.closeOnce.Do(func() { close(connection.closed) })
	return nil
}

func (connection *fakeConnection) respond(id int64, result []byte) {
	document, err := json.Marshal(struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
	}{ID: id, Result: result})
	if err != nil {
		panic(err)
	}
	connection.inbound <- fakeInbound{document: document}
}

func (connection *fakeConnection) respondError(id int64, code int64, message string) {
	document, err := json.Marshal(struct {
		ID    int64    `json:"id"`
		Error rpcError `json:"error"`
	}{ID: id, Error: rpcError{Code: code, Message: message}})
	if err != nil {
		panic(err)
	}
	connection.inbound <- fakeInbound{document: document}
}

func (connection *fakeConnection) notify(method string, params []byte) {
	document, err := json.Marshal(struct {
		Method      string          `json:"method"`
		Params      json.RawMessage `json:"params"`
		EmittedAtMS int64           `json:"emittedAtMs"`
	}{Method: method, Params: params, EmittedAtMS: testNow.UnixMilli()})
	if err != nil {
		panic(err)
	}
	connection.inbound <- fakeInbound{document: document}
}

func (connection *fakeConnection) terminate(err error) {
	connection.inbound <- fakeInbound{err: err}
}

type fakeConnector struct {
	mutex       sync.Mutex
	connections []*fakeConnection
	next        int
}

func (connector *fakeConnector) Connect(context.Context) (Connection, error) {
	connector.mutex.Lock()
	defer connector.mutex.Unlock()
	if connector.next >= len(connector.connections) {
		return nil, ErrUnavailable
	}
	connection := connector.connections[connector.next]
	connector.next++
	return connection, nil
}

type appScript struct {
	mutex         sync.Mutex
	rateResults   [][]byte
	usageResult   []byte
	rateID        int64
	usageID       int64
	cycle         int
	responseOrder []string
	requests      []fakeRequest
	methodError   map[string]rpcError
	dropMethod    string
}

func (script *appScript) handle(connection *fakeConnection, request fakeRequest) {
	script.mutex.Lock()
	script.requests = append(script.requests, request)
	script.mutex.Unlock()
	if request.ID == nil {
		return
	}
	if problem, exists := script.methodError[request.Method]; exists {
		connection.respondError(*request.ID, problem.Code, problem.Message)
		return
	}
	if request.Method == script.dropMethod {
		return
	}
	switch request.Method {
	case "initialize":
		connection.respond(*request.ID, []byte(`{
			"codexHome":"/Users/private-owner/.codex",
			"platformFamily":"unix",
			"platformOs":"macos",
			"userAgent":"private-owner@example.test"
		}`))
	case "account/rateLimits/read", "account/usage/read":
		if len(script.methodError) != 0 || script.dropMethod != "" {
			if request.Method == "account/rateLimits/read" {
				connection.respond(*request.ID, script.rateResults[0])
			} else {
				connection.respond(*request.ID, script.usageResult)
			}
			return
		}
		script.captureCollectionRequest(connection, request)
	case "thread/resume":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(request.Params, &params) != nil {
			connection.respondError(*request.ID, -32602, "invalid params")
			return
		}
		response, err := json.Marshal(map[string]any{
			"thread": map[string]any{
				"id":     params.ThreadID,
				"cwd":    "/Users/private-owner/secret-project",
				"prompt": "PRIVATE PROMPT MUST NOT CROSS THE ADAPTER",
				"status": map[string]any{"type": "idle"},
			},
		})
		if err != nil {
			panic(err)
		}
		connection.respond(*request.ID, response)
	default:
		connection.respondError(*request.ID, -32601, "method not found")
	}
}

func (script *appScript) captureCollectionRequest(
	connection *fakeConnection,
	request fakeRequest,
) {
	script.mutex.Lock()
	if request.Method == "account/rateLimits/read" {
		script.rateID = *request.ID
	} else {
		script.usageID = *request.ID
	}
	if script.rateID == 0 || script.usageID == 0 {
		script.mutex.Unlock()
		return
	}
	rateID := script.rateID
	usageID := script.usageID
	script.rateID = 0
	script.usageID = 0
	index := script.cycle
	if index >= len(script.rateResults) {
		index = len(script.rateResults) - 1
	}
	rate := append([]byte(nil), script.rateResults[index]...)
	usage := append([]byte(nil), script.usageResult...)
	script.cycle++
	script.responseOrder = append(script.responseOrder, "usage", "rate")
	script.mutex.Unlock()

	// The fake deliberately responds in the opposite order from the adapter's
	// receive order, proving response IDs—not arrival order—drive dispatch.
	connection.respond(usageID, usage)
	connection.respond(rateID, rate)
}

func (script *appScript) methods() []string {
	script.mutex.Lock()
	defer script.mutex.Unlock()
	methods := make([]string, 0, len(script.requests))
	for _, request := range script.requests {
		methods = append(methods, request.Method)
	}
	return methods
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	document, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func newTestCollector(t *testing.T, connector Connector, timeout time.Duration) *Collector {
	t.Helper()
	collector, err := New(Config{
		Connector:      connector,
		ClientVersion:  "0.3.0-test",
		RequestTimeout: timeout,
		ReconnectDelay: time.Millisecond,
		Now:            func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return collector
}

func waitUpdate(t *testing.T, updates <-chan Update) Update {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Codex update")
		return Update{}
	}
}

func runCollector(
	t *testing.T,
	collector *Collector,
) (context.CancelFunc, <-chan Update, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan Update, 16)
	completed := make(chan error, 1)
	go func() {
		completed <- collector.Run(ctx, func(_ context.Context, update Update) error {
			updates <- update
			return nil
		})
	}()
	return cancel, updates, completed
}

func assertRunStops(t *testing.T, cancel context.CancelFunc, completed <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("collector stopped with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop")
	}
}

func assertWireValid(t *testing.T, update Update) {
	t.Helper()
	generated := testNow.Add(time.Second).Format(time.RFC3339)
	_, err := aisnapshot.Encode(aisnapshot.Snapshot{
		Type:            "snapshot.ai",
		ProtocolVersion: 1,
		SchemaVersion:   aisnapshot.SchemaVersion{Major: 1, Minor: 0},
		GeneratedAt:     generated,
		ProviderOrder:   []string{"codex"},
		Providers:       []aisnapshot.Provider{update.Provider},
		Sessions:        update.Sessions,
		NextRefresh:     60,
	})
	if err != nil {
		t.Fatalf("normalized update violates AI Snapshot wire contract: %v", err)
	}
}

func TestCollectorNormalizesOutOfOrderTranscriptsAndNotifications(t *testing.T) {
	initialRate := loadFixture(t, "normal-rate.json")
	changedRate := []byte(strings.Replace(
		string(initialRate), `"usedPercent": 25`, `"usedPercent": 31`, 1,
	))
	script := &appScript{
		rateResults: [][]byte{initialRate, changedRate},
		usageResult: loadFixture(t, "normal-usage.json"),
		methodError: make(map[string]rpcError),
	}
	connection := newFakeConnection(script.handle)
	collector := newTestCollector(t, &fakeConnector{connections: []*fakeConnection{connection}}, time.Second)
	cancel, updates, completed := runCollector(t, collector)

	initial := waitUpdate(t, updates)
	if initial.Provider.Status != aisnapshot.ProviderOK ||
		initial.Provider.Source != aisnapshot.ProviderSourceCodexAppServer ||
		initial.Provider.Confidence != aisnapshot.ConfidenceVerified {
		t.Fatalf("unexpected normalized provider: %+v", initial.Provider)
	}
	if len(initial.Provider.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(initial.Provider.Windows))
	}
	primary := initial.Provider.Windows[0]
	if primary.Name != "dynamic_window_primary" || primary.WindowMinutes == nil ||
		*primary.WindowMinutes != 300 || primary.UsedBasisPoints == nil ||
		*primary.UsedBasisPoints != 2500 || primary.RemainingBasisPoints == nil ||
		*primary.RemainingBasisPoints != 7500 || primary.ResetsAt == nil {
		t.Fatalf("dynamic primary window = %+v", primary)
	}
	if initial.Provider.Tokens == nil || initial.Provider.Tokens.Total == nil ||
		*initial.Provider.Tokens.Total != 123456 {
		t.Fatalf("token DTO = %+v", initial.Provider.Tokens)
	}
	assertWireValid(t, initial)
	encoded, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/Users/private-owner", "private-owner@example.test", "PRIVATE PROMPT"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("normalized DTO contains private App Server data %q", secret)
		}
	}

	connection.notify("account/rateLimits/updated", loadFixture(t, "rate-updated.json"))
	updated := waitUpdate(t, updates)
	if got := *updated.Provider.Windows[0].UsedBasisPoints; got != 3100 {
		t.Fatalf("notification refresh used basis points = %d, want 3100", got)
	}
	assertWireValid(t, updated)

	script.mutex.Lock()
	responseOrder := append([]string(nil), script.responseOrder...)
	script.mutex.Unlock()
	if !reflect.DeepEqual(responseOrder, []string{"usage", "rate", "usage", "rate"}) {
		t.Fatalf("response order = %v", responseOrder)
	}
	methodCounts := make(map[string]int)
	for _, method := range script.methods() {
		methodCounts[method]++
	}
	wantCounts := map[string]int{
		"initialize": 1, "initialized": 1,
		"account/rateLimits/read": 2, "account/usage/read": 2,
	}
	if !reflect.DeepEqual(methodCounts, wantCounts) {
		t.Fatalf("App Server method counts = %v, want %v", methodCounts, wantCounts)
	}
	assertRunStops(t, cancel, completed)
}

func TestCollectorReconnectsAndIsolatesProcessExit(t *testing.T) {
	fixture := loadFixture(t, "normal-rate.json")
	usage := loadFixture(t, "normal-usage.json")
	firstScript := &appScript{rateResults: [][]byte{fixture}, usageResult: usage, methodError: map[string]rpcError{}}
	secondScript := &appScript{rateResults: [][]byte{fixture}, usageResult: usage, methodError: map[string]rpcError{}}
	first := newFakeConnection(firstScript.handle)
	second := newFakeConnection(secondScript.handle)
	collector := newTestCollector(t, &fakeConnector{connections: []*fakeConnection{first, second}}, time.Second)
	cancel, updates, completed := runCollector(t, collector)

	if initial := waitUpdate(t, updates); initial.Provider.Status != aisnapshot.ProviderOK {
		t.Fatalf("initial provider = %+v", initial.Provider)
	}
	first.terminate(io.EOF)
	degraded := waitUpdate(t, updates)
	if degraded.Provider.Status != aisnapshot.ProviderDegraded || degraded.Provider.Error == nil ||
		degraded.Provider.Error.Code != aisnapshot.ProviderErrorProcessExited {
		t.Fatalf("process-exit update = %+v", degraded.Provider)
	}
	assertWireValid(t, degraded)
	if reconnected := waitUpdate(t, updates); reconnected.Provider.Status != aisnapshot.ProviderOK {
		t.Fatalf("reconnected provider = %+v", reconnected.Provider)
	}
	assertRunStops(t, cancel, completed)
}

func TestCollectorFailsClosedOnAbnormalNumbersAndSchemaChanges(t *testing.T) {
	normal := loadFixture(t, "normal-rate.json")
	tests := []struct {
		name string
		rate []byte
	}{
		{
			name: "used percentage over one hundred",
			rate: []byte(strings.Replace(string(normal), `"usedPercent": 25`, `"usedPercent": 101`, 1)),
		},
		{name: "unknown response field", rate: loadFixture(t, "schema-changed-rate.json")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := &appScript{
				rateResults: [][]byte{test.rate},
				usageResult: loadFixture(t, "normal-usage.json"),
				methodError: map[string]rpcError{},
			}
			connection := newFakeConnection(script.handle)
			collector := newTestCollector(t, &fakeConnector{connections: []*fakeConnection{connection}}, time.Second)
			cancel, updates, completed := runCollector(t, collector)
			degraded := waitUpdate(t, updates)
			if degraded.Provider.Error == nil ||
				degraded.Provider.Error.Code != aisnapshot.ProviderErrorSchemaChanged ||
				degraded.Provider.Error.Retryable {
				t.Fatalf("schema degradation = %+v", degraded.Provider)
			}
			assertWireValid(t, degraded)
			assertRunStops(t, cancel, completed)
		})
	}
}

func TestCollectorClassifiesTimeoutAuthenticationAndPermissionWithoutSecrets(t *testing.T) {
	tests := []struct {
		name       string
		dropMethod string
		problem    map[string]rpcError
		want       aisnapshot.ProviderErrorCode
	}{
		{
			name: "timeout", dropMethod: "account/usage/read",
			problem: map[string]rpcError{}, want: aisnapshot.ProviderErrorTimeout,
		},
		{
			name: "authentication",
			problem: map[string]rpcError{"account/usage/read": {
				Code: -32000, Message: "not logged in: private-owner@example.test /Users/private-owner",
			}},
			want: aisnapshot.ProviderErrorAuthStale,
		},
		{
			name: "permission",
			problem: map[string]rpcError{"account/rateLimits/read": {
				Code: -32001, Message: "permission denied for prompt PRIVATE PROMPT",
			}},
			want: aisnapshot.ProviderErrorPermissionDenied,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := &appScript{
				rateResults: [][]byte{loadFixture(t, "normal-rate.json")},
				usageResult: loadFixture(t, "normal-usage.json"),
				methodError: test.problem,
				dropMethod:  test.dropMethod,
			}
			connection := newFakeConnection(script.handle)
			collector := newTestCollector(t, &fakeConnector{connections: []*fakeConnection{connection}}, 20*time.Millisecond)
			cancel, updates, completed := runCollector(t, collector)
			degraded := waitUpdate(t, updates)
			if degraded.Provider.Error == nil || degraded.Provider.Error.Code != test.want {
				t.Fatalf("provider error = %+v, want %s", degraded.Provider.Error, test.want)
			}
			encoded, err := json.Marshal(degraded)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"private-owner", "/Users/", "PRIVATE PROMPT"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("degraded DTO leaked %q: %s", secret, encoded)
				}
			}
			assertWireValid(t, degraded)
			assertRunStops(t, cancel, completed)
		})
	}
}

func TestOnlyThreadsLoadedByThisConnectionBecomeVerified(t *testing.T) {
	script := &appScript{
		rateResults: [][]byte{loadFixture(t, "normal-rate.json")},
		usageResult: loadFixture(t, "normal-usage.json"),
		methodError: map[string]rpcError{},
	}
	connection := newFakeConnection(script.handle)
	collector := newTestCollector(t, &fakeConnector{connections: []*fakeConnection{connection}}, time.Second)
	cancel, updates, completed := runCollector(t, collector)
	_ = waitUpdate(t, updates)

	connection.notify("thread/status/changed", []byte(`{
		"threadId":"external-private-thread",
		"status":{"type":"active","activeFlags":["waitingOnApproval"]}
	}`))
	select {
	case update := <-updates:
		t.Fatalf("unowned thread produced an update: %+v", update)
	case <-time.After(30 * time.Millisecond):
	}

	const owned = "private-thread-raw-identifier"
	if err := collector.LoadThread(context.Background(), owned); err != nil {
		t.Fatalf("load owned thread: %v", err)
	}
	connection.notify("thread/status/changed", []byte(`{
		"threadId":"private-thread-raw-identifier",
		"status":{"type":"active","activeFlags":["waitingOnApproval"]}
	}`))
	verified := waitUpdate(t, updates)
	if len(verified.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(verified.Sessions))
	}
	session := verified.Sessions[0]
	if session.ID == owned || !strings.HasPrefix(session.ID, "codex_") ||
		session.Source != aisnapshot.SessionSourceCodexAppServerOwned ||
		session.Confidence != aisnapshot.ConfidenceVerified ||
		session.State != aisnapshot.SessionWaitingApproval || session.DisplayName != nil {
		t.Fatalf("verified session = %+v", session)
	}
	encoded, err := json.Marshal(verified)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{owned, "/Users/private-owner", "PRIVATE PROMPT"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("session update leaked %q", secret)
		}
	}
	assertWireValid(t, verified)

	connection.notify("thread/status/changed", []byte(`{
		"threadId":"private-thread-raw-identifier",
		"status":{"type":"notLoaded"}
	}`))
	removed := waitUpdate(t, updates)
	if len(removed.Sessions) != 0 {
		t.Fatalf("notLoaded retained verified sessions: %+v", removed.Sessions)
	}
	connection.notify("thread/status/changed", []byte(`{
		"threadId":"private-thread-raw-identifier",
		"status":{"type":"active","activeFlags":[]}
	}`))
	select {
	case update := <-updates:
		t.Fatalf("unloaded thread retained authority: %+v", update)
	case <-time.After(30 * time.Millisecond):
	}
	assertRunStops(t, cancel, completed)
}

func TestOwnedThreadAuthorityAndSessionDTOAreBoundedAtSixteen(t *testing.T) {
	script := &appScript{
		rateResults: [][]byte{loadFixture(t, "normal-rate.json")},
		usageResult: loadFixture(t, "normal-usage.json"),
		methodError: map[string]rpcError{},
	}
	connection := newFakeConnection(script.handle)
	collector := newTestCollector(t, &fakeConnector{connections: []*fakeConnection{connection}}, time.Second)
	cancel, updates, completed := runCollector(t, collector)
	_ = waitUpdate(t, updates)

	var final Update
	for index := range maximumOwnedThreads {
		threadID := fmt.Sprintf("owned-thread-%02d", index)
		if err := collector.LoadThread(context.Background(), threadID); err != nil {
			t.Fatalf("LoadThread(%d) error = %v", index, err)
		}
		params, err := json.Marshal(map[string]any{
			"threadId": threadID,
			"status":   map[string]any{"type": "active", "activeFlags": []string{}},
		})
		if err != nil {
			t.Fatal(err)
		}
		connection.notify("thread/status/changed", params)
		final = waitUpdate(t, updates)
	}
	if len(final.Sessions) != maximumOwnedThreads {
		t.Fatalf("sessions = %d, want %d", len(final.Sessions), maximumOwnedThreads)
	}
	assertWireValid(t, final)
	if err := collector.LoadThread(context.Background(), "owned-thread-17"); !errors.Is(err, ErrThreadLimit) {
		t.Fatalf("seventeenth LoadThread() error = %v, want %v", err, ErrThreadLimit)
	}
	methodCounts := 0
	for _, method := range script.methods() {
		if method == "thread/resume" {
			methodCounts++
		}
	}
	if methodCounts != maximumOwnedThreads {
		t.Fatalf("thread/resume requests = %d, want %d", methodCounts, maximumOwnedThreads)
	}
	assertRunStops(t, cancel, completed)
}

func TestPublisherFailureIsNotMisclassifiedAsProviderFailure(t *testing.T) {
	script := &appScript{
		rateResults: [][]byte{loadFixture(t, "normal-rate.json")},
		usageResult: loadFixture(t, "normal-usage.json"),
		methodError: map[string]rpcError{},
	}
	connection := newFakeConnection(script.handle)
	collector := newTestCollector(t, &fakeConnector{connections: []*fakeConnection{connection}}, time.Second)
	want := errors.New("downstream snapshot store stopped")
	err := collector.Run(context.Background(), func(context.Context, Update) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want publisher error", err)
	}
}

func TestCollectorRejectsUnknownAdapterVersion(t *testing.T) {
	_, err := New(Config{AdapterVersion: AdapterVersion + 1, ClientVersion: "test"})
	if err == nil || !strings.Contains(err.Error(), "unsupported Codex App Server adapter version") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestWindowNamesNeverForwardFreeFormUpstreamText(t *testing.T) {
	privateText := "prompt content owned by private-owner@example.test"
	safeID := "dynamic-limit"
	used := int64(40)
	result := rawRateLimitResult{
		RateLimits: &rawRateLimitSnapshot{
			LimitName: &privateText,
			LimitID:   &safeID,
			Primary:   &rawRateWindow{UsedPercent: &used},
		},
	}
	windows, err := normalizeRateLimits(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 || windows[0].Name != "dynamic_limit_primary" ||
		strings.Contains(windows[0].Name, "private") {
		t.Fatalf("privacy-safe dynamic window name = %+v", windows)
	}
}
