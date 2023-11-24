package round

import (
	"context"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	"github.com/dymensionxyz/dymint/tplus"
	abci "github.com/tendermint/tendermint/abci/types"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
)

type FilterEventsData struct {
	events map[string][]abci.Event
}

type EventsFilter struct {
	eventsCh  chan FilterEventsData
	eventsCap int
}

func NewEventsFilter(eventsCap int) *EventsFilter {
	return &EventsFilter{
		eventsCh:  make(chan FilterEventsData, eventsCap),
		eventsCap: eventsCap,
	}
}

type FilterHandler func(events map[string][]abci.Event)

func (ef *EventsFilter) Run(ctx context.Context, handler FilterHandler) {
	for data := range ef.eventsCh {
		handler(data.events)
	}
}

func (ef *EventsFilter) PublishResponses(responses *tmstate.ABCIResponses) {
	ctx := context.Background()
	roundEvents := map[string][]abci.Event{}
	for _, deliverTx := range responses.DeliverTxs {
		if deliverTx.Code != 0 {
			continue
		}
		// Filter Initgame
		eventsInitGame := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeInitGame)
		roundEvents[minidicetypes.EventTypeInitGame] = eventsInitGame

		// Filter StartRound
		eventsStart := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeStartRound)
		roundEvents[minidicetypes.EventTypeStartRound] = eventsStart

		// Filter EndRound
		eventsEnd := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeEndRound)
		roundEvents[minidicetypes.EventTypeEndRound] = eventsEnd

		// Filter FinalizeRound
		eventsFinalize := tplus.FindEventsByType(deliverTx.Events, minidicetypes.EventTypeFinalizeRound)
		roundEvents[minidicetypes.EventTypeFinalizeRound] = eventsFinalize
	}

	select {
	case ef.eventsCh <- FilterEventsData{events: roundEvents}:
		return
	case <-ctx.Done():
		return
	}
}
