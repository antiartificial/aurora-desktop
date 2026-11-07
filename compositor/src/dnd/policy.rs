//! What a drop may carry into a confined app.
//!
//! A file dragged out of a host file manager arrives as `text/uri-list`: lines of
//! `file:///home/arti/photo.png`. Inside a domain that path names nothing (the app cannot see the
//! host filesystem), and handing it over anyway is worse than useless: it leaks a host path and
//! teaches the app that the drop "worked" when nothing was granted. The honest design is a
//! *grant* at the moment of the drop (decision 0017); until that exists, the safe behaviour is to
//! not offer path-bearing types to a confined client at all, and to say so in the log.
//!
//! "Confined" here is the same fail-closed test the clipboard uses (`ClientState::may_read_clipboard`
//! false: inside an `aurora-domains` cgroup, or a client whose cgroup could not be read).

/// A MIME type (or legacy X11 target) that names host files.
pub fn names_files(mime: &str) -> bool {
    let m = mime.split(';').next().unwrap_or("").trim().to_ascii_lowercase();
    m == "text/uri-list"
        || m == "text/x-moz-url"
        || m == "x-special/gnome-copied-files"
        || m == "x-special/mate-copied-files"
        || m == "x-special/nautilus-clipboard"
        || m.contains("urilist")
        || m.starts_with("application/vnd.portal.file")
}

/// Plain-text flavours that file managers fill with the same paths ("/home/arti/photo.png").
fn is_plain_text(mime: &str) -> bool {
    let m = mime.split(';').next().unwrap_or("").trim().to_ascii_lowercase();
    m == "text/plain" || m == "utf8_string" || m == "string" || m == "text" || m == "compound_text"
}

/// The types of `offered` that a confined client may be shown.
///
/// File-naming types are withheld. So is plain text when a file type is present: a file drag's
/// `text/plain` is the path (or the bare name), and a text drag has no file type, so ordinary text
/// still reaches confined apps.
pub fn for_confined(offered: &[String]) -> Vec<String> {
    let has_files = offered.iter().any(|m| names_files(m));
    offered.iter().filter(|m| !names_files(m) && !(has_files && is_plain_text(m))).cloned().collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn v(a: &[&str]) -> Vec<String> {
        a.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn a_file_drag_loses_its_paths_and_its_text_twin() {
        let offered = v(&["text/uri-list", "text/plain;charset=utf-8", "UTF8_STRING", "image/png"]);
        assert_eq!(for_confined(&offered), v(&["image/png"]));
    }

    #[test]
    fn a_text_drag_is_untouched() {
        let offered = v(&["text/plain;charset=utf-8", "text/html"]);
        assert_eq!(for_confined(&offered), offered);
    }

    #[test]
    fn a_pure_file_drag_leaves_nothing_to_offer() {
        assert!(for_confined(&v(&["text/uri-list", "x-special/gnome-copied-files", "text/plain"])).is_empty());
    }

    #[test]
    fn the_other_spellings_of_a_path_are_recognised() {
        for m in ["text/uri-list", "TEXT/URI-LIST; charset=utf-8", "application/x-kde4-urilist", "text/x-moz-url", "x-special/gnome-copied-files", "application/vnd.portal.filetransfer"] {
            assert!(names_files(m), "{m}");
        }
        for m in ["text/plain", "text/html", "image/png", "application/octet-stream"] {
            assert!(!names_files(m), "{m}");
        }
    }
}
