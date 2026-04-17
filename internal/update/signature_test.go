package update

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// issuedCert bundles everything callers need to build a signed release
// fixture: the leaf cert PEM, the matching private key, and the root
// certificate pool to trust it with.
type issuedCert struct {
	leafPEM    []byte
	privateKey *ecdsa.PrivateKey
	rootPool   *x509.CertPool
	interPool  *x509.CertPool
	issuedAt   time.Time
	expiresAt  time.Time
	cert       *x509.Certificate
}

// issueFixture mints a fresh CA + intermediate + leaf chain in memory. The
// leaf carries cosign-compatible OIDC-issuer and SAN-URI extensions so the
// verifier will accept it. identityURI is stuffed into the URI SAN;
// issuer is written to the OIDC-issuer-v2 extension.
func issueFixture(t *testing.T, identityURI, issuer string) *issuedCert {
	t.Helper()

	// Root
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("rootkey: %v", err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-fulcio-root", Organization: []string{"leo-test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	rootCert, _ := x509.ParseCertificate(rootDER)

	// Intermediate
	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("interkey: %v", err)
	}
	interTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "test-fulcio-intermediate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(12 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, rootCert, &interKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create inter: %v", err)
	}
	interCert, _ := x509.ParseCertificate(interDER)

	// Leaf
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leafkey: %v", err)
	}

	// UTF8String-encode the issuer value the way Fulcio does: tag 0x0c
	// followed by single-byte length and payload. We keep issuer short
	// enough that the simple length encoding works.
	issuerExtVal := append([]byte{0x0c, byte(len(issuer))}, []byte(issuer)...)

	sanURI, err := url.Parse(identityURI)
	if err != nil {
		t.Fatalf("parse identity: %v", err)
	}

	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-cosign-ephemeral"},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(10 * time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs:         []*url.URL{sanURI},
		ExtraExtensions: []pkix.Extension{
			{
				Id:    asn1.ObjectIdentifier(oidOIDCIssuerV2),
				Value: issuerExtVal,
			},
		},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, interCert, &leafKey.PublicKey, interKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	leafCert, _ := x509.ParseCertificate(leafDER)

	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})

	rootPool := x509.NewCertPool()
	rootPool.AddCert(rootCert)
	interPool := x509.NewCertPool()
	interPool.AddCert(interCert)

	return &issuedCert{
		leafPEM:    leafPEM,
		privateKey: leafKey,
		rootPool:   rootPool,
		interPool:  interPool,
		cert:       leafCert,
		issuedAt:   leafTmpl.NotBefore,
		expiresAt:  leafTmpl.NotAfter,
	}
}

// signBlob produces a cosign-style base64 ECDSA-ASN.1 signature over data
// using the fixture key.
func signBlob(t *testing.T, fixture *issuedCert, data []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, fixture.privateKey, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(sig)
	return []byte(encoded + "\n")
}

// verifierFor builds a SignatureVerifier rooted at the fixture's test CA
// and keyed to a specific expected SAN regex and issuer.
func verifierFor(t *testing.T, fixture *issuedCert, sanRegex *regexp.Regexp, issuer string) *SignatureVerifier {
	t.Helper()
	return &SignatureVerifier{
		Roots:          fixture.rootPool,
		Intermediates:  fixture.interPool,
		SANRegex:       sanRegex,
		ExpectedIssuer: issuer,
		Now:            func() time.Time { return fixture.issuedAt.Add(time.Minute) },
	}
}

func TestSignatureVerifier_HappyPath(t *testing.T) {
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)

	data := []byte("test-checksums-body")
	sig := signBlob(t, fixture, data)

	v := verifierFor(t, fixture, regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/v[0-9A-Za-z.\-_]+$`), issuer)
	if err := v.Verify(data, sig, fixture.leafPEM); err != nil {
		t.Fatalf("Verify() should accept well-formed sig+cert: %v", err)
	}
}

func TestSignatureVerifier_TamperedData(t *testing.T) {
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)

	original := []byte("original-checksums")
	sig := signBlob(t, fixture, original)

	v := verifierFor(t, fixture, regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/.+$`), issuer)
	tampered := []byte("tampered-checksums")
	err := v.Verify(tampered, sig, fixture.leafPEM)
	if err == nil {
		t.Fatal("Verify() accepted tampered payload; expected signature mismatch")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("error = %q, want mention of signature failure", err.Error())
	}
}

