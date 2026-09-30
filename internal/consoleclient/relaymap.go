// SPDX-License-Identifier: AGPL-3.0-only

//go:build !js

package consoleclient

import (
	"github.com/tmc/go-iroh/netaddr"
	"github.com/tmc/go-iroh/relay"
)

func relayMap(urls []netaddr.RelayURL) *relay.Map {
	return relay.MapFromURLs(urls...)
}
