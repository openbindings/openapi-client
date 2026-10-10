//! Canonical admission payloads retained until dynamic Node construction.
//!
//! The common BFS builder is the sole validator. These vectors preserve that
//! visitation order, so payloads need no duplicate node-ID or parent fields.
use super::{IndexSink, Kind, Member, Node, NodeCounts, Segment};
use std::{collections::HashMap, ops::Range, sync::Arc};

type Object = (Vec<Member>, HashMap<Arc<str>, usize>);

pub(super) struct Admission {
    ranges: Vec<Range<usize>>,
    objects: Vec<Object>,
    arrays: Vec<Range<usize>>,
    strings: Vec<String>,
    bytes: usize,
}

pub(super) struct Builder(Admission);
impl Builder {
    pub(super) fn new(counts: &NodeCounts) -> Self {
        let objects = Vec::with_capacity(counts.objects);
        let arrays = Vec::with_capacity(counts.arrays);
        let strings = Vec::with_capacity(counts.strings);
        let bytes = objects.capacity() * std::mem::size_of::<Object>()
            + arrays.capacity() * std::mem::size_of::<Range<usize>>()
            + strings.capacity() * std::mem::size_of::<String>();
        Self(Admission {
            ranges: Vec::new(),
            objects,
            arrays,
            strings,
            bytes,
        })
    }
}
impl IndexSink for Builder {
    type Output = Admission;
    fn child(&mut self, _: Range<usize>, _: Option<usize>, _: Segment<'_>) {}
    fn object(
        &mut self,
        _: usize,
        members: Vec<Member>,
        lookup: HashMap<Arc<str>, usize>,
        name_bytes: usize,
    ) {
        self.0.bytes += members.capacity() * std::mem::size_of::<Member>()
            + lookup.capacity() * (std::mem::size_of::<(Arc<str>, usize)>() + 1)
            + name_bytes;
        self.0.objects.push((members, lookup));
    }
    fn array(&mut self, _: usize, children: Range<usize>) {
        self.0.arrays.push(children);
    }
    fn string(&mut self, _: usize, value: String) {
        self.0.bytes += value.capacity();
        self.0.strings.push(value);
    }
    fn scalar(&mut self, _: usize, _: Kind) {}
    fn finish(mut self, ranges: Vec<Range<usize>>) -> Admission {
        self.0.bytes += ranges.capacity() * std::mem::size_of::<Range<usize>>();
        self.0.ranges = ranges;
        self.0
    }
}
impl Admission {
    pub(super) fn retained_bytes(&self) -> usize {
        self.bytes
    }

    pub(super) fn into_nodes(self, text: &str) -> Vec<Node> {
        let Self {
            ranges,
            objects,
            arrays,
            strings,
            ..
        } = self;
        let mut objects = objects.into_iter();
        let mut arrays = arrays.into_iter();
        let mut strings = strings.into_iter();
        // Nodes are appended in the original BFS order. Each per-kind payload
        // iterator was filled in that same order, so matching a node requires
        // neither a search nor another parse of its admitted source.
        let mut node = |id: usize, parent, segment| {
            let span = ranges[id].clone();
            let kind = match text.as_bytes()[span.start] {
                b'{' => {
                    let (members, lookup) = objects.next().expect("admitted object payload");
                    Kind::Object(members, lookup)
                }
                b'[' => Kind::Array(arrays.next().expect("admitted array payload").collect()),
                b'"' => Kind::String(strings.next().expect("admitted string payload")),
                b't' => Kind::Bool(true),
                b'f' => Kind::Bool(false),
                b'n' => Kind::Null,
                _ => Kind::Number,
            };
            Node {
                span,
                parent,
                segment,
                kind,
            }
        };
        let mut nodes = Vec::with_capacity(ranges.len());
        nodes.push(node(0, None, Arc::from("")));
        let mut cursor = 0;
        while cursor < nodes.len() {
            // Move the payload out while growing the Node vector, then restore
            // it. Children get their real parent and segment immediately: no
            // placeholder Arc allocation is made for every value.
            let kind = std::mem::replace(&mut nodes[cursor].kind, Kind::Null);
            match &kind {
                Kind::Object(members, _) => {
                    for member in members {
                        debug_assert_eq!(member.value, nodes.len());
                        nodes.push(node(member.value, Some(cursor), member.name.clone()));
                    }
                }
                Kind::Array(ids) => {
                    for (index, &id) in ids.iter().enumerate() {
                        debug_assert_eq!(id, nodes.len());
                        nodes.push(node(id, Some(cursor), Arc::from(index.to_string())));
                    }
                }
                _ => {}
            }
            nodes[cursor].kind = kind;
            cursor += 1;
        }
        debug_assert_eq!(nodes.len(), ranges.len());
        debug_assert!(objects.next().is_none());
        debug_assert!(arrays.next().is_none());
        debug_assert!(strings.next().is_none());
        // All consumed metadata buffers, including ranges, drop before the
        // caller publishes the Node vector through OnceLock.
        nodes
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::value::{ExactJson, Index, Limits};

    #[test]
    fn hydration_moves_decoded_strings_and_object_allocations() {
        let owner = ExactJson::parse_response(
            br#"{"left":["decoded\ntext",{"nested":"more"}],"right":{}}"#,
            Limits::default(),
        )
        .unwrap();
        let Index::Deferred(deferred) = &owner.0.store.index else {
            panic!("deferred response")
        };
        let (strings, members, lookups) = {
            let guard = deferred.admission.lock().unwrap();
            let admission = guard.as_ref().unwrap();
            let strings = admission
                .strings
                .iter()
                .map(|s| s.as_ptr() as usize)
                .collect::<Vec<_>>();
            let members = admission
                .objects
                .iter()
                .map(|(m, _)| m.as_ptr() as usize)
                .collect::<Vec<_>>();
            let lookups = admission
                .objects
                .iter()
                .map(|(_, h)| {
                    h.iter()
                        .map(|(key, value)| (key.to_string(), value as *const usize as usize))
                        .collect::<HashMap<_, _>>()
                })
                .collect::<Vec<_>>();
            (strings, members, lookups)
        };
        let nodes = owner.0.store.nodes();
        assert_eq!(
            nodes
                .iter()
                .filter_map(|n| match &n.kind {
                    Kind::String(s) => Some(s.as_ptr() as usize),
                    _ => None,
                })
                .collect::<Vec<_>>(),
            strings
        );
        let objects = nodes
            .iter()
            .filter_map(|n| match &n.kind {
                Kind::Object(m, h) => Some((m, h)),
                _ => None,
            })
            .collect::<Vec<_>>();
        assert_eq!(
            objects
                .iter()
                .map(|(m, _)| m.as_ptr() as usize)
                .collect::<Vec<_>>(),
            members
        );
        for ((_, lookup), expected) in objects.into_iter().zip(lookups) {
            for (key, address) in expected {
                assert_eq!(
                    lookup.get(key.as_str()).unwrap() as *const usize as usize,
                    address
                );
            }
        }
        assert!(deferred.admission.lock().unwrap().is_none());
    }
}
