//! Checked Serde emission followed by the existing exact parser/indexer.
use crate::{
    Cancellation, Code, Diagnostic, DiagnosticReason, ExactJson, LimitKind, Limits, ParseFailure,
    SerializationReason, SourceOrigin,
};
use serde::{
    Serialize, Serializer,
    ser::{self, SerializeMap, SerializeSeq},
};
use std::{fmt, io, sync::Arc};

// Pinned serde_json 1.0.151 uses this protocol when another dependency enables
// arbitrary_precision. Its shape and number text are checked, never trusted.
const NUMBER: &str = "$serde_json::private::Number";
const PRIVATE: &str = "$serde_json::private::";

#[derive(Clone, Debug)]
struct Error(Arc<Diagnostic>);
impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(f)
    }
}
impl std::error::Error for Error {}
impl ser::Error for Error {
    fn custom<T: fmt::Display>(_: T) -> Self {
        // Custom text can contain secrets and invoke arbitrary Display work.
        Self::reason(SerializationReason::Custom)
    }
}
impl Error {
    fn new(diagnostic: Diagnostic) -> Self {
        Self(Arc::new(diagnostic))
    }
    fn reason(reason: SerializationReason) -> Self {
        Self::new(
            Diagnostic::new(Code::Serialization).reasoned(DiagnosticReason::Serialization(reason)),
        )
    }
    fn into_diagnostic(self) -> Diagnostic {
        Arc::unwrap_or_clone(self.0)
    }
}

