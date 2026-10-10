use crate::ExactJson;
use serde::Deserialize;
use std::fmt;

/// Failure projecting admitted exact JSON into an application Rust type.
/// Display, Debug and Error::source do not expose Serde's potentially sensitive
/// input, field, variant or custom error text. Use detail() only deliberately.
pub struct DeserializationError(serde_json::Error);
impl DeserializationError {
    /// One-based line of the projection failure, when available (zero otherwise).
    pub fn line(&self) -> usize {
        self.0.line()
    }
    /// One-based column of the projection failure, when available (zero otherwise).
    pub fn column(&self) -> usize {
        self.0.column()
    }
    /// Underlying Serde error, which can contain response data or custom error text.
    /// This is explicit access to untrusted information, not safe logging output.
    pub fn detail(&self) -> &serde_json::Error {
        &self.0
    }
}
impl fmt::Display for DeserializationError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "JSON does not match the requested Rust type at line {}, column {}",
            self.line(),
            self.column()
        )
    }
}
impl fmt::Debug for DeserializationError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(self, f)
    }
}
impl std::error::Error for DeserializationError {}

impl ExactJson {
    /// Project the original JSON bytes into a Rust type using standard Serde JSON
    /// conversion. This does not change or consume the exact owner.
    ///
    /// Integer destination types reject overflow; supported i128/u128 values are
    /// parsed directly without a binary64 intermediate. Choosing f32/f64 explicitly
    /// selects ordinary floating-point rounding, unlike Number::to_f64_exact().
    /// Serde token rules also apply: for example, i64 projection of `-0` refuses,
    /// although the exact Number::to_i64() convenience returns zero.
    /// The original number spelling remains available through root(). Borrowed
    /// output can borrow unescaped strings from this owner.
    ///
    /// This is application type conversion, not schema evaluation. Serde's own
    /// recursion limit applies. Custom Deserialize code can allocate or execute
    /// arbitrary caller work and is outside the exact parser's resource bounds.
    pub fn deserialize<'de, T: Deserialize<'de>>(&'de self) -> Result<T, DeserializationError> {
        serde_json::from_str(self.source()).map_err(DeserializationError)
    }
}
