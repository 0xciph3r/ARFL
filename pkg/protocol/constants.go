package protocol

// ARFL protocol constants — fixed at protocol level, changed only by consensus.

const (
	// BundleSizeGB is the standard unit of bandwidth sold per bundle.
	BundleSizeGB = 250

	// BundleExpiryDays is how long a purchased bundle remains valid.
	BundleExpiryDays = 30

	// MinBandwidthMbps is the minimum upload/download speed for node operators.
	MinBandwidthMbps = 50

	// PingIntervalSeconds is how often nodes publish health pings.
	PingIntervalSeconds = 60

	// OfflineTriggerMinutes is consecutive offline time before refund activates.
	OfflineTriggerMinutes = 30

	// QuotaSlabBytes is the nftables kernel quota slab size (256 MB).
	QuotaSlabBytes = 268435456

	// ByteCounterPollSeconds is how often the node polls WireGuard byte counters.
	ByteCounterPollSeconds = 5

	// DNSResolver is the privacy-preserving DNS resolver used within tunnels.
	DNSResolver = "9.9.9.9"

	// OuterTunnelPort is the default WireGuard listen port for entry nodes.
	OuterTunnelPort = 51820

	// InnerTunnelPort is the default WireGuard listen port for exit nodes.
	InnerTunnelPort = 51821

	// TunnelMTU is the MTU for tunnel interfaces (safe for nested WG overhead).
	TunnelMTU = 1280

	// OuterTunnelSubnet is the network prefix for outer tunnel addresses.
	OuterTunnelSubnet = "10.100.0.0/16"

	// InnerTunnelSubnet is the network prefix for inner tunnel addresses.
	InnerTunnelSubnet = "10.200.0.0/16"
)

// Revenue split percentages — applied to the floor price.
const (
	EntryNodeSplitPct  = 40
	ExitNodeSplitPct   = 40
	HubSplitPct        = 20
)

// Nostr event kinds used by the ARFL protocol.
const (
	NostrKindNodeAnnouncement = 30078
	NostrKindHubAnnouncement  = 30079
	NostrKindHubSubscription  = 30080
)
