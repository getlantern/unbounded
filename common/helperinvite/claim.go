package helperinvite

import (
	"crypto/ed25519"
	"fmt"
	"io"
)

// Claim is a volunteer's claim on an invite: the fields of lantern-cloud's
// HelperClaimInviteRequest, and the matching code to show.
type Claim struct {
	InviteHash      []byte
	VolunteerPubkey []byte
	VolunteerBlob   []byte
	Code            string
}

// NewClaim builds the claim that the volunteer whose device key has seed
// sends for secret s, with its name sealed for the inviter.
func NewClaim(s, seed []byte, name string, r io.Reader) (Claim, error) {
	if len(s) != SecretLen {
		return Claim{}, ErrSecret
	}
	priv, err := DeviceKey(seed)
	if err != nil {
		return Claim{}, err
	}
	pub := []byte(priv.Public().(ed25519.PublicKey))
	blob, err := SealName(VolunteerNameKey(s), name, r)
	if err != nil {
		return Claim{}, err
	}
	return Claim{InviteHash: InviteHash(s), VolunteerPubkey: pub, VolunteerBlob: blob,
		Code: MatchingCode(DeriveKey(s), pub)}, nil
}

// Approval is what an inviter sends to approve a claim: the approval
// signature, and its own name sealed for the volunteer.
type Approval struct {
	Signature   []byte
	InviterBlob []byte
}

// NewApproval approves volunteerPub for the invite with secret s.
func NewApproval(s, volunteerPub []byte, inviterName string, r io.Reader) (Approval, error) {
	if len(s) != SecretLen {
		return Approval{}, ErrSecret
	}
	if len(volunteerPub) != ed25519.PublicKeySize {
		return Approval{}, fmt.Errorf("%w: a volunteer key is 32 bytes", ErrKey)
	}
	blob, err := SealName(InviterNameKey(s), inviterName, r)
	if err != nil {
		return Approval{}, err
	}
	return Approval{Signature: Approve(s, volunteerPub), InviterBlob: blob}, nil
}