func TestSignatureVerifier_MismatchedSAN(t *testing.T) {
	// Issue a cert for an attacker workflow identity, then try to verify
	// against our legitimate regex. Should fail the identity check before
	// the crypto check.
	attackerIdentity := "https://github.com/attacker/evil/.github/workflows/release.yml@refs/tags/v0.0.1"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, attackerIdentity, issuer)

	data := []byte("checksums")
	sig := signBlob(t, fixture, data)

	v := verifierFor(t, fixture, regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/.+$`), issuer)
	err := v.Verify(data, sig, fixture.leafPEM)
	if err == nil {
		t.Fatal("Verify() accepted attacker SAN; expected identity mismatch")
	}
	if !strings.Contains(err.Error(), "SAN") {
		t.Errorf("error = %q, want mention of SAN mismatch", err.Error())
	}
}

func TestSignatureVerifier_MismatchedIssuer(t *testing.T) {
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	fixture := issueFixture(t, identity, "https://gitlab.example.com")

	data := []byte("checksums")
	sig := signBlob(t, fixture, data)

	v := verifierFor(t, fixture,
		regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/.+$`),
		"https://token.actions.githubusercontent.com",
	)
	err := v.Verify(data, sig, fixture.leafPEM)
	if err == nil {
		t.Fatal("Verify() accepted mismatched OIDC issuer; expected rejection")
	}
	if !strings.Contains(err.Error(), "issuer") {
		t.Errorf("error = %q, want mention of issuer mismatch", err.Error())
	}
}

func TestSignatureVerifier_UntrustedRoot(t *testing.T) {
	// Issue two separate CA trees. The verifier trusts tree A; the leaf is
	// signed by tree B. This is the "attacker stands up their own Fulcio"
	// scenario — they can produce a valid-looking cert/sig pair, but not
	// one chained to the trusted root we pinned at build time.
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	trusted := issueFixture(t, identity, issuer)
	attacker := issueFixture(t, identity, issuer)

	data := []byte("checksums")
	sig := signBlob(t, attacker, data)

	// Verifier trusts the "real" root, but the cert came from attacker's CA.
	v := &SignatureVerifier{
		Roots:          trusted.rootPool,
		Intermediates:  trusted.interPool,
		SANRegex:       regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/.+$`),
		ExpectedIssuer: issuer,
		Now:            func() time.Time { return attacker.issuedAt.Add(time.Minute) },
	}
	err := v.Verify(data, sig, attacker.leafPEM)
	if err == nil {
		t.Fatal("Verify() accepted attacker-rooted cert; expected chain rejection")
	}
	if !strings.Contains(err.Error(), "chain") && !strings.Contains(err.Error(), "signed by") && !strings.Contains(err.Error(), "authority") {
		t.Errorf("error = %q, want mention of chain failure", err.Error())
	}
}

func TestDecodeSignatureAcceptsBase64AndRaw(t *testing.T) {
	raw := []byte{0x30, 0x45, 0x02, 0x20, 0xaa}
	b64 := []byte(base64.StdEncoding.EncodeToString(raw) + "\n")
	got, err := decodeSignature(b64)
	if err != nil {
		t.Fatalf("decodeSignature(base64) error: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("base64 decode mismatch: got %x want %x", got, raw)
	}

	// An input that isn't valid base64 should be passed through as-is so
	// the crypto layer can reject it.
	garbled := []byte("not-valid-base64-!!!$$$$$$")
	got, err = decodeSignature(garbled)
	if err != nil {
		t.Fatalf("decodeSignature(raw) unexpected error: %v", err)
	}
	if !bytes.Equal(got, garbled) {
		t.Errorf("raw passthrough mismatch")
	}
}

func TestDecodeSignatureEmpty(t *testing.T) {
	if _, err := decodeSignature([]byte("  \n\t ")); err == nil {
		t.Error("expected error for whitespace-only signature")
	}
}

func TestLeafSANURIPreferred(t *testing.T) {
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v1"
	fixture := issueFixture(t, identity, "https://token.actions.githubusercontent.com")
	san, err := leafSAN(fixture.cert)
	if err != nil {
		t.Fatalf("leafSAN: %v", err)
	}
	if san != identity {
		t.Errorf("leafSAN = %q, want %q", san, identity)
	}
}

func TestSignatureVerifierNowDefaultsToTimeNow(t *testing.T) {
	v := &SignatureVerifier{}
	before := time.Now()
	got := v.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("Now() returned %v, not between %v and %v", got, before, after)
	}
}

func TestParseLeafCertificateRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"not-pem":   []byte("plain text, not a PEM block"),
		"wrong-typ": []byte("-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"),
		"bad-der":   []byte("-----BEGIN CERTIFICATE-----\nZm9vYmFy\n-----END CERTIFICATE-----\n"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLeafCertificate(body); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestLoadCertPoolRejectsEmpty(t *testing.T) {
	if _, err := loadCertPool([]byte("no pem blocks here")); err == nil {
		t.Error("expected error when PEM parsing yields zero certs")
	}
}

func TestVerifySignatureUnknownKeyType(t *testing.T) {
	// Pass a key type the verifier explicitly doesn't support.
	type fakeKey struct{}
	err := verifyECDSAOrRSASignature(&fakeKey{}, []byte("data"), []byte("sig"))
	if err == nil {
		t.Fatal("expected unsupported key type error")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error = %q, want mention of unsupported", err.Error())
	}
}

// TestSignatureVerifier_MalformedSignature exercises the signature
// decode + crypto path with valid base64 that happens to decode to a
// non-DER byte sequence. The verifier must reject it, not panic or
// accept.
func TestSignatureVerifier_MalformedSignature(t *testing.T) {
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)

	data := []byte("checksums-body")

	// Valid base64, but decodes to a truncated ECDSA signature (the leading
	// SEQUENCE tag and a bogus length, then nothing). VerifyASN1 must
	// reject this. We base64-encode so the decodeSignature path accepts
	// the bytes and hands them to the crypto layer intact.
	garbledDER := []byte{0x30, 0x45, 0x02, 0x20, 0xAA}
	sig := []byte(base64.StdEncoding.EncodeToString(garbledDER) + "\n")

	v := verifierFor(t, fixture,
		regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/v0\.5\.0$`),
		issuer,
	)

	err := v.Verify(data, sig, fixture.leafPEM)
	if err == nil {
		t.Fatal("Verify() accepted malformed signature; expected rejection")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("error = %q, want mention of signature failure", err.Error())
	}
}

