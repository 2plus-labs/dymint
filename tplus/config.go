package tplus

import (
	"errors"
)

const (
	AddressPrefix   = "tplus"
	DefaultGasLimit = 3000000
)

type Config struct {
	KeyringBackend   string `mapstructure:"keyring_backend"`
	NodeAddress      string `mapstructure:"node_address"`
	KeyringHomeDir   string `mapstructure:"keyring_home_dir"`
	TplusAccountName string `mapstructure:"tplus_account_name"`
	GasLimit         uint64 `mapstructure:"gas_limit"`
	GasPrices        string `mapstructure:"gas_prices"`
	GasFees          string `mapstructure:"gas_fees"`
}

func (c Config) Validate() error {
	if c.GasPrices != "" && c.GasFees != "" {
		return errors.New("cannot provide both fees and gas prices")
	}
	if c.GasPrices == "" && c.GasFees == "" {
		return errors.New("must provide either fees or gas prices")
	}
	return nil
}

func DefaultConfig() *Config {
	return &Config{
		KeyringBackend:   "test",
		NodeAddress:      "http://127.0.0.1:26657",
		KeyringHomeDir:   "~/.2plus",
		TplusAccountName: "rol-user",
		GasLimit:         DefaultGasLimit,
		GasPrices:        "0.025uplus",
	}
}