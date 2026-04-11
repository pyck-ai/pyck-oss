package std

import (
	"encoding/json"
)

func UnmarshalJson[T any](payloadIn []byte) (T, error) {
	var payload T
	err := json.Unmarshal(payloadIn, &payload)
	if err != nil {
		return payload, err
	}
	return payload, nil
}

func MarshalJson(msg interface{}) ([]byte, error) {
	return json.Marshal(msg)
}

func MapJSON[T any](in any, dest *T) error {
	var (
		result T
		b      []byte
		err    error
	)

	switch v := in.(type) {
	case nil:
		// no-op
	case T:
		result = v
	case json.RawMessage:
		if err = json.Unmarshal(v, &result); err != nil {
			return err
		}
	default:
		if b, err = json.Marshal(v); err != nil {
			return err
		}
		if err = json.Unmarshal(b, &result); err != nil {
			return err
		}
	}

	*dest = result

	return nil
}
