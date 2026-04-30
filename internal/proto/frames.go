package proto

import "encoding/json"

// Frame is the single wire type for all messages.
// []byte fields are automatically base64-encoded/decoded by encoding/json.
type Frame struct {
	Type     string   `json:"type"`
	Cmd      string   `json:"cmd,omitempty"`
	Args     []string `json:"args,omitempty"`
	Data     []byte   `json:"data,omitempty"`
	ExitCode *int     `json:"code,omitempty"` // pointer so exit code 0 is preserved in JSON
	ErrCode  string   `json:"err_code,omitempty"`
	Message  string   `json:"message,omitempty"`
	Name     string   `json:"name,omitempty"` // signal name
}

func NewStart(cmd string, args []string) Frame {
	return Frame{Type: "start", Cmd: cmd, Args: args}
}

func NewStdin(data []byte) Frame {
	return Frame{Type: "stdin", Data: data}
}

func NewStdout(data []byte) Frame {
	return Frame{Type: "stdout", Data: data}
}

func NewStderr(data []byte) Frame {
	return Frame{Type: "stderr", Data: data}
}

func NewExit(code int) Frame {
	return Frame{Type: "exit", ExitCode: &code}
}

func NewError(errCode, message string) Frame {
	return Frame{Type: "error", ErrCode: errCode, Message: message}
}

func NewSignal(name string) Frame {
	return Frame{Type: "signal", Name: name}
}

func Encode(f Frame) ([]byte, error) {
	return json.Marshal(f)
}

func Decode(data []byte) (Frame, error) {
	var f Frame
	return f, json.Unmarshal(data, &f)
}
