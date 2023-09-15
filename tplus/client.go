package tplus

import (
	"context"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/dymensionxyz/cosmosclient/cosmosclient"
	"github.com/ignite/cli/ignite/pkg/cosmosaccount"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
)

type TplusClient struct {
	cosmosclient.Client

	minidiceQuery minidicetypes.QueryClient
}

func NewTplusClient(config *Config) (*TplusClient, error) {
	if config.GasLimit == 0 {
		config.GasLimit = DefaultGasLimit
	}

	options := []cosmosclient.Option{
		cosmosclient.WithAddressPrefix(AddressPrefix),
		cosmosclient.WithBroadcastMode(flags.BroadcastSync),
		cosmosclient.WithNodeAddress(config.NodeAddress),
		cosmosclient.WithFees(config.GasFees),
		cosmosclient.WithGasLimit(config.GasLimit),
		cosmosclient.WithGasPrices(config.GasPrices),
	}
	if config.KeyringHomeDir != "" {
		options = append(options,
			cosmosclient.WithKeyringBackend(cosmosaccount.KeyringBackend(config.KeyringBackend)),
			cosmosclient.WithHome(config.KeyringHomeDir),
		)
	}
	client, err := cosmosclient.New(context.Background(), options...)
	if err != nil {
		return nil, err
	}
	return &TplusClient{
		Client:        client,
		minidiceQuery: minidicetypes.NewQueryClient(client.Context()),
	}, nil
}

func (t *TplusClient) GetActiveGames(ctx context.Context) []*minidicetypes.ActiveGame {
	activeGamesReponse, err := t.minidiceQuery.ActiveGames(ctx, &minidicetypes.QueryActiveGamesRequest{})
	if err != nil {
		return []*minidicetypes.ActiveGame{}
	}

	return activeGamesReponse.ActiveGames
}

func (t *TplusClient) StartEventListener() error {
	return t.Client.RPC.WSEvents.Start()
}

func (t *TplusClient) StopEventListener() error {
	return t.Client.RPC.WSEvents.Stop()
}

func (t *TplusClient) EventListenerQuit() <-chan struct{} {
	return t.Client.RPC.GetWSClient().Quit()
}

func (t *TplusClient) SubscribeToEvents(
	ctx context.Context,
	subscriber string,
	query string,
	outCapacity ...int,
) (out <-chan ctypes.ResultEvent, err error) {
	return t.Client.RPC.WSEvents.Subscribe(ctx, subscriber, query, outCapacity...)
}

func (t *TplusClient) GetAccountAddress(accountName string) (string, error) {
	account, err := t.Client.AccountRegistry.GetByName(accountName)
	if err != nil {
		return "", err
	}

	addr, err := account.Address(AddressPrefix)
	if err != nil {
		return "", err
	}
	return addr, nil
}

func (t *TplusClient) GetActiveGame(denom string) (minidicetypes.ActiveGame, error) {
	gameInfo, err := t.minidiceQuery.GameInfo(context.Background(), &minidicetypes.QueryGameInfoRequest{
		Denom: denom,
	})
	if err != nil {
		return minidicetypes.ActiveGame{}, err
	}

	ac := minidicetypes.ActiveGame{
		Denom:         denom,
		RoundId:       gameInfo.GameInfo.CurrentRoundId,
		GameId:        gameInfo.GameInfo.GameId,
		StartRound:    gameInfo.RoundInfo.StartRound,
		EndRound:      gameInfo.RoundInfo.EndRound,
		FinalizeRound: gameInfo.RoundInfo.FinalizeRound,
		State:         gameInfo.RoundInfo.State,
	}
	return ac, nil
}
