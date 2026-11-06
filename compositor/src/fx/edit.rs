//! Writing settings back to the theme file. The compositor's one config
//! mechanism is that file plus hot reload; the settings panel and the desktop
//! menu change it through here and let the normal reload apply the result.
//! Comments and key order in the file are kept (`toml_edit`), so the reference
//! theme stays readable after being edited by a UI.

use toml_edit::{Array, DocumentMut, Item, Table, Value, value};

#[derive(Clone, Debug, PartialEq)]
pub enum Val {
    Str(String),
    Num(f64),
    Int(i64),
    Bool(bool),
    Vec3([f32; 3]),
}

/// Set `[table] key = val`.
#[derive(Clone, Debug, PartialEq)]
pub struct Edit {
    pub table: &'static str,
    pub key: &'static str,
    pub val: Val,
}

impl Edit {
    pub fn new(table: &'static str, key: &'static str, val: Val) -> Self {
        Edit { table, key, val }
    }
    pub fn s(table: &'static str, key: &'static str, v: &str) -> Self {
        Edit::new(table, key, Val::Str(v.into()))
    }
    pub fn n(table: &'static str, key: &'static str, v: f64) -> Self {
        Edit::new(table, key, Val::Num(v))
    }
}

/// Round to 3 decimals so f32 noise never reaches the file.
fn r3(v: f32) -> f64 {
    ((v as f64) * 1000.0).round() / 1000.0
}

fn to_value(v: &Val) -> Value {
    match v {
        Val::Str(s) => Value::from(s.as_str()),
        Val::Num(n) => Value::from(((*n) * 10000.0).round() / 10000.0),
        Val::Int(i) => Value::from(*i),
        Val::Bool(b) => Value::from(*b),
        Val::Vec3(c) => {
            let mut a = Array::new();
            for x in c {
                a.push(r3(*x));
            }
            Value::Array(a)
        }
    }
}

/// Apply `edits` to the TOML in `text`; everything else in the file is untouched.
pub fn apply(text: &str, edits: &[Edit]) -> Result<String, String> {
    let mut doc: DocumentMut = text.parse().map_err(|e: toml_edit::TomlError| e.to_string())?;
    for e in edits {
        if !doc.contains_table(e.table) {
            doc.insert(e.table, Item::Table(Table::new()));
        }
        let t = doc[e.table].as_table_mut().ok_or_else(|| format!("[{}] is not a table", e.table))?;
        let mut new = to_value(&e.val);
        // Keep the comment that trails the old value.
        if let Some(old) = t.get(e.key).and_then(Item::as_value) {
            *new.decor_mut() = old.decor().clone();
        }
        if t.contains_key(e.key) {
            t[e.key] = value(new);
        } else {
            t.insert(e.key, value(new));
        }
    }
    Ok(doc.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    const T: &str = "# header\n[dock]\nedge = \"bottom\"   # where\nitem_height = 36.0\n\n[background]\ntop = [0.27, -0.015, -0.085]  # sky\n";

    #[test]
    fn edits_keep_comments_and_untouched_keys() {
        let out = apply(T, &[Edit::s("dock", "edge", "left"), Edit::new("background", "top", Val::Vec3([0.3, 0.0, -0.1]))]).unwrap();
        assert!(out.contains("# header") && out.contains("edge = \"left\"   # where"), "{out}");
        assert!(out.contains("item_height = 36.0"), "{out}");
        assert!(out.contains("top = [0.3, 0.0, -0.1]  # sky"), "{out}");
    }

    #[test]
    fn missing_tables_and_keys_are_created() {
        let out = apply("[dock]\nedge = \"top\"\n", &[Edit::n("output", "scale", 1.5), Edit::new("dock", "enabled", Val::Bool(false))]).unwrap();
        let v: toml::Value = toml::from_str(&out).unwrap();
        assert_eq!(v["output"]["scale"].as_float(), Some(1.5));
        assert_eq!(v["dock"]["enabled"].as_bool(), Some(false));
        assert_eq!(v["dock"]["edge"].as_str(), Some("top"));
    }

    #[test]
    fn f32_noise_does_not_reach_the_file() {
        let out = apply("", &[Edit::new("background", "top", Val::Vec3([0.27, 0.025, -0.085]))]).unwrap();
        assert!(out.contains("[0.27, 0.025, -0.085]"), "{out}");
    }

    #[test]
    fn unparsable_text_is_refused() {
        assert!(apply("[dock\n", &[Edit::s("dock", "edge", "left")]).is_err());
    }
}
