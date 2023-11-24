package round

import (
	"bytes"
	"fmt"

	abci "github.com/tendermint/tendermint/abci/types"
)

func FindEventsByType(events []abci.Event, eventType string) []abci.Event {
	var found []abci.Event
	for _, event := range events {
		if event.Type == eventType {
			found = append(found, event)
		}
	}

	return found
}

func FindAttributeByKey(event abci.Event, attrKey string) (abci.EventAttribute, error) {
	for _, attr := range event.Attributes {
		if bytes.Equal(attr.Key, []byte(attrKey)) {
			return attr, nil
		}
	}

	return abci.EventAttribute{}, fmt.Errorf("no attribute with key %s found inside event with type %s", attrKey, event.Type)
}
