//! Powerbox grant tickets, minted by the compositor.
//!
//! Decision 0011 left open "which key the compositor signs tickets with, and how the controller
//! learns to trust only it", and both adversarial reviews of the graphical powerbox said that
//! question decides whether any of the attestation is worth more than its interface. The answer
//! chosen is: the compositor mints, under a key only it holds, and the controller verifies by
//! asking the compositor rather than by holding the key.
//!
//! This module is the minting half. The format is not ours to choose: `controller/ticket`
//! already defines it and the controller already verifies it, so what is emitted here has to be
//! byte-compatible with Go's `ticket.Verify`.
//!
//!     base64url(json(claims)) "." base64url(HMAC-SHA256(key, "aurora-powerbox-ticket-v1\0" || json))
//!
//! The JSON field ORDER matters, because the MAC covers the serialised bytes and Go's
//! encoding/json emits struct fields in declaration order. `cross_language` below pins it.

use super::sign::hmac_sha256;

/// What a ticket says. Field order is Go's `ticket.Claims` declaration order and must stay that
/// way: the MAC is over these bytes, so reordering them silently invalidates every ticket.
pub struct Claims<'a> {
    pub path: &'a str,
    pub mode: &'a str,
    pub app: &'a str,
    pub verb: &'a str,
    pub exp: i64,
}

fn b64url(b: &[u8]) -> String {
    const A: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    let mut s = String::new();
    for c in b.chunks(3) {
        let (b0, b1, b2) = (c[0] as u32, *c.get(1).unwrap_or(&0) as u32, *c.get(2).unwrap_or(&0) as u32);
        let n = (b0 << 16) | (b1 << 8) | b2;
        s.push(A[(n >> 18 & 63) as usize] as char);
        s.push(A[(n >> 12 & 63) as usize] as char);
        if c.len() > 1 { s.push(A[(n >> 6 & 63) as usize] as char); }
        if c.len() > 2 { s.push(A[(n & 63) as usize] as char); }
    }
    s   // RawURLEncoding: no padding, which is what Go's base64.RawURLEncoding emits
}

/// JSON string escaping, enough for a path: Go escapes the same set with the same \uXXXX form
/// for control characters. A path may contain a quote, a backslash or a newline.
fn jstr(out: &mut String, s: &str) {
    out.push('"');
    for ch in s.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            // Go's encoding/json also escapes <, > and & by default (HTMLEscape).
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            c => out.push(c),
        }
    }
    out.push('"');
}

/// 16 random bytes, base64url, as Go's Mint does.
fn nonce() -> String {
    let mut b = [0u8; 16];
    // SAFETY: b is valid for 16 bytes.
    if unsafe { libc::getrandom(b.as_mut_ptr().cast(), 16, 0) } != 16 {
        // A ticket with a predictable nonce is worse than no ticket: the nonce is what makes it
        // single-use. Refuse rather than mint a weak one.
        return String::new();
    }
    b64url(&b)
}

/// The claims as Go's encoding/json would write them.
fn body(c: &Claims, nonce: &str) -> String {
    let mut s = String::from("{\"v\":1,\"path\":");
    jstr(&mut s, c.path);
    s.push_str(",\"mode\":");
    jstr(&mut s, c.mode);
    s.push_str(",\"app\":");
    jstr(&mut s, c.app);
    s.push_str(",\"verb\":");
    jstr(&mut s, c.verb);
    s.push_str(",\"nonce\":");
    jstr(&mut s, nonce);
    s.push_str(&format!(",\"exp\":{}}}", c.exp));
    s
}

/// Mint a ticket. `key` is the compositor's own, never written to disk.
/// Returns None only when the claims are unusable or the system refuses randomness.
pub fn mint(key: &[u8], c: &Claims) -> Option<String> {
    let n = nonce();
    if n.is_empty() {
        return None;
    }
    mint_with_nonce(key, c, &n)
}

/// The whole of minting except where the nonce comes from. `mint` is this with 16 bytes of
/// getrandom; the cross-language test is this with the nonce Go's vector used, which is the
/// only way to compare bytes with an implementation that mints its own.
fn mint_with_nonce(key: &[u8], c: &Claims, nonce: &str) -> Option<String> {
    if key.len() < 32 || c.path.is_empty() || c.app.is_empty() || c.verb.is_empty() {
        return None;
    }
    if c.mode != "ro" && c.mode != "rw" {
        return None;
    }
    if nonce.is_empty() {
        return None;
    }
    let b = body(c, nonce);
    let mut msg = Vec::from(&b"aurora-powerbox-ticket-v1\0"[..]);
    msg.extend_from_slice(b.as_bytes());
    Some(format!("{}.{}", b64url(b.as_bytes()), b64url(&hmac_sha256(key, &msg))))
}

