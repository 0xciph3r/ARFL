package wg

import (
	"fmt"
	"sync"
	"time"
)

// MockManager is a fake WireGuard manager for testing.
// It stores peers in memory instead of talking to the kernel.
//
// Why this exists: real WireGuard operations need root permissions and a
// kernel module. Tests run without either. The Manager interface lets us
// swap in this mock, and the admin API / node daemon can't tell the difference.
type MockManager struct {
	mu         sync.RWMutex
	interfaces map[string]*mockInterface
}

type mockInterface struct {
	config InterfaceConfig
	peers  map[string]*mockPeer // keyed by public key
}

type mockPeer struct {
	config        PeerConfig
	receiveBytes  int64
	transmitBytes int64
}

func NewMockManager() *MockManager {
	return &MockManager{
		interfaces: make(map[string]*mockInterface),
	}
}

func (m *MockManager) CreateInterface(cfg InterfaceConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.interfaces[cfg.Name]; exists {
		return fmt.Errorf("interface %s already exists", cfg.Name)
	}

	m.interfaces[cfg.Name] = &mockInterface{
		config: cfg,
		peers:  make(map[string]*mockPeer),
	}
	return nil
}

func (m *MockManager) DeleteInterface(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.interfaces[name]; !exists {
		return fmt.Errorf("interface %s not found", name)
	}
	delete(m.interfaces, name)
	return nil
}

func (m *MockManager) AddPeer(iface string, peer PeerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	intf, exists := m.interfaces[iface]
	if !exists {
		return fmt.Errorf("interface %s not found", iface)
	}

	intf.peers[peer.PublicKey] = &mockPeer{config: peer}
	return nil
}

func (m *MockManager) RemovePeer(iface string, pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	intf, exists := m.interfaces[iface]
	if !exists {
		return fmt.Errorf("interface %s not found", iface)
	}

	if _, exists := intf.peers[pubkey]; !exists {
		return fmt.Errorf("peer %s not found", pubkey)
	}
	delete(intf.peers, pubkey)
	return nil
}

func (m *MockManager) GetPeerStats(iface string) ([]PeerStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	intf, exists := m.interfaces[iface]
	if !exists {
		return nil, fmt.Errorf("interface %s not found", iface)
	}

	stats := make([]PeerStats, 0, len(intf.peers))
	for _, peer := range intf.peers {
		stats = append(stats, PeerStats{
			PublicKey:     peer.config.PublicKey,
			ReceiveBytes:  peer.receiveBytes,
			TransmitBytes: peer.transmitBytes,
			TotalBytes:    peer.receiveBytes + peer.transmitBytes,
			LastHandshake: time.Now(),
		})
	}
	return stats, nil
}

func (m *MockManager) Close() error {
	return nil
}

// --- Test helpers: simulate traffic ---

// SimulateTraffic adds fake byte counts to a peer, simulating network usage.
// This lets us test byte counter polling and quota logic without real traffic.
func (m *MockManager) SimulateTraffic(iface, pubkey string, rx, tx int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	intf, exists := m.interfaces[iface]
	if !exists {
		return fmt.Errorf("interface %s not found", iface)
	}

	peer, exists := intf.peers[pubkey]
	if !exists {
		return fmt.Errorf("peer %s not found", pubkey)
	}

	peer.receiveBytes += rx
	peer.transmitBytes += tx
	return nil
}

// PeerCount returns how many peers are on an interface.
func (m *MockManager) PeerCount(iface string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	intf, exists := m.interfaces[iface]
	if !exists {
		return 0
	}
	return len(intf.peers)
}
