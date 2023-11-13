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

const (
	QueryMinidiceInitGame      = "InitGame.init_game='init_game'"
	QueryMinidiceStartRound    = "StartRound.start_round='start_round'"
	QueryMinidiceFinalizeRound = "FinalizeRound.finalize_round='finalize_round'"
	QueryMinidiceEndRound      = "EndRound.end_round='end_round'"
)

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
