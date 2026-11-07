//! Which domain, if any, a Wayland client belongs to, learned once at connect.
//!
//! A window's title, app id and decorations are whatever the client says. What the client cannot
//! say is which cgroup the kernel put it in: `SO_PEERCRED` gives the pid that connected, and
//! `/proc/<pid>/cgroup` says where that pid lives. A process the adapter started in a domain is in
//! `.../aurora-domains/<name>` (adapter/linux/internal/dom/cgroup.go), so the component after
//! `aurora-domains` is the domain's name, and the domain's name is the controller's: it created the
//! domain and chose it. This is the same read `clipboard::peer_may_read_clipboard` does, and for
//! the same reason it happens here, at accept, and not in a global filter (asking for credentials
//! in there re-enters a lock the backend holds and hangs every connecting client).
//!
//! What this is and is not:
//! - A client the kernel places in a domain cgroup is attributed to it, and no client can lie its
//!   way OUT of the attribution: leaving the cgroup needs write access to the host's cgroup tree,
//!   which a domain's uid does not have.
//! - A host process of the same user can move itself INTO a domain cgroup (the subtree is the
//!   user's), and would then be labelled as that domain. That only makes it look more confined than
//!   it is; it is not a way to look like something it is not. The label is not a proof of the
//!   domain's sandbox, only of where the kernel says the process is.
//! - No domain means: a host process, XWayland, a test client. Normal, and shown as nothing.

use std::os::unix::net::UnixStream;

/// A client the kernel places inside a domain's cgroup.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PeerDomain {
    /// The domain's name: one `[a-z0-9_-]{1,32}` path component.
    pub name: String,
    pub uid: u32,
    pub pid: i32,
}

/// The domain named by a `/proc/<pid>/cgroup` file, or `None` when the process is not in one.
/// The name must be a plain component of the shape the adapter creates, so what chrome later
/// draws can never be something a path trick or a stray cgroup put there.
pub fn domain_of_cgroup(text: &str) -> Option<String> {
    let path = text.lines().find_map(|l| l.strip_prefix("0::"))?;
    let mut parts = path.split('/');
    parts.find(|c| *c == "aurora-domains")?;
    let name = parts.next()?;
    let valid = !name.is_empty() && name.len() <= 32 && name.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_' || b == b'-');
    valid.then(|| name.to_string())
}

/// The domain of the process on the other end of `stream`, if it is in one.
pub fn peer_domain(stream: &UnixStream) -> Option<PeerDomain> {
    let (uid, pid) = crate::trusted::socket::peer_cred(stream).ok()?;
    let cg = std::fs::read_to_string(format!("/proc/{pid}/cgroup")).ok()?;
    domain_of_cgroup(&cg).map(|name| PeerDomain { name, uid, pid })
}

#[cfg(test)]
mod tests {
    use super::*;

    const BASE: &str = "0::/user.slice/user-1000.slice/user@1000.service/aurora-domains";

    #[test]
    fn a_process_in_a_domain_cgroup_is_attributed_to_that_domain() {
        assert_eq!(domain_of_cgroup(&format!("{BASE}/win-text-1a2b3c4d-aabbcc\n")).as_deref(), Some("win-text-1a2b3c4d-aabbcc"));
    }

    #[test]
    fn a_process_nested_deeper_still_names_the_domain_not_its_child() {
        assert_eq!(domain_of_cgroup(&format!("{BASE}/alpha/inner\n")).as_deref(), Some("alpha"));
    }

    #[test]
    fn a_host_process_has_no_domain() {
        assert_eq!(domain_of_cgroup("0::/user.slice/user-1000.slice/session-2.scope\n"), None);
        assert_eq!(domain_of_cgroup("0::/\n"), None);
    }

    #[test]
    fn the_domains_parent_itself_is_no_domain() {
        assert_eq!(domain_of_cgroup(&format!("{BASE}\n")), None);
        assert_eq!(domain_of_cgroup(&format!("{BASE}/\n")), None);
    }

    #[test]
    fn a_name_the_adapter_would_not_create_is_refused_so_chrome_never_draws_it() {
        for bad in ["Has Space", "UPPER", "a\u{1b}b", "x".repeat(33).as_str(), "a.b"] {
            assert_eq!(domain_of_cgroup(&format!("{BASE}/{bad}\n")), None, "{bad:?}");
        }
    }

    #[test]
    fn a_cgroup_v1_only_file_gives_nothing() {
        assert_eq!(domain_of_cgroup("12:cpu:/aurora-domains/alpha\n"), None);
    }

    #[test]
    fn the_domain_component_must_be_a_whole_component() {
        assert_eq!(domain_of_cgroup("0::/user.slice/not-aurora-domains/alpha\n"), None);
    }
}
