package wallet

import (
	"bytes"
	"errors"
	"testing"

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
	raw, err := SealBackup([]byte("k"), nil, "right")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, _, err := OpenBackup(raw, "wrong"); !errors.Is(err, ErrBadBackupPassphrase) {
		t.Fatalf("got %v, want ErrBadBackupPassphrase", err)
	}
	if _, err := SealBackup([]byte("k"), nil, "abc"); err == nil {
		t.Fatal("expected a too-short passphrase to be refused")
	}
	if _, _, err := OpenBackup([]byte(`{"hello":1}`), "x"); err == nil {
		t.Fatal("expected a non-backup file to be refused")
	}
}