struct Encoder<'a> {
    bytes: Vec<u8>,
    limits: &'a Limits,
    cancellation: &'a Cancellation,
    nodes: usize,
    depth: usize,
    first: Option<Error>,
}
impl<'limits> Encoder<'limits> {
    fn latch(&mut self, error: Error) -> Error {
        self.first.get_or_insert(error).clone()
    }
    fn fail(&mut self, reason: SerializationReason) -> Error {
        self.latch(Error::reason(reason))
    }
    fn limit(&mut self, kind: LimitKind, maximum: usize, actual: usize) -> Error {
        self.latch(Error::new(Diagnostic::limited(kind, maximum, actual)))
    }
    fn guard(&mut self) -> Result<(), Error> {
        if let Some(error) = &self.first {
            return Err(error.clone());
        }
        self.cancellation
            .check()
            .map_err(|e| self.latch(Error::new(e)))
    }
    fn room(&mut self, bytes: usize) -> Result<(), Error> {
        self.guard()?;
        if bytes > self.limits.document_bytes.saturating_sub(self.bytes.len()) {
            return Err(self.limit(
                LimitKind::DocumentBytes,
                self.limits.document_bytes,
                self.bytes.len().saturating_add(bytes),
            ));
        }
        Ok(())
    }
    fn write(&mut self, bytes: &[u8]) -> Result<(), Error> {
        self.room(bytes.len())?;
        // Match the pinned JSON writer's small initial buffer, but only after
        // admission and never reserve more than this construction's byte limit.
        if self.bytes.capacity() == 0 && !bytes.is_empty() {
            self.bytes
                .reserve_exact(self.limits.document_bytes.min(128));
        }
        self.bytes.extend_from_slice(bytes);
        Ok(())
    }
    fn node_room(&mut self) -> Result<(), Error> {
        self.guard()?;
        if self.nodes >= self.limits.nodes {
            return Err(self.limit(
                LimitKind::Nodes,
                self.limits.nodes,
                self.nodes.saturating_add(1),
            ));
        }
        Ok(())
    }
    fn node(&mut self) -> Result<(), Error> {
        self.node_room()?;
        self.nodes += 1;
        Ok(())
    }
    fn child<T: Serialize + ?Sized>(&mut self, value: &T) -> Result<(), Error> {
        self.node_room()?;
        let result = value.serialize(&mut *self);
        if let Err(error) = result {
            return Err(self.latch(error));
        }
        self.guard()
    }
    // Only library-selected primitive implementations reach this delegate.
    // serde_json owns escaping and primitive numeric spelling.
    fn primitive<T: Serialize + ?Sized>(&mut self, value: &T) -> Result<(), Error> {
        self.guard()?;
        serde_json::to_writer(&mut *self, value)
            .map_err(|_| self.fail(SerializationReason::InvalidRepresentation))
    }
    fn string(&mut self, value: &str) -> Result<(), Error> {
        // Escaping cannot shorten the input. Reject before scanning it.
        self.room(value.len().saturating_add(2))?;
        self.primitive(value)
    }
    fn begin(&mut self, object: bool) -> Result<Compound<'_, 'limits>, Error> {
        self.node()?;
        let maximum = self.limits.depth.min(128);
        if self.depth >= maximum {
            return Err(self.limit(LimitKind::Depth, maximum, self.depth.saturating_add(1)));
        }
        self.depth += 1;
        self.write(if object { b"{" } else { b"[" })?;
        Ok(Compound {
            encoder: self,
            object,
            pending: false,
            count: 0,
            variant: false,
            number: false,
        })
    }
    fn formatted<T: fmt::Display + ?Sized>(&mut self, value: &T) -> Result<String, Error> {
        self.guard()?;
        let mut capture = Capture {
            text: String::new(),
            maximum: self.limits.document_bytes,
            prior_bytes: self.bytes.len(),
            cancellation: self.cancellation,
            first: None,
        };
        let result = fmt::write(&mut capture, format_args!("{value}"));
        if let Some(error) = capture.first {
            return Err(self.latch(error));
        }
        if result.is_err() {
            return Err(self.fail(SerializationReason::Custom));
        }
        self.guard()?;
        Ok(capture.text)
    }
    fn number(&mut self, value: &str) -> Result<(), Error> {
        self.room(value.len())?;
        if !value
            .as_bytes()
            .first()
            .is_some_and(|b| b.is_ascii_digit() || *b == b'-')
        {
            return Err(self.fail(SerializationReason::InvalidRepresentation));
        }
        let raw: &serde_json::value::RawValue = serde_json::from_str(value)
            .map_err(|_| self.fail(SerializationReason::InvalidRepresentation))?;
        if raw.get() != value {
            return Err(self.fail(SerializationReason::InvalidRepresentation));
        }
        self.write(value.as_bytes())
    }
}
impl io::Write for Encoder<'_> {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        Encoder::write(self, bytes)
            .map_err(|_| io::Error::other("checked serialization refused"))?;
        Ok(bytes.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

struct Capture<'a> {
    text: String,
    maximum: usize,
    prior_bytes: usize,
    cancellation: &'a Cancellation,
    first: Option<Error>,
}
impl fmt::Write for Capture<'_> {
    fn write_str(&mut self, text: &str) -> fmt::Result {
        if self.first.is_some() {
            return Err(fmt::Error);
        }
        if let Err(error) = self.cancellation.check() {
            self.first = Some(Error::new(error));
            return Err(fmt::Error);
        }
        let actual = self
            .prior_bytes
            .saturating_add(self.text.len())
            .saturating_add(text.len());
        if actual > self.maximum {
            self.first = Some(Error::new(Diagnostic::limited(
                LimitKind::DocumentBytes,
                self.maximum,
                actual,
            )));
            return Err(fmt::Error);
        }
        self.text.push_str(text);
        Ok(())
    }
}

