//go:build !linux && !darwin

package meshnet

import "net/netip"

func osGateways() []Gateway { return nil }

func osWireless(string) (bool, string) { return false, "" }

func osRoutes() map[string][]netip.Prefix { return nil }
