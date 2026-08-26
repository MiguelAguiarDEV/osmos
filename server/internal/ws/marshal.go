package ws

import (
	"encoding/json"

	"clip-sync/server/pkg/types"
)

func marshalEnvelope(env types.Envelope) ([]byte, error) {
	return json.Marshal(env)
}