impl ExactJson {
    /// Construct owned exact JSON from the checked Serde data model, then index it once.
    /// Finite floats and exact integers through 128 bits are supported. Object keys
    /// must serialize as strings; duplicate names refuse. None/unit become null,
    /// bytes become arrays, and standard Serde enum representations apply.
    ///
    /// Output obeys document byte/node limits and container depth min(limits.depth,128).
    /// Complete generated source has Constructed origin; earlier failures have no
    /// source or location. Caller Serialize/Display work is not preempted or bounded.
    /// Custom error text is not formatted or retained. When named Serde callbacks
    /// reach this constructor, the pinned arbitrary_precision Number protocol is
    /// strictly validated and RawValue/unknown private protocols refuse. Use parse
    /// for raw JSON. Adapters such as `#[serde(flatten)]` can erase that protocol name
    /// and emit ordinary map entries instead. Flattening Number/RawValue is an
    /// unsupported composition whose origin cannot then be detected; literal map
    /// keys, including private-marker spellings, remain ordinary keys and are
    /// never interpreted as number or raw-JSON instructions.
    /// The depth limit counts emitted JSON containers. Transparent Some/newtype
    /// wrappers do not count; their callback recursion and stack use remain
    /// caller work and can exceed a thread's stack even with shallow JSON output.
    /// Construction runs now; later builder cancellation cannot undo this work.
    pub fn from_serializable<T: Serialize + ?Sized>(
        value: &T,
        limits: Limits,
    ) -> Result<Self, ParseFailure> {
        Self::from_serializable_with_cancellation(value, limits, &Cancellation::default())
    }
    /// Checked construction with cooperative cancellation before caller callbacks
    /// and at owned emission/indexing checkpoints. A pre-cancelled call does not
    /// invoke Serialize. Construction limits and a later operation's body budget
    /// remain separate; this constructor applies document_bytes, nodes and depth.
    pub fn from_serializable_with_cancellation<T: Serialize + ?Sized>(
        value: &T,
        limits: Limits,
        cancellation: &Cancellation,
    ) -> Result<Self, ParseFailure> {
        let mut encoder = Encoder {
            bytes: vec![],
            limits: &limits,
            cancellation,
            nodes: 0,
            depth: 0,
            first: None,
        };
        encoder.child(value).map_err(|error| {
            // Release the encoder's alias before recovering the owned diagnostic.
            // Caller Serialize code may retain an error, so uniqueness is not assumed.
            encoder.first = None;
            crate::ordinary::construction(error.into_diagnostic())
        })?;
        // A custom serializer can abandon a compound without calling end().
        if encoder.depth != 0 || encoder.nodes == 0 {
            return Err(crate::ordinary::construction(
                Error::reason(SerializationReason::InvalidRepresentation).into_diagnostic(),
            ));
        }
        Self::parse_in_origin(
            &encoder.bytes,
            limits.clone(),
            cancellation,
            SourceOrigin::Constructed,
        )
    }
}

struct Compound<'a, 'b> {
    encoder: &'a mut Encoder<'b>,
    object: bool,
    pending: bool,
    count: usize,
    variant: bool,
    number: bool,
}
impl Compound<'_, '_> {
    fn prefix(&mut self) -> Result<(), Error> {
        self.encoder.guard()?;
        if self.count != 0 {
            self.encoder.write(b",")?;
        }
        Ok(())
    }
    fn element<T: Serialize + ?Sized>(&mut self, value: &T) -> Result<(), Error> {
        self.encoder.node_room()?;
        self.prefix()?;
        self.encoder.child(value)?;
        self.count += 1;
        Ok(())
    }
    fn key<T: Serialize + ?Sized>(&mut self, key: &T) -> Result<(), Error> {
        self.encoder.node_room()?;
        if self.pending {
            return Err(self
                .encoder
                .fail(SerializationReason::InvalidRepresentation));
        }
        self.prefix()?;
        let result = key.serialize(Key {
            encoder: self.encoder,
            number: false,
        });
        result.map_err(|e| self.encoder.latch(e))?;
        self.encoder.write(b":")?;
        self.pending = true;
        Ok(())
    }
    fn value<T: Serialize + ?Sized>(&mut self, value: &T) -> Result<(), Error> {
        self.encoder.guard()?;
        if !self.pending {
            return Err(self
                .encoder
                .fail(SerializationReason::InvalidRepresentation));
        }
        self.encoder.child(value)?;
        self.pending = false;
        self.count += 1;
        Ok(())
    }
    fn field<T: Serialize + ?Sized>(&mut self, key: &'static str, value: &T) -> Result<(), Error> {
        if self.number {
            self.encoder.guard()?;
            if key != NUMBER || self.count != 0 {
                return Err(self
                    .encoder
                    .fail(SerializationReason::InvalidRepresentation));
            }
            let result = value.serialize(Key {
                encoder: self.encoder,
                number: true,
            });
            result.map_err(|e| self.encoder.latch(e))?;
            self.count = 1;
            return Ok(());
        }
        self.key(key)?;
        self.value(value)
    }
    fn finish(self) -> Result<(), Error> {
        self.encoder.guard()?;
        if self.number {
            return if self.count == 1 {
                Ok(())
            } else {
                Err(self
                    .encoder
                    .fail(SerializationReason::InvalidRepresentation))
            };
        }
        if self.pending {
            return Err(self
                .encoder
                .fail(SerializationReason::InvalidRepresentation));
        }
        self.encoder.write(if self.object { b"}" } else { b"]" })?;
        self.encoder.depth -= 1;
        if self.variant {
            self.encoder.write(b"}")?;
            self.encoder.depth -= 1;
        }
        Ok(())
    }
}

