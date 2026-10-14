use crate::Diagnostic;
use crate::exact::Value;
use serde::Deserialize;

impl Value {
    /// Project the original JSON bytes into a Rust type using standard Serde JSON
    /// conversion. This does not change or consume the exact owner.
    ///
    /// Integer destination types reject overflow; supported i128/u128 values are
    /// parsed directly without a binary64 intermediate. Choosing f32/f64 explicitly
    /// selects ordinary floating-point rounding, unlike Number::to_f64_exact().
    /// Serde token rules also apply: for example, i64 projection of `-0` refuses,
    /// although the exact Number::to_i64() convenience returns zero.
    /// The original number spelling remains available through raw(). Borrowed
    /// output can borrow unescaped strings from this owner.
    ///
    /// This is application type conversion, not schema evaluation. Serde's own
    /// recursion limit applies. Custom Deserialize code can allocate or execute
    /// arbitrary caller work and is outside the exact parser's resource bounds.
    pub fn deserialize<'de, T: Deserialize<'de>>(&'de self) -> Result<T, Diagnostic> {
        serde_json::from_str(self.raw()).map_err(|error| self.locate(Diagnostic::projection(error)))
    }
}
