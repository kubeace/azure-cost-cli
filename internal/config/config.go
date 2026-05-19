// Package config wires viper for azcost.
//
// Precedence: flag > env (AZCOST_*) > config file > built-in default.
//
// Config file: ~/.config/azcost.yaml (or any directory in viper's search
// path). Loaded once in Init() at process start. If the file is missing the
// process continues silently — config is always optional.
//
// Env variables: AZCOST_TENANT, AZCOST_RATE, AZCOST_RPS, AZCOST_CURRENCY,
// AZCOST_FORMAT, AZCOST_TOP, AZCOST_SUB, AZCOST_SUBS, AZCOST_FROM, AZCOST_TO.
// Dashes in flag names map to underscores in env (none today, but reserved).
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

const (
	// DefaultRate is the divisor applied to the raw billing-currency value
	// to produce the "USD" column. The default of 1.0 is a passthrough —
	// correct when your Azure EA bills in USD. For other billing
	// currencies (INR, EUR, GBP, …), set --rate to your local-to-USD ratio
	// (e.g. --rate 89.985 for an INR-billed enrollment) and --currency
	// both to show the original alongside USD.
	DefaultRate = 1.0
	DefaultRPS  = 5
)

// Init loads the config file if present and binds AZCOST_* env vars. Returns
// the path that was loaded (empty if none).
func Init() string {
	v := viper.GetViper()
	v.SetEnvPrefix("AZCOST")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()

	v.SetDefault("rate", DefaultRate)
	v.SetDefault("rps", DefaultRPS)
	v.SetDefault("currency", "USD")
	v.SetDefault("format", "")

	if home, err := os.UserHomeDir(); err == nil {
		v.AddConfigPath(filepath.Join(home, ".config"))
	}
	v.SetConfigName("azcost")
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err == nil {
		return v.ConfigFileUsed()
	}
	return ""
}