macro_rules! integers {
    ($($method:ident: $ty:ty),*) => {$(
        fn $method(self, value: $ty) -> Result<(), Error> {
            self.node()?;
            self.primitive(&value)
        }
    )*};
}
impl<'a, 'b> Serializer for &'a mut Encoder<'b> {
    type Ok = ();
    type Error = Error;
    type SerializeSeq = Compound<'a, 'b>;
    type SerializeTuple = Compound<'a, 'b>;
    type SerializeTupleStruct = Compound<'a, 'b>;
    type SerializeTupleVariant = Compound<'a, 'b>;
    type SerializeMap = Compound<'a, 'b>;
    type SerializeStruct = Compound<'a, 'b>;
    type SerializeStructVariant = Compound<'a, 'b>;
    integers!(serialize_i8:i8, serialize_i16:i16, serialize_i32:i32, serialize_i64:i64, serialize_i128:i128,
        serialize_u8:u8, serialize_u16:u16, serialize_u32:u32, serialize_u64:u64, serialize_u128:u128);
    fn serialize_bool(self, value: bool) -> Result<(), Error> {
        self.node()?;
        self.primitive(&value)
    }
    fn serialize_f32(self, value: f32) -> Result<(), Error> {
        self.guard()?;
        if !value.is_finite() {
            return Err(self.fail(SerializationReason::NonFiniteNumber));
        }
        self.node()?;
        self.primitive(&value)
    }
    fn serialize_f64(self, value: f64) -> Result<(), Error> {
        self.guard()?;
        if !value.is_finite() {
            return Err(self.fail(SerializationReason::NonFiniteNumber));
        }
        self.node()?;
        self.primitive(&value)
    }
    fn serialize_char(self, value: char) -> Result<(), Error> {
        self.serialize_str(value.encode_utf8(&mut [0; 4]))
    }
    fn serialize_str(self, value: &str) -> Result<(), Error> {
        self.node()?;
        self.string(value)
    }
    fn serialize_bytes(self, value: &[u8]) -> Result<(), Error> {
        let mut seq = self.serialize_seq(Some(value.len()))?;
        for byte in value {
            seq.serialize_element(byte)?;
        }
        SerializeSeq::end(seq)
    }
    fn serialize_none(self) -> Result<(), Error> {
        self.serialize_unit()
    }
    fn serialize_some<T: Serialize + ?Sized>(self, value: &T) -> Result<(), Error> {
        self.child(value)
    }
    fn serialize_unit(self) -> Result<(), Error> {
        self.node()?;
        self.write(b"null")
    }
    fn serialize_unit_struct(self, _: &'static str) -> Result<(), Error> {
        self.serialize_unit()
    }
    fn serialize_unit_variant(
        self,
        _: &'static str,
        _: u32,
        variant: &'static str,
    ) -> Result<(), Error> {
        self.serialize_str(variant)
    }
    fn serialize_newtype_struct<T: Serialize + ?Sized>(
        self,
        name: &'static str,
        value: &T,
    ) -> Result<(), Error> {
        self.guard()?;
        if name.starts_with(PRIVATE) {
            return Err(self.fail(SerializationReason::UnsupportedRepresentation));
        }
        self.child(value)
    }
    fn serialize_newtype_variant<T: Serialize + ?Sized>(
        self,
        _: &'static str,
        _: u32,
        variant: &'static str,
        value: &T,
    ) -> Result<(), Error> {
        let mut map = self.begin(true)?;
        map.key(variant)?;
        map.value(value)?;
        map.finish()
    }
    fn serialize_seq(self, _: Option<usize>) -> Result<Self::SerializeSeq, Error> {
        self.begin(false)
    }
    fn serialize_tuple(self, _: usize) -> Result<Self::SerializeTuple, Error> {
        self.begin(false)
    }
    fn serialize_tuple_struct(
        self,
        _: &'static str,
        _: usize,
    ) -> Result<Self::SerializeTupleStruct, Error> {
        self.begin(false)
    }
    fn serialize_map(self, _: Option<usize>) -> Result<Self::SerializeMap, Error> {
        self.begin(true)
    }
    fn serialize_struct(
        self,
        name: &'static str,
        len: usize,
    ) -> Result<Self::SerializeStruct, Error> {
        self.guard()?;
        if name == NUMBER {
            if len != 1 {
                return Err(self.fail(SerializationReason::InvalidRepresentation));
            }
            self.node()?;
            return Ok(Compound {
                encoder: self,
                object: false,
                pending: false,
                count: 0,
                variant: false,
                number: true,
            });
        }
        if name.starts_with(PRIVATE) {
            return Err(self.fail(SerializationReason::UnsupportedRepresentation));
        }
        self.begin(true)
    }
    fn serialize_tuple_variant(
        self,
        _: &'static str,
        _: u32,
        variant: &'static str,
        _: usize,
    ) -> Result<Self::SerializeTupleVariant, Error> {
        variant_compound(self, variant, false)
    }
    fn serialize_struct_variant(
        self,
        _: &'static str,
        _: u32,
        variant: &'static str,
        _: usize,
    ) -> Result<Self::SerializeStructVariant, Error> {
        variant_compound(self, variant, true)
    }
    fn collect_str<T: fmt::Display + ?Sized>(self, value: &T) -> Result<(), Error> {
        self.node_room()?;
        let text = self.formatted(value)?;
        self.serialize_str(&text)
    }
}
fn variant_compound<'a, 'b>(
    encoder: &'a mut Encoder<'b>,
    name: &'static str,
    object: bool,
) -> Result<Compound<'a, 'b>, Error> {
    let mut outer = encoder.begin(true)?;
    outer.key(name)?;
    let mut inner = outer.encoder.begin(object)?;
    inner.variant = true;
    Ok(inner)
}

