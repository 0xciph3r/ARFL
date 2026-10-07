package helper

// Label identifies the helper to the service manager.
const Label = "io.arfl.helper"

// SocketPath is where the helper listens and the app connects.
const SocketPath = "/var/run/" + Label + ".sock"

// Config is written at install time and read by the helper at start.
type Config struct {
	// AllowedUID is the user who installed the helper; only they (and root)
	// may drive the tunnel.
	AllowedUID int `json:"allowed_uid"`
}
