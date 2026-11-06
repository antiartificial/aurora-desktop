//! The reply is signed over the whole decision. The test recomputes the MAC
//! with its own copy of sign.rs and the key file the compositor created.

mod common;
use common::*;

#[path = "../src/trusted/sign.rs"]
#[allow(dead_code)]
mod sign;

fn fields(a: &serde_json::Value) -> Vec<String> {
    ["request_id", "decision", "key", "reason", "domain_verified", "domain_verify_detail", "domain_source", "domain_identity", "domain_mismatch"]
        .iter().map(|k| a[*k].as_str().unwrap_or_else(|| panic!("reply has no {k}: {a}")).to_string()).collect()
}

fn mac(key: &[u8], a: &serde_json::Value) -> String {
    let f = fields(a);
    sign::sign_fields(key, &f.iter().map(String::as_str).collect::<Vec<_>>())
}

#[test]
fn every_reply_carries_a_signature_over_the_whole_decision() {
    let mut c = Comp::start(&[]);
    c.client();
    let key_file = c.dir.path().join("aurora/approval.key");
    let key = sign::load_key(&key_file, false).expect("the compositor made a 0600 key under XDG_RUNTIME_DIR/aurora");

    // allow, by key
    let pending = ask(&c, &request_json("sig-allow", 60_000));
    wait_for("overlay", || c.dump.exists());
    c.inject("key a");
    let a = answer(pending);
    assert_eq!(a["decision"], "allow", "{a}");
    assert_eq!(a["sig"].as_str().unwrap(), mac(&key, &a), "{a}");
    println!("SIGNED allow: {a}");

    // deny, by timeout: also signed
    let a2 = answer(ask(&c, &request_json("sig-timeout", 1_000)));
    assert_eq!(a2["decision"], "deny");
    assert_eq!(a2["reason"], "timeout");
    assert_eq!(a2["sig"].as_str().unwrap(), mac(&key, &a2));

    // the signature covers each field: changing any one breaks it
    let sig = a["sig"].as_str().unwrap();
    for (k, v) in [("request_id", "sig-other"), ("decision", "deny"), ("key", "d"), ("reason", "timeout"), ("domain_source", "bus"), ("domain_identity", "domain:evil:1"), ("domain_mismatch", "true"), ("domain_verified", "true")] {
        let mut t = a.clone();
        t[k] = v.into();
        assert_ne!(mac(&key, &t), sig, "changing {k} did not change the signature");
    }
    // and a signature from another request's reply is not this one's
    assert_ne!(a["sig"], a2["sig"]);
}

#[test]
fn a_loose_key_file_is_refused_so_replies_are_unsigned() {
    use std::os::unix::fs::PermissionsExt;
    let c = Comp::start_with(|rt| {
        std::fs::create_dir_all(rt.join("aurora")).unwrap();
        let kf = rt.join("aurora/approval.key");
        std::fs::write(&kf, [7u8; 32]).unwrap();
        std::fs::set_permissions(&kf, std::fs::Permissions::from_mode(0o644)).unwrap();
    }, &[]);
    let a = answer(ask(&c, &request_json("loose", 1_000)));
    assert!(a.get("sig").is_none(), "signed with a loose key: {a}");
    assert!(c.log().contains("unusable"), "the refusal is logged: {}", c.log());
}
