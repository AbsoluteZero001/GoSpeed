//go:build !windows && !linux && !darwin

package netdiag

// platformGatewayAndDNS reports that the default gateway and the configured
// DNS servers cannot be read on this platform.
//
// Reporting unknown is the correct outcome. The alternative, presenting a
// plausible looking gateway nobody read, is exactly the kind of invented number
// this package exists to avoid.
func platformGatewayAndDNS() platformNetwork {
	return platformNetwork{
		DNSServers: []string{},
		Note: "this platform has no gateway or DNS reader, so both are reported as " +
			"unknown rather than guessed",
	}
}
