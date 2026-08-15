//go:build darwin

package codexobserver

import (
	"reflect"
	"testing"
)

func TestDarwinLsofParserKeepsPathsPrivateAndBoundToPID(t *testing.T) {
	allowed := map[string]struct{}{
		"/private/.codex/sessions/one.jsonl":   {},
		"/private/.codex/sessions/two.jsonl":   {},
		"/private/.codex/sessions/three.jsonl": {},
	}
	parsed := parseLsofNames([]byte(
		"p41\n"+
			"n/private/.codex/sessions/one.jsonl\n"+
			"n/Users/private/other-secret.txt\n"+
			"p42\n"+
			"n/private/.codex/sessions/two.jsonl\n"+
			"n/private/.codex/sessions/three.jsonl\n",
	), allowed)
	want := map[int][]string{
		41: {"/private/.codex/sessions/one.jsonl"},
		42: {"/private/.codex/sessions/two.jsonl", "/private/.codex/sessions/three.jsonl"},
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("parseLsofNames() = %#v, want %#v", parsed, want)
	}
}

func TestDarwinPIDReuseBetweenEnumerationAndLsofFailsClosed(t *testing.T) {
	before := []processObservation{{identity: processIdentity{pid: 41, startedUnixNano: 100}}}
	after := []processObservation{{identity: processIdentity{pid: 41, startedUnixNano: 200}}}
	if _, err := reconcileDarwinProcesses(before, after, map[int][]string{41: {"/private/session.jsonl"}}); err == nil {
		t.Fatal("PID reuse was accepted")
	}
	after[0].identity.startedUnixNano = 100
	stable, err := reconcileDarwinProcesses(before, after, map[int][]string{41: {"/private/session.jsonl"}})
	if err != nil || len(stable) != 1 || len(stable[0].openFiles) != 1 {
		t.Fatalf("stable reconciliation = %+v, %v", stable, err)
	}
}

func TestDarwinLsofOutputBufferIsBounded(t *testing.T) {
	buffer := &boundedBuffer{maximum: 4}
	if written, err := buffer.Write([]byte("private-path")); err != nil || written != len("private-path") {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	if !buffer.overflow || len(buffer.Bytes()) != 4 {
		t.Fatalf("bounded buffer = %q, overflow=%v", buffer.Bytes(), buffer.overflow)
	}
	buffer.Clear()
	if len(buffer.Bytes()) != 0 {
		t.Fatal("bounded buffer did not clear sensitive bytes")
	}
}