macro_rules! sequence {
    ($trait:ident, $method:ident) => {
        impl ser::$trait for Compound<'_, '_> {
            type Ok = ();
            type Error = Error;
            fn $method<T: Serialize + ?Sized>(&mut self, value: &T) -> Result<(), Error> {
                self.element(value)
            }
            fn end(self) -> Result<(), Error> {
                self.finish()
            }
        }
    };
}
sequence!(SerializeSeq, serialize_element);
sequence!(SerializeTuple, serialize_element);
sequence!(SerializeTupleStruct, serialize_field);
sequence!(SerializeTupleVariant, serialize_field);
impl SerializeMap for Compound<'_, '_> {
    type Ok = ();
    type Error = Error;
    fn serialize_key<T: Serialize + ?Sized>(&mut self, key: &T) -> Result<(), Error> {
        self.key(key)
    }
    fn serialize_value<T: Serialize + ?Sized>(&mut self, value: &T) -> Result<(), Error> {
        self.value(value)
    }
    fn end(self) -> Result<(), Error> {
        self.finish()
    }
}
macro_rules! structure {
    ($trait:ident) => {
        impl ser::$trait for Compound<'_, '_> {
            type Ok = ();
            type Error = Error;
            fn serialize_field<T: Serialize + ?Sized>(
                &mut self,
                key: &'static str,
                value: &T,
            ) -> Result<(), Error> {
                self.field(key, value)
            }
            fn end(self) -> Result<(), Error> {
                self.finish()
            }
        }
    };
}
structure!(SerializeStruct);
structure!(SerializeStructVariant);

