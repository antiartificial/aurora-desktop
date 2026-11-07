//! Signing the compositor's reply, and the key it is signed with.
//!
//! The key is a 32-byte file shared with the controller: created (atomically,
//! 0600) by whichever of the two starts first, and refused on load unless it is
//! a regular file owned by this user with mode 0600. This is the scheme of the
//! powerbox ticket (controller/ticket/key.go); the file is a different one
//! (`approval.key`), so a powerbox ticket and an approval reply cannot stand in
//! for each other, and the MAC input carries its own label as well.
//!
//! The signature is HMAC-SHA256 over the whole decision, each field length
//! prefixed. Self-contained on purpose (no crate imports): the integration
//! tests include this file to check the compositor's output independently.
//!
//! What it buys: a reply the controller accepts was made by something that
//! held this file. What it does not: any process that can READ the file can
//! sign, so this is key possession, not kernel authentication of the compositor.

use std::{
    io::{Read, Write},
    os::unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt},
    path::{Path, PathBuf},
};

pub const LABEL: &[u8] = b"aurora-approval-reply-v1\0";

/// `$AURORA_APPROVAL_KEY`, else `$XDG_RUNTIME_DIR/aurora/approval.key` (the
/// controller's `ticket.ApprovalKeyPath` computes the same).
pub fn key_path() -> PathBuf {
    if let Some(p) = std::env::var_os("AURORA_APPROVAL_KEY").filter(|v| !v.is_empty()) {
        return p.into();
    }
    let rt = std::env::var_os("XDG_RUNTIME_DIR").filter(|v| !v.is_empty()).map(PathBuf::from)
        .unwrap_or_else(|| std::env::temp_dir().join(format!("aurora-{}", unsafe { libc::geteuid() })));
    rt.join("aurora").join("approval.key")
}

fn read_key(path: &Path) -> std::io::Result<Vec<u8>> {
    let bad = |m: String| std::io::Error::new(std::io::ErrorKind::InvalidData, m);
    let st = std::fs::symlink_metadata(path)?;
    if !st.file_type().is_file() {
        return Err(bad(format!("{} is not a regular file", path.display())));
    }
    if st.permissions().mode() & 0o7777 != 0o600 {
        return Err(bad(format!("{} has mode {:o}; want 0600", path.display(), st.permissions().mode() & 0o7777)));
    }
    if st.uid() != unsafe { libc::geteuid() } {
        return Err(bad(format!("{} is not owned by this user", path.display())));
    }
    let mut b = Vec::new();
    std::fs::File::open(path)?.read_to_end(&mut b)?;
    if b.len() != 32 {
        return Err(bad(format!("{} is {} bytes; want 32", path.display(), b.len())));
    }
    Ok(b)
}

/// Load the key, creating it if absent (with `create`). Refuses a loose or foreign file.
pub fn load_key(path: &Path, create: bool) -> std::io::Result<Vec<u8>> {
    match read_key(path) {
        Err(e) if create && e.kind() == std::io::ErrorKind::NotFound => {}
        r => return r,
    }
    let dir = path.parent().unwrap_or(Path::new("."));
    std::fs::create_dir_all(dir)?;
    let _ = std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700));
    let mut b = [0u8; 32];
    // SAFETY: b is valid for 32 bytes.
    if unsafe { libc::getrandom(b.as_mut_ptr().cast(), 32, 0) } != 32 {
        return Err(std::io::Error::last_os_error());
    }
    let tmp = dir.join(format!(".approval-key-{}-{}", std::process::id(), std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map_or(0, |d| d.as_nanos())));
    let mut f = std::fs::OpenOptions::new().write(true).create_new(true).mode(0o600).open(&tmp)?;
    let made = f.write_all(&b).and_then(|_| f.sync_all());
    let linked = made.and_then(|_| match std::fs::hard_link(&tmp, path) {
        Err(e) if e.kind() != std::io::ErrorKind::AlreadyExists => Err(e),
        _ => Ok(()),
    });
    let _ = std::fs::remove_file(&tmp);
    linked?;
    read_key(path)
}

/// What is signed: label, then each field as a 4-byte big-endian length and its bytes.
pub fn message(fields: &[&str]) -> Vec<u8> {
    let mut m = LABEL.to_vec();
    for f in fields {
        m.extend_from_slice(&(f.len() as u32).to_be_bytes());
        m.extend_from_slice(f.as_bytes());
    }
    m
}

/// The decision fields, in the order the controller verifies them.
pub fn sign_fields(key: &[u8], fields: &[&str]) -> String {
    hex(&hmac_sha256(key, &message(fields)))
}

pub fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

const K: [u32; 64] = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

