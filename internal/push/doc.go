// Package push delivers "an agent needs you" notifications to paired phones
// (the Leo iOS app) through the Leo push relay.
//
// The wire contract is shared with the relay and the app:
//
//   - plaintext is JSON {"v":1,"daemon_id","agent","kind","title","ts"} plus
//     an optional "body" (omitted when empty; older apps ignore it) holding
//     what needs attention: the question, the command or path awaiting
//     permission, or the finished turn's summary. The body is sanitised,
//     clipped to 400 runes, and trimmed before the title when the payload
//     would exceed the relay's cap;
//   - ciphertext is base64(nonce(12) ‖ AES-256-GCM(key, plaintext,
//     aad="leo-push-v1") ‖ tag(16)), under the key the device registered;
//   - collapse_id is the first 32 hex chars of SHA-256(daemon_id + "/" +
//     agent), so no agent name reaches Apple or the relay;
//   - the daemon POSTs {grant, ciphertext, collapse_id} to <relay_url>/push.
//
// Notifier watches attention transitions without ever blocking the
// publisher; RelaySender encrypts and delivers; Registry is where device
// registrations come from. Keys, grants and plaintext are never logged.
package push
