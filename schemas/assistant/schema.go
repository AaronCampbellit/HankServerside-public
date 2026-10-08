// Package assistantschema owns the reserved, versioned execution wire contract.
package assistantschema

import (
	_ "embed"
	"encoding/json"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed execution-v2.schema.json
var executionV2 []byte

var resolve = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(executionV2, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
})

func Validate(record any) error {
	schema, err := resolve()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return schema.Validate(value)
}
