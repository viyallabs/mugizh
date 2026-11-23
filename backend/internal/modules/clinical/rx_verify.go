package clinical

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
)

// VerifyResult is all the public verification page may learn (review R-09, TC-60): whether
// the reference names a genuine, unaltered signed prescription, and if so the date it was
// signed and the prescriber's registration number. Never any patient data.
type VerifyResult struct {
	Valid              bool
	SignedAt           time.Time
	RegistrationNumber string
}

// AllowVerify applies the per-IP rate limit of the public verification page.
func (r *Rx) AllowVerify(clientIP string) (bool, time.Duration) {
	return r.limiter.Allow("verify:" + httpx.IPBucket(clientIP))
}

func validReference(ref string) bool {
	if len(ref) != 2*verificationTokenN {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}

// VerifyReference checks the stored signed document behind a verification reference: its
// bytes must still hash to the signed hash and the signature must verify. Unknown or
// malformed references answer exactly like invalid ones.
func (r *Rx) VerifyReference(ctx context.Context, ref string) VerifyResult {
	return r.verify(ctx, ref, nil)
}

// VerifyDocument additionally requires that the supplied bytes (for example a PDF's
// canonical document) equal the signed document: a single changed byte is not valid.
func (r *Rx) VerifyDocument(ctx context.Context, ref string, doc []byte) VerifyResult {
	if doc == nil {
		doc = []byte{}
	}
	return r.verify(ctx, ref, doc)
}

func (r *Rx) verify(ctx context.Context, ref string, supplied []byte) VerifyResult {
	// Same shape of work for every answer: one hash, one lookup, one constant-time compare.
	h := sha256.Sum256([]byte(ref))
	var verID, patient, doctor string
	var docS, sigS careKey
	var stored []byte
	var signedAt time.Time
	found := false
	if validReference(ref) {
		err := r.m.pool.QueryRow(ctx, `SELECT v.id::text, p.patient_ref::text, p.doctor_ref::text, v.document, v.document_sha256, v.signature, v.signed_at
			FROM prescription_version v JOIN prescription p ON p.id = v.prescription_id WHERE v.verification_hash = $1 AND v.state = 'signed'`, h[:]).
			Scan(&verID, &patient, &doctor, &docS, &stored, &sigS, &signedAt)
		found = err == nil
	}
	if !found {
		_ = subtle.ConstantTimeCompare(h[:], h[:])
		return VerifyResult{}
	}
	doc, err := r.open(ctx, "prescription_version", "document", verID, docS)
	if err != nil {
		return VerifyResult{}
	}
	got := sha256.Sum256(doc)
	if subtle.ConstantTimeCompare(got[:], stored) != 1 {
		return VerifyResult{}
	}
	if supplied != nil {
		sup := sha256.Sum256(supplied)
		if subtle.ConstantTimeCompare(sup[:], stored) != 1 {
			return VerifyResult{}
		}
	}
	if r.d.Signer == nil {
		return VerifyResult{} // cannot verify: not valid (fail closed)
	}
	sraw, err := r.open(ctx, "prescription_version", "signature", verID, sigS)
	if err != nil {
		return VerifyResult{}
	}
	var ss storedSignature
	if json.Unmarshal(sraw, &ss) != nil {
		return VerifyResult{}
	}
	verdict, err := r.d.Signer.Verify(ctx, stored, esign.Signature{Value: ss.Value, Certificate: ss.Certificate, SignedAt: ss.SignedAt, Provider: ss.Provider})
	if err != nil || verdict != esign.Valid {
		return VerifyResult{}
	}
	var d Document
	if json.Unmarshal(doc, &d) != nil || d.VerificationReference != ref {
		return VerifyResult{}
	}
	return VerifyResult{Valid: true, SignedAt: signedAt.UTC(), RegistrationNumber: d.Doctor.RegistrationNumber}
}
