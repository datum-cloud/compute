// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"github.com/tmc/go-iroh/netaddr"
	"github.com/tmc/go-iroh/relay"
)

// relayMap leaves out the relays' QUIC address discovery, which needs UDP that
// a browser does not have.
func relayMap(urls []netaddr.RelayURL) *relay.Map {
	configs := make([]relay.Config, len(urls))
	for i, u := range urls {
		configs[i] = relay.Config{URL: u}
	}
	return relay.NewMap(configs...)
}
