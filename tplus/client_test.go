package tplus

import (
	"context"
	"testing"

	minidicetypes "github.com/2plus-labs/2plus-core/x/minidice/types"
)

func testConfig() Config {
	return Config{
		KeyringBackend: "test",
		NodeAddress:    "http://127.0.0.1:26657",
		KeyringHomeDir: "~/.2plus",
		AccountName:    "tpluser",
		GasLimit:       DefaultGasLimit,
		GasPrices:      "0.025uplus",
		GasFees:        "",
	}
}

func TestClientQuery(t *testing.T) {
	config := testConfig()
	client, err := NewTplusClient(&config)
	if err != nil {
		t.Fatalf("error: %s", err.Error())
	}
	paramsResponse, err := client.minidiceQuery.Params(context.Background(), &minidicetypes.QueryParamsRequest{})
	if err != nil {
		t.Fatalf("error: %s", err.Error())
	}

	t.Logf("params: %s", paramsResponse.String())

	activeGamesResponse, err := client.minidiceQuery.ActiveGames(context.Background(), &minidicetypes.QueryActiveGamesRequest{})
	if err != nil {
		t.Fatalf("error query active games: %s", err.Error())
	}

	t.Logf("active games: %s", activeGamesResponse.String())
}

func TestGetAccount(t *testing.T) {
	config := testConfig()
	client, err := NewTplusClient(&config)
	if err != nil {
		t.Fatalf("error: %s", err.Error())
	}
	accountAddr, err := client.GetAccountAddress("rol-user")
	if err != nil {
		t.Fatalf("error: %s", err.Error())
	}

	t.Logf("account address: %s", accountAddr)
}
