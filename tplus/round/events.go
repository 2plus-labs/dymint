package round

import (
	"fmt"

	tmpubsub "github.com/tendermint/tendermint/libs/pubsub"
	tmquery "github.com/tendermint/tendermint/libs/pubsub/query"
)

const (
	EventMinidiceTypekey = "minidice.event"

	EventMinidiceInitGame      = "MinidiceInitGame"
	EventMinidiceStartRound    = "MinidiceStartRound"
	EventMinidiceEndRound      = "MinidiceEndRound"
	EventMinidiceFinalizeRound = "MinidiceFinalizeRound"
)

type MinidiceInitGameData struct {
	Denom string
}

type MinidiceStartRoundData struct {
	Denom string
}

type MinidiceEndRoundData struct {
	Denom string
}

type MinidiceFinalizeRoundData struct {
	Denom string
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
