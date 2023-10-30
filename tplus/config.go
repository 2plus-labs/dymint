package tplus

import (
	"errors"
)

const (
	AddressPrefix   = "tplus"
	DefaultGasLimit = 10000000
)

type Config struct {
	KeyringBackend string `mapstructure:"keyring_backend"`
	NodeAddress    string `mapstructure:"node_address"`
	KeyringHomeDir string `mapstructure:"keyring_home_dir"`
	AccountName    string `mapstructure:"account_name"`
	GasLimit       uint64 `mapstructure:"gas_limit"`
	GasPrices      string `mapstructure:"gas_prices"`
	GasFees        string `mapstructure:"gas_fees"`
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
		KeyringBackend: "test",
		NodeAddress:    "http://127.0.0.1:26657",
		KeyringHomeDir: "~/.2plus",
		AccountName:    "rol-user",
		GasLimit:       DefaultGasLimit,
		GasPrices:      "0.025uplus",
		GasFees:        "",
	}
}