// TestSignatureVerifier_WrongCurveLeaf issues a leaf on a non-Fulcio
// curve (P-384 vs cosign's typical P-256). The verifier doesn't care
// which NIST curve is used as long as ecdsa.VerifyASN1 can consume it;
// what matters is that a *mismatched* key still fails. We swap the
// signing key between the cert and the crypto step so the public key
// embedded in the cert does not match the private key that produced
// the sig — the classic sign-with-different-key attack.
func TestSignatureVerifier_WrongCurveLeaf(t *testing.T) {
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)

	// Generate a P-384 key and sign with it, but serve the fixture's
	// P-256-bound leaf cert. The public key in the cert can't verify a
	// signature from a different private key — regardless of curve.
	wrongKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("gen wrongkey: %v", err)
	}
	data := []byte("checksums")
	digest := sha256.Sum256(data)
	rawSig, err := ecdsa.SignASN1(rand.Reader, wrongKey, digest[:])
	if err != nil {
		t.Fatalf("sign with wrong key: %v", err)
	}
	sig := []byte(base64.StdEncoding.EncodeToString(rawSig) + "\n")

	v := verifierFor(t, fixture,
		regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/v0\.5\.0$`),
		issuer,
	)

	err = v.Verify(data, sig, fixture.leafPEM)
	if err == nil {
		t.Fatal("Verify() accepted sig from mismatched key; expected rejection")
	}
	if !strings.Contains(err.Error(), "signature") && !strings.Contains(err.Error(), "ecdsa") {
		t.Errorf("error = %q, want mention of signature failure", err.Error())
	}
}

func TestSignatureVerifierForVersion_PinsTag(t *testing.T) {
	v, err := SignatureVerifierForVersion("v0.5.0")
	if err != nil {
		t.Fatalf("SignatureVerifierForVersion: %v", err)
	}
	// Requested tag must match.
	if !v.SANRegex.MatchString("https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0") {
		t.Error("expected pinned regex to accept the exact requested tag")
	}
	// Any other tag — older, newer, or prefix — must fail. This is the
	// version-downgrade defence in a nutshell.
	bad := []string{
		"https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.2.0",
		"https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.1",
		"https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0-rc1",
		"https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.00",
	}
	for _, uri := range bad {
		if v.SANRegex.MatchString(uri) {
			t.Errorf("pinned regex should have rejected %q", uri)
		}
	}
}

func TestSignatureVerifierForVersion_EmptyVersion(t *testing.T) {
	if _, err := SignatureVerifierForVersion(""); err == nil {
		t.Error("expected error for empty version")
	}
}

func TestSignatureVerifierForVersion_EscapesRegexMetachars(t *testing.T) {
	// A version string is CLI-supplied; regex metacharacters in it must be
	// escaped, not compiled as regex — otherwise "v.+" would match any tag.
	v, err := SignatureVerifierForVersion("v.+")
	if err != nil {
		t.Fatalf("SignatureVerifierForVersion: %v", err)
	}
	if v.SANRegex.MatchString("https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0") {
		t.Error("regex metachars in version must not leak into the SAN regex")
	}
}

func TestDefaultSignatureVerifierLoadsEmbeddedRoots(t *testing.T) {
	v, err := DefaultSignatureVerifier()
	if err != nil {
		t.Fatalf("DefaultSignatureVerifier: %v", err)
	}
	if v.Roots == nil || v.Intermediates == nil {
		t.Fatal("expected populated root/intermediate pools")
	}
	if v.ExpectedIssuer != "https://token.actions.githubusercontent.com" {
		t.Errorf("unexpected default issuer %q", v.ExpectedIssuer)
	}
	// A valid tag must match; an arbitrary workflow identity must not.
	ok := v.SANRegex.MatchString("https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v1.2.3")
	bad := v.SANRegex.MatchString("https://github.com/attacker/evil/.github/workflows/release.yml@refs/tags/v1.0.0")
	if !ok {
		t.Error("expected default SAN regex to accept a legitimate tag URI")
	}
	if bad {
		t.Error("expected default SAN regex to reject a foreign-owner URI")
	}
}

// TestDownloadAndReplace_SignedHappyPath wires the signature fixture all
// the way through the release-download flow. It confirms that when sig+cert
// are served correctly, the binary is replaced without any opt-out flag.
func TestDownloadAndReplace_SignedHappyPath(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho updated\n")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName))

	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)
	sig := signBlob(t, fixture, checksums)

	// Swap the package-level verifier factory to return a verifier tied to
	// our in-memory CA. Without this the verifier would try the real Fulcio
	// root, which didn't issue our fixture.
	origFactory := newSignatureVerifier
	defer func() { newSignatureVerifier = origFactory }()
	newSignatureVerifier = func(version string) (*SignatureVerifier, error) {
		return verifierFor(t, fixture,
			regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/`+regexp.QuoteMeta(version)+`$`),
			issuer,
		), nil
	}

	_, teardown := testServer(t, archiveName, archive, string(checksums), sig, fixture.leafPEM)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("old binary"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	// Strict mode — no AllowUnsigned opt-out.
	if _, err := DownloadAndReplace("v0.5.0"); err != nil {
		t.Fatalf("DownloadAndReplace() error: %v", err)
	}
	got, _ := os.ReadFile(fakeBinary)
	if !bytes.Equal(got, binaryContent) {
		t.Errorf("binary not replaced; content = %q", got)
	}
}

