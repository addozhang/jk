package auth

import (
	"bytes"

	"github.com/BurntSushi/toml"
)

// tomlMarshal encodes v as a TOML document. BurntSushi/toml only exposes
// the streaming Encoder; this wrapper gives us a []byte-returning helper
// to keep the call sites in store.go simple.
func tomlMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.Indent = "" // keep the file compact; users rarely hand-edit
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
