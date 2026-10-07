package app

import (
	"errors"
	"testing"

	"github.com/Radi-Labs/ARFL/pkg/types"
)

func pinNodes() []types.NodeInfo {
	return []types.NodeInfo{
		{ID: "a", NostrPubkey: "op1-node-a", OperatorID: "op1", Role: types.RoleBoth},
		{ID: "b", NostrPubkey: "op2", Role: types.RoleExit},
		{ID: "c", NostrPubkey: "op1-node-c", OperatorID: "op1", Role: types.RoleExit},
		{ID: "d", NostrPubkey: "op3", Role: types.RoleEntry},
	}
}

func TestRestrictToPinnedKeepsOnlyTheChosenPairInTheirRoles(t *testing.T) {
	got, err := restrictToPinned(pinNodes(), PinnedPair{EntryID: "a", ExitID: "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[0].Role != types.RoleEntry || got[1].ID != "b" || got[1].Role != types.RoleExit {
		t.Fatalf("got %+v, want entry a then exit b", got)
	}
}

func TestRestrictToPinnedRejectsWrongRoleOrOfflineNode(t *testing.T) {
	cases := []PinnedPair{
		{EntryID: "b", ExitID: "d"}, // b cannot enter, d cannot exit
		{EntryID: "a", ExitID: "gone"},
	}
	for _, pin := range cases {
		if _, err := restrictToPinned(pinNodes(), pin); !errors.Is(err, ErrPinnedNodeOffline) {
			t.Errorf("pin %+v: got %v, want ErrPinnedNodeOffline", pin, err)
		}
	}
}

func TestRestrictToPinnedAllowsDistinctNodesOfOneOperator(t *testing.T) {
	nodes, err := restrictToPinned(pinNodes(), PinnedPair{EntryID: "a", ExitID: "c"})
	if err != nil || len(nodes) != 2 {
		t.Fatalf("distinct nodes of one operator must be usable with a privacy warning: %v, %+v", err, nodes)
	}
}

func TestSetPinnedPairValidatesAndClears(t *testing.T) {
	s := &Service{}
	if err := s.SetPinnedPair(&PinnedPair{EntryID: "a"}); err == nil {
		t.Fatal("expected an error for a pin without an exit")
	}
	if err := s.SetPinnedPair(&PinnedPair{EntryID: "a", ExitID: "a"}); !errors.Is(err, ErrPinnedSameNode) {
		t.Fatalf("got %v, want ErrPinnedSameNode", err)
	}
	if err := s.SetPinnedPair(&PinnedPair{EntryID: "a", ExitID: "b"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p := s.PinnedPair(); p == nil || p.ExitID != "b" {
		t.Fatalf("pin not stored: %+v", p)
	}
	_ = s.SetPinnedPair(nil)
	if s.PinnedPair() != nil {
		t.Fatal("pin not cleared")
	}
}

// One operator running two nodes under different keys must not be accepted
// as two separate hops.
func TestRestrictToPinnedAcceptsAttestedSameOperatorWithWarning(t *testing.T) {
	nodes := []types.NodeInfo{
		{ID: "a", NostrPubkey: "k1", OperatorID: "same-op", Role: types.RoleEntry},
		{ID: "b", NostrPubkey: "k2", OperatorID: "same-op", Role: types.RoleExit},
		{ID: "c", NostrPubkey: "k3", OperatorID: "other-op", Role: types.RoleExit},
	}
	if _, err := restrictToPinned(nodes, PinnedPair{EntryID: "a", ExitID: "b"}); err != nil {
		t.Fatalf("different nodes of one operator should be allowed: %v", err)
	}
	if _, err := restrictToPinned(nodes, PinnedPair{EntryID: "a", ExitID: "c"}); err != nil {
		t.Fatalf("different operators refused: %v", err)
	}
}
