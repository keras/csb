package proto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrameRoundtrip(t *testing.T) {
	frames := []Frame{
		NewStart("git", []string{"log", "--oneline"}),
		NewStart("cat", nil),
		NewStdin([]byte("hello\x00world")),
		NewStdin(nil), // EOF signal
		NewStdout([]byte{0x00, 0x01, 0xff}),
		NewStderr([]byte("error output")),
		NewExit(0),
		NewExit(42),
		NewExit(1),
		NewError("not_whitelisted", "command not permitted"),
		NewError("exec_failed", "no such file"),
		NewSignal("SIGTERM"),
		NewSignal("SIGINT"),
	}

	for _, f := range frames {
		data, err := Encode(f)
		if err != nil {
			t.Fatalf("Encode(%s): %v", f.Type, err)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("Decode(%s): %v", f.Type, err)
		}
		if got.Type != f.Type {
			t.Errorf("%s: type: got %q want %q", f.Type, got.Type, f.Type)
		}
		if !bytes.Equal(got.Data, f.Data) {
			t.Errorf("%s: data mismatch: got %v want %v", f.Type, got.Data, f.Data)
		}
		if f.ExitCode != nil {
			if got.ExitCode == nil {
				t.Errorf("%s: exit code is nil, want %d", f.Type, *f.ExitCode)
			} else if *got.ExitCode != *f.ExitCode {
				t.Errorf("%s: exit code: got %d want %d", f.Type, *got.ExitCode, *f.ExitCode)
			}
		}
		if got.ErrCode != f.ErrCode {
			t.Errorf("%s: err_code: got %q want %q", f.Type, got.ErrCode, f.ErrCode)
		}
		if got.Message != f.Message {
			t.Errorf("%s: message: got %q want %q", f.Type, got.Message, f.Message)
		}
		if got.Name != f.Name {
			t.Errorf("%s: name: got %q want %q", f.Type, got.Name, f.Name)
		}
	}
}

func TestExitCodeZeroInJSON(t *testing.T) {
	// exit code 0 must appear in the JSON output (not omitted)
	f := NewExit(0)
	data, _ := Encode(f)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["code"]; !ok {
		t.Error("exit code 0 must appear in JSON as \"code\" field")
	}
	if m["code"] != float64(0) {
		t.Errorf("code: got %v want 0", m["code"])
	}
}

func TestBinaryDataBase64(t *testing.T) {
	// Verify arbitrary bytes survive the base64 encode/decode round-trip
	input := make([]byte, 256)
	for i := range input {
		input[i] = byte(i)
	}
	f := NewStdout(input)
	data, _ := Encode(f)
	got, _ := Decode(data)
	if !bytes.Equal(got.Data, input) {
		t.Error("binary data did not survive base64 round-trip")
	}
}
