// Package fulcio bundles the Sigstore public-good Fulcio certificate
// authority roots that Leo trusts to issue short-lived code-signing
// certificates via GitHub OIDC.
//
// Both files are served by the upstream sigstore/root-signing TUF
// repository and embedded here so `leo update` can verify release
// signatures offline, without fetching the TUF trust bundle at runtime.
// They are the long-lived CA keys — not ephemeral signing identities —
// and expire in 2031. When they are rotated upstream, bump the files
// here in lockstep.
package fulcio

import _ "embed"

// RootPEM is the Sigstore public-good Fulcio root CA certificate.
//
// Source: https://github.com/sigstore/root-signing/blob/main/targets/fulcio_v1.crt.pem
//
//go:embed root.pem
var RootPEM []byte

// IntermediatePEM is the Sigstore public-good Fulcio intermediate CA
// certificate. Short-lived leaf certificates issued to OIDC identities
// are signed by this intermediate, which is signed by the root above.
//
// Source: https://github.com/sigstore/root-signing/blob/main/targets/fulcio_intermediate_v1.crt.pem
//
//go:embed intermediate.pem
var IntermediatePEM []byte
