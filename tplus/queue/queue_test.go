package queue

import (
	"testing"
	"time"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/davecgh/go-spew/spew"
)

func TestPopBatch(t *testing.T) {
	qu := NewQueue()
	qu.Push(MsgInQueue{
		Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "2")},
		TimeExec: 2})
	qu.Push(MsgInQueue{
		Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "3")},
		TimeExec: 3})
	qu.Push(MsgInQueue{
		Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "1")},
		TimeExec: 1})

	batch, found := qu.PopBatch(2)
	if !found {
		t.Errorf("PopBatch should return true")
	}
	spew.Dump(batch)
}

func TestPopByTime(t *testing.T) {
	timeStart := time.Now()
	qu := NewQueue()
	qu.Push(MsgInQueue{
		Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "2")},
		TimeExec: 2})
	qu.Push(MsgInQueue{
		Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "3")},
		TimeExec: 3})
	qu.Push(MsgInQueue{
		Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "3")},
		TimeExec: 4})
	qu.Push(MsgInQueue{
		Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "1")},
		TimeExec: 1})

	batch, found := qu.PopByTime(3, 10)
	if !found {
		t.Errorf("PopBatch should return true")
	}
	spew.Dump(batch)
	t.Logf("PopByTime %d", qu.Len())

	msg := []MsgInQueue{
		{Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "2")}, TimeExec: 12},
		//{Messages: []sdk.Msg{minidicetypes.NewMsgStartRound("test", "3")}, TimeExec: 3},
	}

	qu.PushBatch(msg)

	batch, found = qu.PopByTime(10, 10)
	if !found {
		t.Errorf("PopBatch should return true")
	}
	spew.Dump(batch)
	t.Logf("PopByTime %d", qu.Len())

	batch, found = qu.PopByTime(13, 10)
	if !found {
		t.Errorf("PopBatch should return true")
	}
	spew.Dump(batch)
	t.Logf("PopByTime %d", qu.Len())
	t.Logf("Time %d", time.Since(timeStart).Milliseconds())
}
