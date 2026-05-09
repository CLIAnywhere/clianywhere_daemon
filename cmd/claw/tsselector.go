package main

// SelectBestTurnServer concurrently probe all TS, return the best (lowest latency)
// NOTE: currently unused, replaced by Worker-side getturnserver selection
func SelectBestTurnServer(servers []TurnServerEntry, logger Logger) *TurnServerEntry {
	if len(servers) == 0 {
		return nil
	}
	if len(servers) == 1 {
		return &servers[0]
	}

	// TODO: re-implement latency probing with new addr format if needed
	return &servers[0]
}