// TestDownloadAndReplace_TamperedChecksums asserts that a mutated
// checksums.txt (same sig+cert as the real release, but body altered to
// point to an attacker's archive hash) is rejected before the archive
// hash check runs.
func TestDownloadAndReplace_TamperedChecksums(t *testing.T) {
	binaryContent := []byte("malicious")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	// Attacker replaces the archive and rewrites checksums.txt to match —
	// but reuses the original sig (which was over the legitimate body).
	originalBody := []byte("original-checksums-body\n")
	maliciousChecksums := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName))

	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)
	sig := signBlob(t, fixture, originalBody) // sig binds original, not the mutated file

	origFactory := newSignatureVerifier
	defer func() { newSignatureVerifier = origFactory }()
	newSignatureVerifier = func(version string) (*SignatureVerifier, error) {
		return verifierFor(t, fixture,
			regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/`+regexp.QuoteMeta(version)+`$`),
			issuer,
		), nil
	}

	_, teardown := testServer(t, archiveName, archive, string(maliciousChecksums), sig, fixture.leafPEM)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("original-install"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected signature failure when checksums.txt differs from signed body")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("error = %q, want signature failure", err.Error())
	}

	// Belt-and-suspenders: binary must not have been touched.
	got, _ := os.ReadFile(fakeBinary)
	if string(got) != "original-install" {
		t.Errorf("binary was replaced despite signature failure: %q", got)
	}
}

// TestDownloadAndReplace_AttackerSANIsRejected ensures that even a
// cryptographically valid signature over the checksums file is rejected
// if the certificate's SAN doesn't belong to our release workflow.
func TestDownloadAndReplace_AttackerSANIsRejected(t *testing.T) {
	binaryContent := []byte("evil")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName))

	attackerIdentity := "https://github.com/attacker/evil/.github/workflows/release.yml@refs/tags/v1.0.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, attackerIdentity, issuer)
	sig := signBlob(t, fixture, checksums)

	origFactory := newSignatureVerifier
	defer func() { newSignatureVerifier = origFactory }()
	newSignatureVerifier = func(version string) (*SignatureVerifier, error) {
		// Verifier insists on the legitimate repo — attacker's cert won't match.
		return verifierFor(t, fixture,
			regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/`+regexp.QuoteMeta(version)+`$`),
			issuer,
		), nil
	}

	_, teardown := testServer(t, archiveName, archive, string(checksums), sig, fixture.leafPEM)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("orig"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected SAN mismatch to abort the update")
	}
	if !strings.Contains(err.Error(), "SAN") {
		t.Errorf("error = %q, want SAN mismatch", err.Error())
	}
}