pub fn sha256(data: &[u8]) -> [u8; 32] {
    let mut h: [u32; 8] = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
    let mut m = data.to_vec();
    m.push(0x80);
    while m.len() % 64 != 56 {
        m.push(0);
    }
    m.extend_from_slice(&((data.len() as u64) * 8).to_be_bytes());
    for chunk in m.chunks(64) {
        let mut w = [0u32; 64];
        for i in 0..16 {
            w[i] = u32::from_be_bytes(chunk[i * 4..i * 4 + 4].try_into().unwrap());
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16].wrapping_add(s0).wrapping_add(w[i - 7]).wrapping_add(s1);
        }
        let mut v = h;
        for i in 0..64 {
            let s1 = v[4].rotate_right(6) ^ v[4].rotate_right(11) ^ v[4].rotate_right(25);
            let ch = (v[4] & v[5]) ^ (!v[4] & v[6]);
            let t1 = v[7].wrapping_add(s1).wrapping_add(ch).wrapping_add(K[i]).wrapping_add(w[i]);
            let s0 = v[0].rotate_right(2) ^ v[0].rotate_right(13) ^ v[0].rotate_right(22);
            let maj = (v[0] & v[1]) ^ (v[0] & v[2]) ^ (v[1] & v[2]);
            let t2 = s0.wrapping_add(maj);
            v = [t1.wrapping_add(t2), v[0], v[1], v[2], v[3].wrapping_add(t1), v[4], v[5], v[6]];
        }
        for i in 0..8 {
            h[i] = h[i].wrapping_add(v[i]);
        }
    }
    let mut out = [0u8; 32];
    for (i, x) in h.iter().enumerate() {
        out[i * 4..i * 4 + 4].copy_from_slice(&x.to_be_bytes());
    }
    out
}

pub fn hmac_sha256(key: &[u8], msg: &[u8]) -> [u8; 32] {
    let mut k = if key.len() > 64 { sha256(key).to_vec() } else { key.to_vec() };
    k.resize(64, 0);
    let mut inner: Vec<u8> = k.iter().map(|b| b ^ 0x36).collect();
    inner.extend_from_slice(msg);
    let mut outer: Vec<u8> = k.iter().map(|b| b ^ 0x5c).collect();
    outer.extend_from_slice(&sha256(&inner));
    sha256(&outer)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sha256_and_hmac_match_the_published_vectors() {
        assert_eq!(hex(&sha256(b"abc")), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
        assert_eq!(hex(&sha256(b"")), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
        // RFC 4231 test cases 1 and 2
        assert_eq!(hex(&hmac_sha256(&[0x0b; 20], b"Hi There")), "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7");
        assert_eq!(hex(&hmac_sha256(b"Jefe", b"what do ya want for nothing?")), "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843");
        // a key longer than the block size is hashed first (RFC 4231 case 6)
        assert_eq!(hex(&hmac_sha256(&[0xaa; 131], b"Test Using Larger Than Block-Size Key - Hash Key First")), "60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54");
        // a multi-block message
        assert_eq!(hex(&sha256(b"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq")), "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1");
    }

    #[test]
    fn fields_are_length_prefixed_so_boundaries_matter() {
        assert_ne!(message(&["ab", "c"]), message(&["a", "bc"]));
    }

    #[test]
    fn key_is_created_once_and_checked_on_load() {
        let d = std::env::temp_dir().join(format!("aurora-sign-test-{}", std::process::id()));
        let p = d.join("approval.key");
        let a = load_key(&p, true).unwrap();
        assert_eq!(a, load_key(&p, true).unwrap());
        assert_eq!(std::fs::metadata(&p).unwrap().permissions().mode() & 0o7777, 0o600);
        assert!(load_key(&d.join("absent"), false).is_err());
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o640)).unwrap();
        assert!(load_key(&p, true).is_err(), "a group-readable key must be refused");
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o600)).unwrap();
        std::fs::write(&p, b"short").unwrap();
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o600)).unwrap();
        assert!(load_key(&p, true).is_err(), "a wrong-sized key must be refused");
        let _ = std::fs::remove_dir_all(&d);
    }
}

#[cfg(test)]
mod cross_language_digest {
    use super::*;

    /// The request digest crosses a language boundary: the compositor computes it in Rust and
    /// the controller recomputes it in Go (controller/internal/invoke/compositor.go). If the
    /// two ever disagree, every signed reply is refused and the compositor route silently falls
    /// back to the bus, which is a failure that looks like a configuration problem.
    ///
    /// This pins the exact bytes. The matching Go assertion is
    /// TestRequestDigestMatchesTheCompositorVector.
    #[test]
    fn the_request_digest_is_lowercase_hex_sha256_of_the_line() {
        let line = br#"{"v":1,"op":"approve","request_id":"r1"}"#;
        let got = hex(&sha256(line));
        assert_eq!(got, "aab51d14477500a94b09cb5d34f6c71fe9506e5be51139c424de2ca5ce8f7ec0");
    }
}
