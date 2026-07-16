package main

// version is overridden at build time via -ldflags "-X main.version=...".
// No release process exists yet (W7.2 wires the installer; cutting actual
// releases is later work), so builds outside that pipeline report "dev".
var version = "dev"
