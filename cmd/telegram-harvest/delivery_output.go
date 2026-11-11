package main

import (
	"encoding/json"
	"io"
)

// writeDeliveryResult keeps evidence and failure states in apply receipts without
// repeating reviewed message bodies. Full previews remain available before send.
func writeDeliveryResult(out io.Writer, result any, compact bool) error {
	if !compact {
		return json.NewEncoder(out).Encode(result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	delete(value, "text")
	delete(value, "incoming_text")
	delete(value, "effective_entities")
	if raw, ok := value["rows"]; ok {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			delete(row, "effective_entities")
			if rawPost, ok := row["post"]; ok {
				var post map[string]json.RawMessage
				if err := json.Unmarshal(rawPost, &post); err != nil {
					return err
				}
				delete(post, "text")
				delete(post, "entities")
				row["post"], err = json.Marshal(post)
				if err != nil {
					return err
				}
			}
		}
		value["rows"], err = json.Marshal(rows)
		if err != nil {
			return err
		}
	}
	value["content_omitted"] = json.RawMessage(`true`)
	return json.NewEncoder(out).Encode(value)
}