/// A key for this compositor process only: 32 bytes from getrandom, never written anywhere.
/// That is the whole point of it. A file, however well permissioned, is readable by every
/// process of the same user, which is exactly the residual decision 0007 keeps naming.
pub fn new_key() -> Option<Vec<u8>> {
    let mut b = [0u8; 32];
    // SAFETY: b is valid for 32 bytes.
    if unsafe { libc::getrandom(b.as_mut_ptr().cast(), 32, 0) } != 32 {
        return None;
    }
    Some(b.to_vec())
}

#[cfg(test)]
mod cross_language {
    use super::*;

    /// The one fact this module exists to get right: a ticket minted here is accepted by Go's
    /// `ticket.Verify` in controller/ticket. Nothing above this is worth anything if it is not,
    /// and a mismatch does not present as a protocol break -- it presents as every grant being
    /// refused with ticket_bad_signature, which reads like a misconfigured key.
    ///
    /// So the bytes are pinned rather than described. The vector was produced BY the Go
    /// implementation (json.Marshal of ticket.Claims, then its own mac), so this asserts
    /// agreement with the authority and not with my reading of it. The path carries a quote, a
    /// backslash and the three characters Go's encoding/json HTML-escapes by default, because
    /// each of those is a place a hand-written emitter diverges silently.
    ///
    /// The matching Go assertions are TestCompositorVectorVerifies and
    /// TestClaimsMarshalMatchesTheCompositorVector.
    const KEY: &[u8] = b"aurora-ticket-test-key-012345678";
    const NONCE: &str = "AAECAwQFBgcICQoLDA0ODw";
    const TOKEN: &str = "eyJ2IjoxLCJwYXRoIjoiL3RtcC9hXCJiXFxjXHUwMDI2ZFx1MDAzY2VcdTAwM2VmLnBuZyIsIm1vZGUiOiJybyIsImFwcCI6Im9yZy5hdXJvcmEubHVtZW4iLCJ2ZXJiIjoicmVzaXplQHYxIiwibm9uY2UiOiJBQUVDQXdRRkJnY0lDUW9MREEwT0R3IiwiZXhwIjoxNzYyNTAwMDAwfQ.Umq0y6qWMSG12rlsO3XzHZQSDInNAfO2pqRR9NYVE20";

    fn vector_claims() -> Claims<'static> {
        Claims {
            path: "/tmp/a\"b\\c&d<e>f.png",
            mode: "ro",
            app: "org.aurora.lumen",
            verb: "resize@v1",
            exp: 1_762_500_000,
        }
    }

    #[test]
    fn a_minted_ticket_is_byte_identical_to_gos() {
        assert_eq!(mint_with_nonce(KEY, &vector_claims(), NONCE).unwrap(), TOKEN);
    }

    /// Field ORDER is the failure nobody would guess at: the MAC covers the serialised bytes,
    /// and Go emits struct fields in declaration order. Reordering `body` keeps every claim
    /// and every value and still invalidates every ticket. This is the same assertion as above
    /// read the other way, kept separate so the test name says what broke.
    #[test]
    fn the_claim_order_is_gos_declaration_order() {
        let b = body(&vector_claims(), NONCE);
        let order: Vec<&str> = b
            .split("\":")
            .filter_map(|seg| seg.rsplit('"').next())
            .collect();
        assert_eq!(&order[..7], &["v", "path", "mode", "app", "verb", "nonce", "exp"]);
    }

    #[test]
    fn minting_refuses_what_go_refuses() {
        let c = vector_claims();
        let short = b"too-short";
        assert!(mint_with_nonce(short, &c, NONCE).is_none(), "key under 32 bytes");
        for bad in [
            Claims { path: "", ..vector_claims() },
            Claims { app: "", ..vector_claims() },
            Claims { verb: "", ..vector_claims() },
            Claims { mode: "wr", ..vector_claims() },
            Claims { mode: "", ..vector_claims() },
        ] {
            assert!(mint_with_nonce(KEY, &bad, NONCE).is_none(), "claims {:?} should refuse", bad.mode);
        }
        assert!(mint_with_nonce(KEY, &c, "").is_none(), "empty nonce");
    }

    /// A fresh mint has to differ every time or the ticket is not single-use.
    #[test]
    fn each_mint_carries_a_fresh_nonce() {
        let c = vector_claims();
        let a = mint(KEY, &c).unwrap();
        let b = mint(KEY, &c).unwrap();
        assert_ne!(a, b);
    }

    #[test]
    fn a_key_is_32_bytes_and_never_repeats() {
        let a = new_key().unwrap();
        assert_eq!(a.len(), 32);
        assert_ne!(a, new_key().unwrap());
    }
}
