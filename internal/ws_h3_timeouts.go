package internal

import "time"

// Shared between runtime code and unit-tag builds that still compile healthcheck logic.
const h3HandshakeTimeout = 12 * time.Second