// TestDownloadAndReplace_UnsignedReleaseFallback confirms that when sig or
// cert is absent, callers can opt into SHA-only verification. The warn
// callback fires exactly once.
func TestDownloadAndReplace_UnsignedReleaseFallback(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho updated\n")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName)

	_, teardown := testServer(t, archiveName, archive, checksums, nil, nil)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("old"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	// Strict mode must refuse.
	if _, err := DownloadAndReplace("v0.5.0"); err == nil {
		t.Fatal("strict DownloadAndReplace should fail when signature is absent")
	}

	// Fallback mode must succeed and surface a warning.
	var warned int
	opts := UpdateOptions{
		AllowUnsigned: true,
		Warn: func(format string, args ...any) {
			warned++
		},
	}
	if _, err := DownloadAndReplaceWithOptions("v0.5.0", opts); err != nil {
		t.Fatalf("fallback should succeed: %v", err)
	}
	if warned != 1 {
		t.Errorf("Warn fired %d times, want 1", warned)
	}
	got, _ := os.ReadFile(fakeBinary)
	if !bytes.Equal(got, binaryContent) {
		t.Errorf("binary not replaced in fallback mode")
	}
}

// TestDownloadAndReplace_VersionDowngradeRejected is the regression test
// for the version-downgrade attack:
//
//	attacker serves a *valid* sig+cert+checksums bundle from an old
//	(vulnerable) release behind the URL path of a newer release. Chain,
//	SAN shape, issuer, signature, and the archive hash all pass — but the
//	cert's SAN binds the old tag. Without pinning the verifier to the
//	requested version, the update silently downgrades.
//
// We mint a fixture for v0.2.0 and ask the update flow to install v0.5.0.
// The verifier must reject with a SAN mismatch.
func TestDownloadAndReplace_VersionDowngradeRejected(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho downgraded\n")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName))

	// Fixture is issued for the OLD tag. Everything else (issuer, chain,
	// signature over the served checksums body) is legitimate.
	oldIdentity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.2.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, oldIdentity, issuer)
	sig := signBlob(t, fixture, checksums)

	// Emulate the production factory's behaviour: build a verifier that
	// pins its SAN regex to the version the caller requested. Fixture
	// uses our self-signed root, not real Fulcio, so we stub the factory
	// but reproduce its per-version SAN construction.
	origFactory := newSignatureVerifier
	defer func() { newSignatureVerifier = origFactory }()
	newSignatureVerifier = func(version string) (*SignatureVerifier, error) {
		return verifierFor(t, fixture,
			regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/`+regexp.QuoteMeta(version)+`$`),
			issuer,
		), nil
	}

	_, teardown := testServer(t, archiveName, archive, string(checksums), sig, fixture.leafPEM)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("orig"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	// Caller asks for v0.5.0; attacker serves a v0.2.0-issued bundle.
	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected SAN mismatch when bundle was issued for an older tag")
	}
	if !strings.Contains(err.Error(), "SAN") {
		t.Errorf("error = %q, want mention of SAN mismatch", err.Error())
	}

	// Binary must not have been replaced.
	got, _ := os.ReadFile(fakeBinary)
	if string(got) != "orig" {
		t.Errorf("binary was replaced despite downgrade attempt: %q", got)
	}
}

// TestDownloadAndReplace_MatchingVersionAccepted complements the
// downgrade test: the same pinned verifier accepts a bundle issued for
// the tag actually being requested.
func TestDownloadAndReplace_MatchingVersionAccepted(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho ok\n")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName))

	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)
	sig := signBlob(t, fixture, checksums)

	origFactory := newSignatureVerifier
	defer func() { newSignatureVerifier = origFactory }()
	newSignatureVerifier = func(version string) (*SignatureVerifier, error) {
		return verifierFor(t, fixture,
			regexp.MustCompile(`^https://github\.com/blackpaw-studio/leo/\.github/workflows/release\.yml@refs/tags/`+regexp.QuoteMeta(version)+`$`),
			issuer,
		), nil
	}

	_, teardown := testServer(t, archiveName, archive, string(checksums), sig, fixture.leafPEM)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("orig"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	if _, err := DownloadAndReplace("v0.5.0"); err != nil {
		t.Fatalf("DownloadAndReplace() should accept matching-version bundle: %v", err)
	}
	got, _ := os.ReadFile(fakeBinary)
	if !bytes.Equal(got, binaryContent) {
		t.Errorf("binary not replaced; content = %q", got)
	}
}

// TestDownloadAndReplace_VerifierConstructionFailureAllowsUnsigned covers
// the embedded-PEM-gone-bad degradation path. If the verifier factory
// itself fails (simulating a future build where fulcio.RootPEM was
// corrupted), --allow-unsigned callers must still be able to fall back
// to SHA-only verification instead of hard-failing. Strict callers must
// still abort so the underlying bug can't hide.
func TestDownloadAndReplace_VerifierConstructionFailureAllowsUnsigned(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho fallback\n")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName))

	// Mint a fixture just so we have some realistic-looking sig/cert
	// bytes to serve — we won't get past verifier construction anyway.
	identity := "https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0"
	issuer := "https://token.actions.githubusercontent.com"
	fixture := issueFixture(t, identity, issuer)
	sig := signBlob(t, fixture, checksums)

	// Verifier factory simulates a corrupted embedded PEM.
	origFactory := newSignatureVerifier
	defer func() { newSignatureVerifier = origFactory }()
	newSignatureVerifier = func(string) (*SignatureVerifier, error) {
		return nil, fmt.Errorf("simulated: embedded Fulcio PEM unreadable")
	}

	_, teardown := testServer(t, archiveName, archive, string(checksums), sig, fixture.leafPEM)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("orig"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	// Strict mode must still abort — a build-time bug shouldn't silently
	// erase the signature guarantee.
	if _, err := DownloadAndReplace("v0.5.0"); err == nil {
		t.Fatal("strict mode should refuse to update when verifier won't build")
	}

	// Reset the binary so the second leg doesn't see the already-updated
	// content from a passing first leg.
	os.WriteFile(fakeBinary, []byte("orig"), 0750)

	var warned int
	var warnedMsg string
	opts := UpdateOptions{
		AllowUnsigned: true,
		Warn: func(format string, args ...any) {
			warned++
			warnedMsg = fmt.Sprintf(format, args...)
		},
	}
	if _, err := DownloadAndReplaceWithOptions("v0.5.0", opts); err != nil {
		t.Fatalf("allow-unsigned should degrade to SHA-only, got: %v", err)
	}
	if warned != 1 {
		t.Errorf("Warn fired %d times, want 1", warned)
	}
	if !strings.Contains(warnedMsg, "verifier unavailable") {
		t.Errorf("warning = %q, want mention of unavailable verifier", warnedMsg)
	}

	got, _ := os.ReadFile(fakeBinary)
	if !bytes.Equal(got, binaryContent) {
		t.Errorf("binary not replaced in fallback mode; content = %q", got)
	}
}
