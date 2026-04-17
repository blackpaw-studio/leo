package update

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/blackpaw-studio/leo/internal/update/fulcio"
)

// Cosign keyless signatures produced by `cosign sign-blob --yes` bind an
// ephemeral signing key to an OIDC identity. The identity lives as X.509
// extensions on the Fulcio-issued leaf certificate. These OIDs come from
// https://github.com/sigstore/fulcio/blob/main/docs/oid-info.md.
var (
	// oidOIDCIssuerV2 is the new, string-typed Issuer (1.3.6.1.4.1.57264.1.8).
	// Cosign >=2 writes issuer claims here as a UTF8String.
	oidOIDCIssuerV2 = []int{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	// oidOIDCIssuerV1 is the legacy raw-bytes Issuer (1.3.6.1.4.1.57264.1.1)
	// still emitted for backwards compatibility alongside v2.
	oidOIDCIssuerV1 = []int{1, 3, 6, 1, 4, 1, 57264, 1, 1}
)

// SignatureVerifier checks that checksumsBytes was signed by a Fulcio-issued
// identity whose SAN URI matches sanRegex and whose OIDC issuer matches
// expectedIssuer. It is intentionally narrow: it doesn't touch Rekor, doesn't
// load TUF state, and doesn't need network access. Callers embed the trusted
// Fulcio roots at build time (see the fulcio subpackage).
type SignatureVerifier struct {
	// Roots contains the Fulcio root CA(s) used as trust anchors.
	Roots *x509.CertPool
	// Intermediates contains intermediate CA(s) that may chain Fulcio
	// leaf certs to the root. Optional.
	Intermediates *x509.CertPool
	// SANRegex matches the leaf certificate's SAN URI. For GitHub Actions
	// OIDC, this looks like
	//   https://github.com/<owner>/<repo>/.github/workflows/<file>@refs/tags/<tag>
	SANRegex *regexp.Regexp
	// ExpectedIssuer is the OIDC issuer that must appear in the leaf's
	// Fulcio extensions. For GitHub Actions that's
	//   https://token.actions.githubusercontent.com
	ExpectedIssuer string
	// Now returns "verification time" — useful to swap in tests where the
	// fixture certs are long-expired. Defaults to time.Now.
	Now func() time.Time
}

// DefaultSignatureVerifier builds a verifier trusting the embedded Sigstore
// public-good Fulcio roots, with SAN regex and issuer tuned for Leo's own
// GitHub Actions release workflow. It accepts ANY tag matching our tag
// shape. Callers that know the target version should prefer
// SignatureVerifierForVersion, which pins the SAN to that exact tag and
// closes a version-downgrade attack where an attacker serves an old
// (vulnerable) release's valid signature+cert under a newer release URL.
func DefaultSignatureVerifier() (*SignatureVerifier, error) {
	return buildVerifier(`v[0-9A-Za-z.\-_]+`)
}

// SignatureVerifierForVersion builds a verifier that pins the SAN regex
// to the exact tag passed in (e.g. "v0.5.0"). The tag is baked into the
// Fulcio leaf certificate at signing time by GitHub Actions OIDC, so a
// verifier that demands the caller-supplied version rejects any signature
// issued for a different release — including stale signatures served via
// CDN cache/MITM/malicious mirror that would otherwise pass all other
// checks (chain + issuer + signature) and silently downgrade the user.
func SignatureVerifierForVersion(version string) (*SignatureVerifier, error) {
	if version == "" {
		return nil, errors.New("version is required")
	}
	return buildVerifier(regexp.QuoteMeta(version))
}

func buildVerifier(tagPattern string) (*SignatureVerifier, error) {
	roots, err := loadCertPool(fulcio.RootPEM)
	if err != nil {
		return nil, fmt.Errorf("loading Fulcio root: %w", err)
	}
	intermediates, err := loadCertPool(fulcio.IntermediatePEM)
	if err != nil {
		return nil, fmt.Errorf("loading Fulcio intermediate: %w", err)
	}

	// The expected SAN is the release workflow identity. Owner, repo, and
	// workflow filename are hard-pinned; the tag segment is pinned to
	// tagPattern so callers with a known version close the downgrade
	// window. An attacker who can't impersonate the release workflow for
	// this exact tag can't forge a matching certificate.
	sanRegex, err := regexp.Compile(
		`^https://github\.com/` + repoOwner + `/` + repoName +
			`/\.github/workflows/release\.yml@refs/tags/` + tagPattern + `$`,
	)
	if err != nil {
		return nil, fmt.Errorf("compiling SAN regex: %w", err)
	}

	return &SignatureVerifier{
		Roots:          roots,
		Intermediates:  intermediates,
		SANRegex:       sanRegex,
		ExpectedIssuer: "https://token.actions.githubusercontent.com",
	}, nil
}

// Verify checks that sigBase64 is a valid signature by leafPEM over
// checksumsBytes, and that leafPEM's identity matches the verifier's
// policy. Any failure aborts the update — the caller must not proceed to
// extract or install anything.
func (v *SignatureVerifier) Verify(checksumsBytes []byte, sigBase64, leafPEM []byte) error {
	leaf, err := parseLeafCertificate(leafPEM)
	if err != nil {
		return fmt.Errorf("parsing certificate: %w", err)
	}

	if err := v.verifyChain(leaf); err != nil {
		return fmt.Errorf("certificate chain: %w", err)
	}

	if err := v.verifyIdentity(leaf); err != nil {
		return err
	}

	sigBytes, err := decodeSignature(sigBase64)
	if err != nil {
		return fmt.Errorf("decoding signature: %w", err)
	}

	if err := verifyECDSAOrRSASignature(leaf.PublicKey, checksumsBytes, sigBytes); err != nil {
		return fmt.Errorf("verifying signature: %w", err)
	}

	return nil
}

func (v *SignatureVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// verifyChain walks the leaf cert up to the embedded Fulcio root. Leaf
// certificates issued by Fulcio for keyless signing are short-lived
// (~10 minutes), so by the time a user runs `leo update` they are
// almost always "expired" relative to now. We deliberately verify with
// the *cert's* notBefore as the reference time — that's the same
// behavior cosign uses for historical verification and what Sigstore's
// transparency log guarantees over time.
//
// Limitation: verifying with leaf.NotBefore means the cert vouches for
// its own validity window — "the cert says it was valid, we trust the
// cert to vouch for itself". Closing that loop requires consulting
// Rekor (the Sigstore transparency log) to prove the signature was
// actually entered during the cert's validity window. That's
// intentionally out of scope here; users who want it can run
// `cosign verify-blob --rfc3161-timestamp` manually (see README).
func (v *SignatureVerifier) verifyChain(leaf *x509.Certificate) error {
	opts := x509.VerifyOptions{
		Roots:         v.Roots,
		Intermediates: v.Intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		CurrentTime:   leaf.NotBefore,
	}
	if _, err := leaf.Verify(opts); err != nil {
		return err
	}
	return nil
}

// verifyIdentity enforces that the Fulcio-issued leaf actually represents
// the expected GitHub Actions workflow identity.
func (v *SignatureVerifier) verifyIdentity(leaf *x509.Certificate) error {
	san, err := leafSAN(leaf)
	if err != nil {
		return fmt.Errorf("reading SAN: %w", err)
	}
	if v.SANRegex == nil || !v.SANRegex.MatchString(san) {
		return fmt.Errorf("certificate SAN %q does not match expected identity", san)
	}

	issuer, err := leafIssuerClaim(leaf)
	if err != nil {
		return fmt.Errorf("reading OIDC issuer: %w", err)
	}
	if issuer != v.ExpectedIssuer {
		return fmt.Errorf("certificate OIDC issuer %q does not match expected %q", issuer, v.ExpectedIssuer)
	}
	return nil
}

// parseLeafCertificate accepts a PEM block — possibly a chain — and
// returns the first (leaf) certificate. cosign sign-blob writes a single
// leaf cert to the .pem output, but we tolerate extra intermediates in
// case a future cosign version bundles them.
func parseLeafCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

// loadCertPool parses one or more PEM-encoded certificates into a CertPool.
func loadCertPool(pemBytes []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("no certificates parsed from PEM")
	}
	return pool, nil
}

// decodeSignature accepts either base64-encoded or raw-DER signature bytes.
// cosign sign-blob writes base64; we accept both for resilience.
func decodeSignature(sig []byte) ([]byte, error) {
	// Trim whitespace — cosign writes a trailing newline.
	trimmed := trimASCIIWhitespace(sig)
	if len(trimmed) == 0 {
		return nil, errors.New("signature is empty")
	}
	if decoded, err := base64.StdEncoding.DecodeString(string(trimmed)); err == nil {
		return decoded, nil
	}
	// Accept raw DER as a fallback. This keeps the client forgiving if a
	// future cosign output changes format; we'll still reject mismatched
	// signatures during the crypto check.
	return trimmed, nil
}

func trimASCIIWhitespace(b []byte) []byte {
	start := 0
	end := len(b)
	for start < end && isASCIISpace(b[start]) {
		start++
	}
	for end > start && isASCIISpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// verifyECDSAOrRSASignature checks an ASN.1-DER ECDSA or PKCS1 RSA signature
// over sha256(data). Fulcio issues ECDSA P-256 keys almost exclusively, but
// we keep RSA-PSS/PKCS1 as a branch so the code doesn't brittle-fail on a
// future cosign change.
func verifyECDSAOrRSASignature(pub any, data, sig []byte) error {
	digest := sha256.Sum256(data)
	switch key := pub.(type) {
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(key, digest[:], sig) {
			return errors.New("ecdsa signature invalid")
		}
		return nil
	case *rsa.PublicKey:
		// Try PSS first (modern cosign default for RSA), then PKCS1v15.
		if err := rsa.VerifyPSS(key, crypto.SHA256, digest[:], sig, nil); err == nil {
			return nil
		}
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
			return fmt.Errorf("rsa signature invalid: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported public key type %T", pub)
	}
}

// leafSAN returns the canonical SAN for Fulcio-issued identities. GitHub
// Actions identities land in the URI SAN slot; we ignore DNS / email /
// IP SANs because cosign keyless doesn't use them.
func leafSAN(cert *x509.Certificate) (string, error) {
	if len(cert.URIs) > 0 {
		return cert.URIs[0].String(), nil
	}
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0], nil
	}
	return "", errors.New("no URI or email SAN found")
}

// leafIssuerClaim returns the OIDC issuer embedded in the Fulcio
// extensions. Fulcio writes the issuer twice — once as raw bytes under
// OID 1.3.6.1.4.1.57264.1.1 (historical) and once as a UTF8String under
// OID 1.3.6.1.4.1.57264.1.8 (current). We prefer the newer encoding but
// fall back to the historical one for older signatures.
//
// The v2 extension is an ASN.1 UTF8String, so we use encoding/asn1 to
// decode it. The older hand-rolled parser only handled short-form
// lengths (<128 bytes); encoding/asn1 handles long-form BER lengths as
// well. It's not currently exploitable — Fulcio's issuer URL is 43
// bytes — but brittle parsing around crypto boundaries is worth
// avoiding on principle.
func leafIssuerClaim(cert *x509.Certificate) (string, error) {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidOIDCIssuerV2) {
			var s string
			if _, err := asn1.Unmarshal(ext.Value, &s); err == nil {
				return s, nil
			}
			// Some encoders write the raw payload without the ASN.1
			// wrapper. Fall through so we don't break on them.
			return string(ext.Value), nil
		}
	}
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidOIDCIssuerV1) {
			return string(ext.Value), nil
		}
	}
	return "", errors.New("no OIDC issuer extension found")
}
