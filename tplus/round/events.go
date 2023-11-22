package round

import (
	"bytes"
	"fmt"

	abci "github.com/tendermint/tendermint/abci/types"
	tmpubsub "github.com/tendermint/tendermint/libs/pubsub"
	tmquery "github.com/tendermint/tendermint/libs/pubsub/query"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
)

const (
	EventMinidiceTypekey = "minidice.event"

	EventMinidiceInitGame      = "MinidiceInitGame"
	EventMinidiceStartRound    = "MinidiceStartRound"
	EventMinidiceEndRound      = "MinidiceEndRound"
	EventMinidiceFinalizeRound = "MinidiceFinalizeRound"
)

const (
	QueryMinidiceInitGame      = "InitGame.init_game='init_game'"
	QueryMinidiceStartRound    = "StartRound.start_round='start_round'"
	QueryMinidiceFinalizeRound = "FinalizeRound.finalize_round='finalize_round'"
	QueryMinidiceEndRound      = "EndRound.end_round='end_round'"
)

type Manager interface {
	FilterRoundEvent(state *tmstate.ABCIResponses) error
	Start() error
}

type MinidiceInitGameData struct {
	GameId string
}

type MinidiceStartRoundData struct {
	GameId string
}

type MinidiceEndRoundData struct {
	GameId string
}

type MinidiceFinalizeRoundData struct {
	GameId string
}

var (
	EventMinidiceInitGameQuery      = QueryForEvent(EventMinidiceInitGame)
	EventMinidiceStartRoundQuery    = QueryForEvent(EventMinidiceStartRound)
	EventMinidiceEndRoundQuery      = QueryForEvent(EventMinidiceEndRound)
	EventMinidiceFinalizeRoundQuery = QueryForEvent(EventMinidiceFinalizeRound)
)

// QueryForEvent returns a query for the given event.
func QueryForEvent(eventType string) tmpubsub.Query {
	return tmquery.MustParse(fmt.Sprintf("%s='%s'", EventMinidiceTypekey, eventType))
}

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
