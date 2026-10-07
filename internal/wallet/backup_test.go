package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gcrypto "github.com/elnosh/gonuts/crypto"

	"github.com/elnosh/gonuts/cashu"
)

func TestBackupRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	hubs := map[string]cashu.Proofs{"http://hub": {{Amount: 64, Id: "ks", Secret: "s1", C: "02ab"}}}

	raw, err := SealBackup(key, hubs, "correct horse")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(raw, []byte("s1")) {
		t.Fatal("proof secret visible in the backup file")
	}

	gotKey, gotHubs, err := OpenBackup(raw, "correct horse")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(gotKey, key) || len(gotHubs["http://hub"]) != 1 || gotHubs["http://hub"][0].Secret != "s1" {
		t.Fatalf("round trip mismatch: key=%q hubs=%+v", gotKey, gotHubs)
	}
}

func TestBackupRejectsWrongPassphraseAndShortOnes(t *testing.T) {
	raw, err := SealBackup([]byte("k"), nil, "right passphrase")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, _, err := OpenBackup(raw, "wrong passphrase"); !errors.Is(err, ErrBadBackupPassphrase) {
		t.Fatalf("got %v, want ErrBadBackupPassphrase", err)
	}
	if _, err := SealBackup([]byte("k"), nil, "elevenchars"); err == nil {
		t.Fatal("expected a too-short passphrase to be refused")
	}
	if _, _, err := OpenBackup([]byte(`{"hello":1}`), "x"); err == nil {
		t.Fatal("expected a non-backup file to be refused")
	}
}

func TestMoveHubRefilesProofsWithoutDuplicates(t *testing.T) {
	s, err := OpenProofStore(t.TempDir()+"/v.json", "pass")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Add("http://old", cashu.Proofs{{Amount: 64, Secret: "a"}, {Amount: 32, Secret: "b"}})
	_ = s.Add("https://new", cashu.Proofs{{Amount: 32, Secret: "b"}})
	if err := s.MoveHub("http://old", "https://new"); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if len(snap["http://old"]) != 0 || snap["https://new"].Amount() != 96 {
		t.Fatalf("got %+v", snap)
	}
}

// A file with a missing or wrong-sized nonce must be refused, not crash.
func TestOpenBackupWithBadNonceDoesNotPanic(t *testing.T) {
	raw, err := SealBackup([]byte("k"), nil, "right passphrase")
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	_ = json.Unmarshal(raw, &f)
	f["nonce"] = ""
	broken, _ := json.Marshal(f)
	if _, _, err := OpenBackup(broken, "right passphrase"); err == nil {
		t.Fatal("expected an error for a missing nonce")
	}
}

func checkstateServer(t *testing.T, state func(y string) string) *MintClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Ys []string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		type st struct {
			Y     string `json:"Y"`
			State string `json:"state"`
		}
		var out struct {
			States []st `json:"states"`
		}
		for _, y := range req.Ys {
			if s := state(y); s != "" {
				out.States = append(out.States, st{Y: y, State: s})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	c, err := NewMintClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func yOf(t *testing.T, secret string) string {
	t.Helper()
	y, err := gcrypto.HashToCurve([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(y.SerializeCompressed())
}

func TestUnspentProofsDropsOnlySpent(t *testing.T) {
	spentY := yOf(t, "spent")
	c := checkstateServer(t, func(y string) string {
		if y == spentY {
			return "SPENT"
		}
		return "UNSPENT"
	})
	got, err := c.UnspentProofs(context.Background(), cashu.Proofs{{Amount: 1, Secret: "spent"}, {Amount: 2, Secret: "live"}})
	if err != nil || len(got) != 1 || got[0].Secret != "live" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Pending or unreported proofs may still be good; restore keeps them all.
func TestUnspentProofsRefusesPendingOrMissing(t *testing.T) {
	pending := checkstateServer(t, func(string) string { return "PENDING" })
	if _, err := pending.UnspentProofs(context.Background(), cashu.Proofs{{Amount: 1, Secret: "a"}}); err == nil {
		t.Fatal("pending proof was not reported as an error")
	}
	missing := checkstateServer(t, func(string) string { return "" })
	if _, err := missing.UnspentProofs(context.Background(), cashu.Proofs{{Amount: 1, Secret: "a"}}); err == nil {
		t.Fatal("missing state was not reported as an error")
	}
}
