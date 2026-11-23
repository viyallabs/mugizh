package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/oapi-codegen/runtime"
)

// strictCodec rejects unknown JSON members and trailing data in request bodies. Our
// request schemas declare additionalProperties: false; this makes the server enforce it,
// so a client cannot slip extra fields (for example health data into scheduler payloads,
// TC-61) past the generated types.
type strictCodec struct{}

func (strictCodec) Marshal(v any) ([]byte, error) { return json.Marshal(v) }

func (strictCodec) Unmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after JSON value")
	}
	return nil
}

// UseStrictJSON installs the strict codec process-wide (call once from main or tests).
func UseStrictJSON() { runtime.SetJSONCodec(strictCodec{}) }
