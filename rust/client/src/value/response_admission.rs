//! Checked response admission without retaining a dynamic index.
//!
//! The caller has already admitted bytes/depth, validated the complete grammar
//! through RawValue, and counted all actual JSON nodes. This visitor validates
//! decoded strings and duplicate names. Its error is never a public diagnostic:
//! canonical indexing replays refusals in the established breadth-first order.
use serde::{
    Deserialize, Deserializer,
    de::{self, DeserializeSeed, MapAccess, SeqAccess, Visitor},
};
use std::{borrow::Cow, collections::HashSet, fmt};

pub(super) fn validate(root: &str, node_limit: usize) -> Result<(), serde_json::Error> {
    Admission(node_limit).deserialize(&mut serde_json::Deserializer::from_str(root))
}

#[derive(Clone, Copy)]
struct Admission(usize);
impl<'de> DeserializeSeed<'de> for Admission {
    type Value = ();
    fn deserialize<D: Deserializer<'de>>(self, deserializer: D) -> Result<(), D::Error> {
        deserializer.deserialize_any(self)
    }
}
impl<'de> Visitor<'de> for Admission {
    type Value = ();
    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("admitted JSON")
    }
    fn visit_unit<E: de::Error>(self) -> Result<(), E> {
        Ok(())
    }
    fn visit_bool<E: de::Error>(self, _: bool) -> Result<(), E> {
        Ok(())
    }
    fn visit_i64<E: de::Error>(self, _: i64) -> Result<(), E> {
        Ok(())
    }
    fn visit_u64<E: de::Error>(self, _: u64) -> Result<(), E> {
        Ok(())
    }
    fn visit_f64<E: de::Error>(self, _: f64) -> Result<(), E> {
        Ok(())
    }
    fn visit_str<E: de::Error>(self, _: &str) -> Result<(), E> {
        Ok(())
    }
    fn visit_string<E: de::Error>(self, _: String) -> Result<(), E> {
        Ok(())
    }
    fn visit_seq<A: SeqAccess<'de>>(self, mut array: A) -> Result<(), A::Error> {
        let mut count = 0;
        while array.next_element_seed(self)?.is_some() {
            if count >= self.0 {
                return Err(de::Error::custom("node limit"));
            }
            count += 1;
        }
        Ok(())
    }
    fn visit_map<A: MapAccess<'de>>(self, mut object: A) -> Result<(), A::Error> {
        // One-name maps need no heap set. This also avoids a set allocation for
        // serde_json's arbitrary_precision synthetic Number map. We do not
        // interpret its marker: actual objects with that key stay ordinary data,
        // and authoritative node counts came from the original JSON tokens.
        let mut first: Option<Cow<'de, str>> = None;
        let mut rest = HashSet::new();
        let mut count = 0;
        while let Some(Key(key)) = object.next_key()? {
            if count >= self.0 {
                return Err(de::Error::custom("node limit"));
            }
            if let Some(first) = &first {
                if first == &key || !rest.insert(key) {
                    return Err(de::Error::custom("duplicate member"));
                }
            } else {
                first = Some(key);
            }
            count += 1;
            object.next_value_seed(self)?;
        }
        Ok(())
    }
}

struct Key<'de>(Cow<'de, str>);
impl<'de> Deserialize<'de> for Key<'de> {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct KeyVisitor;
        impl<'de> Visitor<'de> for KeyVisitor {
            type Value = Key<'de>;
            fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str("JSON member name")
            }
            fn visit_borrowed_str<E: de::Error>(self, value: &'de str) -> Result<Key<'de>, E> {
                Ok(Key(Cow::Borrowed(value)))
            }
            fn visit_str<E: de::Error>(self, value: &str) -> Result<Key<'de>, E> {
                Ok(Key(Cow::Owned(value.to_owned())))
            }
            fn visit_string<E: de::Error>(self, value: String) -> Result<Key<'de>, E> {
                Ok(Key(Cow::Owned(value)))
            }
        }
        deserializer.deserialize_str(KeyVisitor)
    }
}