// Map keys write directly into the bounded output; no second key index or copy.
// Private Number fields require a direct string, without newtype/Display coercion.
struct Key<'a, 'b> {
    encoder: &'a mut Encoder<'b>,
    number: bool,
}
impl Key<'_, '_> {
    fn invalid(&mut self) -> Error {
        self.encoder.fail(if self.number {
            SerializationReason::InvalidRepresentation
        } else {
            SerializationReason::NonStringKey
        })
    }
}
macro_rules! invalid_keys {
    ($($method:ident:$ty:ty),*) => {$(
        fn $method(mut self, _: $ty) -> Result<(), Error> { Err(self.invalid()) }
    )*};
}
impl Serializer for Key<'_, '_> {
    type Ok = ();
    type Error = Error;
    type SerializeSeq = ser::Impossible<(), Error>;
    type SerializeTuple = Self::SerializeSeq;
    type SerializeTupleStruct = Self::SerializeSeq;
    type SerializeTupleVariant = Self::SerializeSeq;
    type SerializeMap = Self::SerializeSeq;
    type SerializeStruct = Self::SerializeSeq;
    type SerializeStructVariant = Self::SerializeSeq;
    invalid_keys!(serialize_bool:bool,serialize_i8:i8,serialize_i16:i16,serialize_i32:i32,serialize_i64:i64,serialize_i128:i128,
        serialize_u8:u8,serialize_u16:u16,serialize_u32:u32,serialize_u64:u64,serialize_u128:u128,serialize_f32:f32,serialize_f64:f64,serialize_bytes:&[u8]);
    fn serialize_str(self, value: &str) -> Result<(), Error> {
        if self.number {
            self.encoder.number(value)
        } else {
            self.encoder.string(value)
        }
    }
    fn serialize_char(mut self, value: char) -> Result<(), Error> {
        if self.number {
            return Err(self.invalid());
        }
        self.serialize_str(value.encode_utf8(&mut [0; 4]))
    }
    fn serialize_none(mut self) -> Result<(), Error> {
        Err(self.invalid())
    }
    fn serialize_some<T: Serialize + ?Sized>(mut self, _: &T) -> Result<(), Error> {
        Err(self.invalid())
    }
    fn serialize_unit(mut self) -> Result<(), Error> {
        Err(self.invalid())
    }
    fn serialize_unit_struct(mut self, _: &'static str) -> Result<(), Error> {
        Err(self.invalid())
    }
    fn serialize_unit_variant(
        mut self,
        _: &'static str,
        _: u32,
        variant: &'static str,
    ) -> Result<(), Error> {
        if self.number {
            return Err(self.invalid());
        }
        self.serialize_str(variant)
    }
    fn serialize_newtype_struct<T: Serialize + ?Sized>(
        mut self,
        name: &'static str,
        value: &T,
    ) -> Result<(), Error> {
        self.encoder.guard()?;
        if self.number || name.starts_with(PRIVATE) {
            return Err(self.invalid());
        }
        value.serialize(self)
    }
    fn serialize_newtype_variant<T: Serialize + ?Sized>(
        mut self,
        _: &'static str,
        _: u32,
        _: &'static str,
        _: &T,
    ) -> Result<(), Error> {
        Err(self.invalid())
    }
    fn serialize_seq(mut self, _: Option<usize>) -> Result<Self::SerializeSeq, Error> {
        Err(self.invalid())
    }
    fn serialize_tuple(mut self, _: usize) -> Result<Self::SerializeTuple, Error> {
        Err(self.invalid())
    }
    fn serialize_tuple_struct(
        mut self,
        _: &'static str,
        _: usize,
    ) -> Result<Self::SerializeTupleStruct, Error> {
        Err(self.invalid())
    }
    fn serialize_tuple_variant(
        mut self,
        _: &'static str,
        _: u32,
        _: &'static str,
        _: usize,
    ) -> Result<Self::SerializeTupleVariant, Error> {
        Err(self.invalid())
    }
    fn serialize_map(mut self, _: Option<usize>) -> Result<Self::SerializeMap, Error> {
        Err(self.invalid())
    }
    fn serialize_struct(
        mut self,
        _: &'static str,
        _: usize,
    ) -> Result<Self::SerializeStruct, Error> {
        Err(self.invalid())
    }
    fn serialize_struct_variant(
        mut self,
        _: &'static str,
        _: u32,
        _: &'static str,
        _: usize,
    ) -> Result<Self::SerializeStructVariant, Error> {
        Err(self.invalid())
    }
    fn collect_str<T: fmt::Display + ?Sized>(mut self, value: &T) -> Result<(), Error> {
        self.encoder.guard()?;
        if self.number {
            return Err(self.invalid());
        }
        let text = self.encoder.formatted(value)?;
        self.serialize_str(&text)
    }
}
