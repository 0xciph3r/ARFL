package app

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/client"
)

func TestShouldTreatProofsAsSpent(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "burned node rejection",
			err:  &client.NodeRejectedError{StatusCode: http.StatusConflict, Message: "already spent"},
			want: true,
		},
		{
			name: "non-burned node rejection",
			err:  &client.NodeRejectedError{StatusCode: http.StatusBadRequest, Message: "invalid payload"},
			want: false,
		},
		{
			name: "proof spend uncertainty",
			err:  &client.ProofSpendUncertainError{Reason: "published but timed out"},
			want: true,
		},
		{
			name: "wrapped uncertainty",
			err:  fmt.Errorf("entry connect failed: %w", &client.ProofSpendUncertainError{Cause: errors.New("timeout")}),
			want: true,
		},
		{
			name: "generic error",
			err:  errors.New("network down"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldTreatProofsAsSpent(tc.err); got != tc.want {
				t.Fatalf("shouldTreatProofsAsSpent()=%v, want %v (err=%v)", got, tc.want, tc.err)
			}
		})
	}
}
